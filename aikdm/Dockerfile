# syntax=docker/dockerfile:1.7

FROM python:3.12-slim AS builder
RUN pip install --no-cache-dir uv
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY README.md ./
COPY aikdm ./aikdm
COPY schemas ./schemas
RUN uv sync --frozen --no-dev

FROM python:3.12-slim
RUN adduser --system --uid 65532 --group aikdm
WORKDIR /app
COPY --from=builder --chown=aikdm:aikdm /app/.venv /app/.venv
COPY --from=builder --chown=aikdm:aikdm /app/aikdm /app/aikdm
COPY --from=builder --chown=aikdm:aikdm /app/schemas /app/schemas
ENV PATH="/app/.venv/bin:$PATH"
USER aikdm
ENTRYPOINT ["aikdm"]
