# tests/unit/test_history.py
import json

from aikdm.eval.history import reference_entries, to_target_messages
from aikdm.eval.schemas import InputMessage, InputToolCall


def test_plain_entries():
    out = to_target_messages(
        [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]
    )
    assert out == [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]


def test_tool_round_becomes_call_and_result_pairs():
    entries = [
        {"role": "user", "content": "find order 7"},
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [
                {
                    "name": "get_order",
                    "args": {"id": 7},
                    "result": {"ok": True},
                    "source": "mocked",
                },
                {"name": "get_user", "args": {}, "result": None},
            ],
        },
        {"role": "assistant", "content": "done"},
    ]
    out = to_target_messages(entries)
    assert out[1]["role"] == "assistant"
    assert [c["id"] for c in out[1]["tool_calls"]] == ["call_1_0", "call_1_1"]
    assert out[1]["tool_calls"][0]["function"] == {
        "name": "get_order",
        "arguments": json.dumps({"id": 7}),
    }
    assert out[2] == {
        "role": "tool",
        "tool_call_id": "call_1_0",
        "content": json.dumps({"ok": True}),
    }
    assert out[3] == {"role": "tool", "tool_call_id": "call_1_1", "content": "null"}
    assert out[4] == {"role": "assistant", "content": "done"}


def test_ids_unique_across_rounds_in_same_turn():
    entries = [
        {"role": "user", "content": "x"},
        {"role": "assistant", "content": "", "tool_calls": [{"name": "a", "args": {}}]},
        {"role": "assistant", "content": "", "tool_calls": [{"name": "b", "args": {}}]},
    ]
    ids = [c["id"] for m in to_target_messages(entries) for c in m.get("tool_calls", [])]
    assert ids == ["call_1_0", "call_1_1"]


def test_reference_entries_text_only_skip_tool_role():
    span = [
        InputMessage(
            message_id="a1t",
            role="assistant",
            content=[
                {"type": "text", "text": "let me check"},
                {"type": "tool_use", "id": "x", "name": "get_order", "input": {}},
            ],
            tool_calls=[InputToolCall(name="get_order", args={"id": 7}, result=None)],
        ),
        InputMessage(
            message_id="t1",
            role="tool",
            content=[{"type": "tool_result", "tool_use_id": "x", "content": "{}"}],
        ),
        InputMessage(message_id="a1", role="assistant", content="here it is"),
    ]
    out = reference_entries(span, declared_tools={"get_order"})
    assert out == [
        {
            "role": "assistant",
            "content": "let me check",
            "tool_calls": [{"name": "get_order", "args": {"id": 7}, "result": None}],
        },
        {"role": "assistant", "content": "here it is"},
    ]


def test_reference_entries_undeclared_tool_becomes_text():
    span = [
        InputMessage(
            message_id="a",
            role="assistant",
            content="",
            tool_calls=[InputToolCall(name="gone", args={"q": 1}, result={"r": 2})],
        )
    ]
    out = reference_entries(span, declared_tools=set())
    assert out == [{"role": "assistant", "content": '[called tool gone with {"q": 1} → {"r": 2}]'}]


def test_empty_user_and_final_assistant_get_placeholders():
    out = to_target_messages(
        [{"role": "user", "content": ""}, {"role": "assistant", "content": ""}]
    )
    assert out == [
        {"role": "user", "content": "(empty message)"},
        {"role": "assistant", "content": "(empty reply)"},
    ]


def test_empty_assistant_with_tool_calls_omits_content():
    out = to_target_messages(
        [
            {"role": "user", "content": "q"},
            {"role": "assistant", "content": "", "tool_calls": [
                {"name": "lookup", "args": {}, "result": 1}]},
        ]
    )
    assert "content" not in out[1] and out[1]["tool_calls"]
