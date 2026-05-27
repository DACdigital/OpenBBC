"""NDJSON event emitter for stderr. Each call writes exactly one line."""

from __future__ import annotations

import json
import sys
from datetime import UTC, datetime
from typing import Any, TextIO


class ProgressEmitter:
    def __init__(self, sink: TextIO | None = None) -> None:
        self._sink = sink if sink is not None else sys.stderr

    def emit(self, event: str, **fields: Any) -> None:
        payload: dict[str, Any] = {"event": event, "at": datetime.now(UTC).isoformat()}
        payload.update(fields)
        self._sink.write(json.dumps(payload, separators=(",", ":")) + "\n")
        self._sink.flush()
