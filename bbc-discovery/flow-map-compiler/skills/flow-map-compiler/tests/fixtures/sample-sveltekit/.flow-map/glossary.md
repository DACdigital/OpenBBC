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
| [`list-ping`](#list-ping) | TODO | [`ping-list`](capabilities/ping.md#ping-list) | `ping.list` |

## Intent anchors

### List Ping {#list-ping}

- **Role:** read
- **Capability:** [`ping-list`](capabilities/ping.md#ping-list)
- **Proposed tool:** `ping.list` (proposed — no MCP server yet)
- **User phrases:** TODO
- **What it does:** TODO: domain-level description of `list-ping`.


<!-- HUMAN id="glossary-additions" -->
<!-- /HUMAN -->
