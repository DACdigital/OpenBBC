---
schema_version: 1
id: place-order
name: Place order
description: "Use when the user wants to check out"
intent: "Submit the cart as an order"
user_phrases:
  - "check out"
entry: src/pages/Cart.tsx
trigger: user clicks Place order
preconditions:
  - User is signed in
skills_used:
  - skill: place-order
    role: write
    skill_ref: ../skills/place-order.md
postconditions:
  - The cart is persisted as an order
side_effects: [audit-log-entry]
related_flows: []
confidence: high
workflow: |
  flowchart TD
    start([start]) --> s_place_order[place-order]
    s_place_order --> e([end])
---

# Place order

<!-- AGENT id="prose" -->
The user submits the cart.
<!-- /AGENT -->

## How the agent handles this

1. Confirm signed in.
2. Submit cart via place-order.
