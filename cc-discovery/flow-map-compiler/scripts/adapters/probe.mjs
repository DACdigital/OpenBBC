#!/usr/bin/env node
// Probe all adapters against a repo root and print the winner as JSON.
// Usage: node scripts/adapters/probe.mjs <repoRoot>

import { resolve } from "node:path";
import * as next from "./next.mjs";
import * as react from "./react.mjs";

const ADAPTERS = [next, react];

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const scored = ADAPTERS.map((a) => ({
    id: a.meta.id,
    score: a.probe(repoRoot),
    router: a.detectRouter ? a.detectRouter(repoRoot) : null,
  }));
  scored.sort((a, b) => b.score - a.score);
  const winner = scored[0]?.score > 0 ? scored[0] : null;
  process.stdout.write(JSON.stringify({
    repoRoot,
    winner,
    candidates: scored,
  }, null, 2) + "\n");
}

await main();
