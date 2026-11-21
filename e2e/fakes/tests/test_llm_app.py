"""fake-llm against a real uvicorn server."""

import json
import socket
import threading
import time

import httpx
import pytest
import uvicorn

from fakes.llm_app import app


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


@pytest.fixture(scope="module")
def base_url():
    port = _free_port()
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning"))
    t = threading.Thread(target=server.run, daemon=True)
    t.start()
    deadline = time.time() + 10
    while not server.started:
        assert time.time() < deadline, "fake-llm did not start"
        time.sleep(0.02)
    yield f"http://127.0.0.1:{port}"
    server.should_exit = True
    t.join(timeout=5)


@pytest.fixture(autouse=True)
def _reset(base_url):
    httpx.post(f"{base_url}/_reset").raise_for_status()
    yield
    httpx.post(f"{base_url}/_reset").raise_for_status()


KB_TOOL = {"type": "function", "function": {"name": "kb__search", "parameters": {"type": "object"}}}


def _script(base_url, role, replies, cycle=False):
    r = httpx.post(f"{base_url}/_script", json={"role": role, "replies": replies, "cycle": cycle})
    assert r.status_code == 204


def _chat(base_url, model="openai/fake-target", messages=None, tools=None, stream=False,
          path="/v1/chat/completions", include_usage=True):
    body = {"model": model, "messages": messages or [{"role": "user", "content": "hi"}]}
    if tools:
        body["tools"] = tools
    if stream:
        body["stream"] = True
        if include_usage:
            body["stream_options"] = {"include_usage": True}
    return httpx.post(f"{base_url}{path}", json=body)


def test_nonstream_text_reply(base_url):
    _script(base_url, "fake-target", [{"text": "hello"}])
    for path in ("/v1/chat/completions", "/chat/completions"):
        _script(base_url, "fake-target", [{"text": "hello"}])
        r = _chat(base_url, path=path)
        assert r.status_code == 200
        choice = r.json()["choices"][0]
        assert choice["message"] == {"role": "assistant", "content": "hello"}
        assert choice["finish_reason"] == "stop"


def test_nonstream_tool_call_reply(base_url):
    _script(base_url, "fake-target",
            [{"tool_calls": [{"name": "kb__search", "args": {"query": "x"}}]}])
    r = _chat(base_url, tools=[KB_TOOL])
    choice = r.json()["choices"][0]
    call = choice["message"]["tool_calls"][0]
    assert call["type"] == "function"
    assert call["function"]["name"] == "kb__search"
    assert json.loads(call["function"]["arguments"]) == {"query": "x"}
    assert choice["finish_reason"] == "tool_calls"


def _sse(r):
    lines = [ln for ln in r.text.split("\n\n") if ln]
    assert all(ln.startswith("data: ") for ln in lines)
    return [ln[len("data: "):] for ln in lines]


def test_stream_tool_call_shape(base_url):
    _script(base_url, "fake-agent",
            [{"tool_calls": [{"name": "kb__search", "args": {"query": "x"}}]}])
    r = _chat(base_url, model="openai/fake-agent", tools=[KB_TOOL], stream=True)
    assert r.headers["content-type"].startswith("text/event-stream")
    data = _sse(r)
    assert data[-1] == "[DONE]"
    chunks = [json.loads(d) for d in data[:-1]]
    assert len(chunks) == 4
    for c in chunks:
        assert c["object"] == "chat.completion.chunk" and c["id"] and c["model"]
    first = chunks[0]["choices"][0]
    assert first["index"] == 0 and first["finish_reason"] is None
    assert first["delta"]["role"] == "assistant"
    tc = first["delta"]["tool_calls"][0]
    assert tc["index"] == 0 and tc["id"] and tc["type"] == "function"
    assert tc["function"] == {"name": "kb__search", "arguments": ""}
    second = chunks[1]["choices"][0]
    assert second["finish_reason"] is None
    tc2 = second["delta"]["tool_calls"][0]
    assert tc2["index"] == 0 and "id" not in tc2
    assert json.loads(tc2["function"]["arguments"]) == {"query": "x"}
    third = chunks[2]["choices"][0]
    assert third["delta"] == {} and third["finish_reason"] == "tool_calls"
    assert chunks[3]["choices"] == []
    u = chunks[3]["usage"]
    assert u["total_tokens"] == u["prompt_tokens"] + u["completion_tokens"]


def test_stream_text_shape_and_usage_optional(base_url):
    _script(base_url, "fake-agent", [{"text": "hey"}], cycle=True)
    data = _sse(_chat(base_url, model="fake-agent", stream=True))
    chunks = [json.loads(d) for d in data[:-1]]
    assert chunks[0]["choices"][0]["delta"] == {"role": "assistant", "content": "hey"}
    assert chunks[1]["choices"][0]["finish_reason"] == "stop"
    assert chunks[2]["choices"] == [] and "usage" in chunks[2]
    data = _sse(_chat(base_url, model="fake-agent", stream=True, include_usage=False))
    assert len(data) == 3 and data[-1] == "[DONE]"


def test_cycle_repeats(base_url):
    _script(base_url, "fake-target", [{"text": "a"}, {"text": "b"}], cycle=True)
    got = [_chat(base_url).json()["choices"][0]["message"]["content"] for _ in range(5)]
    assert got == ["a", "b", "a", "b", "a"]


def test_exhausted_queue_is_500(base_url):
    _script(base_url, "fake-target", [{"text": "only"}])
    assert _chat(base_url).status_code == 200
    r = _chat(base_url)
    assert r.status_code == 500 and "script exhausted" in r.text
    assert "script exhausted" in _chat(base_url, model="openai/fake-teacher").text


def test_echo_last_tool_result(base_url):
    _script(base_url, "fake-agent", [{"text": "got {{last_tool_result}}"}])
    msgs = [
        {"role": "user", "content": "q"},
        {"role": "tool", "tool_call_id": "1", "content": "X0"},
        {"role": "assistant", "content": "mid"},
        {"role": "tool", "tool_call_id": "2", "content": "X1"},
    ]
    r = _chat(base_url, model="openai/fake-agent", messages=msgs)
    assert r.json()["choices"][0]["message"]["content"] == "got X1"


def test_reply_with_tool_name_not_offered_is_500(base_url):
    _script(base_url, "fake-target",
            [{"tool_calls": [{"name": "Skill", "args": {}}]}])
    r = _chat(base_url, tools=[KB_TOOL])
    assert r.status_code == 500 and "Skill" in r.text


def _judge_request(criteria, transcript):
    user = (
        "<context>\n[]\n</context>\n"
        "<turn_under_test>\n" + json.dumps(transcript, indent=2) + "\n</turn_under_test>\n"
        "<criteria>\n" + "\n".join(f"{i+1}. {c}" for i, c in enumerate(criteria)) + "\n</criteria>"
    )
    return [{"role": "system", "content": "judge"}, {"role": "user", "content": user}]


def _judge(base_url, criteria, transcript):
    r = _chat(base_url, model="openai/fake-judge", messages=_judge_request(criteria, transcript))
    assert r.status_code == 200
    return json.loads(r.json()["choices"][0]["message"]["content"])["judgments"]


def test_judge_require_rule(base_url):
    criteria = ["is polite", "mentions guide [[require:PATCHED-GUIDE]]"]
    without = _judge(base_url, criteria, [{"role": "assistant", "content": "old guide"}])
    assert [j["criterion"] for j in without] == criteria
    assert [j["passed"] for j in without] == [True, False]
    assert all(isinstance(j["reason"], str) for j in without)
    with_ = _judge(base_url, criteria,
                   [{"role": "assistant", "content": 'uses "PATCHED-GUIDE" text'}])
    assert [j["passed"] for j in with_] == [True, True]


def test_judge_never_consumes_queue(base_url):
    _script(base_url, "fake-judge", [{"text": "ignored"}])
    _judge(base_url, ["c"], [])
    _judge(base_url, ["c"], [])
    assert _chat(base_url, model="fake-judge",
                 messages=_judge_request(["c"], [])).status_code == 200


def test_requests_log_filters_by_role(base_url):
    _script(base_url, "fake-target", [{"text": "a"}])
    _script(base_url, "fake-agent", [{"text": "b"}])
    _chat(base_url, model="openai/fake-target", tools=[KB_TOOL])
    _chat(base_url, model="openai/fake-agent", stream=True)
    allr = httpx.get(f"{base_url}/_requests").json()
    assert [r["role"] for r in allr] == ["fake-target", "fake-agent"]
    only = httpx.get(f"{base_url}/_requests", params={"role": "fake-target"}).json()
    assert len(only) == 1
    assert only[0]["model"] == "openai/fake-target" and only[0]["stream"] is False
    assert only[0]["tools"] == [KB_TOOL] and only[0]["messages"][0]["content"] == "hi"
    assert httpx.get(f"{base_url}/_requests", params={"role": "fake-agent"}).json()[0]["stream"] is True
