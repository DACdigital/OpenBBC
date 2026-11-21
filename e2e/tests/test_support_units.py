"""Stack-free unit tests for the suite's support helpers."""

import base64
import json

from support.agui import parse_events, text_of
from support.api import _multi, mcp_headers_envelope


def test_parse_events_stops_at_run_finished():
    lines = ['data: {"type":"RUN_STARTED"}', "",
             'data: {"type":"TEXT_MESSAGE_CONTENT","delta":"hi"}', "",
             'data: {"type":"RUN_FINISHED"}', "",
             'data: {"type":"NEVER"}', ""]
    events = parse_events(lines)
    assert [e["type"] for e in events] == ["RUN_STARTED", "TEXT_MESSAGE_CONTENT", "RUN_FINISHED"]
    assert text_of(events) == "hi"


def test_parse_events_keeps_unterminated_tail():
    assert parse_events(['data: {"type":"RUN_ERROR"}']) == [{"type": "RUN_ERROR"}]


def test_mcp_headers_envelope_is_padded_base64url_json():
    raw = mcp_headers_envelope({"kb": {"X-Test-Identity": "deployed-user"}})
    assert json.loads(base64.urlsafe_b64decode(raw)) == {"kb": {"X-Test-Identity": "deployed-user"}}


def test_multi_groups_repeated_fields_in_order():
    assert _multi([("k", "a"), ("v", "1"), ("k", "b")]) == {"k": ["a", "b"], "v": ["1"]}
