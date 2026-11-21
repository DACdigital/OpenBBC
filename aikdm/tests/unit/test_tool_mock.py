from aikdm.eval.tool_mock import ToolMock


def test_replays_exact_match_from_transcript():
    transcript = [
        {"role": "user", "content": "hi"},
        {"role": "assistant", "content": "", "tool_calls": [
            {"name": "search", "args": {"q": "cats"}, "result": {"hits": 42}}
        ]},
    ]
    tools_schema = {"search": {"body_shape": {"type": "object"}}}
    m = ToolMock(transcript, tools_schema)
    out = m.call("search", {"q": "cats"})
    assert out["source"] == "replayed"
    assert out["result"] == {"hits": 42}


def test_synthesizes_when_not_in_transcript():
    m = ToolMock(transcript=[], tools_schema={"search": {"body_shape": {"type": "object"}}})
    out = m.call("search", {"q": "dogs"})
    assert out["source"] == "mocked"
    assert isinstance(out["result"], dict)


def test_unknown_tool_returns_error():
    m = ToolMock(transcript=[], tools_schema={})
    out = m.call("nope", {})
    assert out["source"] == "error"
    assert "unknown" in out["result"]["error"].lower()


def test_replays_from_pydantic_model_dump():
    from aikdm.eval.schemas import InputMessage, InputToolCall
    msg = InputMessage(
        message_id="m-a-1",
        role="assistant",
        content=[{"type": "text", "text": "let me look"}],
        tool_calls=[InputToolCall(name="search", args={"q": "cats"}, result={"hits": 42})],
    )
    m = ToolMock([msg.model_dump()], {"search": {"body_shape": {"type": "object"}}})
    out = m.call("search", {"q": "cats"})
    assert out["source"] == "replayed"
    assert out["result"] == {"hits": 42}


def test_mcp_tool_replays_recorded_call_else_synthesizes_from_output_schema():
    from aikdm.eval.target import build_tool_specs

    recorded = {"content": [{"type": "text", "text": "3 hits"}], "text": "3 hits"}
    transcript = [{"role": "assistant", "tool_calls": [
        {"name": "kb__search", "args": {"q": "pricing"}, "result": recorded}]}]
    _, schemas = build_tool_specs({"tools": []}, {"kb": {"tools": [
        {"name": "kb__search", "input_schema": {"type": "object"},
         "output_schema": {"type": "object", "properties": {"hits": {"type": "integer"}}}},
        {"name": "kb__plain", "input_schema": {"type": "object"}},
    ]}})
    mock = ToolMock(transcript, schemas)
    assert mock.call("kb__search", {"q": "pricing"}) == {"source": "replayed", "result": recorded}
    assert mock.call("kb__search", {"q": "other"}) == {"source": "mocked", "result": {"hits": 1}}
    assert mock.call("kb__plain", {})["result"]["status"] == "ok"
