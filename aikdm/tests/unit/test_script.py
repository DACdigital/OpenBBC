# tests/unit/test_script.py
import logging

from aikdm.eval.schemas import InputCriterion, InputMessage, InputSession
from aikdm.eval.script import (
    AgentStep,
    CriterionItem,
    JudgeStep,
    UserStep,
    compile_script,
    render_user_turn,
    strip_client_context,
)


def _m(mid, role, content="x", tool_calls=None):
    return InputMessage(message_id=mid, role=role, content=content, tool_calls=tool_calls or [])


def _session(transcript, criteria):
    return InputSession(session_id="s", transcript=transcript, criteria=criteria)


def test_render_user_turn_string():
    assert render_user_turn(_m("u", "user", "hello")) == "hello"


def test_render_user_turn_blocks_text_only():
    content = [
        {"type": "text", "text": "a"},
        {"type": "image", "source": {}},
        {"type": "text", "text": "b"},
    ]
    assert render_user_turn(_m("u", "user", content)) == "a\n\nb"


def test_render_user_turn_none_or_other():
    assert render_user_turn(_m("u", "user", None)) == ""


def test_one_turn_with_criteria():
    s = _session(
        [_m("u1", "user"), _m("a1", "assistant")],
        [InputCriterion(message_id="a1", rating="up", items=["c1", "c2"])],
    )
    sc = compile_script(s)
    assert [type(x) for x in sc.steps] == [UserStep, AgentStep, JudgeStep]
    assert sc.turn_count == 1
    j = sc.steps[2]
    assert j.turn == 1
    assert [c.criterion for c in j.criteria] == ["c1", "c2"]
    assert all(not c.unmapped for c in j.criteria)


def test_two_turns_no_criteria_on_first():
    s = _session(
        [_m("u1", "user"), _m("a1", "assistant"), _m("u2", "user"), _m("a2", "assistant")],
        [InputCriterion(message_id="a2", rating="up", items=["c"])],
    )
    sc = compile_script(s)
    assert [type(x).__name__ for x in sc.steps] == [
        "UserStep",
        "AgentStep",
        "UserStep",
        "AgentStep",
        "JudgeStep",
    ]
    assert sc.steps[4].turn == 2
    assert [m.message_id for m in sc.steps[1].ref_span] == ["a1"]


def test_tool_calling_intermediate_message_routes_to_its_turn():
    s = _session(
        [
            _m("u1", "user"),
            _m("a1t", "assistant"),
            _m("t1", "tool"),
            _m("a1", "assistant"),
            _m("u2", "user"),
            _m("a2", "assistant"),
        ],
        [InputCriterion(message_id="a1t", rating="up", items=["uses tool"])],
    )
    sc = compile_script(s)
    judge = [x for x in sc.steps if isinstance(x, JudgeStep)]
    assert len(judge) == 1 and judge[0].turn == 1
    assert [m.message_id for m in sc.steps[1].ref_span] == ["a1t", "t1", "a1"]


def test_pre_first_user_assistant_goes_to_turn_1():
    s = _session(
        [_m("g", "assistant"), _m("u1", "user"), _m("a1", "assistant")],
        [InputCriterion(message_id="g", rating="up", items=["greets"])],
    )
    sc = compile_script(s)
    judge = [x for x in sc.steps if isinstance(x, JudgeStep)][0]
    assert judge.turn == 1 and not judge.criteria[0].unmapped


def test_unmapped_ids_go_to_last_turn():
    s = _session(
        [_m("u1", "user"), _m("a1", "assistant"), _m("u2", "user"), _m("a2", "assistant")],
        [
            InputCriterion(message_id="u1", rating="up", items=["on user"]),
            InputCriterion(message_id="nope", rating="up", items=["unknown"]),
        ],
    )
    sc = compile_script(s)
    judge = [x for x in sc.steps if isinstance(x, JudgeStep)]
    assert len(judge) == 1 and judge[0].turn == 2
    assert [c.unmapped for c in judge[0].criteria] == [True, True]
    assert [c.criterion for c in judge[0].criteria] == ["on user", "unknown"]


def test_criteria_order_stable_under_shuffle():
    tr = [_m("u1", "user"), _m("a1a", "assistant"), _m("a1b", "assistant")]
    c_a = InputCriterion(message_id="a1a", rating="up", items=["x1", "x2"])
    c_b = InputCriterion(message_id="a1b", rating="up", items=["y1"])
    one = compile_script(_session(tr, [c_a, c_b]))
    two = compile_script(_session(tr, [c_b, c_a]))
    get = lambda sc: [c.criterion for c in sc.steps[-1].criteria]  # noqa: E731
    assert get(one) == get(two) == ["x1", "x2", "y1"]


def test_empty_transcript_unrouted():
    s = _session([], [InputCriterion(message_id="a", rating="up", items=["c"])])
    sc = compile_script(s)
    assert sc.steps == [] and sc.turn_count == 0
    assert sc.unrouted == [CriterionItem(message_id="a", criterion="c", unmapped=False)]


def test_unmapped_order_deterministic_under_input_order():
    tr = [_m("u1", "user"), _m("a1", "assistant")]
    on_user = InputCriterion(message_id="u1", rating="up", items=["on user"])
    unknown = InputCriterion(message_id="nope", rating="up", items=["unknown"])
    get = lambda sc: [c.criterion for c in sc.steps[-1].criteria]  # noqa: E731
    one = compile_script(_session(tr, [on_user, unknown]))
    two = compile_script(_session(tr, [unknown, on_user]))
    assert isinstance(one.steps[-1], JudgeStep)
    assert get(one) == get(two) == ["on user", "unknown"]


def test_three_turns_full_step_sequence_and_ref_spans():
    tr = [
        _m("u1", "user"),
        _m("a1t", "assistant"),
        _m("t1", "tool"),
        _m("a1", "assistant"),
        _m("u2", "user"),
        _m("a2", "assistant"),
        _m("u3", "user"),
        _m("a3", "assistant"),
    ]
    sc = compile_script(
        _session(
            tr,
            [
                InputCriterion(message_id="a1", rating="up", items=["c1"]),
                InputCriterion(message_id="a3", rating="up", items=["c3"]),
            ],
        )
    )
    assert sc.turn_count == 3
    assert [(type(x).__name__, x.turn) for x in sc.steps] == [
        ("UserStep", 1),
        ("AgentStep", 1),
        ("JudgeStep", 1),
        ("UserStep", 2),
        ("AgentStep", 2),
        ("UserStep", 3),
        ("AgentStep", 3),
        ("JudgeStep", 3),
    ]
    spans = {
        x.turn: [m.message_id for m in x.ref_span] for x in sc.steps if isinstance(x, AgentStep)
    }
    assert spans == {1: ["a1t", "t1", "a1"], 2: ["a2"], 3: ["a3"]}


CC1 = {"type": "client_context", "canonical": '{"p":1}', "context": {"p": 1},
       "rendered": '<client_context>{"p":1}</client_context>'}
CC2 = {"type": "client_context", "canonical": '{"p":2}', "context": {"p": 2},
       "rendered": '<client_context>{"p":2}</client_context>'}


def test_render_user_turn_client_context_first_verbatim():
    content = [{"type": "text", "text": "a"}, CC1, {"type": "text", "text": "b"}]
    assert render_user_turn(_m("u", "user", content)) == CC1["rendered"] + "\n\na\n\nb"


def test_render_user_turn_multiple_contexts_in_order():
    content = [CC1, CC2, {"type": "text", "text": "hi"}]
    assert render_user_turn(_m("u", "user", content)) == "\n\n".join(
        [CC1["rendered"], CC2["rendered"], "hi"])


def test_render_user_turn_context_only():
    assert render_user_turn(_m("u", "user", [CC1])) == CC1["rendered"]


def test_render_user_turn_skips_block_without_rendered():
    legacy = {"type": "client_context", "canonical": "{}", "context": {}}
    assert render_user_turn(_m("u", "user", [legacy, {"type": "text", "text": "hi"}])) == "hi"


def test_compile_script_warns_once_per_session_for_missing_rendered(caplog):
    legacy = {"type": "client_context", "canonical": "{}", "context": {}}
    s = _session([_m("u1", "user", [legacy, {"type": "text", "text": "a"}]),
                  _m("a1", "assistant"),
                  _m("u2", "user", [legacy, {"type": "text", "text": "b"}]),
                  _m("a2", "assistant")], [])
    with caplog.at_level(logging.WARNING, logger="aikdm.eval.script"):
        compile_script(s)
    records = [r for r in caplog.records if "client_context" in r.getMessage()]
    assert len(records) == 1
    assert "session s" in records[0].getMessage() and "2" in records[0].getMessage()


def test_compile_script_no_warning_when_rendered_present(caplog):
    s = _session([_m("u1", "user", [CC1, {"type": "text", "text": "a"}]),
                  _m("a1", "assistant")], [])
    with caplog.at_level(logging.WARNING, logger="aikdm.eval.script"):
        compile_script(s)
    assert not [r for r in caplog.records if "client_context" in r.getMessage()]


def test_strip_client_context():
    msg = {"message_id": "u", "role": "user",
           "content": [CC1, {"type": "text", "text": "hi"}], "tool_calls": []}
    out = strip_client_context(msg)
    assert out["content"] == [{"type": "text", "text": "hi"}]
    assert msg["content"][0] is CC1  # input not mutated
    assert strip_client_context({"role": "user", "content": "plain"}) == {
        "role": "user", "content": "plain"}
