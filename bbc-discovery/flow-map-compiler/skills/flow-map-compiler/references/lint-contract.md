# Lint contract

The agent walks these 16 rules as a self-check after rendering and
must not ship a wiki that fails any of them. There is no `lint.mjs` in
the v0 skill — the agent is the linter. Report failures naming the
file and the rule number; do not stop on first failure, surface them
all, fix them, and re-render.

| #  | Rule |
|----|------|
| 1  | `AGENTS.md` body length ≤ 500 lines |
| 2  | `APP.md` body length ≤ 300 lines |
| 3  | Every `flows/*.md` between 60 and 400 lines |
| 4  | Every flow file's frontmatter has non-empty `description`, `user_phrases`, `skills_used`, `preconditions` |
| 5  | `description` field on every flow starts with `Use when` |
| 6  | Every skill listed in a flow's `skills_used[]` resolves to a `skills/<id>.md` file whose `capability_ref` points at a real anchor in some `capabilities/*.md` `tools` list |
| 7  | Every endpoint participant in any sequence diagram (when used) matches `^(GET\|POST\|PUT\|PATCH\|DELETE\|HEAD\|OPTIONS) /` OR is a logical tool name like `<word>.<word>`. One convention per file. |
| 8  | All Mermaid blocks parse |
| 9  | No `flows/*.md` contains HTTP method strings at the start of a line (`GET `, `POST `, etc.); no `fetch(`, no `axios.`, no `/api/` paths. Tool names appear only inside `skills/*.md`, never in flow bodies. |
| 10 | Every `<!-- HUMAN id="..." -->` block has a matching `<!-- /HUMAN -->`. Same for `<!-- AGENT id="..." -->` / `<!-- /AGENT -->` |
| 11 | `AGENTS.md` Skills table covers every skill id present under `skills/`; AGENTS.md Flows table covers every flow id present under `flows/` |
| 12 | `glossary.md` Skill column entries all resolve to a real `skills/<id>.md` file; Capability column entries all resolve to a real capability anchor |
| 13 | Two-hop integrity: every `skills/<id>.md` link target referenced from a `flows/*.md` exists, **and** every capability anchor referenced from a `skills/*.md` `capability_ref` exists in the linked `capabilities/*.md` |
| 14 | Every tool listed in `tools-proposed.json` is referenced by at least one capability file's `tools:` frontmatter, and vice versa |
| 15 | Every `proposed: true` flag is present where required (every capability `tools[]` entry, every `tools-proposed.json` entry). The skill must never emit `proposed: false`. Flows do not carry tool entries — they reference skills only. |
| 16 | Every `skills/<id>.md` `proposed_tool` matches a `tool:` entry in the capability file referenced by its `capability_ref`, and that capability also lists this skill's flow ids in `flows_using_this:` (transitive reachability) |

## Failure messages

Format: `<file>: rule <N> — <short reason>`. Example:

```
flows/update-profile.md: rule 5 — description must start with "Use when"
flows/update-profile.md: rule 13 — link target skills/write-user-profile.md not found
skills/write-user-profile.md: rule 13 — capability anchor #users-update not in capabilities/users.md
```

Surface every failure in the same pass; don't stop on the first one.

## Cross-link integrity (rule 13) — two hops

**Hop 1 — flow → skill.** For each `flows/*.md`, find every markdown
link of the form `(.../skills/<id>.md)` and every `skills_used[].skill_ref`.
The skills file must exist.

**Hop 2 — skill → capability.** For each `skills/*.md`, read the
`capability_ref` frontmatter field — a path of the form
`capabilities/<name>.md#<anchor>`. The capability file must exist;
the anchor must appear there.

A failure on either hop is reported with the file and rule:

```
flows/update-profile.md: rule 13 - link skills/write-user-profile.md not found
skills/write-user-profile.md: rule 13 - capability anchor #users-update not in capabilities/users.md
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

## Skill ↔ capability transitive reachability (rule 16)

For each `skills/<id>.md`, the `proposed_tool` field must equal some
`tool:` value inside the capability referenced by `capability_ref`. And
the capability's `flows_using_this:` must include every flow id listed
in the skill's `flows_using_this:`. This guarantees the three-way graph
(flow → skill → capability) stays consistent on edits.

```
skills/write-user-profile.md: rule 16 — proposed_tool "users.update" not in capabilities/users.md tools
capabilities/users.md: rule 16 — flow "update-profile" used by skills/write-user-profile.md but not in flows_using_this
```
