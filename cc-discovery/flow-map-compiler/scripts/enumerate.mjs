#!/usr/bin/env node
// Phase 1.2 - enumerate. Calls the active adapter's enumerateEntries()
// and writes .flow-map/.cache/entries.json.

import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { resolve, join } from "node:path";
import * as next from "./adapters/next.mjs";
import * as react from "./adapters/react.mjs";

const ADAPTERS = { next, react };

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const reconPath = join(repoRoot, ".flow-map", ".cache", "recon.json");
  const recon = JSON.parse(readFileSync(reconPath, "utf8"));
  const adapter = ADAPTERS[recon.framework.adapter];
  if (!adapter) {
    console.error(`enumerate: unknown adapter ${recon.framework.adapter}`);
    process.exit(1);
  }
  const entries = await adapter.enumerateEntries(repoRoot);
  const out = {
    schema_version: 1,
    generated_by: "flow-map-compiler/enumerate",
    adapter: recon.framework.adapter,
    entries,
  };
  const outDir = join(repoRoot, ".flow-map", ".cache");
  mkdirSync(outDir, { recursive: true });
  writeFileSync(join(outDir, "entries.json"), JSON.stringify(out, null, 2) + "\n");
  process.stdout.write(`enumerate: ${entries.length} entries\n`);
}

await main();
