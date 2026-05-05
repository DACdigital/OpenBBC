---
schema_version: 1
---

# Glossary

One row per agent skill — a thin pivot table linking each skill to
its capability and proposed tool. The per-skill body lives in
[`skills/<id>.md`](skills/).

## Lookup table

| Skill | User phrases | Capability | Proposed tool |
|---|---|---|---|
| [`update-user-record`](skills/update-user-record.md) | "change my name", "update my email", "fix my profile", "my email is wrong" | [`users-update`](capabilities/users.md#users-update) | `users.update` |

<!-- HUMAN id="glossary-additions" -->
<!-- /HUMAN -->
