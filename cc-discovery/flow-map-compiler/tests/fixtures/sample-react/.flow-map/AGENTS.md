---
schema_version: 1
generated_by: flow-map-compiler
generated_at: 2026-04-28T11:55:45+02:00
generated_from_sha: 67a486a5b18f4cd3cc4c8fe6a79868c3539abbff
app_name: sample-react
stack:
  framework: react
  version: "19.0.0"
  router: react-router-dom
  language: ts
counts:
  flows: 1
  capabilities: 1
  proposed_tools: 1
freshness:
  last_verified: 2026-04-28T11:55:45+02:00
  staleness_check: weekly
files:
  app_context: APP.md
  glossary: glossary.md
  flows_dir: flows/
  capabilities_dir: capabilities/
  proposed_tools: tools-proposed.json
---

# sample-react — flow map

<!-- AGENT id="summary" -->
A minimal Vite + React + react-router-dom application with one user-facing flow: profile editing. The agent surface is one route at /profile and one write capability against the user record.
<!-- /AGENT -->

## Reading order for agents

1. Load APP.md once per session.
2. For "tell me about X" / behavior questions → load `flows/<id>.md`
   matching the intent table below.
3. For "I need to do Y" / capability questions → load
   `capabilities/<name>.md`.
4. For unfamiliar terms → consult `glossary.md`.

## Overview

```mermaid
flowchart LR
  User --> update_profile_E["/Profile"]
  update_profile_E --> update_user_record_I[update-user-record]
  update_user_record_I --> users_C[users]
```

## Intent → flow

| User intent | Flow |
|---|---|
| Persist the user's edited profile fields to the backend | [update-profile](flows/update-profile.md) |

## Flows

| id | file | what it does |
|---|---|---|
| update-profile | [flows/update-profile.md](flows/update-profile.md) | Persist the user's edited profile fields to the backend |

## Capabilities

| name | file | proposed tools |
|---|---|---|
| users | [capabilities/users.md](capabilities/users.md) | 1 |

## Note on tool names

Tool names referenced anywhere in this wiki are *proposed* — derived
from frontend call sites. The actual MCP server does not exist yet. See
[`tools-proposed.json`](tools-proposed.json) for the full
machine-readable list intended for whoever wires up the MCP server.

Flow files do not name proposed tools at all; they reference intents
defined in [`glossary.md`](glossary.md), which is the single
indirection layer that maps intents to currently-proposed tool names.
When tools are renamed, only the glossary updates.

## Unresolved

None.

<!-- HUMAN id="agents-extra" -->
<!-- /HUMAN -->
