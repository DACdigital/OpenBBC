---
schema_version: 1
---

# Glossary

The glossary is the indirection layer between flows (intent only) and
capabilities (HTTP detail + proposed tool name). When the MCP server is
built and tools are renamed, only the "Proposed tool" column here
updates — flow files do not churn.

## Lookup table

| Intent | User phrases | Capability | Proposed tool |
|---|---|---|---|
| [`update-user-record`](#update-user-record) | "change my name", "update my email", "fix my profile", "my email is wrong" | [`users-update`](capabilities/users.md#users-update) | `users.update` |

## Intent anchors

### Update User Record {#update-user-record}

- **Role:** write
- **Capability:** [`users-update`](capabilities/users.md#users-update)
- **Proposed tool:** `users.update` (proposed — no MCP server yet)
- **User phrases:** "change my name", "update my email", "fix my profile", "my email is wrong"
- **What it does:** Persist the user's edited profile fields to the backend


<!-- HUMAN id="glossary-additions" -->
<!-- /HUMAN -->
