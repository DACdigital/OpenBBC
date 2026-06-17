---
schema_version: 2
---

# Glossary

One row per agent skill — a thin pivot table linking each skill to its
suggested endpoints and the flows that surface it. The per-skill body
(domain vocabulary, endpoint selection guide, failure modes) lives in
[`skills/<id>.md`](skills/). Glossary is the catalog only.

## Lookup table

| Skill | User phrases | Suggested endpoints | Flows |
|---|---|---|---|
| [`health`](skills/health.md) | "is the backend up", "ping the server", "show me the ping log" | [`ping.list`](endpoints/ping.list.md) | [view-home](flows/view-home.md) |

<!-- HUMAN id="glossary-additions" -->
<!-- /HUMAN -->
