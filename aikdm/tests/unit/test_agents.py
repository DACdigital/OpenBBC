from pathlib import Path

from google.adk.models.base_llm import BaseLlm

from aikdm import agents
from aikdm.loader import load_flow_map_config
from aikdm.schemas import Bundle, BundleMetadata, TokenUsage

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


class _StubLlm(BaseLlm):
    """Minimal BaseLlm stub. ADK's LlmAgent validates model as
    str | BaseLlm (Pydantic union), so MagicMock fails. A concrete subclass
    satisfies the validator without touching any network."""

    @classmethod
    def supported_models(cls) -> list[str]:
        return ["stub-model"]

    async def generate_content_async(self, *args, **kwargs):  # type: ignore[override]
        raise NotImplementedError("stub — not for real calls")


def test_build_main_prompt_agent_returns_llm_agent():
    agent = agents.build_main_prompt_agent(_StubLlm(model="stub-model"))
    assert getattr(agent, "name", None) == "aikdm_main_prompt"


def test_build_skill_prompt_agent_returns_llm_agent():
    agent = agents.build_skill_prompt_agent(_StubLlm(model="stub-model"))
    assert getattr(agent, "name", None) == "aikdm_skill_prompt"


def test_build_critic_agent_returns_llm_agent():
    agent = agents.build_critic_agent(_StubLlm(model="stub-model"))
    assert getattr(agent, "name", None) == "aikdm_critic"


def test_call_main_prompt_forwards_config_and_scaffold(mocker):
    seen = {}

    def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return agents.MainPromptResult(
            main_prompt="<role>generated</role>", tokens_in=10, tokens_out=20,
        )

    mocker.patch.object(agents, "_run_main_prompt", side_effect=fake_run)
    cfg = load_flow_map_config(CONFIG)
    result = agents.call_main_prompt(
        _StubLlm(model="stub-model"), cfg, scaffold="<scaffold/>",
    )
    assert result.main_prompt == "<role>generated</role>"
    assert result.tokens_in == 10
    assert "<flow_map_config>" in seen["xml"]
    assert "<main_prompt_scaffold>" in seen["xml"]


def test_call_skill_prompt_forwards_target_and_scaffold(mocker):
    seen = {}

    def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return agents.SkillPromptResult(
            skill_name="place_order", prompt="<role>p</role>",
            tokens_in=5, tokens_out=10,
        )

    mocker.patch.object(agents, "_run_skill_prompt", side_effect=fake_run)
    cfg = load_flow_map_config(CONFIG)
    skill = next(s for s in cfg.skills if s.id == "place_order")
    capability = next(c for c in cfg.capabilities if c.name == skill.capability_ref)
    result = agents.call_skill_prompt(
        _StubLlm(model="stub-model"), cfg, skill, capability,
        scaffold="<scaffold/>", main_prompt_for_context="<role>main</role>",
    )
    assert result.skill_name == "place_order"
    assert "<target_skill id=\"place_order\">" in seen["xml"]
    assert "<main_prompt_for_context>" in seen["xml"]


def test_call_critic_signature_returns_critic_result(mocker):
    mocker.patch.object(
        agents, "_run_critic",
        return_value=agents.CriticResult(issues=["one issue"], tokens_in=5, tokens_out=5),
    )
    cfg = load_flow_map_config(CONFIG)
    bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1, prompt_schema_version="v1",
            model_generator="m", model_critic="m",
            generated_at="t", critic_rounds_run=0, critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt="<role>r</role>",
    )
    result = agents.call_critic(_StubLlm(model="stub-model"), cfg, bundle)
    assert result.issues == ["one issue"]
