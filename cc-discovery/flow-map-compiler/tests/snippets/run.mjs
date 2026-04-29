#!/usr/bin/env node
// Per-rule-pack smoke tests. Each case feeds a known snippet to a single
// rule pack and asserts the extracted callsite shape. Run with:
//
//   node tests/snippets/run.mjs
//
// Exit code 0 = all green, 1 = at least one mismatch (with a diff).

import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const HERE = dirname(fileURLToPath(import.meta.url));
const RULES = join(HERE, "..", "..", "scripts", "rules");

const cases = [
  // ----- fetch -----
  {
    pack: "fetch.mjs",
    pkg: { dependencies: {} },
    snippet: `
      export async function getUser(id: string) {
        return fetch(\`/api/users/\${id}\`, {
          method: "GET",
          headers: { Authorization: \`Bearer \${token}\` },
        }).then((r) => r.json());
      }`,
    expect: [{ method: "GET", path: "/api/users/{id}", auth: "bearer", client: "fetch" }],
  },
  {
    pack: "fetch.mjs",
    pkg: { dependencies: {} },
    snippet: `await fetch("/api/health");`,
    expect: [{ method: "GET", path: "/api/health", client: "fetch" }],
  },

  // ----- axios -----
  {
    pack: "axios.mjs",
    pkg: { dependencies: { axios: "^1.0.0" } },
    snippet: `
      import axios from "axios";
      export const updateUser = (id, body) =>
        axios.patch(\`/api/users/\${id}\`, body, {
          headers: { Authorization: \`Bearer \${token}\` },
        });`,
    expect: [{ method: "PATCH", path: "/api/users/{id}", auth: "bearer", client: "axios" }],
  },
  {
    pack: "axios.mjs",
    pkg: { dependencies: { axios: "^1.0.0" } },
    snippet: `axios({ method: "DELETE", url: "/api/posts/{slug}" });`,
    expect: [{ method: "DELETE", path: "/api/posts/{slug}", client: "axios" }],
  },

  // ----- openapi-fetch -----
  {
    pack: "openapi-fetch.mjs",
    pkg: { dependencies: { "openapi-fetch": "^0.10.0" } },
    snippet: `
      const { data } = await client.GET("/users/{id}", {
        params: { path: { id } },
      });`,
    expect: [{ method: "GET", path: "/users/{id}", client: "openapi-fetch" }],
  },

  // ----- trpc -----
  {
    pack: "trpc.mjs",
    pkg: { dependencies: { "@trpc/client": "^11.0.0" } },
    snippet: `
      const { data } = trpc.users.byId.useQuery({ id });
      const m = trpc.users.update.useMutation();
      await m.mutateAsync({ id, name });`,
    expect: [
      { method: "TRPC-QUERY", path: "trpc:users.byId", client: "trpc" },
      { method: "TRPC-MUTATE", path: "trpc:users.update", client: "trpc" },
    ],
  },

  // ----- apollo -----
  {
    pack: "apollo.mjs",
    pkg: { dependencies: { "@apollo/client": "^3.0.0" } },
    snippet: `
      const UPDATE_USER = gql\`
        mutation UpdateUserRecord($id: ID!, $input: UserUpdateInput!) {
          updateUser(id: $id, input: $input) { id email }
        }
      \`;`,
    expect: [{ method: "GQL-MUTATION", path: "graphql:UpdateUserRecord", client: "apollo" }],
  },

  // ----- urql -----
  {
    pack: "urql.mjs",
    pkg: { dependencies: { urql: "^4.0.0" } },
    snippet: `
      const GetUsers = gql\`
        query GetUsers($limit: Int!) {
          users(limit: $limit) { id email }
        }
      \`;`,
    expect: [{ method: "GQL-QUERY", path: "graphql:GetUsers", client: "urql" }],
  },

  // ----- server-actions -----
  {
    pack: "server-actions.mjs",
    pkg: { dependencies: { next: "^14.0.0" } },
    snippet: `"use server";
      export async function updateProfile(formData) {
        // ...
      }
      export async function deleteAccount(id) {
        // ...
      }`,
    expect: [
      { method: "ACTION", path: "action:updateProfile", client: "server-actions" },
      { method: "ACTION", path: "action:deleteAccount", client: "server-actions" },
    ],
  },

  // ----- tanstack-query (delegator: emits no calls, but wrappers) -----
  {
    pack: "tanstack-query.mjs",
    pkg: { dependencies: { "@tanstack/react-query": "^5.0.0" } },
    snippet: `
      const q = useQuery({ queryKey: ["u", id], queryFn: () => getUser(id) });
      const m = useMutation({ mutationFn: updateUser });`,
    expect: [],
    expectWrappers: 2,
  },

  // ----- swr (delegator) -----
  {
    pack: "swr.mjs",
    pkg: { dependencies: { swr: "^2.0.0" } },
    snippet: `const { data } = useSWR("/api/users/" + id, fetcher);`,
    expect: [],
    expectWrappers: 1,
  },

  // ===== regression: false-positive guards =====

  // axios pack must NOT match Express app/router
  {
    pack: "axios.mjs",
    pkg: { dependencies: { axios: "^1.0.0" } },
    snippet: `
      import express from "express";
      const app = express();
      app.get('/users', (req, res) => res.json({}));
      app.post('/items', handler);
      const r = router.delete('/x', h);`,
    expect: [],
  },

  // axios pack must NOT match Map/Set/cache.delete
  {
    pack: "axios.mjs",
    pkg: { dependencies: { axios: "^1.0.0" } },
    snippet: `
      const m = new Map();
      m.delete(key);
      cache.delete(token);
      Map.prototype.delete.call(x, k);`,
    expect: [],
  },

  // fetch pack must NOT match prefetch / refetch
  {
    pack: "fetch.mjs",
    pkg: { dependencies: {} },
    snippet: `
      prefetch('/api/users');
      router.prefetch('/dashboard');
      const x = obj.refetch();`,
    expect: [],
  },

  // fetch nested options: Authorization header inside nested headers
  {
    pack: "fetch.mjs",
    pkg: { dependencies: {} },
    snippet: `
      await fetch('/api/secret', {
        method: 'POST',
        headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
        body: JSON.stringify({ a: 1 }),
      });`,
    expect: [{ method: "POST", path: "/api/secret", auth: "bearer", client: "fetch" }],
  },

  // axios nested options: Authorization in deeply-nested headers config
  {
    pack: "axios.mjs",
    pkg: { dependencies: { axios: "^1.0.0" } },
    snippet: `
      import axios from "axios";
      const api = axios.create({ baseURL: '/api' });
      await api.post('/items', body, {
        headers: { Authorization: 'Bearer x', 'X-Trace': 'y' },
      });`,
    expect: [{ method: "POST", path: "/items", auth: "bearer", client: "axios" }],
  },

  // Comments must not produce calls
  {
    pack: "fetch.mjs",
    pkg: { dependencies: {} },
    snippet: `
      // fetch('/api/legacy')   <- example only, do not use
      /* axios.get('/api/old')  <- removed */
      /** @example fetch('/api/jsdoc') */
      const real = "no-op";`,
    expect: [],
  },

  // tRPC optional chaining
  {
    pack: "trpc.mjs",
    pkg: { dependencies: { "@trpc/client": "^11.0.0" } },
    snippet: `
      const m = trpc?.users?.update?.useMutation();
      await m.mutateAsync({ id });`,
    expect: [{ method: "TRPC-MUTATE", path: "trpc:users.update", client: "trpc" }],
  },

  // tRPC aliased import
  {
    pack: "trpc.mjs",
    pkg: { dependencies: { "@trpc/react-query": "^11.0.0" } },
    snippet: `
      import { trpc as foo } from "@trpc/react-query";
      const q = foo.posts.list.useQuery();`,
    expect: [{ method: "TRPC-QUERY", path: "trpc:posts.list", client: "trpc" }],
  },

  // Apollo: gql passed directly to useQuery (most common pattern)
  {
    pack: "apollo.mjs",
    pkg: { dependencies: { "@apollo/client": "^3.0.0" } },
    snippet: `
      const { data } = useQuery(gql\`
        query GetUsersInline { users { id email } }
      \`);`,
    expect: [{ method: "GQL-QUERY", path: "graphql:GetUsersInline", client: "apollo" }],
  },

  // Apollo: gql in object property
  {
    pack: "apollo.mjs",
    pkg: { dependencies: { "@apollo/client": "^3.0.0" } },
    snippet: `
      const operations = {
        getOne: gql\`query GetOne { one { id } }\`,
      };`,
    expect: [{ method: "GQL-QUERY", path: "graphql:GetOne", client: "apollo" }],
  },

  // server-actions: CRLF line endings
  {
    pack: "server-actions.mjs",
    pkg: { dependencies: { next: "^14.0.0" } },
    snippet: "\"use server\";\r\n\r\nexport async function saveDraft(data) {\r\n  // ...\r\n}\r\n",
    expect: [{ method: "ACTION", path: "action:saveDraft", client: "server-actions" }],
  },

  // server-actions: arrow with inline directive
  {
    pack: "server-actions.mjs",
    pkg: { dependencies: { next: "^14.0.0" } },
    snippet: `
      export const submitForm = async (formData) => {
        "use server";
        // ...
      };`,
    expect: [{ method: "ACTION", path: "action:submitForm", client: "server-actions" }],
  },

  // openapi-fetch: template-literal path
  {
    pack: "openapi-fetch.mjs",
    pkg: { dependencies: { "openapi-fetch": "^0.10.0" } },
    snippet: `
      await client.GET(\`/users/\${id}/posts\`);`,
    expect: [{ method: "GET", path: "/users/{id}/posts", client: "openapi-fetch" }],
  },

  // openapi-fetch: destructured shorthand
  {
    pack: "openapi-fetch.mjs",
    pkg: { dependencies: { "openapi-fetch": "^0.10.0" } },
    snippet: `
      const { GET, POST } = client;
      await GET("/health");`,
    expect: [{ method: "GET", path: "/health", client: "openapi-fetch" }],
  },
];

let failed = 0;
for (const tc of cases) {
  const mod = await import(join(RULES, tc.pack));
  if (!mod.detect(tc.pkg)) {
    fail(tc, `detect() returned false for ${JSON.stringify(tc.pkg)}`);
    continue;
  }
  const result = mod.extract("test.tsx", tc.snippet) ?? {};
  const calls = result.calls ?? [];
  const wrappers = result.wrappers ?? [];

  if (typeof tc.expectWrappers === "number" && wrappers.length !== tc.expectWrappers) {
    fail(tc, `expected ${tc.expectWrappers} wrappers, got ${wrappers.length}`);
    continue;
  }
  if (calls.length !== tc.expect.length) {
    fail(tc, `expected ${tc.expect.length} calls, got ${calls.length}\n  got: ${JSON.stringify(calls, null, 2)}`);
    continue;
  }
  let ok = true;
  for (let i = 0; i < tc.expect.length; i++) {
    const exp = tc.expect[i];
    const act = calls[i];
    for (const k of Object.keys(exp)) {
      if (exp[k] !== act[k]) {
        fail(tc, `call[${i}].${k}: expected ${JSON.stringify(exp[k])}, got ${JSON.stringify(act[k])}`);
        ok = false;
        break;
      }
    }
    if (!ok) break;
  }
  if (ok) pass(tc);
}

if (failed > 0) {
  console.error(`\n${failed} case(s) failed`);
  process.exit(1);
} else {
  console.log(`\nall ${cases.length} cases passed`);
}

function pass(tc) {
  console.log(`  ok  ${tc.pack.padEnd(22)} ${(tc.expect[0]?.path ?? `wrappers=${tc.expectWrappers ?? 0}`)}`);
}
function fail(tc, msg) {
  failed++;
  console.error(`  FAIL ${tc.pack}: ${msg}`);
}
