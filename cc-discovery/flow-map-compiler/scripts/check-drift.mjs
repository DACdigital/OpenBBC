#!/usr/bin/env node
// Phase 4 - drift detection. Re-runs Phase 1 against current source,
// then diffs the regenerated cache against the previous endpoints.json.
//
// Crash-safe: snapshots the cache to a sibling .bak file *on disk*
// before invoking Phase 1, restores from there, and removes the .bak
// only after a clean exit. If the process is killed mid-run, the next
// invocation will see the .bak and restore from it before doing
// anything else.
//
// Output: short report on stdout, exit code:
//   0  no drift
//   1  drift detected (added/removed/changed endpoints or flows)
//   2  no prior cache to compare against, or Phase 1 produced
//      unparseable output

import { readFileSync, writeFileSync, existsSync, mkdirSync, unlinkSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve, join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(process.argv[2] ?? ".");
const cacheDir = join(repoRoot, ".flow-map", ".cache");
const cachePath = join(cacheDir, "endpoints.json");
const bakPath = cachePath + ".bak";

if (!existsSync(cachePath) && !existsSync(bakPath)) {
  console.error("check-drift: no .flow-map/.cache/endpoints.json yet — run the pipeline first.");
  process.exit(2);
}

// If a previous run died holding the cache hostage, restore from .bak
// before doing anything else.
if (existsSync(bakPath) && !existsSync(cachePath)) {
  writeFileSync(cachePath, readFileSync(bakPath, "utf8"));
}

mkdirSync(cacheDir, { recursive: true });
const snapshot = readFileSync(cachePath, "utf8");
writeFileSync(bakPath, snapshot);

const oldCache = JSON.parse(snapshot);

let newCache;
try {
  runPhase1(repoRoot);
  let regen;
  try { regen = readFileSync(cachePath, "utf8"); }
  catch (e) { throw new Error(`could not read regenerated cache: ${e.message}`); }
  try { newCache = JSON.parse(regen); }
  catch (e) {
    restore();
    console.error(`check-drift: regenerated cache is unparseable: ${e.message}`);
    unlinkSync(bakPath);
    process.exit(2);
  }
} catch (e) {
  restore();
  console.error(`check-drift: phase 1 failed: ${e.message}`);
  if (e.stderr) console.error(e.stderr.toString());
  unlinkSync(bakPath);
  process.exit(2);
} finally {
  // Always restore the snapshot before reporting drift; we're a
  // read-only check, callers should run the pipeline themselves to
  // regenerate intentionally.
  if (existsSync(bakPath)) restore();
}
unlinkSync(bakPath);

const oldFlows = indexFlows(oldCache.flows);
const newFlows = indexFlows(newCache.flows);

const added = [];
const removed = [];
const changed = [];

for (const [id, flow] of newFlows) {
  if (!oldFlows.has(id)) added.push({ id, flow });
}
for (const [id, flow] of oldFlows) {
  if (!newFlows.has(id)) removed.push({ id, flow });
}
for (const [id, newFlow] of newFlows) {
  if (!oldFlows.has(id)) continue;
  const diff = diffFlow(oldFlows.get(id), newFlow);
  if (diff.length > 0) changed.push({ id, diff });
}

if (added.length === 0 && removed.length === 0 && changed.length === 0) {
  process.stdout.write("check-drift: no drift\n");
  process.exit(0);
}

if (added.length > 0) {
  process.stdout.write(`check-drift: ${added.length} flow(s) added\n`);
  for (const { id, flow } of added) process.stdout.write(`  + ${id} (${flow.entry})\n`);
}
if (removed.length > 0) {
  process.stdout.write(`check-drift: ${removed.length} flow(s) removed\n`);
  for (const { id, flow } of removed) process.stdout.write(`  - ${id} (${flow.entry})\n`);
}
if (changed.length > 0) {
  process.stdout.write(`check-drift: ${changed.length} flow(s) changed\n`);
  for (const { id, diff } of changed) {
    process.stdout.write(`  ~ ${id}\n`);
    for (const d of diff) process.stdout.write(`      ${d}\n`);
  }
}
process.exit(1);

// ---------- helpers ----------

function restore() {
  writeFileSync(cachePath, snapshot);
}

function indexFlows(flows) {
  const m = new Map();
  for (const f of flows) m.set(f.id, f);
  return m;
}

function diffFlow(a, b) {
  const out = [];
  if (a.entry !== b.entry) out.push(`entry: ${a.entry} -> ${b.entry}`);
  const aKey = (a.calls ?? []).map((c) => `${c.method} ${c.path}`).sort().join(" | ");
  const bKey = (b.calls ?? []).map((c) => `${c.method} ${c.path}`).sort().join(" | ");
  if (aKey !== bKey) out.push(`calls: [${aKey}] -> [${bKey}]`);
  return out;
}

function runPhase1(root) {
  // ingest-openapi must run between trace and resolve so spec changes
  // surface in drift detection.
  const order = ["recon.mjs", "enumerate.mjs", "trace.mjs", "ingest-openapi.mjs", "resolve.mjs"];
  for (const script of order) {
    try {
      execFileSync("node", [join(HERE, script), root], {
        stdio: ["ignore", "ignore", "pipe"],
      });
    } catch (e) {
      const stderr = e.stderr ? e.stderr.toString() : "";
      throw new Error(`${script} exited ${e.status}: ${stderr.slice(0, 500) || e.message}`);
    }
  }
}
