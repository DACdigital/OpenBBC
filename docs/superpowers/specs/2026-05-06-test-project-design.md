# Test project design — e-commerce lite

**Date:** 2026-05-06  
**Purpose:** Provide a realistic, runnable frontend+backend fixture for testing the `bbc-discovery` `flow-map-compiler` skill against a richer codebase than the existing single-flow fixtures.

---

## Goal

A small but realistic Node.js app that lives in `.test-project/` (gitignored) with:

- Hardcoded Bearer token auth
- Separate Express backend and React+Vite frontend
- Three user flows across three capability groups, two cross-related and one independent
- Fully runnable in dev mode (`npm run dev` in each directory)

---

## Flows and capabilities

| Flow id | Pages | Capability | Cross-related? |
|---|---|---|---|
| `browse-catalog` | `Products.tsx` | `products` | yes — with `place-order` |
| `place-order` | `Cart.tsx` | `orders` (reads `products`) | yes — with `browse-catalog` |
| `manage-profile` | `Profile.tsx` | `users` | **no** — fully independent |

Cross-relation between `browse-catalog` and `place-order`: the cart holds product IDs fetched during browsing, and `POST /api/orders` submits them. `manage-profile` only touches `GET /api/users/me` and `PATCH /api/users/me` — zero overlap with the other two.

### Proposed tools per capability

| Capability | Proposed tools |
|---|---|
| `products` | `products.list`, `products.get` |
| `orders` | `orders.create`, `orders.list` |
| `users` | `users.getMe`, `users.updateMe` |

---

## Auth model

- Hardcoded token: `tok_test_abc123`
- FE stores the token in `localStorage` under the key `token`
- FE sends `Authorization: Bearer <token>` on every API request
- BE checks the token in an Express middleware applied to all `/api/*` routes; returns `401` on mismatch
- No login flow — token is pre-seeded in the FE on startup

---

## Project structure

```
.test-project/
├── backend/                     # Node.js + Express, port 3001
│   ├── package.json
│   └── src/
│       ├── index.js             # app entry, mounts auth middleware + routes
│       ├── data/
│       │   ├── products.js      # array of 5 mock products
│       │   ├── orders.js        # array of 2 mock orders (starts populated)
│       │   └── users.js         # single mock user record
│       └── routes/
│           ├── products.js      # GET /api/products, GET /api/products/:id
│           ├── orders.js        # GET /api/orders, POST /api/orders
│           └── users.js         # GET /api/users/me, PATCH /api/users/me
└── frontend/                    # React + Vite + TypeScript, port 5173
    ├── package.json
    ├── vite.config.ts           # proxy /api → http://localhost:3001
    └── src/
        ├── App.tsx              # react-router-dom v6 routes: /products, /cart, /profile
        ├── api/
        │   ├── products.ts      # listProducts(), getProduct(id)
        │   ├── orders.ts        # listOrders(), createOrder(items)
        │   └── users.ts         # getMe(), updateMe(input)
        └── pages/
            ├── Products.tsx     # flow: browse-catalog — lists products, add to cart
            ├── Cart.tsx         # flow: place-order — shows cart, submits order
            └── Profile.tsx      # flow: manage-profile — view/edit profile
```

---

## Backend API surface

All routes require `Authorization: Bearer tok_test_abc123`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/products` | List all products |
| `GET` | `/api/products/:id` | Get single product |
| `GET` | `/api/orders` | List orders for the current user |
| `POST` | `/api/orders` | Create order (`{ items: [{ productId, quantity }] }`) |
| `GET` | `/api/users/me` | Get current user profile |
| `PATCH` | `/api/users/me` | Update profile (`{ name?, email? }`) |

---

## Frontend pages

### `Products.tsx` — browse-catalog flow
- On mount: calls `GET /api/products`, renders a list
- Each product has an "Add to cart" button — stores `{ productId, quantity }` into `localStorage` under `cart`
- Reads auth token from `localStorage.token`

### `Cart.tsx` — place-order flow
- On mount: reads cart from `localStorage.cart`, calls `GET /api/products` to enrich items with names/prices
- "Place order" button: calls `POST /api/orders` with cart contents, clears `localStorage.cart` on success
- Cross-capability: reads `products` capability to enrich display, writes to `orders` capability to submit

### `Profile.tsx` — manage-profile flow
- On mount: calls `GET /api/users/me`
- Editable name + email fields; "Save" button calls `PATCH /api/users/me`
- Fully independent — no relation to products or orders

---

## Running the project

```bash
# Backend
cd .test-project/backend && npm install && npm run dev   # listens on :3001

# Frontend
cd .test-project/frontend && npm install && npm run dev  # listens on :5173
```

---

## Gitignore

Add `.test-project/` to the root `.gitignore`.

---

## Why this tests flow-map-compiler well

- Three capability groups (`products`, `orders`, `users`) with distinct HTTP shapes
- Two flows sharing a capability (`Cart` reads `products` via a separate call, surfacing a cross-flow capability reference)
- One flow (`Profile`) completely isolated — tests that the compiler doesn't over-connect flows
- Bearer auth is statically detectable from the `Authorization` header in each `fetch` call
- TypeScript typed bodies and response shapes — tests the compiler's static shape resolution
- Vite proxy means all API calls use relative `/api/…` paths — tests path normalisation
