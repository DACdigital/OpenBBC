---
schema_version: 1
capability: orders
summary: "Create orders"
tools:
  - tool: orders.create
    proposed: true
    does: "Create a new order"
    method: POST
    path: "/api/orders"
    auth: bearer
    confidence: high
    source: src/api/orders.ts:32
flows_using_this: [place-order]
---

# Orders

<!-- AGENT id="overview" -->
The orders capability.
<!-- /AGENT -->
