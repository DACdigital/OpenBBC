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
