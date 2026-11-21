"""Rule-based fake judge: the reply content aikdm/eval/judge.py parses.

Request shape (judge_turn): the user message holds
  <turn_under_test>JSON</turn_under_test> and <criteria>1. text\\n2. text</criteria>.
Reply content: {"judgments":[{"criterion": <verbatim>, "passed": bool, "reason": str}]}
in criteria order. A criterion with [[require:X]] passes only if X appears in the
turn-under-test text; every other criterion passes.
"""

import json
import re

_REQUIRE = re.compile(r"\[\[require:(.+?)\]\]")
_NUMBERED = re.compile(r"^\d+\. ", re.M)


def _section(text: str, tag: str) -> str:
    m = re.search(rf"<{tag}>\n?(.*?)\n?</{tag}>", text, re.S)
    return m.group(1) if m else ""


def _strings(node) -> list[str]:
    if isinstance(node, str):
        return [node]
    if isinstance(node, dict):
        return [s for v in node.values() for s in _strings(v)]
    if isinstance(node, list):
        return [s for v in node for s in _strings(v)]
    return []


def _text_of(content) -> str:
    if isinstance(content, str):
        return content
    return "\n".join(_strings(content))


def _criteria(block: str) -> list[str]:
    starts = [m.start() for m in _NUMBERED.finditer(block)]
    out = []
    for i, s in enumerate(starts):
        end = starts[i + 1] if i + 1 < len(starts) else len(block)
        out.append(_NUMBERED.sub("", block[s:end].strip(), count=1))
    return out


def judge_reply(request_body: dict) -> dict:
    user_text = ""
    for msg in request_body.get("messages", []):
        if msg.get("role") == "user":
            user_text = _text_of(msg.get("content"))
    raw_turn = _section(user_text, "turn_under_test")
    try:
        transcript = "\n".join(_strings(json.loads(raw_turn))) + "\n" + raw_turn
    except ValueError:
        transcript = raw_turn
    judgments = []
    for criterion in _criteria(_section(user_text, "criteria")):
        missing = [x for x in _REQUIRE.findall(criterion) if x not in transcript]
        judgments.append({
            "criterion": criterion,
            "passed": not missing,
            "reason": f"required text not found: {missing[0]}" if missing else "ok",
        })
    return {"judgments": judgments}
