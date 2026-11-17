# tests/unit/test_judge.py
import pytest
from google.adk.models.lite_llm import LiteLlm  # type: ignore[import-untyped]

from aikdm.eval import judge as judge_mod
from aikdm.eval.script import CriterionItem

CTX = [{"role": "user", "content": "earlier"}, {"role": "assistant", "content": "earlier reply"}]
TURN = [{"role": "user", "content": "now"}, {"role": "assistant", "content": "UNDER TEST"}]
ITEMS = [CriterionItem("a1", "c1"), CriterionItem("a1", "c2")]


async def test_judge_turn_scores_and_uses_input_criterion_text(monkeypatch):
    seen = {}

    async def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return ('{"judgments":[{"criterion":"C1 ECHO","passed":true,"reason":"ok"},'
                '{"criterion":"c2","passed":false,"reason":"no"}]}', 10, 20)

    monkeypatch.setattr(judge_mod, "_adk_run_once", fake_run)
    r = await judge_mod.judge_turn(agent=object(), context=CTX, turn_under_test=TURN,
                                   criteria_items=ITEMS)
    assert [(j.criterion, j.passed, j.message_id) for j in r.judgments] == [
        ("c1", True, "a1"), ("c2", False, "a1")]
    assert (r.passed, r.total, r.tokens_in, r.tokens_out) == (1, 2, 10, 20)
    xml = seen["xml"]
    assert "<context>" in xml and "<turn_under_test>" in xml and "<criteria>" in xml
    assert xml.index("earlier reply") < xml.index("<turn_under_test>")
    assert xml.index("UNDER TEST") > xml.index("<turn_under_test>")


@pytest.mark.parametrize("payload", [
    '{"judgments":[{"criterion":"c1","passed":true,"reason":""}]}',           # too few
    '{"judgments":[{"passed":true},{"passed":true},{"passed":true}]}',        # too many
])
async def test_count_mismatch_raises(monkeypatch, payload):
    async def fake_run(*, agent, user_message_xml):
        return payload, 1, 1

    monkeypatch.setattr(judge_mod, "_adk_run_once", fake_run)
    with pytest.raises(judge_mod.JudgeError, match="expected 2 judgments"):
        await judge_mod.judge_turn(agent=object(), context=[], turn_under_test=TURN,
                                   criteria_items=ITEMS)


@pytest.mark.parametrize(("payload", "bad_idx"), [
    ('{"judgments":[{"criterion":"c1","reason":"x"},{"passed":true}]}', 0),
    ('{"judgments":[{"passed":true},{"passed":"false"}]}', 1),
])
async def test_non_boolean_passed_raises(monkeypatch, payload, bad_idx):
    async def fake_run(*, agent, user_message_xml):
        return payload, 1, 1

    monkeypatch.setattr(judge_mod, "_adk_run_once", fake_run)
    with pytest.raises(judge_mod.JudgeError,
                       match=f"judge entry {bad_idx} has no boolean 'passed'"):
        await judge_mod.judge_turn(agent=object(), context=[], turn_under_test=TURN,
                                   criteria_items=ITEMS)


async def test_unparseable_raises_judge_error(monkeypatch):
    async def fake_run(*, agent, user_message_xml):
        return "not json", 1, 1

    monkeypatch.setattr(judge_mod, "_adk_run_once", fake_run)
    with pytest.raises(judge_mod.JudgeError) as ei:
        await judge_mod.judge_turn(agent=object(), context=[], turn_under_test=TURN,
                                   criteria_items=ITEMS)
    assert (ei.value.tokens_in, ei.value.tokens_out) == (1, 1)


def test_judge_agent_temperature_zero():
    agent = judge_mod.build_judge_agent(LiteLlm(model="anthropic/claude-haiku-4-5"))
    assert agent.generate_content_config.temperature == 0
