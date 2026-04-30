# Lint contract

The agent walks these 15 rules as a self-check after rendering and
must not ship a wiki that fails any of them. There is no `lint.mjs` in
the v0 skill — the agent is the linter. Report failures naming the
file and the rule number; do not stop on first failure, surface them
all, fix them, and re-render.

| #  | Rule |
|----|------|
| 1  | `AGENTS.md` body length ≤ 500 lines |
| 2  | `APP.md` body length ≤ 300 lines |
| 3  | Every `flows/*.md` between 60 and 400 lines |
| 4  | Every flow file's frontmatter has non-empty `description`, `user_phrases`, `intents_used`, `preconditions` |
| 5  | `description` field on every flow starts with `Use when` |
| 6  | Every tool referenced in any flow file appears in some `capabilities/*.md` `tools` list |
| 7  | Every endpoint participant in any sequence diagram (when used) matches `^(GET\|POST\|PUT\|PATCH\|DELETE\|HEAD\|OPTIONS) /` OR is a logical tool name like `<word>.<word>`. One convention per file. |
| 8  | All Mermaid blocks parse |
| 9  | No `flows/*.md` contains HTTP method strings at the start of a line (`GET `, `POST `, etc.); no `fetch(`, no `axios.`, no `/api/` paths |
| 10 | Every `<!-- HUMAN id="..." -->` block has a matching `<!-- /HUMAN -->`. Same for `<!-- AGENT id="..." -->` / `<!-- /AGENT -->` |
| 11 | `AGENTS.md` Intent → flow table covers every flow id listed in the Flows table |
| 12 | `glossary.md` "Tool / capability" column entries all resolve to a real file or tool name |
| 13 | Every glossary anchor referenced from a `flows/*.md` link exists in `glossary.md`, **and** every capability anchor referenced from `glossary.md` exists in the linked `capabilities/*.md`. Two-hop integrity. |
| 14 | Every tool listed in `tools-proposed.json` is referenced by at least one capability file's `tools:` frontmatter, and vice versa |
| 15 | Every `proposed: true` flag is present where required (every flow `tools_used[]`, every capability `tools[]`). The skill must never emit `proposed: false`. |

## Failure messages

Format: `<file>: rule <N> — <short reason>`. Example:

```
flows/update-profile.md: rule 5 — description must start with "Use when"
flows/update-profile.md: rule 13 — link target #users-update not found in capabilities/users.md
```

Surface every failure in the same pass; don't stop on the first one.

## Cross-link integrity (rule 13) — two hops

**Hop 1 — flow → glossary.** For each `flows/*.md`, find every markdown
link of the form `(.../glossary.md#<intent>)`. The glossary file must
exist; the intent anchor must appear in `glossary.md` as either an
explicit `{#<intent>}` or the auto-slug of a heading.

**Hop 2 — glossary → capability.** For `glossary.md`, find every link of
the form `(capabilities/<name>.md#<anchor>)` (in either the table cells
or the per-intent anchor sections). The capability file must exist; the
anchor must appear there.

A failure on either hop is reported with the file and rule:

```
flows/update-profile.md: rule 13 - intent #write-user-profile not in glossary.md
glossary.md: rule 13 - capability anchor #users-update not in capabilities/users.md
```

Anchor matching: `{#<id>}` explicit, or auto-generated slug from a heading
(lowercase, strip non-`[a-z0-9\s-]`, spaces → hyphens). The skill always
emits explicit anchors; the slug fallback covers hand-edited files.

## Bidirectional consistency (rule 14)

Build a set `T_json` from `tools-proposed.json` (`tools[].proposed_name`).
Build a set `T_caps` by union of every `capabilities/*.md` frontmatter
`tools[].tool`. They must be equal. Report each direction separately:

```
tools-proposed.json: rule 14 — tool "users.delete" not present in any capability frontmatter
capabilities/users.md: rule 14 — tool "users.archive" not present in tools-proposed.json
```
