# tests/unit/test_simulator.py
from aikdm.eval import simulator as sim_mod


async def test_paraphrase_returns_content_and_sends_intent(monkeypatch):
    seen = {}

    async def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return '{"content": "could you list the products?"}', 5, 6

    monkeypatch.setattr(sim_mod, "_adk_run_once", fake_run)
    t = await sim_mod.next_user_turn(agent=object(), reference_transcript=[{"role": "user"}],
                                     simulated_so_far=[], intent="list prodycts")
    assert t.content == "could you list the products?"
    assert (t.tokens_in, t.tokens_out) == (5, 6)
    assert "<intent>\nlist prodycts\n</intent>" in seen["xml"]
    assert not hasattr(t, "stop")


def test_output_schema_has_no_stop():
    assert "stop" not in sim_mod._SimulatorOutput.model_fields


def test_prompt_forbids_answering_instead_of_intent():
    p = sim_mod.USER_SIMULATOR_SYSTEM_PROMPT
    assert "<intent>" in p and "even if" in p
