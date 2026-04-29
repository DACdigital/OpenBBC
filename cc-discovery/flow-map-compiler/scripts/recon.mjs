#!/usr/bin/env node
// Phase 1.1 - recon. Detects framework adapter, language, monorepo
// layout, API client libraries, in-repo specs. Writes
// .flow-map/.cache/recon.json.

import { readFileSync, writeFileSync, mkdirSync, existsSync, readdirSync, statSync } from "node:fs";
import { resolve, join } from "node:path";
import * as next from "./adapters/next.mjs";
import * as react from "./adapters/react.mjs";
import { loadRulePacks } from "./rules/_loader.mjs";

const ADAPTERS = [next, react];

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const pkgPath = join(repoRoot, "package.json");
  if (!existsSync(pkgPath)) {
    console.error(`recon: ${pkgPath} not found`);
    process.exit(1);
  }
  const pkg = JSON.parse(readFileSync(pkgPath, "utf8"));
  const deps = { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) };

  const adapterScores = ADAPTERS.map((a) => ({
    id: a.meta.id,
    score: a.probe(repoRoot),
    router: a.detectRouter ? a.detectRouter(repoRoot) : null,
  })).sort((a, b) => b.score - a.score);
  const winner = adapterScores[0]?.score > 0 ? adapterScores[0] : null;
  if (!winner) {
    console.error(`recon: no framework adapter matched ${repoRoot}`);
    process.exit(1);
  }

  const packs = await loadRulePacks();
  const activeClients = packs.filter((p) => p.detect(pkg)).map((p) => p.meta.id);

  const language = inferLanguage(repoRoot);

  const monorepo = inferMonorepo(repoRoot, pkg);

  const openapiSpecs = ["openapi.yaml", "openapi.yml", "openapi.json"]
    .filter((p) => existsSync(join(repoRoot, p)));
  const graphqlSchemas = ["schema.graphql"]
    .filter((p) => existsSync(join(repoRoot, p)));

  const recon = {
    schema_version: 1,
    generated_by: "flow-map-compiler/recon",
    framework: {
      adapter: winner.id,
      router: winner.router,
      version: deps.next ?? deps.react ?? null,
      language,
    },
    monorepo,
    api_clients: activeClients,
    api_base_url: inferApiBaseUrl(),
    auth: { type: "unknown", token_source: null, refresh: null },
    openapi_specs: openapiSpecs,
    graphql_schemas: graphqlSchemas,
    providers: [],
  };

  const outDir = join(repoRoot, ".flow-map", ".cache");
  mkdirSync(outDir, { recursive: true });
  writeFileSync(join(outDir, "recon.json"), JSON.stringify(recon, null, 2) + "\n");
  process.stdout.write(`recon: adapter=${winner.id} router=${winner.router} clients=[${activeClients.join(", ")}]\n`);
}

function inferLanguage(repoRoot) {
  if (existsSync(join(repoRoot, "tsconfig.json"))) return "ts";
  // No tsconfig but any .ts/.tsx file under common roots? Treat as TS.
  for (const dir of ["app", "src", "pages", "lib", "components"]) {
    const root = join(repoRoot, dir);
    if (existsSync(root) && hasTsFile(root)) return "ts";
  }
  return "js";
}

function hasTsFile(absDir) {
  let entries;
  try { entries = readdirSync(absDir); } catch { return false; }
  for (const name of entries) {
    if (name.startsWith(".") || name === "node_modules") continue;
    const child = join(absDir, name);
    let s;
    try { s = statSync(child); } catch { continue; }
    if (s.isFile() && /\.tsx?$/.test(name)) return true;
    if (s.isDirectory() && hasTsFile(child)) return true;
  }
  return false;
}

function inferMonorepo(repoRoot, pkg) {
  if (Array.isArray(pkg.workspaces) || (pkg.workspaces && Array.isArray(pkg.workspaces.packages))) {
    return { tool: "yarn-or-pnpm-workspaces", workspaces: pkg.workspaces };
  }
  if (existsSync(join(repoRoot, "pnpm-workspace.yaml"))) return { tool: "pnpm", workspaces: "pnpm-workspace.yaml" };
  if (existsSync(join(repoRoot, "turbo.json"))) return { tool: "turbo", config: "turbo.json" };
  if (existsSync(join(repoRoot, "nx.json"))) return { tool: "nx", config: "nx.json" };
  return null;
}

function inferApiBaseUrl() {
  // Phase-1 minimum: assume "/api" relative until trace finds an env var.
  return { source: "hardcoded", name: null, default: "/api" };
}

await main();
