from pathlib import Path

from google.adk.models.base_llm import BaseLlm

from aikdm import agents
from aikdm.loader import load_flow_map_config
from aikdm.schemas import Bundle, BundleMetadata, TokenUsage

CONFIG = Path(__file__).parents[1] / "fixtures" / "flow_map_config" / "coffee_shop.yaml"


class _StubLlm(BaseLlm):
    """Minimal BaseLlm stub for agent construction tests.

    ADK's LlmAgent validates model as str | BaseLlm (Pydantic union),
    so MagicMock() fails. A concrete BaseLlm subclass satisfies the
    validator without touching any network.
    """

    @classmethod
    def supported_models(cls) -> list[str]:
        return ["stub-model"]

    async def generate_content_async(self, *args, **kwargs):  # type: ignore[override]
        raise NotImplementedError("stub — not for real calls")


def test_build_generator_agent_returns_llm_agent():
    model = _StubLlm(model="stub-model")
    agent = agents.build_generator_agent(model)
    assert agent is not None
    # ADK LlmAgent exposes .name and .model
    assert getattr(agent, "name", None) == "aikdm_generator"


def test_build_critic_agent_returns_llm_agent():
    model = _StubLlm(model="stub-model")
    agent = agents.build_critic_agent(model)
    assert agent is not None
    assert getattr(agent, "name", None) == "aikdm_critic"


def test_call_generator_signature_returns_generator_result(monkeypatch):
    """We monkey-patch the underlying ADK runner so we don't make a real call.
    This verifies our wrapper passes the right inputs and returns the right shape.
    """
    fake_bundle = Bundle(
        metadata=BundleMetadata(
            config_schema_version=1, prompt_schema_version="v1",
            model_generator="m", model_critic="m",
            generated_at="t", critic_rounds_run=0, critic_notes=[],
            tokens_used=TokenUsage(),
        ),
        main_prompt="<role>r</role>",
    )

    def fake_run(*, agent, user_message_xml, **_):
        assert "<flow_map_config>" in user_message_xml
        return agents.GeneratorResult(bundle=fake_bundle, tokens_in=10, tokens_out=20)

    monkeypatch.setattr(agents, "_run_generator", fake_run)
    cfg = load_flow_map_config(CONFIG)
    agent = _StubLlm(model="stub-model")
    result = agents.call_generator(agent, cfg, scaffold_main="<x></x>",
                                   scaffold_skills={"place_order": "<y></y>",
                                                    "check_rewards": "<z></z>"})
    assert result.bundle == fake_bundle
    assert result.tokens_in == 10


def test_call_critic_signature_returns_critic_result(monkeypatch):
    def fake_run(*, agent, user_message_xml, **_):
        return agents.CriticResult(issues=["one issue"], tokens_in=5, tokens_out=5)

    monkeypatch.setattr(agents, "_run_critic", fake_run)
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
