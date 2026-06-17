# Lint contract

The agent walks these 15 rules as a self-check after rendering and
must not ship a wiki that fails any of them. There is no `lint.mjs` in
the v2 skill — the agent is the linter. Report failures naming the
file and the rule number; do not stop on first failure, surface them
all, fix them, and re-render.

| #  | Rule |
|----|------|
| 1  | `AGENTS.md` body length ≤ 500 lines |
| 2  | `APP.md` body length ≤ 300 lines |
| 3  | Every `flows/*.md` between 60 and 400 lines |
| 4  | Every flow file's frontmatter has non-empty `description`, `user_phrases`, `skills_used`, `preconditions` |
| 5  | `description` field on every flow starts with `Use when` |
| 6  | Every skill listed in a flow's `skills_used[]` resolves to a `skills/<id>.md` file. Every endpoint listed in a skill's `suggested_endpoints[]` resolves to an `endpoints/<id>.md` file. |
| 7  | Every endpoint participant in any sequence diagram (when used) matches `^(GET\|POST\|PUT\|PATCH\|DELETE\|HEAD\|OPTIONS) /` OR is a logical endpoint id like `<word>.<word>`. One convention per file. |
| 8  | All Mermaid blocks parse |
| 9  | No `flows/*.md` or `skills/*.md` body contains HTTP method strings at line start (`GET `, `POST `, etc.), `fetch(`, `axios.`, or `/api/` paths. HTTP detail lives only in `endpoints/*.md`. |
| 10 | Every `<!-- HUMAN id="..." -->` block has a matching `<!-- /HUMAN -->`. Same for `<!-- AGENT id="..." -->` / `<!-- /AGENT -->`. |
| 11 | `AGENTS.md` Skills table covers every skill id under `skills/`; AGENTS.md Flows table covers every flow id under `flows/`; AGENTS.md Endpoints table covers every endpoint id under `endpoints/`. |
| 12 | `glossary.md` Skill column entries all resolve to a real `skills/<id>.md` file; every Suggested endpoint cell entry resolves to a real `endpoints/<id>.md` file. |
| 13 | Two-hop integrity: every `skills/<id>.md` link target referenced from a `flows/*.md` exists; every endpoint id referenced from a `skills/*.md` `suggested_endpoints[]` exists in `endpoints/`. |
| 14 | Round-trip: every `endpoints/<id>.md` `used_by_skills[]` entry names a real `skills/<id>.md`, and that skill's `suggested_endpoints[]` includes this endpoint id. Conversely, every `suggested_endpoints[].endpoint` value appears in the named endpoint's `used_by_skills[]`. |
| 15 | Every flow file's frontmatter has a `workflow:` field; it is a multiline mermaid `flowchart` block; every `id[<skill-id>]` skill node's label appears in `skills_used[].skill` on the same flow; every node id referenced in an edge appears in the node list; every flowchart parses. |

## Failure messages

Format: `<file>: rule <N> — <short reason>`. Example:

```
flows/checkout.md: rule 5 — description must start with "Use when"
flows/checkout.md: rule 13 — link target skills/shopping.md not found
skills/shopping.md: rule 13 — endpoint id orders.create not present under endpoints/
```

Surface every failure in the same pass; don't stop on the first one.

## Cross-link integrity (rule 13) — two hops

**Hop 1 — flow → skill.** For each `flows/*.md`, find every markdown
link of the form `(.../skills/<id>.md)` and every `skills_used[].skill_ref`.
The skills file must exist.

**Hop 2 — skill → endpoint.** For each `skills/*.md`, read the
`suggested_endpoints[]` frontmatter list. Every `endpoint:` value must
resolve to an existing `endpoints/<id>.md` file.

A failure on either hop is reported with the file and rule:

```
flows/checkout.md: rule 13 — link skills/shopping.md not found
skills/shopping.md: rule 13 — endpoint orders.create not present under endpoints/
```

Anchor matching: not used in v2 (each endpoint is a whole file, not an anchor in
a larger capability file). Skill→endpoint resolution is filename-only.

## Skill ↔ endpoint round-trip (rule 14)

Build a set `E_used_by_skills` by union of every `endpoints/*.md` frontmatter
`used_by_skills[]` value. Build a set `E_suggested` by union of every
`skills/*.md` frontmatter `suggested_endpoints[].endpoint` value scoped to the
declaring skill. The two must agree pair-wise:

- For each `skills/<id>.md` whose `suggested_endpoints[]` includes endpoint
  `E`, `endpoints/<E>.md` `used_by_skills[]` must include `<id>`.
- For each `endpoints/<E>.md` whose `used_by_skills[]` includes `<id>`,
  `skills/<id>.md` `suggested_endpoints[]` must include `E`.

Report each direction separately:

```
endpoints/orders.create.md: rule 14 — used_by_skills includes "shopping" but skills/shopping.md does not list this endpoint
skills/shopping.md: rule 14 — suggested_endpoints includes "products.list" but endpoints/products.list.md does not list this skill
```

## Workflow well-formedness (rule 15)

For each `flows/<id>.md`:

1. The frontmatter has a `workflow:` field whose value is a string.
2. The string starts with `flowchart TD` (or `flowchart LR`) followed by mermaid flowchart node and edge syntax.
3. Every node declared with `id[<label>]` (rectangle = skill call) has its label exactly equal to some `skills_used[].skill` entry on the same flow. Mismatches mean either the skill list is wrong or the workflow references something that should be added.
4. Every edge endpoint is a declared node id.
5. The block parses (rule 8 covers all mermaid blocks; rule 15 narrows to skill-id correspondence).

Failure messages:

```
flows/<id>.md: rule 15 — workflow node "s_foo[do-foo]" references skill "do-foo" not in skills_used[]
flows/<id>.md: rule 15 — workflow edge from "s_a" to "s_b" but "s_b" is not declared
flows/<id>.md: rule 15 — workflow field missing from frontmatter
```
