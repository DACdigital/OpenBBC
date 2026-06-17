from pathlib import Path

from google.adk.models.base_llm import BaseLlm

from aikdm import agents
from aikdm.loader import load_flow_map_config

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


def test_build_main_prompt_critic_agent_returns_llm_agent():
    agent = agents.build_main_prompt_critic_agent(_StubLlm(model="stub-model"))
    assert getattr(agent, "name", None) == "aikdm_main_prompt_critic"


def test_build_skill_prompt_critic_agent_returns_llm_agent():
    agent = agents.build_skill_prompt_critic_agent(_StubLlm(model="stub-model"))
    assert getattr(agent, "name", None) == "aikdm_skill_prompt_critic"


async def test_call_main_prompt_forwards_config_and_scaffold(mocker):
    seen = {}

    async def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return agents.MainPromptResult(
            main_prompt="<role>generated</role>", tokens_in=10, tokens_out=20,
        )

    mocker.patch.object(agents, "_run_main_prompt", side_effect=fake_run)
    cfg = load_flow_map_config(CONFIG)
    result = await agents.call_main_prompt(
        _StubLlm(model="stub-model"), cfg, scaffold="<scaffold/>",
    )
    assert result.main_prompt == "<role>generated</role>"
    assert "<flow_map_config>" in seen["xml"]
    assert "<main_prompt_scaffold>" in seen["xml"]


async def test_call_skill_prompt_forwards_target_and_scaffold(mocker):
    seen = {}

    async def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return agents.SkillPromptResult(
            skill_name="place_order", prompt="<role>p</role>",
            tokens_in=5, tokens_out=10,
        )

    mocker.patch.object(agents, "_run_skill_prompt", side_effect=fake_run)
    cfg = load_flow_map_config(CONFIG)
    skill = next(s for s in cfg.skills if s.id == "place_order")
    result = await agents.call_skill_prompt(
        _StubLlm(model="stub-model"), cfg, skill,
        scaffold="<scaffold/>",
    )
    assert result.skill_name == "place_order"
    assert "<target_skill id=\"place_order\">" in seen["xml"]
    # v2: linked_endpoints (not linked_capabilities)
    assert "<linked_endpoints>" in seen["xml"]
    assert "<suggested_endpoints>" in seen["xml"]


async def test_call_main_prompt_critic_forwards_main_prompt(mocker):
    seen = {}

    async def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return agents.CriticResult(issues=[], tokens_in=2, tokens_out=2)

    mocker.patch.object(agents, "_run_critic", side_effect=fake_run)
    cfg = load_flow_map_config(CONFIG)
    result = await agents.call_main_prompt_critic(
        _StubLlm(model="stub-model"), cfg, "<role>r</role>",
    )
    assert result.issues == []
    assert "<main_prompt>" in seen["xml"]
    assert "<role>r</role>" in seen["xml"]


async def test_call_skill_prompt_critic_forwards_target_and_prompt(mocker):
    seen = {}

    async def fake_run(*, agent, user_message_xml):
        seen["xml"] = user_message_xml
        return agents.CriticResult(issues=["nope"], tokens_in=3, tokens_out=3)

    mocker.patch.object(agents, "_run_critic", side_effect=fake_run)
    cfg = load_flow_map_config(CONFIG)
    skill = next(s for s in cfg.skills if s.id == "place_order")
    result = await agents.call_skill_prompt_critic(
        _StubLlm(model="stub-model"), cfg, skill, "<role>body</role>",
    )
    assert result.issues == ["nope"]
    assert "<target_skill id=\"place_order\">" in seen["xml"]
    assert "<skill_prompt>" in seen["xml"]
