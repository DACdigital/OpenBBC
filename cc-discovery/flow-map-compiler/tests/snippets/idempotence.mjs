#!/usr/bin/env node
// Idempotence guard. Runs the full Phase 1 + Phase 2 pipeline twice on
// each fixture and asserts byte-identical output across both runs.
// Without this, drift detection is undefended.

import { readFileSync, statSync, readdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = join(HERE, "..", "..");
const FIXTURES = ["sample-nextjs", "sample-react"];
const SCRIPTS = ["recon", "enumerate", "trace", "ingest-openapi", "resolve", "group", "propose-tools", "compile"];

let failed = 0;
for (const fx of FIXTURES) {
  const repo = join(ROOT, "tests", "fixtures", fx);
  runPipeline(repo);
  const a = digestWiki(repo);
  runPipeline(repo);
  const b = digestWiki(repo);
  if (a.hash === b.hash) {
    console.log(`  ok  ${fx}: idempotent (sha=${a.hash.slice(0, 12)}, ${a.files} files)`);
  } else {
    console.error(`  FAIL ${fx}: re-run produced different output`);
    for (const k of Object.keys(a.perFile)) {
      if (a.perFile[k] !== b.perFile[k]) console.error(`     differs: ${k}`);
    }
    failed++;
  }
}

if (failed > 0) process.exit(1);
console.log(`\nidempotence: ok`);

function runPipeline(repo) {
  for (const s of SCRIPTS) {
    execFileSync("node", [join(ROOT, "scripts", s + ".mjs"), repo], { stdio: ["ignore", "ignore", "ignore"] });
  }
}

function digestWiki(repo) {
  const root = join(repo, ".flow-map");
  const perFile = {};
  for (const rel of walkRel(root, "").sort()) {
    if (rel.startsWith(".cache/")) continue;
    if (!rel.endsWith(".md") && !rel.endsWith(".json")) continue;
    perFile[rel] = createHash("sha256").update(readFileSync(join(root, rel))).digest("hex");
  }
  const aggregate = createHash("sha256");
  for (const k of Object.keys(perFile).sort()) aggregate.update(`${k}\0${perFile[k]}\n`);
  return { hash: aggregate.digest("hex"), files: Object.keys(perFile).length, perFile };
}

function walkRel(dir, prefix) {
  const out = [];
  let entries;
  try { entries = readdirSync(dir); } catch { return out; }
  for (const name of entries.sort()) {
    const rel = prefix === "" ? name : prefix + "/" + name;
    const abs = join(dir, name);
    let s;
    try { s = statSync(abs); } catch { continue; }
    if (s.isDirectory()) out.push(...walkRel(abs, rel));
    else out.push(rel);
  }
  return out;
}
