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
| [`account`](skills/account.md) | "change my name", "fix my email", "update my profile" | [`users.update`](endpoints/users.update.md) | [update-profile](flows/update-profile.md) |

<!-- HUMAN id="glossary-additions" -->
<!-- /HUMAN -->
