#!/usr/bin/env node
// Phase 1.3 - trace. Walks each entry's reachable file set and runs
// every active rule pack against each file. Writes
// .flow-map/.cache/{callsites.ndjson, deps.json, unresolved.json, wrappers.json}.
//
// Import resolution honors:
//   - relative paths (`./x`, `../x`)
//   - tsconfig.json `compilerOptions.paths` (wildcard aliases)
//   - dynamic `import("./x")` and CJS `require("./x")`
//   - re-exports `export ... from "./x"`
// Bare package imports (`react`, `axios`) are skipped — they aren't
// part of the user's source surface.
//
// Output is sorted at every layer where serialization happens, so the
// same source produces byte-identical files across operating systems
// (macOS HFS+ and Linux ext4 enumerate directories in different orders).

import {
  readFileSync,
  writeFileSync,
  mkdirSync,
  existsSync,
} from "node:fs";
import { resolve, join, dirname, relative, sep } from "node:path";
import { loadRulePacks } from "./rules/_loader.mjs";
import { maskCommentsAndStrings } from "./rules/_util.mjs";

function toPosix(p) {
  return sep === "/" ? p : p.split(sep).join("/");
}

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const cacheDir = join(repoRoot, ".flow-map", ".cache");
  const recon = JSON.parse(readFileSync(join(cacheDir, "recon.json"), "utf8"));
  const entries = JSON.parse(readFileSync(join(cacheDir, "entries.json"), "utf8")).entries;
  const pkg = JSON.parse(readFileSync(join(repoRoot, "package.json"), "utf8"));

  const packs = (await loadRulePacks()).filter((p) => p.detect(pkg));
  if (packs.length === 0) {
    console.error("trace: no active rule packs");
    process.exit(1);
  }
  packs.sort((a, b) => a.meta.id.localeCompare(b.meta.id));

  const aliases = loadTsconfigPaths(repoRoot);

  const deps = {};
  const callsites = [];
  const unresolved = [];
  const wrappers = [];

  for (const entry of entries) {
    const reachable = new Set();
    walkImports(repoRoot, entry.file, reachable, aliases);
    deps[entry.file] = [...reachable].sort();
    const sortedFiles = [...reachable].sort();
    for (const file of sortedFiles) {
      const abs = join(repoRoot, file);
      let text;
      try { text = readFileSync(abs, "utf8"); } catch { continue; }
      for (const pack of packs) {
        const result = pack.extract(file, text) ?? {};
        const calls = result.calls ?? [];
        const u = result.unresolved ?? [];
        const w = result.wrappers ?? [];
        for (const c of calls) callsites.push({ ...c, entry: entry.file, rule_pack: pack.meta.id });
        for (const x of u) unresolved.push({ ...x, entry: entry.file, rule_pack: pack.meta.id });
        for (const wr of w) wrappers.push({ ...wr, entry: entry.file, rule_pack: pack.meta.id });
      }
    }
  }

  // Sort cross-cutting collections too. Within a single file the order
  // is already deterministic (regex match index). The risk is across
  // files when filesystem walk order varies.
  const cmp = (a, b) =>
    a.entry.localeCompare(b.entry) ||
    a.file.localeCompare(b.file) ||
    (a.line ?? 0) - (b.line ?? 0) ||
    (a.method ?? "").localeCompare(b.method ?? "") ||
    (a.path ?? "").localeCompare(b.path ?? "");
  callsites.sort(cmp);
  unresolved.sort((a, b) =>
    a.entry.localeCompare(b.entry) || a.file.localeCompare(b.file) || (a.line ?? 0) - (b.line ?? 0));
  wrappers.sort((a, b) =>
    a.entry.localeCompare(b.entry) || a.file.localeCompare(b.file) || (a.line ?? 0) - (b.line ?? 0));

  // Sort the deps map keys too.
  const sortedDeps = {};
  for (const k of Object.keys(deps).sort()) sortedDeps[k] = deps[k];

  mkdirSync(cacheDir, { recursive: true });
  writeFileSync(join(cacheDir, "deps.json"), JSON.stringify({
    schema_version: 1,
    generated_by: "flow-map-compiler/trace",
    deps: sortedDeps,
  }, null, 2) + "\n");

  const ndjson = callsites.map((c) => JSON.stringify(c)).join("\n") + (callsites.length ? "\n" : "");
  writeFileSync(join(cacheDir, "callsites.ndjson"), ndjson);

  writeFileSync(join(cacheDir, "unresolved.json"), JSON.stringify({
    schema_version: 1,
    generated_by: "flow-map-compiler/trace",
    items: unresolved,
  }, null, 2) + "\n");

  writeFileSync(join(cacheDir, "wrappers.json"), JSON.stringify({
    schema_version: 1,
    generated_by: "flow-map-compiler/trace",
    items: wrappers,
  }, null, 2) + "\n");

  process.stdout.write(
    `trace: ${callsites.length} callsites, ${unresolved.length} unresolved, ${wrappers.length} wrappers\n`,
  );
}

const TS_EXTS = [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"];

function walkImports(repoRoot, startRel, visited, aliases) {
  // Iterative BFS — recursion blew the stack on deep dependency graphs.
  const queue = [startRel];
  while (queue.length > 0) {
    const cur = queue.shift();
    if (visited.has(cur)) continue;
    visited.add(cur);
    const abs = join(repoRoot, cur);
    let raw;
    try { raw = readFileSync(abs, "utf8"); } catch { continue; }
    // Mask comments + string contents so a sample import inside a
    // string/JSDoc doesn't pull a phantom file in.
    const text = maskCommentsAndStrings(raw);

    const specs = new Set();
    for (const m of text.matchAll(/\bfrom\s+["']([^"']+)["']/g)) specs.add(m[1]);
    for (const m of text.matchAll(/\bimport\s+["']([^"']+)["']/g)) specs.add(m[1]);
    for (const m of text.matchAll(/\bimport\s*\(\s*["']([^"']+)["']\s*\)/g)) specs.add(m[1]);
    for (const m of text.matchAll(/\brequire\s*\(\s*["']([^"']+)["']\s*\)/g)) specs.add(m[1]);

    for (const spec of specs) {
      const resolved = resolveImport(repoRoot, dirname(cur), spec, aliases);
      if (resolved) queue.push(resolved);
    }
  }
}

function resolveImport(repoRoot, fromDir, spec, aliases) {
  if (spec.startsWith(".")) return resolveRelative(repoRoot, fromDir, spec);
  // tsconfig path aliases: e.g. "@/lib/foo" → "src/lib/foo"
  for (const { match, targets } of aliases) {
    const m = spec.match(match);
    if (!m) continue;
    for (const target of targets) {
      const concrete = target.replace(/\*/, m[1] ?? "");
      const candidate = resolveRelative(repoRoot, ".", "./" + concrete);
      if (candidate) return candidate;
    }
  }
  return null; // bare package import — out of source scope
}

function resolveRelative(repoRoot, fromDir, spec) {
  const candidates = [];
  for (const ext of TS_EXTS) candidates.push(spec + ext);
  for (const ext of TS_EXTS) candidates.push(spec + "/index" + ext);
  candidates.push(spec);
  for (const c of candidates) {
    const abs = resolve(join(repoRoot, fromDir, c));
    if (existsSync(abs)) {
      return toPosix(relative(repoRoot, abs));
    }
  }
  return null;
}

function loadTsconfigPaths(repoRoot) {
  const tsconfigPath = join(repoRoot, "tsconfig.json");
  if (!existsSync(tsconfigPath)) return [];
  let cfg;
  try {
    // Strip line comments — TS allows them in tsconfig.
    const raw = readFileSync(tsconfigPath, "utf8")
      .replace(/^\s*\/\/.*$/gm, "")
      .replace(/\/\*[\s\S]*?\*\//g, "");
    cfg = JSON.parse(raw);
  } catch { return []; }
  const paths = cfg?.compilerOptions?.paths;
  const baseUrl = cfg?.compilerOptions?.baseUrl ?? ".";
  if (!paths) return [];
  const aliases = [];
  for (const key of Object.keys(paths).sort()) {
    const targets = (paths[key] ?? []).map((t) => join(baseUrl, t).replace(/\\/g, "/"));
    // "@/*" → /^@\/(.*)$/, "@app/foo" → /^@app\/foo$/
    const escaped = key.replace(/[.+?^${}()|[\]\\]/g, "\\$&");
    const pattern = "^" + escaped.replace(/\*/, "(.*)") + "$";
    aliases.push({ match: new RegExp(pattern), targets });
  }
  return aliases;
}

await main();
