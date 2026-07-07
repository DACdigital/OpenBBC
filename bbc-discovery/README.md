# bbc-discovery

A small Claude Code plugin marketplace for **discovery skills** —
plugins that prepare a frontend repo for consumption by an MCP-driven
agent.

Right now, this marketplace ships one plugin:

| Plugin | What it does |
|---|---|
| [`flow-map-compiler`](./flow-map-compiler/) | Compile a frontend repo into a `.flow-map/` agent wiki — flows, capabilities, and proposed MCP tools. |

> **Just want the skill?** You don't need this marketplace at all.
> Each plugin here wraps a plain Claude Code skill that can be dropped
> straight into `~/.claude/skills/`. See the plugin's own README
> ([flow-map-compiler](./flow-map-compiler/#install), Option A) for the
> standalone install one-liner. The plugin/marketplace path below is
> for people who want `/plugin update` semantics and namespaced
> invocation.

## How this fits into OpenBBC

`bbc-discovery` is one leg of the OpenBBC triad:

- **`aikdm/`** — Python CLI that generates and trains agents.
- **`open-bbcd/`** — Go service that hosts the backoffice UI, REST API, and deployed agent runtime (AG-UI streaming over `/deployed/*`, MCP-mediated calls into your backend).
- **`bbc-discovery/`** — this marketplace, currently shipping the `flow-map-compiler` plugin.

The `flow-map-compiler` plugin runs against a target frontend repo and produces a `.flow-map/` wiki (flows, capabilities, backend endpoints). That wiki is the input `aikdm generate-agent` consumes to build the initial agent bundle — so the plugin is the front door of the agent-generation pipeline.

## Install

### From GitHub (no clone needed)

```sh
# inside Claude Code
/plugin marketplace add https://raw.githubusercontent.com/DACdigital/OpenBBC/main/bbc-discovery/.claude-plugin/marketplace.json
/plugin install flow-map-compiler@bbc-discovery
```

This works because the plugin entry uses a `git-subdir` source that
points into the monorepo at `bbc-discovery/flow-map-compiler/`.

### From a local clone

```sh
/plugin marketplace add /absolute/path/to/OpenBBC/bbc-discovery
/plugin install flow-map-compiler@bbc-discovery
```

After install, the skill auto-triggers on phrases like *"make a flow
map for this repo"* or you can invoke it directly with
`/flow-map-compiler:flow-map-compiler`.

To update later:

```sh
/plugin marketplace update bbc-discovery
/plugin update flow-map-compiler@bbc-discovery
```

## Hosting note

The short `/plugin marketplace add owner/repo` form (e.g.
`/plugin marketplace add DACdigital/OpenBBC`) doesn't work for this
marketplace because Claude Code looks for `.claude-plugin/marketplace.json`
at the **repo root** and ours sits at `bbc-discovery/.claude-plugin/`.
There's an [open feature request](https://github.com/anthropics/claude-code/issues/20268)
to add a subpath option for github marketplace sources; until it lands,
the raw-URL form above is the canonical GitHub install.

A future cleanup is to split this directory into its own repo
(`DACdigital/bbc-discovery`) once the contents stabilize, which would
enable the short form.

## Layout

```
bbc-discovery/
├── .claude-plugin/marketplace.json      # this marketplace's catalog
├── README.md                            # this file
└── flow-map-compiler/                   # one plugin
    ├── .claude-plugin/plugin.json
    ├── README.md
    └── skills/flow-map-compiler/        # the SKILL.md + assets
```

## Adding more plugins

To add another discovery plugin to this marketplace later:

1. Create a sibling directory next to `flow-map-compiler/` with its own
   `.claude-plugin/plugin.json` and a `skills/<name>/SKILL.md`.
2. Add an entry to `plugins[]` in
   [`.claude-plugin/marketplace.json`](./.claude-plugin/marketplace.json).
3. Ship a README at the plugin root explaining what it does and how to
   use it.

## License

Apache-2.0.
