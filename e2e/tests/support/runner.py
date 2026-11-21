"""Run repo scripts inside the compose `runner` service, as the drainer does."""

import subprocess
from pathlib import Path

PROJECT = "openbbc-e2e"
COMPOSE_FILE = Path(__file__).resolve().parents[2] / "compose.yml"
COMPOSE = ["docker", "compose", "-p", PROJECT, "-f", str(COMPOSE_FILE)]


def compose_logs(tail: int = 300) -> str:
    r = subprocess.run([*COMPOSE, "--profile", "jobs", "logs", "--no-color", "--tail", str(tail)],
                       capture_output=True, text=True, timeout=60)
    return r.stdout + r.stderr


def run_script(script: str, *args: str, timeout: float = 600,
               check: bool = True) -> subprocess.CompletedProcess:
    cmd = [*COMPOSE, "run", "--rm", "-T", "runner", "bash", f"scripts/{script}", *map(str, args)]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as e:
        out = "".join(x.decode(errors="replace") if isinstance(x, bytes) else (x or "")
                      for x in (e.stdout, e.stderr))
        raise AssertionError(
            f"{script} {' '.join(map(str, args))} timed out after {timeout}s\n"
            f"--- output ---\n{out}\n"
            f"--- compose logs ---\n{compose_logs()}") from e
    if check and r.returncode != 0:
        raise AssertionError(
            f"{script} exited {r.returncode}\n--- stdout ---\n{r.stdout}\n"
            f"--- stderr ---\n{r.stderr}\n--- compose logs ---\n{compose_logs()}")
    return r
