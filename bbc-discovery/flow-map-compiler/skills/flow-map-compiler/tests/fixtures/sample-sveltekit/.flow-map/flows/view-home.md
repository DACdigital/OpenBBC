---
schema_version: 1
id: view-home
name: View Home
description: "Use when this flow's preconditions hold (TODO: refine wording)."
intent: "Drives the view-home interaction"
user_phrases:
  - "TODO: phrase 1"
  - "TODO: phrase 2"
entry: src/routes/+page.svelte
trigger: TODO
preconditions:
  - TODO: precondition
skills_used:
  - skill: list-ping
    role: read
    skill_ref: ../skills/list-ping.md
postconditions:
  - TODO: postcondition
side_effects: []
related_flows: []
confidence: medium
---

# View Home

<!-- AGENT id="prose" -->
TODO: 2-4 sentence summary of this flow's purpose, agent behaviour, and any idempotence properties.
<!-- /AGENT -->

## Entry point

`src/routes/+page.svelte` — TODO: how the user reaches this entry

## How the agent handles this

1. Perform [list ping](../skills/list-ping.md).

## Decision points

(none documented yet)

## Sequence

```mermaid
sequenceDiagram
  actor User
  participant Agent
  participant T as MCP tools

  User->>Agent: "TODO: phrase 1"
  Agent->>T: list ping
  T-->>Agent: result
  Agent->>User: confirms outcome
```

## Failure modes

| What happens | What it means | What to do |
|---|---|---|
| Tool returns 401 | auth missing/expired | ask the user to sign in again |
| Tool returns non-2xx | operation failed | surface the error to the user |

## Skills used

- [list ping](../skills/list-ping.md) — read

<!-- HUMAN id="extra" -->
<!-- /HUMAN -->

## Unresolved

None.
