from aikdm.train.teacher import _render_last_eval


def test_render_last_eval_skips_inconclusive_keeps_score():
    out = _render_last_eval(
        {
            "score": 0.5,
            "sessions": [
                {
                    "judgments": [
                        {"criterion": "real fail", "passed": False, "reason": "rude"},
                        {
                            "criterion": "infra",
                            "passed": False,
                            "inconclusive": True,
                            "reason": "turn 2 not reached: turn 1 target error: boom",
                        },
                        {"criterion": "ok", "passed": True, "reason": "fine"},
                    ]
                }
            ],
        }
    )
    assert "<score>0.5</score>" in out
    assert 'criterion="real fail"' in out and 'criterion="ok"' in out
    assert "infra" not in out and "not reached" not in out
