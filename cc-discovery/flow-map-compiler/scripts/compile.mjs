#!/usr/bin/env node
// Phase 2.3 - compile. Reads .flow-map/.cache/ artefacts plus an optional
// prose.json sidecar, splices back any existing <!-- HUMAN --> blocks,
// and writes the wiki tree.
//
// Flags:
//   --check                   drift only, no writes; nonzero exit on any
//                             diff *or* zombie file under .flow-map/.
//   --only flow:<id>          regenerate only flows/<id>.md (plus the
//                             three index files: AGENTS.md, glossary.md,
//                             tools-proposed.json — keeping rule 14
//                             bidirectional consistency intact).
//   --only capability:<name>  same shape, for capabilities/<name>.md.
//   --only changed            regenerate only files affected by source
//                             changes since AGENTS.md frontmatter's
//                             generated_from_sha, plus the index trio.
//
// Idempotence: timestamps and shas come from cache + git, never from
// wall-clock time. Two consecutive runs on unchanged source produce
// byte-identical output.

import {
  readFileSync,
  writeFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  statSync,
} from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve, join, dirname, basename } from "node:path";

const args = parseArgs(process.argv.slice(2));

// File names referenced from a path are validated against this. We
// interpolate flow.id and cap.name into output paths; any value with
// `..`, `/`, leading dot, etc. is rejected.
const SAFE_NAME = /^[a-z0-9][a-z0-9_-]*$/;

async function main() {
  const repoRoot = resolve(args.positional[0] ?? ".");
  const flowMapRoot = join(repoRoot, ".flow-map");
  const cacheDir = join(flowMapRoot, ".cache");

  const recon = readJSON(join(cacheDir, "recon.json"));
  const endpoints = readJSON(join(cacheDir, "endpoints.json"));
  const flows = readJSON(join(cacheDir, "flows.json")).flows;
  const tools = readJSON(join(cacheDir, "tools.json"));
  const prose = readJSONOpt(join(cacheDir, "prose.json")) ?? {};

  if (endpoints.unresolved_rate > 0.25) {
    console.error(`compile: refusing - unresolved rate ${(endpoints.unresolved_rate * 100).toFixed(1)}% > 25%`);
    process.exit(1);
  }

  // Validate every name we'll interpolate into an output path.
  for (const f of flows) assertSafeName("flow", f.id);
  for (const c of tools.capabilities) assertSafeName("capability", c.name);

  const sha = gitSha(repoRoot) ?? "unknown";
  const ts = gitCommitISO(repoRoot, sha) ?? "1970-01-01T00:00:00Z";
  const appName = basename(repoRoot);

  const humanBlocks = collectHumanBlocks(flowMapRoot);

  // Pre-compute selection. --only changed needs the previous sha from
  // an existing AGENTS.md before we render anything new.
  const changedSet = args.only === "changed"
    ? computeChangedSelection(repoRoot, flowMapRoot, flows, tools)
    : null;

  const filesToWrite = {};

  // Index files (AGENTS.md, glossary.md, tools-proposed.json) are
  // *always* regenerated, regardless of --only mode, because rule 14
  // (tools-proposed.json ↔ capability frontmatter consistency) and
  // rule 13 (flow→glossary→capability link integrity) only hold if
  // the index files match the per-file content.
  filesToWrite["AGENTS.md"] = renderAgents({ appName, recon, flows, tools, prose, sha, ts });
  filesToWrite["APP.md"]    = renderApp({ recon, prose });
  filesToWrite["glossary.md"] = renderGlossary({ tools, prose });
  filesToWrite["tools-proposed.json"] = renderToolsProposed({ tools, sha, ts });

  for (const flow of flows) {
    if (!shouldWriteFlow(args, flow.id, changedSet)) continue;
    filesToWrite[`flows/${flow.id}.md`] = renderFlow({ flow, tools, prose });
  }
  for (const cap of tools.capabilities) {
    if (!shouldWriteCapability(args, cap.name, changedSet)) continue;
    filesToWrite[`capabilities/${cap.name}.md`] = renderCapability({ cap, tools, prose });
  }

  for (const [rel, content] of Object.entries(filesToWrite)) {
    if (rel.endsWith(".json")) continue;
    filesToWrite[rel] = spliceHumanBlocks(rel, content, humanBlocks[rel] ?? new Map());
  }

  if (args.check) {
    const diffs = checkDrift(flowMapRoot, filesToWrite, args, changedSet, flows, tools);
    if (diffs.length === 0) {
      process.stdout.write("compile --check: no drift\n");
      process.exit(0);
    } else {
      for (const d of diffs) console.error(`drift: ${d}`);
      process.exit(1);
    }
  }

  for (const [rel, content] of Object.entries(filesToWrite)) {
    const abs = join(flowMapRoot, rel);
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, content);
  }
  process.stdout.write(`compile: wrote ${Object.keys(filesToWrite).length} file(s)\n`);
}

function assertSafeName(kind, name) {
  if (typeof name !== "string" || !SAFE_NAME.test(name)) {
    console.error(`compile: refusing — unsafe ${kind} name ${JSON.stringify(name)}; expected ${SAFE_NAME}`);
    process.exit(1);
  }
}

// ---------- argument parsing ----------

function parseArgs(argv) {
  const out = { positional: [], check: false, only: null };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--check") out.check = true;
    else if (a === "--only") out.only = argv[++i];
    else if (a.startsWith("--only=")) out.only = a.slice("--only=".length);
    else out.positional.push(a);
  }
  return out;
}

function shouldWriteFlow(args, id, changedSet) {
  if (!args.only) return true;
  if (args.only === "changed") return changedSet?.flows?.has(id) ?? false;
  if (args.only.startsWith("flow:")) return args.only.slice("flow:".length) === id;
  // --only capability:* doesn't write per-flow files
  return false;
}

function shouldWriteCapability(args, name, changedSet) {
  if (!args.only) return true;
  if (args.only === "changed") return changedSet?.capabilities?.has(name) ?? false;
  if (args.only.startsWith("capability:")) return args.only.slice("capability:".length) === name;
  return false;
}

// --only changed: figure out which flows / capabilities are touched by
// source changes since AGENTS.md's recorded sha. If no prior sha exists
// or `git diff` fails, fall back to "regenerate everything" so the user
// isn't silently left with stale files.
function computeChangedSelection(repoRoot, flowMapRoot, flows, tools) {
  const prev = previousSha(flowMapRoot);
  if (!prev) {
    process.stdout.write("compile --only changed: no previous sha; regenerating all\n");
    return { flows: new Set(flows.map((f) => f.id)), capabilities: new Set(tools.capabilities.map((c) => c.name)) };
  }
  const changedFiles = gitDiffFiles(repoRoot, prev);
  if (!changedFiles) {
    process.stdout.write("compile --only changed: git diff unavailable; regenerating all\n");
    return { flows: new Set(flows.map((f) => f.id)), capabilities: new Set(tools.capabilities.map((c) => c.name)) };
  }
  const changedSet = new Set(changedFiles);

  const flowSet = new Set();
  for (const f of flows) {
    const sources = collectFlowSources(f);
    if (sources.some((s) => changedSet.has(s))) flowSet.add(f.id);
  }
  const capSet = new Set();
  for (const cap of tools.capabilities) {
    const capTools = tools.tools.filter((t) => t.capability === cap.name);
    const sources = capTools.flatMap((t) => (t.source ?? []).map((s) => s.file));
    if (sources.some((s) => changedSet.has(s))) capSet.add(cap.name);
  }
  return { flows: flowSet, capabilities: capSet };
}

function previousSha(flowMapRoot) {
  const agents = join(flowMapRoot, "AGENTS.md");
  if (!existsSync(agents)) return null;
  const text = readFileSync(agents, "utf8");
  const m = text.match(/^---\s*\n([\s\S]*?)\n---/);
  if (!m) return null;
  const sha = m[1].match(/^generated_from_sha:\s*(\S+)/m);
  return sha ? sha[1] : null;
}

function gitDiffFiles(repoRoot, prevSha) {
  try {
    const out = execFileSync("git", ["diff", "--name-only", `${prevSha}..HEAD`], {
      cwd: repoRoot, stdio: ["ignore", "pipe", "ignore"],
    }).toString();
    return out.split("\n").map((s) => s.trim()).filter(Boolean);
  } catch { return null; }
}

function collectFlowSources(flow) {
  const seen = new Set();
  if (flow.entry) seen.add(flow.entry);
  for (const c of flow.calls ?? []) {
    for (const s of c.source ?? []) if (s?.file) seen.add(s.file);
  }
  for (const u of flow.unresolved ?? []) if (u?.file) seen.add(u.file);
  return [...seen];
}

// ---------- helpers ----------

function readJSON(p) { return JSON.parse(readFileSync(p, "utf8")); }
function readJSONOpt(p) { return existsSync(p) ? readJSON(p) : null; }

function gitSha(repoRoot) {
  try {
    return execFileSync("git", ["rev-parse", "HEAD"], { cwd: repoRoot, stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
  } catch { return null; }
}

function gitCommitISO(repoRoot, sha) {
  try {
    return execFileSync("git", ["show", "-s", "--format=%cI", sha], { cwd: repoRoot, stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
  } catch { return null; }
}

// ---- HUMAN block handling ----

const HUMAN_OPEN_RE = /<!--\s*HUMAN\s+id="([^"]+)"\s*-->/g;
const HUMAN_CLOSE_RE = /<!--\s*\/HUMAN\s*-->/g;
const HUMAN_PAIR_RE = /<!--\s*HUMAN\s+id="([^"]+)"\s*-->([\s\S]*?)<!--\s*\/HUMAN\s*-->/g;

function collectHumanBlocks(flowMapRoot) {
  const out = {};
  if (!existsSync(flowMapRoot)) return out;
  for (const rel of walkRel(flowMapRoot)) {
    if (!rel.endsWith(".md")) continue;
    const text = readFileSync(join(flowMapRoot, rel), "utf8");
    validateHumanMarkers(rel, text);
    const blocks = new Map();
    for (const m of text.matchAll(HUMAN_PAIR_RE)) {
      if (blocks.has(m[1])) {
        console.error(`compile: refusing — duplicate HUMAN id ${JSON.stringify(m[1])} in ${rel}`);
        process.exit(1);
      }
      blocks.set(m[1], m[2]);
    }
    if (blocks.size > 0) out[rel] = blocks;
  }
  return out;
}

function validateHumanMarkers(rel, text) {
  const opens = [...text.matchAll(HUMAN_OPEN_RE)].length;
  const closes = [...text.matchAll(HUMAN_CLOSE_RE)].length;
  if (opens !== closes) {
    console.error(`compile: refusing — ${rel} has ${opens} HUMAN open marker(s) and ${closes} close marker(s); fix manually`);
    process.exit(1);
  }
}

function walkRel(dir, prefix = "") {
  const out = [];
  let entries;
  try { entries = readdirSync(dir); } catch { return out; }
  for (const name of entries.sort()) {
    if (name === ".cache") continue;
    const rel = prefix === "" ? name : prefix + "/" + name;
    const abs = join(dir, name);
    let s;
    try { s = statSync(abs); } catch { continue; }
    if (s.isDirectory()) out.push(...walkRel(abs, rel));
    else out.push(rel);
  }
  return out;
}

// Splice preserved HUMAN blocks back into a freshly-rendered file. If
// the new file no longer has a slot for an existing block, append it
// under "## Orphaned human notes" so the user's content is never lost.
function spliceHumanBlocks(rel, content, existingBlocks) {
  validateHumanMarkers(rel + " (rendered)", content);
  const slotsInNew = new Set();
  let result = content.replace(HUMAN_PAIR_RE, (whole, id) => {
    slotsInNew.add(id);
    if (existingBlocks.has(id)) {
      const body = existingBlocks.get(id);
      return `<!-- HUMAN id="${id}" -->${body}<!-- /HUMAN -->`;
    }
    return whole;
  });
  const orphans = [];
  for (const [id, body] of existingBlocks) {
    if (!slotsInNew.has(id)) orphans.push({ id, body });
  }
  if (orphans.length > 0) {
    let block = "\n## Orphaned human notes\n\n";
    for (const o of orphans.sort((a, b) => a.id.localeCompare(b.id))) {
      block += `### id="${o.id}"\n\n<!-- HUMAN id="${o.id}" -->${o.body}<!-- /HUMAN -->\n\n`;
    }
    result += block;
  }
  return result;
}

function checkDrift(flowMapRoot, freshFiles, args, changedSet, flows, tools) {
  const diffs = [];
  for (const [rel, fresh] of Object.entries(freshFiles)) {
    const abs = join(flowMapRoot, rel);
    if (!existsSync(abs)) {
      diffs.push(`${rel}: missing on disk`);
      continue;
    }
    const onDisk = readFileSync(abs, "utf8");
    if (onDisk !== fresh) diffs.push(`${rel}: differs`);
  }

  // Zombie detection: walk the on-disk tree, flag any tracked file that
  // we wouldn't have produced this run. In a full run (no --only), every
  // expected file shows up in `freshFiles`; in a targeted run, expand
  // the expected set to the union of "would write" + "out of scope but
  // already known to exist".
  if (args.only) return diffs; // targeted run — don't flag siblings
  const expected = new Set(Object.keys(freshFiles));
  for (const rel of walkRel(flowMapRoot)) {
    if (!rel.endsWith(".md") && !rel.endsWith(".json")) continue;
    if (expected.has(rel)) continue;
    diffs.push(`${rel}: zombie (no longer produced; delete it)`);
  }
  return diffs;
}

// ---------- renderers ----------

function renderAgents({ appName, recon, flows, tools, prose, sha, ts }) {
  const summary = prose?.AGENTS?.summary ?? `TODO: one-paragraph summary of ${appName}.`;
  const flowRows = flows.map((f) =>
    `| ${f.id} | [flows/${f.id}.md](flows/${f.id}.md) | ${proseString(prose?.flows?.[f.id]?.intent) ?? "TODO: one-line summary"} |`,
  ).join("\n");
  const capRows = tools.capabilities.map((c) =>
    `| ${c.name} | [capabilities/${c.name}.md](capabilities/${c.name}.md) | ${c.tool_count} |`,
  ).join("\n");
  const intentRows = tools.intents.map((i) => {
    const flowId = i.used_by_flows[0];
    if (!flowId) return null;
    return `| ${proseString(prose?.flows?.[flowId]?.intent) ?? i.intent.replace(/-/g, " ")} | [${flowId}](flows/${flowId}.md) |`;
  }).filter(Boolean).join("\n");

  const overviewLines = [];
  const safe = (id) => id.replace(/[^a-z0-9]/gi, "_");
  for (const flow of flows) {
    overviewLines.push(`  User --> ${safe(flow.id)}_E["${flow.route}"]`);
    for (const c of flow.calls) {
      const tool = tools.tools.find((t) => t.method === c.method && t.path === c.path);
      const intent = tools.intents.find((i) => i.proposed_tool === tool?.proposed_name);
      if (!intent) continue;
      overviewLines.push(`  ${safe(flow.id)}_E --> ${safe(intent.intent)}_I[${intent.intent}]`);
      overviewLines.push(`  ${safe(intent.intent)}_I --> ${safe(intent.capability)}_C[${intent.capability}]`);
    }
  }

  return `---
schema_version: 1
generated_by: flow-map-compiler
generated_at: ${ts}
generated_from_sha: ${sha}
app_name: ${appName}
stack:
  framework: ${recon.framework.adapter}
  version: "${recon.framework.version ?? "unknown"}"
  router: ${recon.framework.router}
  language: ${recon.framework.language}
counts:
  flows: ${flows.length}
  capabilities: ${tools.capabilities.length}
  proposed_tools: ${tools.tools.length}
freshness:
  last_verified: ${ts}
  staleness_check: weekly
files:
  app_context: APP.md
  glossary: glossary.md
  flows_dir: flows/
  capabilities_dir: capabilities/
  proposed_tools: tools-proposed.json
---

# ${appName} — flow map

<!-- AGENT id="summary" -->
${summary}
<!-- /AGENT -->

## Reading order for agents

1. Load APP.md once per session.
2. For "tell me about X" / behavior questions → load \`flows/<id>.md\`
   matching the intent table below.
3. For "I need to do Y" / capability questions → load
   \`capabilities/<name>.md\`.
4. For unfamiliar terms → consult \`glossary.md\`.

## Overview

\`\`\`mermaid
flowchart LR
${overviewLines.join("\n")}
\`\`\`

## Intent → flow

| User intent | Flow |
|---|---|
${intentRows}

## Flows

| id | file | what it does |
|---|---|---|
${flowRows}

## Capabilities

| name | file | proposed tools |
|---|---|---|
${capRows}

## Note on tool names

Tool names referenced anywhere in this wiki are *proposed* — derived
from frontend call sites. The actual MCP server does not exist yet. See
[\`tools-proposed.json\`](tools-proposed.json) for the full
machine-readable list intended for whoever wires up the MCP server.

Flow files do not name proposed tools at all; they reference intents
defined in [\`glossary.md\`](glossary.md), which is the single
indirection layer that maps intents to currently-proposed tool names.
When tools are renamed, only the glossary updates.

## Unresolved

${endpointsUnresolvedSummary(flows)}

<!-- HUMAN id="agents-extra" -->
<!-- /HUMAN -->
`;
}

function endpointsUnresolvedSummary(flows) {
  const total = flows.reduce((acc, f) => acc + (f.unresolved?.length ?? 0), 0);
  if (total === 0) return "None.";
  return `${total} unresolved call site(s). See per-flow "Unresolved" section.`;
}

function renderApp({ recon, prose }) {
  const overview = prose?.APP?.overview ?? `TODO: 2-3 sentence app context.`;
  return `---
schema_version: 1
framework:
  name: ${recon.framework.adapter}
  version: "${recon.framework.version ?? "unknown"}"
  router: ${recon.framework.router}
api_clients: [${recon.api_clients.join(", ")}]
api_base_url:
  source: ${recon.api_base_url.source}
  name: ${recon.api_base_url.name ?? "null"}
  default: "${recon.api_base_url.default}"
auth:
  type: ${recon.auth.type}
  token_source: ${recon.auth.token_source ?? "null"}
  refresh: ${recon.auth.refresh ?? "null"}
providers: [${recon.providers.join(", ")}]
---

# App context

<!-- AGENT id="overview" -->
${overview}
<!-- /AGENT -->

## Stack

- ${recon.framework.adapter} ${recon.framework.version ?? ""}
- Language: ${recon.framework.language}
- API clients: ${recon.api_clients.join(", ")}

## Invariants

${prose?.APP?.invariants_md ?? "1. TODO: list system-wide invariants."}

## Auth model

${prose?.APP?.auth_model ?? "TODO: where tokens come from, how attached, refresh, 401 behavior."}

## Conventions

${prose?.APP?.conventions_md ?? "- TODO: cross-flow patterns."}

## Boundaries

${prose?.APP?.boundaries_md ?? "1. TODO: list things the agent must never do."}

<!-- HUMAN id="extra-context" -->
<!-- /HUMAN -->
`;
}

function renderGlossary({ tools, prose }) {
  const intentRows = tools.intents.map((i) => {
    const flowId = i.used_by_flows[0];
    const phrases = prose?.flows?.[flowId]?.user_phrases?.map((q) => `"${q}"`).join(", ") ?? "TODO";
    return `| [\`${i.intent}\`](#${i.intent}) | ${phrases} | [\`${i.capability_anchor}\`](capabilities/${i.capability}.md#${i.capability_anchor}) | \`${i.proposed_tool}\` |`;
  }).join("\n");

  const intentSections = tools.intents.map((i) => {
    const flowId = i.used_by_flows[0];
    const phrases = prose?.flows?.[flowId]?.user_phrases?.map((q) => `"${q}"`).join(", ") ?? "TODO";
    const label = i.intent.split("-").map((w) => w[0].toUpperCase() + w.slice(1)).join(" ");
    const what = prose?.flows?.[flowId]?.intent ?? `TODO: domain-level description of \`${i.intent}\`.`;
    return `### ${label} {#${i.intent}}

- **Role:** ${i.role}
- **Capability:** [\`${i.capability_anchor}\`](capabilities/${i.capability}.md#${i.capability_anchor})
- **Proposed tool:** \`${i.proposed_tool}\` (proposed — no MCP server yet)
- **User phrases:** ${phrases}
- **What it does:** ${what}
`;
  }).join("\n");

  return `---
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
${intentRows}

## Intent anchors

${intentSections}

<!-- HUMAN id="glossary-additions" -->
<!-- /HUMAN -->
`;
}

function renderFlow({ flow, tools, prose }) {
  const p = prose?.flows?.[flow.id] ?? {};
  const intents = tools.intents.filter((i) => i.used_by_flows.includes(flow.id));
  const description = p.description ?? `Use when this flow's preconditions hold (TODO: refine wording).`;
  const userPhrases = p.user_phrases ?? ["TODO: phrase 1", "TODO: phrase 2"];
  const preconditions = p.preconditions ?? ["TODO: precondition"];
  const postconditions = p.postconditions ?? ["TODO: postcondition"];
  const sideEffects = p.side_effects ?? [];
  const relatedFlows = p.related_flows ?? [];
  const trigger = p.trigger ?? `TODO`;
  const intentName = p.name ?? toTitleCase(flow.id.replace(/-/g, " "));
  const proseText = p.prose ?? `TODO: 2-4 sentence summary of this flow's purpose, agent behaviour, and any idempotence properties.`;
  const howReached = p.how_reached ?? `TODO: how the user reaches this entry`;

  const steps = p.steps ?? intents.map((i) => `Perform [${(p.intents_as_phrase?.[i.intent]) ?? i.intent.replace(/-/g, " ")}](../glossary.md#${i.intent}).`);
  const decisions = p.decision_points ?? [];
  const failures = p.failure_modes ?? [
    { what: "Tool returns 401", means: "auth missing/expired", do: "ask the user to sign in again" },
    { what: "Tool returns non-2xx", means: "operation failed", do: "surface the error to the user" },
  ];

  const intentBullets = intents.map((i) =>
    `- [${(p.intents_as_phrase?.[i.intent]) ?? i.intent.replace(/-/g, " ")}](../glossary.md#${i.intent}) — ${i.role}`,
  ).join("\n");

  const intentsUsedFM = intents.map((i) =>
    `  - intent: ${i.intent}\n    role: ${i.role}\n    glossary_ref: ../glossary.md#${i.intent}`,
  ).join("\n");

  const sequenceLines = [
    `  User->>Agent: "${userPhrases[0] ?? "(user phrase)"}"`,
    ...intents.map((i) => `  Agent->>T: ${i.intent.replace(/-/g, " ")}`),
    `  T-->>Agent: result`,
    `  Agent->>User: confirms outcome`,
  ];

  const unresolvedList = (flow.unresolved ?? []).length === 0
    ? "None."
    : flow.unresolved.map((u) => `- ${u.file}:${u.line} — ${u.reason}`).join("\n");

  return `---
schema_version: 1
id: ${flow.id}
name: ${intentName}
description: "${description}"
intent: "${proseString(p.intent) ?? `Drives the ${flow.id} interaction`}"
user_phrases:
${userPhrases.map((q) => `  - "${q}"`).join("\n")}
entry: ${flow.entry}
trigger: ${trigger}
preconditions:
${preconditions.map((q) => `  - ${q}`).join("\n")}
intents_used:
${intentsUsedFM}
postconditions:
${postconditions.map((q) => `  - ${q}`).join("\n")}
side_effects: [${sideEffects.join(", ")}]
related_flows: [${relatedFlows.join(", ")}]
confidence: ${p.confidence ?? "medium"}
---

# ${intentName}

<!-- AGENT id="prose" -->
${proseText}
<!-- /AGENT -->

## Entry point

\`${flow.entry}\` — ${howReached}

## How the agent handles this

${steps.map((s, i) => `${i + 1}. ${s}`).join("\n")}

## Decision points

${decisions.length === 0 ? "(none documented yet)" : decisions.map((d) => `- **${d.condition}** → ${d.action}`).join("\n")}

## Sequence

\`\`\`mermaid
sequenceDiagram
  actor User
  participant Agent
  participant T as MCP tools

${sequenceLines.join("\n")}
\`\`\`

## Failure modes

| What happens | What it means | What to do |
|---|---|---|
${failures.map((f) => `| ${f.what} | ${f.means} | ${f.do} |`).join("\n")}

## Intents used

${intentBullets}

<!-- HUMAN id="extra" -->
<!-- /HUMAN -->

## Unresolved

${unresolvedList}
`;
}

function renderCapability({ cap, tools, prose }) {
  const p = prose?.capabilities?.[cap.name] ?? {};
  const capTools = tools.tools.filter((t) => t.capability === cap.name);
  const flowsUsing = [...new Set(capTools.flatMap((t) => t.used_by_flows))].sort();
  const summary = p.summary ?? `TODO: one-sentence summary of the ${cap.name} capability.`;
  const overview = p.overview ?? `TODO: what ${cap.name} represents in the domain; constraints and invariants.`;
  const concepts = p.concepts ?? [`TODO: invariant for ${cap.name}.`];
  const cannotDo = p.cannot_do ?? [];

  const fmTools = capTools.map((t) =>
    `  - tool: ${t.proposed_name}\n    proposed: true\n    does: "${(p.tool_overrides?.[t.proposed_name]?.does) ?? `TODO: one-line semantic description`}"\n    method: ${t.method}\n    path: "${t.path}"\n    auth: ${t.auth}\n    confidence: ${t.confidence}\n    source: ${t.source[0]?.file ?? "?"}:${t.source[0]?.line ?? "?"}`,
  ).join("\n");

  const whenToReach = (p.when_to_reach ?? capTools.map((t) => ({
    intent: `TODO: user intent phrase`,
    tool: t.proposed_name,
  }))).map((w) => `- "${w.intent}" → \`${w.tool}\``).join("\n");

  const sections = capTools.map((t) => {
    const o = p.tool_overrides?.[t.proposed_name] ?? {};
    const pathParams = t.path_params.length === 0 ? "none" : t.path_params.map((pp) => `\`${pp.name}: ${pp.type}\` (${pp.required ? "required" : "optional"})`).join(", ");
    const queryParams = t.query_params.length === 0 ? "none" : t.query_params.map((qp) => `\`${qp.name}\``).join(", ");
    const bodyShape = renderShape(t.body_shape);
    const respShape = renderShape(t.response_shape);
    const phrases = (o.when_to_call_phrases ?? ["TODO: phrase"]).map((q) => `- "${q}"`).join("\n");
    const usedBy = [...t.used_by_flows].sort().map((id) => `[${id}](../flows/${id}.md)`).join(", ");

    return `## ${t.proposed_name}  {#${t.anchor}}

**Proposed tool name:** \`${t.proposed_name}\` (proposed — no MCP server yet)
**HTTP:** \`${t.method} ${t.path}\`
**Auth:** ${t.auth}
**Confidence:** ${t.confidence}
**Source:** \`${t.source[0]?.file ?? "?"}:${t.source[0]?.line ?? "?"}\`

**Path params:** ${pathParams}

**Query params:** ${queryParams}

**Body shape:**

${bodyShape}

**Response shape:**

${respShape}

**When to call it:**
${phrases}

**Used by flows:** ${usedBy}

---
`;
  }).join("\n");

  return `---
schema_version: 1
capability: ${cap.name}
summary: "${summary}"
tools:
${fmTools}
flows_using_this: [${flowsUsing.join(", ")}]
---

# ${toTitleCase(cap.name)}

<!-- AGENT id="overview" -->
${overview}
<!-- /AGENT -->

## Concepts the agent must know

${concepts.map((c) => `- ${c}`).join("\n")}

## When to reach for which tool

${whenToReach}

---

${sections}

## Things this capability cannot do

${cannotDo.length === 0 ? "(none documented yet)" : cannotDo.map((c) => `- ${c}`).join("\n")}

<!-- HUMAN id="notes" -->
<!-- /HUMAN -->
`;
}

function renderShape(shape) {
  if (shape === "unknown" || shape === undefined || shape === null) return "`unknown`";
  if (typeof shape === "string") return "`" + shape + "`";
  return "```ts\n" + JSON.stringify(shape, null, 2) + "\n```";
}

function renderToolsProposed({ tools, sha, ts }) {
  return JSON.stringify({
    schema_version: 1,
    generated_by: "flow-map-compiler",
    generated_at: ts,
    generated_from_sha: sha,
    naming_convention: tools.naming_convention,
    tools: tools.tools.map((t) => ({
      proposed_name: t.proposed_name,
      method: t.method,
      path: t.path,
      path_params: t.path_params,
      query_params: t.query_params,
      body_shape: t.body_shape,
      response_shape: t.response_shape,
      auth: t.auth,
      capability_file: `capabilities/${t.capability}.md`,
      anchor: t.anchor,
      source: t.source,
      used_by_flows: t.used_by_flows,
      confidence: t.confidence,
      openapi_operation_id: t.openapi_operation_id,
    })),
    unresolved: [],
  }, null, 2) + "\n";
}

function proseString(v) { return typeof v === "string" ? v : null; }
function toTitleCase(s) { return s.replace(/\b\w/g, (c) => c.toUpperCase()); }

await main();
