"""OPENBBC_LLM_REQUEST_EXTRAS: per-model extra headers and body fields for
outbound LLM calls.

open-bbcd reads the same variable with the same semantics
(open-bbcd/internal/llmextras); the shared fixtures under the monorepo root's
testdata/llm-request-extras/ pin that both reject and match identically.

Header and body values may be credentials: nothing here logs them, only
header names and body keys.
"""

from __future__ import annotations

import copy
import json
import logging
import re
from dataclasses import dataclass, field
from typing import Any

ENV_VAR = "OPENBBC_LLM_REQUEST_EXTRAS"

log = logging.getLogger(__name__)

# Top-level body fields the callers set themselves.
RESERVED_BODY_KEYS = frozenset({
    "model", "messages", "tools", "tool_choice", "stream", "stream_options",
    "max_tokens", "max_completion_tokens", "system",
})
# Lower-cased headers carrying credentials or framing.
RESERVED_HEADERS = frozenset({
    "authorization", "x-api-key", "x-goog-api-key", "anthropic-version",
    "content-type", "content-length", "host", "transfer-encoding",
})
# sjson path syntax (open-bbcd's Anthropic adapter): rejected in body keys so
# a key means the same top-level field on every adapter.
SJSON_SYNTAX = ".*?|#@\\"
_TOKEN = re.compile(r"^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$")  # RFC 7230 token
_ALLOWED_KEYS = ("match", "headers", "body")


class RequestExtrasError(ValueError):
    """An invalid OPENBBC_LLM_REQUEST_EXTRAS value. config.load_settings
    re-raises it as ConfigError (exit code 2)."""


@dataclass(frozen=True)
class Rule:
    index: int
    match: str
    headers: dict[str, str] = field(default_factory=dict)
    body: dict[str, Any] = field(default_factory=dict)

    def kwargs(self) -> dict[str, Any]:
        """LiteLLM kwargs for this rule: extra_headers / extra_body, each only
        when non-empty. Fresh copies on every call."""
        out: dict[str, Any] = {}
        if self.headers:
            out["extra_headers"] = dict(self.headers)
        if self.body:
            out["extra_body"] = copy.deepcopy(self.body)
        return out


def _err(msg: str) -> RequestExtrasError:
    return RequestExtrasError(f"{ENV_VAR}: {msg}")


def _reject_constant(name: str) -> Any:
    raise ValueError(f"invalid JSON value {name}")


def parse_rules(raw: str | None) -> list[Rule]:
    """Validate the variable's value. Unset, empty or whitespace-only means no
    rules. Errors name the rule as "rule <i>: <field>: …"."""
    if raw is None or not raw.strip():
        return []
    try:
        data = json.loads(raw, parse_constant=_reject_constant)
    except ValueError as e:
        raise _err(f"invalid JSON: {e}") from None
    if not isinstance(data, list):
        raise _err("top level must be a JSON array")
    rules = []
    for i, item in enumerate(data):
        try:
            rules.append(_parse_rule(i, item))
        except RequestExtrasError as e:
            raise _err(f"rule {i}: {e}") from None
    return rules


def _parse_rule(i: int, item: Any) -> Rule:
    if not isinstance(item, dict):
        raise RequestExtrasError("must be an object with match and headers and/or body")
    unknown = sorted(k for k in item if k not in _ALLOWED_KEYS)
    if unknown:
        raise RequestExtrasError(f"{unknown[0]}: unknown key (allowed: match, headers, body)")
    match = item.get("match")
    if not isinstance(match, str) or match == "":
        raise RequestExtrasError("match: required, a non-empty string")
    headers = _parse_headers(item["headers"]) if "headers" in item else {}
    body = _parse_body(item["body"]) if "body" in item else {}
    if not headers and not body:
        raise RequestExtrasError(
            "neither headers nor body is set (an empty object counts as absent)")
    return Rule(index=i, match=match, headers=headers, body=body)


def _parse_headers(h: Any) -> dict[str, str]:
    if not isinstance(h, dict):
        raise RequestExtrasError("headers: must be an object of header name to string value")
    for name in sorted(h):
        if name == "":
            raise RequestExtrasError("headers: empty header name")
        if not _TOKEN.fullmatch(name):
            raise RequestExtrasError(f"headers.{name}: not a valid header name (RFC 7230 token)")
        if name.lower() in RESERVED_HEADERS:
            raise RequestExtrasError(f"headers.{name}: reserved header, set by the adapter")
        if not isinstance(h[name], str):
            raise RequestExtrasError(f"headers.{name}: value must be a string")
        if any((ord(c) < 0x20 and c != "\t") or ord(c) == 0x7F for c in h[name]):
            raise RequestExtrasError(
                f"headers.{name}: value must not contain control characters")
    return dict(h)


def _parse_body(b: Any) -> dict[str, Any]:
    if not isinstance(b, dict):
        raise RequestExtrasError("body: must be a JSON object")
    for k in sorted(b):
        if k == "":
            raise RequestExtrasError("body: empty key")
        if k in RESERVED_BODY_KEYS:
            raise RequestExtrasError(f"body.{k}: reserved key, set by the adapter")
        if any(c in k for c in SJSON_SYNTAX):
            raise RequestExtrasError(f"body.{k}: key must not contain any of . * ? | # @ \\")
    return dict(b)


def glob_match(pattern: str, s: str) -> bool:
    """Whole-string, case-sensitive glob: '*' matches any run of characters
    (including none and '/'), '?' exactly one character; everything else,
    including '[', ']' and '\\', is literal. Deliberately not fnmatch: open-bbcd
    implements the same syntax and the two must agree."""
    pi = si = 0
    star, mark = -1, 0
    while si < len(s):
        if pi < len(pattern) and pattern[pi] == "*":
            star, mark = pi, si
            pi += 1
        elif pi < len(pattern) and (pattern[pi] == "?" or pattern[pi] == s[si]):
            pi += 1
            si += 1
        elif star >= 0:
            pi = star + 1
            mark += 1
            si = mark
        else:
            return False
    while pi < len(pattern) and pattern[pi] == "*":
        pi += 1
    return pi == len(pattern)


def resolve(rules: list[Rule], model: str) -> Rule | None:
    """First rule whose glob matches model, or None."""
    for r in rules:
        if glob_match(r.match, model):
            return r
    return None


def log_rules(rules: list[Rule], role_models: dict[str, str]) -> None:
    """One line per rule (index, match, header names, body keys — never
    values), then the active rule per role. Silent without rules."""
    if not rules:
        return
    for r in rules:
        log.info("llm request extras rule index=%d match=%r headers=%r body_keys=%r",
                 r.index, r.match, sorted(r.headers), sorted(r.body))
    for role, model in role_models.items():
        active = resolve(rules, model)
        if active is None:
            log.info("llm request extras inactive role=%s model=%r", role, model)
        else:
            log.info("llm request extras active role=%s rule=%d match=%r model=%r",
                     role, active.index, active.match, model)
