"""Scenarios 1-3: BO chat header override, deployed routing envelope, missing identity."""

import json

from support.factory import (AGENT_ROLE, MCP_DEFAULT_HEADERS, SKILL_PROMPT, Seeded,
                             agent_turn_script)


def _tool_results(messages: list[dict]) -> dict[str, list[dict]]:
    """Persisted tool_result blocks grouped by the name of the tool_use they answer."""
    names: dict[str, str] = {}
    results: dict[str, list[dict]] = {}
    for m in messages:
        for block in m["content"]:
            if block.get("type") == "tool_use":
                names[block["id"]] = block["name"]
            elif block.get("type") == "tool_result":
                name = names.get(block["tool_use_id"])
                assert name, f"tool_result without a matching tool_use: {block}"
                results.setdefault(name, []).append(block)
    return results


def _search_calls(mcp) -> list[dict]:
    return [c for c in mcp.calls() if c["method"] == "tools/call" and c["tool"] == "search"]


def _skill_enum(llm) -> list[list[str]]:
    """The Skill tool's `name` enum in every fake-agent request."""
    enums = []
    for req in llm.requests(AGENT_ROLE):
        for tool in req["tools"] or []:
            fn = tool.get("function") or {}
            if fn.get("name") == "Skill":
                enums.append(fn["parameters"]["properties"]["name"]["enum"])
    return enums


def _assert_last_event(events: list[dict], want: str) -> None:
    last = events[-1]["type"] if events else None
    assert last == want, f"last event {last!r}, want {want!r}: {events[-3:]}"


def _assert_identity_scenario(seeded: Seeded, messages: list[dict], identity: str,
                              mcp, llm) -> None:
    searches = _search_calls(mcp)
    assert searches, "fake-mcp saw no tools/call search"
    for call in searches:
        assert call["headers"].get("authorization") == MCP_DEFAULT_HEADERS["Authorization"]
        assert call["headers"].get("x-test-identity") == identity

    results = _tool_results(messages)
    search_results = results.get(seeded.search_tool)
    assert search_results and len(search_results) == 1, results
    assert not search_results[0]["is_error"], search_results
    assert f"as {identity}" in json.dumps(search_results[0]["content"])

    skill_results = results.get("Skill")
    assert skill_results and len(skill_results) == 1, results
    assert not skill_results[0]["is_error"], skill_results
    assert SKILL_PROMPT in json.dumps(skill_results[0]["content"])

    enums = _skill_enum(llm)
    assert enums, "no fake-agent request offered the Skill tool"
    assert all(e == ["draft-section"] for e in enums), enums


def test_bo_chat_mcp_identity_and_skill(seeded, factory, api, db, llm, mcp):
    llm.script(AGENT_ROLE, agent_turn_script(seeded), cycle=True)
    session_id = factory.chat(seeded)
    api.set_chat_headers(seeded.version_id, session_id,
                         {seeded.backend_id: {"X-Test-Identity": "bo-user"}})

    events = api.chat_turn(seeded.version_id, session_id, "Search for alpha and draft a section.")

    _assert_last_event(events, "RUN_FINISHED")
    _assert_identity_scenario(seeded, db.chat_messages(session_id), "bo-user", mcp, llm)


def test_deployed_turn_envelope_identity(seeded, factory, api, db, llm, mcp):
    llm.script(AGENT_ROLE, agent_turn_script(seeded), cycle=True)
    s = factory.deployed_session(seeded)

    events = api.deployed_turn(
        s.agent_id, s.session_id, s.user_id, "Search for alpha and draft a section.",
        mcp_headers={seeded.backend_name: {"X-Test-Identity": "deployed-user"}})

    _assert_last_event(events, "RUN_FINISHED")
    messages = db.deployed_messages(s.session_id)
    assert {m["agent_version_id"] for m in messages} == {seeded.version_id}
    _assert_identity_scenario(seeded, messages, "deployed-user", mcp, llm)


def test_missing_identity_is_tool_error(seeded, factory, api, db, llm, mcp):
    llm.script(AGENT_ROLE, [
        {"tool_calls": [{"name": seeded.search_tool, "args": {"query": "alpha"}}]},
        {"text": "done"},
    ])
    session_id = factory.chat(seeded)

    events = api.chat_turn(seeded.version_id, session_id, "Search for alpha.")

    _assert_last_event(events, "RUN_FINISHED")
    searches = _search_calls(mcp)
    assert searches, "fake-mcp saw no tools/call search"
    assert all("x-test-identity" not in c["headers"] for c in searches), searches

    results = _tool_results(db.chat_messages(session_id)).get(seeded.search_tool)
    assert results and len(results) == 1, results
    assert results[0]["is_error"] is True, results
    assert "missing identity" in json.dumps(results[0]["content"])

    requests = llm.requests(AGENT_ROLE)
    assert len(requests) == 2, [r["messages"][-1] for r in requests]
    tool_msgs = [m for m in requests[1]["messages"] if m.get("role") == "tool"]
    assert tool_msgs, requests[1]["messages"]
    assert "missing identity" in json.dumps(tool_msgs[-1]["content"]), tool_msgs
