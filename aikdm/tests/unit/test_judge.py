import asyncio
from dataclasses import dataclass

from aikdm.eval import judge as judge_mod


@dataclass
class FakeModel:
    responses: list[str]

    def call_index(self, i: int) -> str:
        return self.responses[min(i, len(self.responses) - 1)]


def test_judge_scores_each_criterion(monkeypatch):
    canned = '{"judgments":[{"criterion":"c1","passed":true,"reason":"ok"},' \
             '{"criterion":"c2","passed":false,"reason":"missed"}]}'
    async def fake_run(*, agent, user_message_xml):
        return canned, 10, 20

    monkeypatch.setattr(judge_mod, "_adk_run_once", fake_run)
    result = asyncio.run(judge_mod.judge_transcript(
        agent=object(),
        transcript=[{"role": "user", "content": "hi"}],
        criteria_items=[
            {"message_id": "m-1", "criterion": "c1"},
            {"message_id": "m-1", "criterion": "c2"},
        ],
    ))
    assert result.passed == 1
    assert result.total == 2
    assert [j.passed for j in result.judgments] == [True, False]
    assert result.judgments[0].message_id == "m-1"
