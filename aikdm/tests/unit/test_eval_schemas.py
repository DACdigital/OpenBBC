import pytest
from pydantic import ValidationError

from aikdm.eval.schemas import AikdmMeta, Judgment


def test_judgment_defaults_not_inconclusive():
    j = Judgment(message_id="m", criterion="c", passed=True)
    assert j.inconclusive is False


def test_inconclusive_must_not_pass():
    with pytest.raises(ValidationError, match="inconclusive"):
        Judgment(message_id="m", criterion="c", passed=True, inconclusive=True)


def test_inconclusive_failed_ok():
    j = Judgment(message_id="m", criterion="c", passed=False, inconclusive=True, reason="x")
    assert j.model_dump()["inconclusive"] is True


def test_meta_modes_default():
    m = AikdmMeta(
        user_simulator_model="a", judge_model="b", target_agent_model="c", duration_seconds=1.0
    )
    assert m.user_turn_mode == "verbatim"
    assert m.history_mode == "simulated"
