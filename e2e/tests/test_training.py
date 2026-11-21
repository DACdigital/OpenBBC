"""Scenario 7: training from scenario 4's eval promotes a patched skill prompt."""

import json

from support.evals import EVAL_IDENTITY, EvalRun
from support.factory import AGENT_ROLE, TARGET_ROLE, Seeded, agent_turn_script
from support.runner import run_script

TEACHER_ROLE = "fake-teacher"  # AIKDM_MODEL_TEACHER in compose.yml
PATCHED_PROMPT = "PATCHED-GUIDE drafting guide"
# aikdm/train/teacher.py parses the reply text as TeacherOutput JSON; patcher.py
# applies each patch by section_id ("skill.<name>.prompt").
TEACHER_REPLY = {
    "patches": [{
        "section_id": "skill.draft-section.prompt",
        "new": PATCHED_PROMPT,
        "rationale": "The drafting criterion requires PATCHED-GUIDE.",
    }],
    "focus_notes": "e2e",
}


def _eval_user_calls(mcp) -> list[dict]:
    return [c for c in mcp.calls() if c["method"] == "tools/call"
            and c["headers"].get("x-test-identity") == EVAL_IDENTITY]


def _skill_results(messages: list[dict]) -> list[dict]:
    skill_ids = {b["id"] for m in messages for b in m["content"]
                 if b.get("type") == "tool_use" and b["name"] == "Skill"}
    return [b for m in messages for b in m["content"]
            if b.get("type") == "tool_result" and b["tool_use_id"] in skill_ids]


def test_training_promotes_patched_skill(real_eval: EvalRun, factory, api, db, llm, mcp):
    seeded = real_eval.seeded
    llm.script(TARGET_ROLE, agent_turn_script(seeded), cycle=True)
    llm.script(TEACHER_ROLE, [{"text": json.dumps(TEACHER_REPLY)}])
    sid = factory.training(real_eval.eval_id)

    out = run_script("train_from_session.sh", sid, "--epochs", "1", "--patience", "1", "--yes")
    log = out.stdout + out.stderr

    session = api.training_status(sid)
    assert session["status"] == "DONE", (session, log)
    assert len(llm.requests(TEACHER_ROLE)) == 1, log
    # The eval's identity reached fake-mcp in every turn: baseline + candidate, 2 turns each.
    assert len(_eval_user_calls(mcp)) >= 4, (mcp.calls(), log)

    new_version = db.training_new_version(sid)
    assert new_version, (session, log)
    skill_prompts = db.version_prompts(new_version)["skill_prompts"]
    assert "PATCHED-GUIDE" in skill_prompts["draft-section"], skill_prompts

    if seeded.backend_id not in db.version_backend_ids(new_version):
        factory.attach_backend(new_version, seeded.backend_id)
    trained = Seeded(seeded.agent_id, new_version, seeded.backend_id, seeded.backend_name)
    llm.script(AGENT_ROLE, agent_turn_script(trained), cycle=True)
    chat_id = factory.chat(trained)
    api.set_chat_headers(new_version, chat_id, {seeded.backend_id: {"X-Test-Identity": "bo-user"}})
    events = api.chat_turn(new_version, chat_id, "Draft a section about alpha.")

    assert events and events[-1]["type"] == "RUN_FINISHED", events[-3:]
    results = _skill_results(db.chat_messages(chat_id))
    assert len(results) == 1, results
    assert not results[0]["is_error"], results
    assert PATCHED_PROMPT in json.dumps(results[0]["content"]), results
