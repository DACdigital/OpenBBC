"""OPENBBC_LLM_REQUEST_EXTRAS in aikdm: the shared fixtures under the monorepo
root's testdata/llm-request-extras/ (also read by open-bbcd's Go tests) pin
that both services reject the same configs and match the same way."""

from __future__ import annotations

import json
import logging
from pathlib import Path

import pytest

from aikdm import models
from aikdm import request_extras as rx
from aikdm.config import ConfigError, load_settings
from aikdm.eval import target as target_mod

GLM_RULES = json.dumps([{
    "match": "*/glm-*",
    "headers": {"x-bf-passthrough-extra-params": "true"},
    "body": {"chat_template_kwargs": {"reasoning_effort": "low"}},
}])

ROLES = ("generator", "critic", "user_simulator", "judge", "target", "teacher")


def _fixture(name: str) -> dict:
    """Walk up from this file to the directory holding testdata/llm-request-extras."""
    for parent in Path(__file__).resolve().parents:
        p = parent / "testdata" / "llm-request-extras" / name
        if p.is_file():
            return json.loads(p.read_text())
    raise FileNotFoundError(f"testdata/llm-request-extras/{name} not found above {__file__}")


INVALID = _fixture("invalid.json")["cases"]
MATCHING = _fixture("matching.json")["cases"]


@pytest.fixture
def env(monkeypatch):
    """Clean aikdm env with an OpenAI key, returning a setter."""
    load_settings.cache_clear()
    for k in ("ANTHROPIC_API_KEY", "GEMINI_API_KEY", rx.ENV_VAR,
              *(f"AIKDM_MODEL_{r.upper()}" for r in ROLES)):
        monkeypatch.delenv(k, raising=False)
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-ant")
    monkeypatch.setenv("OPENAI_API_KEY", "sk-oai")

    def set_(**kw):
        for k, v in kw.items():
            monkeypatch.setenv(k, v)
        load_settings.cache_clear()
        return load_settings()

    yield set_
    load_settings.cache_clear()


@pytest.mark.parametrize("case", INVALID, ids=[c["name"] for c in INVALID])
def test_shared_invalid_fixture_raises_config_error(env, case):
    with pytest.raises(ConfigError) as ei:
        env(**{rx.ENV_VAR: case["config"]})
    msg = str(ei.value)
    assert msg.startswith(rx.ENV_VAR + ": "), msg
    if case["rule"] is None:
        assert "rule " not in msg, msg
    else:
        assert f"rule {case['rule']}:" in msg, msg
    assert case["field"] in msg, msg


@pytest.mark.parametrize("case", [c for c in MATCHING if c["model"] is not None],
                         ids=[c["name"] for c in MATCHING if c["model"] is not None])
def test_shared_matching_fixture(case):
    rules = [rx.Rule(index=i, match=p, headers={"x-rule": str(i)}, body={})
             for i, p in enumerate(case["patterns"])]
    got = rx.resolve(rules, case["model"])
    assert (got.index if got else None) == case["expect"]


@pytest.mark.parametrize("case", [c for c in MATCHING if c["model"] is None],
                         ids=[c["name"] for c in MATCHING if c["model"] is None])
@pytest.mark.parametrize("role", ROLES)
def test_shared_matching_fixture_unset_model_uses_role_default(env, case, role):
    rules = [{"match": p, "headers": {"x-rule": str(i)}} for i, p in enumerate(case["patterns"])]
    settings = env(**{rx.ENV_VAR: json.dumps(rules)})
    want = case["expect_by_service"]["aikdm"]
    kw = models.extra_kwargs(role, settings)
    if want is None:
        assert kw == {}
    else:
        assert kw == {"extra_headers": {"x-rule": str(want)}}


def test_unset_and_empty_mean_no_rules(env):
    assert env().request_extras == []
    assert env(**{rx.ENV_VAR: "  "}).request_extras == []
    assert env(**{rx.ENV_VAR: "[]"}).request_extras == []


def test_glob_basics():
    for pattern, s, want in [
        ("*", "", True), ("*", "a/b/c", True), ("", "", True), ("", "a", False),
        ("?", "", False), ("?", "é", True), ("**", "x", True),
        ("*x*x", "axbxcx", True), ("a*b*c", "abbbc", True), ("a*b*c", "acb", False),
    ]:
        assert rx.glob_match(pattern, s) is want, (pattern, s)


def test_empty_headers_with_body_is_valid():
    rules = rx.parse_rules('[{"match":"*","headers":{},"body":{"a":1}}]')
    assert rules[0].headers == {} and rules[0].body == {"a": 1}
    assert rules[0].kwargs() == {"extra_body": {"a": 1}}


def test_kwargs_are_fresh_copies():
    rule = rx.parse_rules(GLM_RULES)[0]
    kw = rule.kwargs()
    kw["extra_body"]["chat_template_kwargs"]["reasoning_effort"] = "high"
    kw["extra_headers"]["x"] = "y"
    assert rule.kwargs() == {
        "extra_headers": {"x-bf-passthrough-extra-params": "true"},
        "extra_body": {"chat_template_kwargs": {"reasoning_effort": "low"}},
    }


# --- build_model (generator, critic, simulator, judge, teacher) -------------

class _RecordingLiteLlm:
    calls: list[dict] = []

    def __init__(self, model, **kwargs):
        _RecordingLiteLlm.calls.append({"model": model, **kwargs})


def test_build_model_passes_extras_only_for_matching_roles(env, monkeypatch):
    settings = env(**{rx.ENV_VAR: GLM_RULES,
                      "AIKDM_MODEL_GENERATOR": "openai/glm-5.3-flash",
                      "AIKDM_MODEL_JUDGE": "openai/gpt-4o-mini"})
    _RecordingLiteLlm.calls = []
    monkeypatch.setattr(models, "LiteLlm", _RecordingLiteLlm)
    models.build_model("generator", settings)
    models.build_model("judge", settings)
    models.build_model("critic", settings)  # default claude-haiku-4-5
    gen, judge, critic = _RecordingLiteLlm.calls
    assert gen == {"model": "openai/glm-5.3-flash",
                   "extra_headers": {"x-bf-passthrough-extra-params": "true"},
                   "extra_body": {"chat_template_kwargs": {"reasoning_effort": "low"}}}
    assert judge == {"model": "openai/gpt-4o-mini"}
    assert critic == {"model": "anthropic/claude-haiku-4-5"}


def test_build_model_unset_variable_passes_nothing(env, monkeypatch):
    settings = env(AIKDM_MODEL_GENERATOR="openai/glm-5.3-flash")
    _RecordingLiteLlm.calls = []
    monkeypatch.setattr(models, "LiteLlm", _RecordingLiteLlm)
    for role in ROLES:
        models.build_model(role, settings)
    assert all(set(c) == {"model"} for c in _RecordingLiteLlm.calls)


def test_real_litellm_carries_extras_to_completion_args(env):
    """ADK LiteLlm forwards unknown constructor kwargs to litellm.acompletion."""
    settings = env(**{rx.ENV_VAR: GLM_RULES, "AIKDM_MODEL_TEACHER": "openai/glm-5.3-flash"})
    m = models.build_model("teacher", settings)
    assert m._additional_args["extra_headers"] == {"x-bf-passthrough-extra-params": "true"}
    assert m._additional_args["extra_body"] == {"chat_template_kwargs": {"reasoning_effort": "low"}}


# --- target_turn (target role) ---------------------------------------------

def _final(text="ok"):
    from types import SimpleNamespace as NS
    return NS(choices=[NS(message=NS(content=text, tool_calls=[]))],
              usage=NS(prompt_tokens=1, completion_tokens=1))


async def _run_target(monkeypatch, extra_kwargs):
    seen = []

    async def fake(**kw):
        seen.append(kw)
        return _final()

    monkeypatch.setattr(target_mod.litellm, "acompletion", fake)
    await target_mod.target_turn(model="openai/glm-5.3-flash", system_prompt="s", tools_spec=[],
                                 history=[{"role": "user", "content": "hi"}], tool_caller=None,
                                 extra_kwargs=extra_kwargs)
    return seen[0]


async def test_target_turn_passes_extras(env, monkeypatch):
    settings = env(**{rx.ENV_VAR: GLM_RULES, "AIKDM_MODEL_TARGET": "openai/glm-5.3-flash"})
    kw = await _run_target(monkeypatch, models.extra_kwargs("target", settings))
    assert kw["extra_headers"] == {"x-bf-passthrough-extra-params": "true"}
    assert kw["extra_body"] == {"chat_template_kwargs": {"reasoning_effort": "low"}}


async def test_target_turn_without_match_passes_nothing(env, monkeypatch):
    settings = env(**{rx.ENV_VAR: GLM_RULES, "AIKDM_MODEL_TARGET": "openai/gpt-4o-mini"})
    extra = models.extra_kwargs("target", settings)
    assert extra == {}
    kw = await _run_target(monkeypatch, extra)
    assert set(kw) == {"model", "messages", "tools"}
    kw = await _run_target(monkeypatch, None)
    assert set(kw) == {"model", "messages", "tools"}


async def test_run_eval_threads_target_extras(env, monkeypatch):
    """The eval orchestrator hands the target role's extras to run_session."""
    from aikdm.eval import orchestrator as eval_orch
    from aikdm.eval import runner as runner_mod

    settings = env(**{rx.ENV_VAR: GLM_RULES, "AIKDM_MODEL_TARGET": "openai/glm-5.3-flash"})
    seen = {}

    async def fake_run_session(**kw):
        seen.update(kw)
        raise RuntimeError("stop here")

    monkeypatch.setattr(runner_mod, "run_session", fake_run_session)
    monkeypatch.setattr(models, "build_model", lambda role, s: object())
    monkeypatch.setattr(eval_orch.sim_mod, "build_simulator_agent", lambda m: object())
    monkeypatch.setattr(eval_orch.judge_mod, "build_judge_agent", lambda m: object())
    inp = _minimal_eval_input()
    await eval_orch.run_eval(inp, settings)
    assert seen["target_extra_kwargs"] == {
        "extra_headers": {"x-bf-passthrough-extra-params": "true"},
        "extra_body": {"chat_template_kwargs": {"reasoning_effort": "low"}},
    }


def _minimal_eval_input():
    from aikdm.eval.schemas import EvalInput
    fixtures = Path(__file__).resolve().parents[1] / "fixtures"
    for p in sorted(fixtures.rglob("eval_input*.yaml")):
        import yaml
        return EvalInput.model_validate(yaml.safe_load(p.read_text()))
    pytest.skip("no eval-input fixture")


# --- startup log ------------------------------------------------------------

def test_log_names_rules_and_roles_never_values(env, caplog):
    secret = "sk-gw-SECRET-1a2b3c4d5e"
    rules = json.dumps([{"match": "*/glm-*", "headers": {"x-gateway-token": secret},
                         "body": {"chat_template_kwargs": {"api_secret": secret}}}])
    settings = env(**{rx.ENV_VAR: rules, "AIKDM_MODEL_TARGET": "openai/glm-5.3-flash"})
    with caplog.at_level(logging.INFO, logger="aikdm.request_extras"):
        models.log_request_extras(settings)
    out = caplog.text
    assert secret not in out and "1a2b3c4d5e" not in out
    assert ("llm request extras rule index=0 match='*/glm-*' headers=['x-gateway-token'] "
            "body_keys=['chat_template_kwargs']") in out
    assert "llm request extras active role=target rule=0 match='*/glm-*'" in out
    assert "llm request extras inactive role=generator" in out


def test_log_silent_without_rules(env, caplog):
    settings = env()
    with caplog.at_level(logging.INFO, logger="aikdm.request_extras"):
        models.log_request_extras(settings)
    assert caplog.text == ""


@pytest.mark.parametrize("extra", [{"extra_body": {"a": 1}}, {}, None])
async def test_run_session_forwards_target_extras_only_when_set(monkeypatch, extra):
    from aikdm.eval import judge as judge_mod
    from aikdm.eval import runner as runner_mod
    from aikdm.eval.schemas import InputCriterion, InputMessage, InputSession, Judgment

    seen = []

    async def fake_target(**kw):
        seen.append(kw)
        return target_mod.TargetTurn(span=[{"role": "assistant", "content": "ok"}],
                                     tokens_in=1, tokens_out=1, tool_call_count=0)

    async def fake_judge(*, agent, context, turn_under_test, criteria_items):
        js = [Judgment(message_id=c.message_id, criterion=c.criterion, passed=True, reason="ok")
              for c in criteria_items]
        return judge_mod.JudgeResult(judgments=js, passed=len(js), total=len(js),
                                     tokens_in=1, tokens_out=1)

    monkeypatch.setattr(target_mod, "target_turn", fake_target)
    monkeypatch.setattr(judge_mod, "judge_turn", fake_judge)
    session = InputSession(
        session_id="s",
        transcript=[InputMessage(message_id="u1", role="user", content="hi"),
                    InputMessage(message_id="a1", role="assistant", content="x")],
        criteria=[InputCriterion(message_id="a1", rating="up", items=["c"])])
    await runner_mod.run_session(session=session, bundle={"main_prompt": "s", "tools": [],
                                                          "skills": []},
                                 simulator_agent=object(), judge_agent=object(), target_model="m",
                                 tool_caller=None, target_extra_kwargs=extra)
    assert len(seen) == 1
    if extra:
        assert seen[0]["extra_kwargs"] == extra
    else:
        assert "extra_kwargs" not in seen[0]
