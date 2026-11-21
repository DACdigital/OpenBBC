"""ResourceFactory: the only creator of test resources (names e2e-<run_id>-<kind>-<n>)."""

import os
import subprocess
from collections import defaultdict
from dataclasses import dataclass
from pathlib import Path

import psycopg


E2E_DIR = Path(__file__).resolve().parents[2]
REPO_ROOT = E2E_DIR.parent
BUNDLE = E2E_DIR / "fixtures" / "bundle.yaml"
SEED_SCRIPT = REPO_ROOT / "scripts" / "seed_bundle.py"

MCP_URL = "http://fake-mcp:8000/mcp"  # as open-bbcd sees it on the compose network
MCP_DEFAULT_HEADERS = {"Authorization": "Bearer e2e-static-key"}
AGENT_ROLE = "fake-agent"
DATASET_IDENTITY = "dataset-user"
DATASET_TURNS = [
    ("Search the knowledge base for alpha.", ["Answers the question"]),
    ("Draft a section about alpha.", ["Follows the drafting guide [[require:PATCHED-GUIDE]]"]),
]


@dataclass(frozen=True)
class Seeded:
    agent_id: str
    version_id: str
    backend_id: str
    backend_name: str

    @property
    def search_tool(self) -> str:
        return f"{self.backend_name}__search"


@dataclass(frozen=True)
class DeployedSession:
    agent_id: str
    session_id: str
    user_id: str


def agent_turn_script(seeded: Seeded, query: str = "alpha") -> list[dict]:
    """Per-turn agent script: search -> Skill(draft-section) -> echo the Skill result."""
    return [
        {"tool_calls": [{"name": seeded.search_tool, "args": {"query": query}}]},
        {"tool_calls": [{"name": "Skill", "args": {"name": "draft-section"}}]},
        {"text": "{{last_tool_result}}"},
    ]


def _assert_finished(events: list[dict]) -> None:
    last = events[-1]["type"] if events else None
    if last != "RUN_FINISHED":
        raise AssertionError(f"turn did not finish cleanly (last event {last!r}): {events[-3:]}")


class ResourceFactory:
    def __init__(self, api, db, llm, run_id: str, seed_dsn: str):
        self.api = api
        self.db = db
        self.llm = llm
        self.run_id = run_id
        self._seed_dsn = seed_dsn  # writable; used only for the agent_versions INSERT + seeding
        self._counters: dict[str, int] = defaultdict(int)
        self._deployed: set[str] = set()

    def name(self, kind: str) -> str:
        self._counters[kind] += 1
        return f"e2e-{self.run_id}-{kind}-{self._counters[kind]}"

    # --- agents ------------------------------------------------------------------

    def agent(self) -> Seeded:
        agent_id = self.api.create_agent(self.name("agent"))
        with psycopg.connect(self._seed_dsn, autocommit=True) as conn:
            version_id = str(conn.execute(
                "INSERT INTO agent_versions (agent_id, status) VALUES (%s::uuid, 'DRAFT') "
                "RETURNING id", (agent_id,)).fetchone()[0])
        self._seed_bundle(version_id)
        backend_name = self.name("kb")
        backend_id = self.api.create_mcp_backend(backend_name, MCP_URL, MCP_DEFAULT_HEADERS)
        self.api.attach_backend(version_id, backend_id)
        return Seeded(agent_id, version_id, backend_id, backend_name)

    def _seed_bundle(self, version_id: str) -> None:
        r = subprocess.run(
            ["uv", "run", "--quiet", "--script", str(SEED_SCRIPT),
             "--version-id", version_id, "--bundle", str(BUNDLE)],
            env={**os.environ, "DATABASE_URL": self._seed_dsn},
            capture_output=True, text=True, timeout=180)
        if r.returncode != 0:
            raise AssertionError(f"seed_bundle.py exited {r.returncode}\n{r.stdout}\n{r.stderr}")

    # --- chats and deployed sessions ---------------------------------------------

    def chat(self, seeded: Seeded) -> str:
        return self.api.new_chat(seeded.version_id)

    def deployed_session(self, seeded: Seeded) -> DeployedSession:
        if seeded.version_id not in self._deployed:
            self.api.deploy(seeded.agent_id, seeded.version_id)
            self._deployed.add(seeded.version_id)
        user_id = self.name("user")
        session_id = self.api.deployed_session(seeded.agent_id, user_id)
        return DeployedSession(seeded.agent_id, session_id, user_id)

    # --- datasets, evals, training -----------------------------------------------

    def dataset(self, seeded: Seeded) -> str:
        """Real product path: BO chat -> feedback -> assign -> close. Returns the CLOSED
        dataset_version_id. Its session holds a recorded search call and 2 criteria."""
        self.llm.script(AGENT_ROLE, agent_turn_script(seeded), cycle=True)
        try:
            session_id = self.chat(seeded)
            self.api.set_chat_headers(seeded.version_id, session_id,
                                      {seeded.backend_id: {"X-Test-Identity": DATASET_IDENTITY}})
            for text, _ in DATASET_TURNS:
                _assert_finished(self.api.chat_turn(seeded.version_id, session_id, text))
        finally:
            self.llm.script(AGENT_ROLE, [])  # don't leak the cyclic script into the test

        finals = self.db.final_assistant_message_ids(session_id)
        if len(finals) != len(DATASET_TURNS):
            raise AssertionError(f"want {len(DATASET_TURNS)} final assistant messages, "
                                 f"got {len(finals)}: {self.db.chat_messages(session_id)}")
        for mid, (_, criteria) in zip(finals, DATASET_TURNS):
            self.api.feedback(seeded.version_id, session_id, mid, criteria)

        dataset_id = self.api.create_dataset(self.name("dataset"))
        self.api.assign_dataset(seeded.version_id, session_id, dataset_id)
        self.api.close_draft(dataset_id)
        dv = self.db.latest_dataset_version(dataset_id)
        if dv["status"] != "CLOSED":
            raise AssertionError(f"dataset version not CLOSED: {dv}")
        return dv["id"]

    def eval(self, seeded: Seeded, dataset_version_id: str, mock: bool,
             overrides: dict[str, dict[str, str]]) -> str:
        return self.api.create_eval(seeded.version_id, dataset_version_id, mock, overrides)

    def training(self, eval_id: str) -> str:
        return self.api.create_training(eval_id)
