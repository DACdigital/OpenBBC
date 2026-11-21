"""Read-only Postgres access for assertions where the only product surface is HTML."""

import psycopg
from psycopg.rows import dict_row


class Db:
    def __init__(self, dsn: str):
        self._conn = psycopg.connect(dsn, autocommit=True, row_factory=dict_row)
        self._conn.execute("SET default_transaction_read_only = on")

    def close(self) -> None:
        self._conn.close()

    def _all(self, query: str, *params) -> list[dict]:
        return self._conn.execute(query, params).fetchall()

    def _one(self, query: str, *params) -> dict | None:
        return self._conn.execute(query, params).fetchone()

    def chat_messages(self, session_id: str) -> list[dict]:
        return self._all(
            "SELECT id::text, role, content, seq FROM chat_messages "
            "WHERE session_id = %s::uuid ORDER BY seq", session_id)

    def deployed_messages(self, session_id: str) -> list[dict]:
        return self._all(
            "SELECT id::text, agent_version_id::text, role, content, seq FROM deployed_messages "
            "WHERE session_id = %s::uuid ORDER BY seq", session_id)

    def eval_sessions(self, eval_id: str) -> list[dict]:
        return self._all(
            "SELECT id::text, session_id::text, score, total_criteria, passed_criteria, "
            "transcript, judgments FROM eval_sessions WHERE eval_id = %s::uuid ORDER BY id",
            eval_id)

    def version_prompts(self, version_id: str) -> dict:
        row = self._one("SELECT prompts FROM agent_versions WHERE id = %s::uuid", version_id)
        if row is None:
            raise AssertionError(f"agent version {version_id} not found")
        return row["prompts"]

    def training_new_version(self, sid: str) -> str | None:
        row = self._one("SELECT new_version_id::text AS v FROM training_sessions "
                        "WHERE id = %s::uuid", sid)
        if row is None:
            raise AssertionError(f"training session {sid} not found")
        return row["v"]

    def backend_id(self, name: str) -> str:
        row = self._one("SELECT id::text FROM tool_backends WHERE name = %s", name)
        if row is None:
            raise AssertionError(f"tool backend {name!r} not found")
        return row["id"]

    def latest_dataset_version(self, dataset_id: str) -> dict:
        row = self._one(
            "SELECT id::text, status, version_num FROM dataset_versions "
            "WHERE dataset_id = %s::uuid ORDER BY version_num DESC LIMIT 1", dataset_id)
        if row is None:
            raise AssertionError(f"dataset {dataset_id} has no versions")
        return row

    def dataset_version(self, dataset_version_id: str) -> dict:
        row = self._one("SELECT id::text, status, version_num FROM dataset_versions "
                        "WHERE id = %s::uuid", dataset_version_id)
        if row is None:
            raise AssertionError(f"dataset version {dataset_version_id} not found")
        sessions = self._all("SELECT session_id::text FROM dataset_version_sessions "
                             "WHERE dataset_version_id = %s::uuid", dataset_version_id)
        return {**row, "session_ids": [s["session_id"] for s in sessions]}

    def final_assistant_message_ids(self, session_id: str) -> list[str]:
        """The last assistant message of each user turn, in order."""
        finals: list[str] = []
        last: str | None = None
        for m in self.chat_messages(session_id):
            if m["role"] == "user":
                if last:
                    finals.append(last)
                last = None
            elif m["role"] == "assistant":
                last = m["id"]
        if last:
            finals.append(last)
        return finals

    def feedback_criteria(self, message_ids: list[str]) -> dict[str, list[str]]:
        rows = self._all(
            "SELECT message_id::text, judge_criteria FROM chat_message_feedback "
            "WHERE message_id = ANY(%s::uuid[])", list(message_ids))
        return {r["message_id"]: r["judge_criteria"] for r in rows}
