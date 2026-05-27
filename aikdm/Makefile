.PHONY: install test fmt lint run-help

install:
	uv sync --all-extras

test:
	uv run pytest -q tests/unit tests/integration

test-smoke:
	RUN_SMOKE=1 uv run pytest -q tests/smoke

fmt:
	uv run ruff format aikdm tests

lint:
	uv run ruff check aikdm tests

run-help:
	uv run aikdm --help
