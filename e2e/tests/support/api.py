"""Thin client over open-bbcd's REST and form endpoints (no HTML parsing)."""

import base64
import json
import time
from collections.abc import Callable
from typing import Any

import httpx

from support.agui import read_stream

TURN_TIMEOUT = httpx.Timeout(10, read=120)


def _expect(r: httpx.Response, *codes: int) -> httpx.Response:
    if r.status_code not in codes:
        raise AssertionError(
            f"{r.request.method} {r.request.url.path} -> HTTP {r.status_code} "
            f"(want {codes}): {r.text[:500]}")
    return r


def _location_id(r: httpx.Response, prefix: str) -> str:
    loc = r.headers.get("location", "")
    if not loc.startswith(prefix):
        raise AssertionError(f"unexpected Location {loc!r}, want prefix {prefix!r}")
    return loc[len(prefix):].split("/")[0].split("?")[0]


def _multi(pairs: list[tuple[str, str]]) -> dict[str, list[str]]:
    """Repeated form fields, in order (httpx wants {key: [values]})."""
    out: dict[str, list[str]] = {}
    for k, v in pairs:
        out.setdefault(k, []).append(v)
    return out


def mcp_headers_envelope(mcp_headers: dict[str, dict[str, str]]) -> str:
    """base64url (padded, as open-bbcd decodes it) JSON keyed by backend name."""
    return base64.urlsafe_b64encode(json.dumps(mcp_headers).encode()).decode()


def wait_until(fn: Callable[[], Any], pred: Callable[[Any], bool], timeout: float = 120,
               interval: float = 0.5) -> Any:
    """Poll fn() until pred(value) holds; raise with the last value on deadline."""
    deadline = time.monotonic() + timeout
    while True:
        value = fn()
        if pred(value):
            return value
        if time.monotonic() >= deadline:
            raise TimeoutError(f"condition not met within {timeout}s; last value: {value!r}")
        time.sleep(interval)


class Api:
    def __init__(self, base_url: str, db=None):
        self._http = httpx.Client(base_url=base_url, timeout=30, follow_redirects=False)
        self._db = db  # read-only sql.Db, for ids the form endpoints don't return

    # --- agents and backends -------------------------------------------------

    def create_agent(self, name: str) -> str:
        r = _expect(self._http.post("/agents", json={"name": name}), 201)
        return r.json()["id"]

    def create_mcp_backend(self, name: str, url: str, headers: dict[str, str]) -> str:
        form = [("name", name), ("kind", "mcp_client"), ("url", url),
                ("transport", "streamable_http")]
        for k, v in headers.items():
            form += [("default_headers_key", k), ("default_headers_value", v)]
        _expect(self._http.post("/mcp", data=_multi(form)), 303)
        if self._db is None:
            raise RuntimeError("create_mcp_backend needs Api(db=...) to resolve the id")
        return self._db.backend_id(name)

    def attach_backend(self, version_id: str, backend_id: str) -> None:
        _expect(self._http.post(
            f"/agent_versions/{version_id}/architecture/mcp/{backend_id}/toggle",
            data={"attached": "1"}), 200)

    # --- BO chat ---------------------------------------------------------------

    def new_chat(self, version_id: str) -> str:
        r = _expect(self._http.post(f"/agent_versions/{version_id}/chat/sessions"), 303)
        return _location_id(r, f"/agent_versions/{version_id}/chat/")

    def set_chat_headers(self, version_id: str, session_id: str,
                         overrides: dict[str, dict[str, str]]) -> None:
        form = []
        for backend_id, hdrs in overrides.items():
            for k, v in hdrs.items():
                form += [("header_backend[]", backend_id), ("header_key[]", k),
                         ("header_val[]", v)]
        _expect(self._http.post(f"/agent_versions/{version_id}/chat/{session_id}/headers",
                                data=_multi(form)), 200)

    def chat_turn(self, version_id: str, session_id: str, text: str,
                  headers: dict[str, str] | None = None) -> list[dict]:
        with self._http.stream(
                "POST", f"/agent_versions/{version_id}/chat/{session_id}/turn",
                json={"input": [{"type": "text", "text": text}]},
                headers=headers, timeout=TURN_TIMEOUT) as r:
            return read_stream(r)

    def feedback(self, version_id: str, session_id: str, message_id: str,
                 criteria: list[str]) -> None:
        _expect(self._http.post(
            f"/agent_versions/{version_id}/chat/{session_id}/messages/{message_id}/feedback",
            data={"rating": "up", "judge_criteria_json": json.dumps(criteria)}), 200)

    # --- datasets ----------------------------------------------------------------

    def create_dataset(self, name: str) -> str:
        r = _expect(self._http.post("/datasets", data={"name": name, "description": "e2e"}), 303)
        return _location_id(r, "/datasets/")

    def assign_dataset(self, version_id: str, session_id: str, dataset_id: str) -> None:
        _expect(self._http.post(f"/agent_versions/{version_id}/chat/{session_id}/assign-dataset",
                                data={"dataset_id": dataset_id}), 204)

    def close_draft(self, dataset_id: str) -> None:
        _expect(self._http.post(f"/datasets/{dataset_id}/close-draft",
                                data={"close_note": "e2e"}), 303)

    # --- deployed runtime --------------------------------------------------------

    def deploy(self, agent_id: str, version_id: str) -> None:
        _expect(self._http.post(f"/agents/{agent_id}/deploy", json={"version_id": version_id}),
                200)

    def deployed_session(self, agent_id: str, user_id: str) -> str:
        r = _expect(self._http.post(f"/deployed/{agent_id}/sessions",
                                    json={"user_id": user_id}), 201)
        return r.json()["id"]

    def deployed_turn(self, agent_id: str, session_id: str, user_id: str, text: str,
                      mcp_headers: dict[str, dict[str, str]] | None = None) -> list[dict]:
        headers = {"X-Openbbcd-Mcp-Headers": mcp_headers_envelope(mcp_headers)} \
            if mcp_headers else None
        with self._http.stream(
                "POST", f"/deployed/{agent_id}/sessions/{session_id}/turn",
                json={"user_id": user_id, "input": [{"type": "text", "text": text}]},
                headers=headers, timeout=TURN_TIMEOUT) as r:
            return read_stream(r)

    # --- evals and training ------------------------------------------------------

    def create_eval(self, version_id: str, dataset_version_id: str, mock: bool,
                    mcp_header_overrides: dict[str, dict[str, str]]) -> str:
        r = _expect(self._http.post(f"/agent_versions/{version_id}/evals", json={
            "dataset_version_id": dataset_version_id,
            "mock_mcp_tools": mock,
            "mcp_header_overrides": mcp_header_overrides,
        }), 201)
        return r.json()["id"]

    def export(self, eval_id: str) -> httpx.Response:
        return self._http.get(f"/evals/{eval_id}/export.yaml", timeout=60)

    def eval_status(self, eval_id: str) -> dict:
        # No per-id JSON endpoint: find it in the list.
        r = _expect(self._http.get("/evals.json", params={"limit": 10000}), 200)
        for e in r.json():
            if e["id"] == eval_id:
                return e
        raise AssertionError(f"eval {eval_id} not in /evals.json")

    def create_training(self, eval_id: str) -> str:
        r = _expect(self._http.post("/training-sessions", data={"source_eval_id": eval_id}), 303)
        return _location_id(r, "/training-sessions/")

    def training_status(self, sid: str) -> dict:
        return _expect(self._http.get(f"/training-sessions/{sid}/json"), 200).json()

    wait_until = staticmethod(wait_until)
