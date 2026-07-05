"""Training log writers. Two files at end-of-run + NDJSON progress on stderr.
Kept out of orchestrator so the loop stays testable without touching disk."""

from __future__ import annotations

import csv
import json
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any, TextIO

from aikdm.train.schemas import TrainingReport


_CSV_HEADER = [
    "epoch", "baseline_score", "candidate_score", "promoted",
    "patches_count", "tokens_in", "tokens_out", "duration_seconds", "error",
]


def write_report(report: TrainingReport, out_dir: Path) -> None:
    """Write training-report.json + training-report.csv into out_dir."""
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "training-report.json").write_text(
        report.model_dump_json(indent=2), encoding="utf-8",
    )
    with open(out_dir / "training-report.csv", "w", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow(_CSV_HEADER)
        for ep in report.epochs:
            writer.writerow([
                ep.epoch, ep.baseline_score, ep.candidate_score, ep.promoted,
                len(ep.patches), ep.tokens_in, ep.tokens_out,
                ep.duration_seconds, ep.error,
            ])


@dataclass
class ProgressEmitter:
    """NDJSON line emitter — one JSON object per line, one line per event."""
    sink: TextIO = sys.stderr

    def __call__(self, event: str, **fields: Any) -> None:
        obj: dict[str, Any] = {"event": event}
        obj.update(fields)
        self.sink.write(json.dumps(obj, separators=(",", ":"), default=str) + "\n")
        self.sink.flush()
