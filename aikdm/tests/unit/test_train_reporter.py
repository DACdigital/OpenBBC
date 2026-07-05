"""Reporter tests — file writes only, no LLM."""

from __future__ import annotations

import csv
import json
import io
from pathlib import Path

from aikdm.train.reporter import ProgressEmitter, write_report
from aikdm.train.schemas import EpochRecord, SectionPatch, TrainingReport


def _report(epochs: list[EpochRecord]) -> TrainingReport:
    return TrainingReport(
        input_eval_id="e-1",
        initial_score=0.4, final_score=0.7,
        total_epochs_run=len(epochs), stopped_reason="max_epochs",
        epochs=epochs,
        final_bundle_path="/tmp/bundle.yaml",
    )


def test_write_report_creates_both_json_and_csv(tmp_path: Path):
    epochs = [
        EpochRecord(epoch=1, baseline_score=0.4, candidate_score=0.5,
                    promoted=True,
                    patches=[SectionPatch(section_id="main_prompt", new="x", rationale="y")],
                    tokens_in=100, tokens_out=20),
        EpochRecord(epoch=2, baseline_score=0.5, candidate_score=0.5,
                    promoted=False, patches=[], tokens_in=50, tokens_out=10),
    ]
    write_report(_report(epochs), tmp_path)

    j = json.loads((tmp_path / "training-report.json").read_text())
    assert j["schema_version"] == "training-report-v1"
    assert j["epochs"][0]["promoted"] is True

    with open(tmp_path / "training-report.csv", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    assert len(rows) == 2
    assert rows[0]["epoch"] == "1"
    assert rows[0]["promoted"] == "True"
    assert rows[1]["baseline_score"] == "0.5"


def test_write_report_csv_has_expected_columns(tmp_path: Path):
    write_report(_report([]), tmp_path)
    with open(tmp_path / "training-report.csv", encoding="utf-8") as f:
        reader = csv.reader(f)
        header = next(reader)
    assert header == [
        "epoch", "baseline_score", "candidate_score", "promoted",
        "patches_count", "tokens_in", "tokens_out", "duration_seconds", "error",
    ]


def test_progress_emitter_writes_ndjson_line():
    buf = io.StringIO()
    emit = ProgressEmitter(sink=buf)
    emit("epoch_done", epoch=3, best_score=0.71, promoted=True)
    line = buf.getvalue().strip()
    obj = json.loads(line)
    assert obj["event"] == "epoch_done"
    assert obj["epoch"] == 3
    assert obj["best_score"] == 0.71
    assert obj["promoted"] is True
