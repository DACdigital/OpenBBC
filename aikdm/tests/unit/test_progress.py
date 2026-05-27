import io
import json

from aikdm.progress import ProgressEmitter


def test_emits_single_line_json():
    sink = io.StringIO()
    emitter = ProgressEmitter(sink)
    emitter.emit("started", model_generator="m1", model_critic="m2")

    lines = sink.getvalue().splitlines()
    assert len(lines) == 1
    parsed = json.loads(lines[0])
    assert parsed["event"] == "started"
    assert parsed["model_generator"] == "m1"
    assert "at" in parsed  # auto-timestamped


def test_emits_in_order():
    sink = io.StringIO()
    emitter = ProgressEmitter(sink)
    emitter.emit("started")
    emitter.emit("round_started", round=1)
    emitter.emit("done")

    events = [json.loads(line)["event"] for line in sink.getvalue().splitlines()]
    assert events == ["started", "round_started", "done"]


def test_each_event_is_valid_json():
    sink = io.StringIO()
    emitter = ProgressEmitter(sink)
    emitter.emit("draft_done", round=1, tokens_in=100)

    line = sink.getvalue().splitlines()[0]
    payload = json.loads(line)
    assert payload["tokens_in"] == 100
