"""Clients for the fake LLM and fake MCP control APIs."""

import httpx


class Llm:
    def __init__(self, base: str):
        self._http = httpx.Client(base_url=base, timeout=10)

    def script(self, role: str, replies: list[dict], cycle: bool = False) -> None:
        r = self._http.post("/_script", json={"role": role, "replies": replies, "cycle": cycle})
        r.raise_for_status()

    def requests(self, role: str | None = None) -> list[dict]:
        r = self._http.get("/_requests", params={"role": role} if role else None)
        r.raise_for_status()
        return r.json()

    def reset(self) -> None:
        self._http.post("/_reset").raise_for_status()


class Mcp:
    def __init__(self, base: str):
        self._http = httpx.Client(base_url=base, timeout=10)

    def calls(self) -> list[dict]:
        r = self._http.get("/_calls")
        r.raise_for_status()
        return r.json()

    def reset(self) -> None:
        self._http.post("/_reset").raise_for_status()

    def down(self, down: bool) -> None:
        self._http.post("/_mode", json={"down": down}).raise_for_status()
