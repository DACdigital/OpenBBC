"""AG-UI SSE reading: collect events until the run ends."""

import json
from collections.abc import Iterable

import httpx

TERMINAL = ("RUN_FINISHED", "RUN_ERROR")


def parse_events(lines: Iterable[str]) -> list[dict]:
    """Decode `data:` SSE lines into events, stopping after RUN_FINISHED/RUN_ERROR."""
    events: list[dict] = []
    data: list[str] = []
    for line in lines:
        if line.startswith("data:"):
            data.append(line[5:].lstrip())
            continue
        if line == "" and data:
            event = json.loads("\n".join(data))
            data = []
            events.append(event)
            if event.get("type") in TERMINAL:
                return events
    if data:  # stream closed without a trailing blank line
        events.append(json.loads("\n".join(data)))
    return events


def read_stream(response: httpx.Response) -> list[dict]:
    if response.status_code != 200:
        response.read()
        raise AssertionError(f"turn failed: HTTP {response.status_code}: {response.text}")
    return parse_events(response.iter_lines())


def text_of(events: list[dict]) -> str:
    """Concatenated assistant text deltas."""
    return "".join(e.get("delta", "") for e in events if e.get("type") == "TEXT_MESSAGE_CONTENT")
