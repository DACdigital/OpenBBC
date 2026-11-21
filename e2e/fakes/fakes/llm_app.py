"""Test-only scripted OpenAI-compatible fake LLM (chat completions).

Role = request `model` with any "openai/" prefix stripped. Streaming chunks mirror
open-bbcd/internal/llm/bifrost/bifrosttest/fake.go (Delta/Usage/SSE/Text/ToolCall).
"""

import json

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, PlainTextResponse, Response, StreamingResponse

from fakes.judge import judge_reply

JUDGE_ROLE = "fake-judge"
_ECHO = "{{last_tool_result}}"

_scripts: dict[str, dict] = {}
_requests: list[dict] = []

app = FastAPI()


def _role(model: str) -> str:
    return model[len("openai/"):] if model.startswith("openai/") else model


def _content_text(content) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(p.get("text", "") for p in content if isinstance(p, dict))
    return "" if content is None else json.dumps(content)


def _last_tool_result(messages: list[dict]) -> str:
    for m in reversed(messages):
        if m.get("role") == "tool":
            return _content_text(m.get("content"))
    return ""


def _offered(tools: list[dict]) -> set[str]:
    names = set()
    for t in tools or []:
        name = (t.get("function") or {}).get("name") or t.get("name")
        if name:
            names.add(name)
    return names


def _next_reply(role: str) -> dict | None:
    script = _scripts.get(role)
    if not script or not script["replies"]:
        return None
    replies = script["replies"]
    if script["cycle"]:
        reply = replies[script["pos"] % len(replies)]
        script["pos"] += 1
        return reply
    if script["pos"] >= len(replies):
        return None
    reply = replies[script["pos"]]
    script["pos"] += 1
    return reply


def _chunk(choices: list, **extra) -> dict:
    return {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1,
            "model": "gpt-fake", "choices": choices, **extra}


def _delta(delta: dict, finish) -> dict:
    return _chunk([{"index": 0, "delta": delta, "finish_reason": finish}])


def _usage() -> dict:
    return _chunk([], usage={"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8})


def _sse(chunks: list[dict]) -> str:
    return "".join(f"data: {json.dumps(c)}\n\n" for c in chunks) + "data: [DONE]\n\n"


def _tool_calls(reply: dict) -> list[dict]:
    return [{"id": f"call_{i}", "name": c["name"], "args": json.dumps(c.get("args", {}))}
            for i, c in enumerate(reply["tool_calls"])]


def _stream_body(content: str | None, calls: list[dict], include_usage: bool) -> str:
    if calls:
        first = [{"index": i, "id": c["id"], "type": "function",
                  "function": {"name": c["name"], "arguments": ""}} for i, c in enumerate(calls)]
        second = [{"index": i, "function": {"arguments": c["args"]}} for i, c in enumerate(calls)]
        chunks = [_delta({"role": "assistant", "tool_calls": first}, None),
                  _delta({"tool_calls": second}, None),
                  _delta({}, "tool_calls")]
    else:
        chunks = [_delta({"role": "assistant", "content": content}, None), _delta({}, "stop")]
    if include_usage:
        chunks.append(_usage())
    return _sse(chunks)


def _completion(content: str | None, calls: list[dict]) -> dict:
    message: dict = {"role": "assistant", "content": content}
    if calls:
        message["tool_calls"] = [{"id": c["id"], "type": "function",
                                  "function": {"name": c["name"], "arguments": c["args"]}}
                                 for c in calls]
    return {"id": "chatcmpl-fake", "object": "chat.completion", "created": 1, "model": "gpt-fake",
            "choices": [{"index": 0, "message": message,
                         "finish_reason": "tool_calls" if calls else "stop"}],
            "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}}


def _fail(message: str) -> Response:
    return PlainTextResponse(message, status_code=500)


@app.post("/v1/chat/completions")
@app.post("/chat/completions")
async def chat_completions(request: Request) -> Response:
    body = await request.json()
    model = body.get("model", "")
    role = _role(model)
    messages = body.get("messages") or []
    stream = bool(body.get("stream"))
    _requests.append({"role": role, "model": model, "messages": messages,
                      "tools": body.get("tools"), "stream": stream})

    calls: list[dict] = []
    content: str | None
    if role == JUDGE_ROLE:
        content = json.dumps(judge_reply(body))
    else:
        reply = _next_reply(role)
        if reply is None:
            return _fail(f"script exhausted for role {role}")
        if "tool_calls" in reply:
            calls = _tool_calls(reply)
            offered = _offered(body.get("tools"))
            missing = [c["name"] for c in calls if c["name"] not in offered]
            if missing:
                return _fail(f"scripted tool {missing[0]!r} not offered; offered: {sorted(offered)}")
            content = None
        else:
            content = reply.get("text", "").replace(_ECHO, _last_tool_result(messages))

    if stream:
        include_usage = bool((body.get("stream_options") or {}).get("include_usage"))
        return StreamingResponse(_stream_body(content, calls, include_usage),
                                 media_type="text/event-stream")
    return JSONResponse(_completion(content, calls))


@app.post("/_script")
async def script(request: Request) -> Response:
    body = await request.json()
    _scripts[body["role"]] = {"replies": body["replies"], "cycle": bool(body.get("cycle")), "pos": 0}
    return Response(status_code=204)


@app.get("/_requests")
async def requests_log(role: str | None = None) -> list[dict]:
    return [r for r in _requests if role is None or r["role"] == role]


@app.post("/_reset")
async def reset() -> Response:
    _scripts.clear()
    _requests.clear()
    return Response(status_code=204)
