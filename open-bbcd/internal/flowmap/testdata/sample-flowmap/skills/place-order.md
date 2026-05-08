---
schema_version: 1
id: place-order
name: Place order
description: "Use when the user wants to convert their cart"
user_phrases:
  - "check out"
role: write
capability_ref: capabilities/orders.md#orders-create
proposed_tool: orders.create
flows_using_this: [place-order]
confidence: high
---

# Place order

<!-- AGENT id="overview" -->
Submit the cart as an order.
<!-- /AGENT -->
