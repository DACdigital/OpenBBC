#!/usr/bin/env node
// Phase 2.1 - group. Reads endpoints.json (already grouped per-flow by
// the resolver) and produces a flows-with-context.json with the grouping
// finalised by the active adapter's groupFlows() rule. In our current
// pipeline the resolver does most of the work; this script is a thin
// pass that exists so the framework adapter can apply per-framework
// nuance (e.g. "merge subroutes under shared layout").

import { readFileSync, writeFileSync } from "node:fs";
import { resolve, join } from "node:path";

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const cacheDir = join(repoRoot, ".flow-map", ".cache");
  const endpoints = JSON.parse(readFileSync(join(cacheDir, "endpoints.json"), "utf8"));
  const recon = JSON.parse(readFileSync(join(cacheDir, "recon.json"), "utf8"));

  // Adapter-specific finalisation hook. Both current adapters return the
  // input unchanged; tracked here so the seam exists when later adapters
  // (e.g. shared-layout merging in App Router) need it.
  const adapterId = recon.framework.adapter;
  let flows = endpoints.flows;
  if (adapterId === "next") {
    flows = mergeSiblingsUnderLayout(flows);
  }

  const out = {
    schema_version: 1,
    generated_by: "flow-map-compiler/group",
    framework: endpoints.framework,
    flows,
  };
  writeFileSync(join(cacheDir, "flows.json"), JSON.stringify(out, null, 2) + "\n");
  process.stdout.write(`group: ${flows.length} flows\n`);
}

function mergeSiblingsUnderLayout(flows) {
  // Future: collapse `/profile`, `/profile/edit` into one flow if they
  // share a layout.tsx. Not implemented for the slice; pass-through.
  return flows;
}

await main();
