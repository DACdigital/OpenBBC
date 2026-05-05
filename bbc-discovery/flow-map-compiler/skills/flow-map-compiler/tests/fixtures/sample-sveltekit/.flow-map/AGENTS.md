---
schema_version: 1
generated_by: flow-map-compiler
generated_at: 2026-04-29T14:46:11+02:00
generated_from_sha: 6d8d83e4a61789ff4ffe16c053b01eaf8effe26a
app_name: sample-sveltekit
stack:
  framework: sveltekit
  version: "^2.0.0"
  router: filesystem
  language: js
counts:
  skills: 1
  flows: 1
  capabilities: 1
  proposed_tools: 1
freshness:
  last_verified: 2026-04-29T14:46:11+02:00
  staleness_check: weekly
files:
  app_context: APP.md
  glossary: glossary.md
  skills_dir: skills/
  flows_dir: flows/
  capabilities_dir: capabilities/
  proposed_tools: tools-proposed.json
---

# sample-sveltekit — flow map

<!-- AGENT id="summary" -->
TODO: one-paragraph summary of sample-sveltekit.
<!-- /AGENT -->

## Reading order for agents

An *agent skill* below is one navigable file describing a tool the
agent can invoke — not the same as a Claude Code SKILL.md plugin.

1. Load APP.md once per session.
2. For "I want to do X" → load `skills/<id>.md` (primary read).
3. For "what triggered this UI" → load `flows/<id>.md`.
4. For "how do I implement the MCP server for resource Y" →
   load `capabilities/<name>.md`.
5. `glossary.md` is the one-page index, not a primary read.

## Overview

```mermaid
flowchart LR
  User --> view_home_E["/"]
  view_home_E --> list_ping_S[list-ping]
  list_ping_S --> ping_C[ping]
```

## Skills

| skill | file | proposed tool |
|---|---|---|
| list-ping | [skills/list-ping.md](skills/list-ping.md) | `ping.list` |

## Flows

| id | file | what it does |
|---|---|---|
| view-home | [flows/view-home.md](flows/view-home.md) | TODO: one-line summary |

## Capabilities

| name | file | proposed tools |
|---|---|---|
| ping | [capabilities/ping.md](capabilities/ping.md) | 1 |

## Note on tool names

Tool names referenced anywhere in this wiki are *proposed* — derived
from frontend call sites. The actual MCP server does not exist yet. See
[`tools-proposed.json`](tools-proposed.json) for the full
machine-readable list intended for whoever wires up the MCP server.

Flow files do not name proposed tools at all; they link to skills under
[`skills/`](skills/). Each skill's `proposed_tool` frontmatter field is
the indirection layer — when tools are renamed, only those frontmatter
fields update; flow bodies stay byte-identical.

## Unresolved

None.

<!-- HUMAN id="agents-extra" -->
<!-- /HUMAN -->
