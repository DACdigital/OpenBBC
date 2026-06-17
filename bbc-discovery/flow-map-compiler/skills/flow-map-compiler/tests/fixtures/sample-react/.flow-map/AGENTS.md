---
schema_version: 2
generated_by: flow-map-compiler
generated_at: 2026-04-29T14:46:11+02:00
generated_from_sha: 6d8d83e4a61789ff4ffe16c053b01eaf8effe26a
app_name: sample-react
stack:
  framework: react
  version: "19.0.0"
  router: react-router-dom
  language: ts
counts:
  skills: 1
  flows: 1
  endpoints: 1
freshness:
  last_verified: 2026-04-29T14:46:11+02:00
  staleness_check: weekly
files:
  app_context: APP.md
  glossary: glossary.md
  skills_dir: skills/
  flows_dir: flows/
  endpoints_dir: endpoints/
---

# sample-react — flow map

<!-- AGENT id="summary" -->
A minimal Vite + React + react-router-dom application with one user-facing flow: profile editing. The agent surface is one route at /profile and one write endpoint against the user record.
<!-- /AGENT -->

## Reading order for agents

1. Load APP.md once per session.
2. For "I want to do X" → load `skills/<id>.md` (primary read — domain context).
3. For "what triggered this UI" → load `flows/<id>.md`.
4. For "what's the HTTP shape of call Y" → load `endpoints/<id>.md`.
5. `glossary.md` is the one-page pivot, not a primary read.

## Overview

```mermaid
flowchart LR
  User --> update_profile_E["/Profile"]
  update_profile_E --> s_account[account]
```

## Skills

| skill | file | suggests N endpoints |
|---|---|---|
| account | [skills/account.md](skills/account.md) | 1 |

## Flows

| id | file | what it does |
|---|---|---|
| update-profile | [flows/update-profile.md](flows/update-profile.md) | Persist the user's edited profile fields to the backend |

## Endpoints

| id | method | path | used by skills |
|---|---|---|---|
| `users.update` | PATCH | `/api/users/{id}` | account |

## Note on endpoint and tool naming

Endpoint ids referenced throughout this wiki are **proposed** — derived from
frontend call sites. They become MCP tool names downstream; the bundle and the
runtime agent refer to them as *tools*. Inside `.flow-map/`, they are always
*endpoints*.

## Unresolved

None.

<!-- HUMAN id="agents-extra" -->
<!-- /HUMAN -->
