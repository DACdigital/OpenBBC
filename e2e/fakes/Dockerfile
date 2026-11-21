# syntax=docker/dockerfile:1.7
# Test-only image for the e2e fakes. FAKE_APP selects the ASGI app:
#   mcp_app (default, port 8000) or llm_app (port 8001). PORT overrides the port.
FROM python:3.12-slim
ENV PIP_ROOT_USER_ACTION=ignore UV_LINK_MODE=copy PYTHONUNBUFFERED=1
RUN pip install --no-cache-dir uv
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY fakes ./fakes
ENV PATH="/app/.venv/bin:$PATH" FAKE_APP=mcp_app PORT=8000
CMD ["sh", "-c", "exec uvicorn fakes.${FAKE_APP}:app --host 0.0.0.0 --port ${PORT}"]
