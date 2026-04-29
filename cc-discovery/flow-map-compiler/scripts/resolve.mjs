#!/usr/bin/env node
// Phase 1.4 - resolve. Consolidates callsites into a typed endpoints
// list. Writes .flow-map/.cache/endpoints.json.
//
// Determinism contract:
// - All Map iterations are wrapped in `[...m].sort()` before serialization.
// - Sources, calls, flows are sorted by stable keys (file/line/path/method).
//
// Hard fail when the unresolved-rate (computed over *frontend* callsites
// only — openapi-spec synthetic callsites are excluded) exceeds 25 %.

import { readFileSync, writeFileSync } from "node:fs";
import { resolve, join } from "node:path";

const UNRESOLVED_THRESHOLD = 0.25;

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const cacheDir = join(repoRoot, ".flow-map", ".cache");

  const recon = JSON.parse(readFileSync(join(cacheDir, "recon.json"), "utf8"));
  const entries = JSON.parse(readFileSync(join(cacheDir, "entries.json"), "utf8")).entries;
  const ndjson = readFileSync(join(cacheDir, "callsites.ndjson"), "utf8").trim();
  const callsites = ndjson === "" ? [] : ndjson.split("\n").map((l) => JSON.parse(l));
  const unresolved = JSON.parse(readFileSync(join(cacheDir, "unresolved.json"), "utf8")).items;

  const byKey = new Map();
  for (const c of callsites) {
    const key = `${c.method} ${c.path}`;
    if (!byKey.has(key)) {
      byKey.set(key, {
        method: c.method,
        path: c.path,
        path_params: extractPathParams(c.path),
        query_params: [],
        body_shape: "unknown",
        response_shape: "unknown",
        auth: c.auth,
        client: c.client,
        confidence: c.confidence,
        sources: [],
        used_by_entries: new Set(),
      });
    }
    const e = byKey.get(key);
    e.sources.push({ file: c.file, line: c.line });
    if (c.entry) e.used_by_entries.add(c.entry);
  }

  const callsByEntry = new Map();
  for (const ep of [...byKey.values()].sort(epCmp)) {
    for (const entryFile of [...ep.used_by_entries].sort()) {
      if (!callsByEntry.has(entryFile)) callsByEntry.set(entryFile, []);
      callsByEntry.get(entryFile).push(ep);
    }
  }

  const flowMap = new Map();
  for (const entry of [...entries].sort((a, b) => a.file.localeCompare(b.file))) {
    const calls = callsByEntry.get(entry.file) ?? [];
    flowMap.set(entry.file, {
      id: flowIdFromEntry(entry, calls),
      entry: entry.file,
      route: entry.route,
      trigger: null,
      calls: [],
      unresolved: [],
    });
  }
  for (const ep of [...byKey.values()].sort(epCmp)) {
    for (const entryFile of [...ep.used_by_entries].sort()) {
      const flow = flowMap.get(entryFile);
      if (!flow) continue;
      flow.calls.push({
        method: ep.method,
        path: ep.path,
        path_params: ep.path_params,
        query_params: ep.query_params,
        body_shape: ep.body_shape,
        response_shape: ep.response_shape,
        auth: ep.auth,
        client: ep.client,
        confidence: ep.confidence,
        source: [...ep.sources].sort((a, b) =>
          (a.file ?? "").localeCompare(b.file ?? "") || (a.line ?? 0) - (b.line ?? 0)),
      });
    }
  }
  for (const u of unresolved) {
    const flow = flowMap.get(u.entry);
    if (flow) flow.unresolved.push(u);
  }

  const flows = [...flowMap.values()]
    .filter((f) => f.calls.length > 0 || f.unresolved.length > 0)
    .sort((a, b) => a.id.localeCompare(b.id));

  for (const f of flows) {
    f.calls.sort((a, b) =>
      (a.method ?? "").localeCompare(b.method ?? "") || (a.path ?? "").localeCompare(b.path ?? ""));
    f.unresolved.sort((a, b) =>
      (a.file ?? "").localeCompare(b.file ?? "") || (a.line ?? 0) - (b.line ?? 0));
  }

  const backendDeclared = [];
  for (const ep of [...byKey.values()].sort(epCmp)) {
    if (ep.used_by_entries.size > 0) continue;
    backendDeclared.push({
      method: ep.method,
      path: ep.path,
      path_params: ep.path_params,
      query_params: ep.query_params,
      body_shape: ep.body_shape,
      response_shape: ep.response_shape,
      auth: ep.auth,
      client: ep.client,
      confidence: ep.confidence,
      source: [...ep.sources].sort((a, b) =>
        (a.file ?? "").localeCompare(b.file ?? "") || (a.line ?? 0) - (b.line ?? 0)),
    });
  }

  // Compute the unresolved rate against frontend callsites only — the
  // synthetic openapi-spec entries should not dilute a real frontend
  // trace failure.
  const frontendCallsites = callsites.filter((c) => c.client !== "openapi-spec");
  const frontendUnresolved = unresolved.filter((u) => u.rule_pack !== "openapi-spec");
  const totalFrontend = frontendCallsites.length + frontendUnresolved.length;
  const unresolvedRate = totalFrontend === 0 ? 0 : frontendUnresolved.length / totalFrontend;

  const out = {
    schema_version: 1,
    generated_by: "flow-map-compiler/resolve",
    framework: recon.framework,
    flows,
    backend_declared: backendDeclared,
    unresolved_rate: unresolvedRate,
  };
  writeFileSync(join(cacheDir, "endpoints.json"), JSON.stringify(out, null, 2) + "\n");

  process.stdout.write(
    `resolve: ${flows.length} flows, ${frontendCallsites.length} frontend calls, ${backendDeclared.length} backend-declared, unresolved rate ${(unresolvedRate * 100).toFixed(1)}%\n`,
  );

  if (unresolvedRate > UNRESOLVED_THRESHOLD) {
    console.error(`resolve: unresolved rate ${(unresolvedRate * 100).toFixed(1)}% exceeds ${(UNRESOLVED_THRESHOLD * 100).toFixed(0)}% — Phase 2 will refuse to advance`);
    process.exit(1);
  }
}

function epCmp(a, b) {
  return (a.method ?? "").localeCompare(b.method ?? "") || (a.path ?? "").localeCompare(b.path ?? "");
}

function extractPathParams(path) {
  const params = [];
  for (const m of path.matchAll(/\{([^}]+)\}/g)) {
    params.push({ name: m[1], type: "string", required: true });
  }
  return params;
}

function flowIdFromEntry(entry, calls) {
  // Sanitize: strip Next-style [param] and OpenAPI-style {param}, lower-case,
  // replace non-alphanumeric runs with single dashes, trim dashes.
  const route = (entry.route || "/").toString();
  const cleanedParts = route.split("/").map((p) => p.replace(/[\[\]{}]/g, "")).filter(Boolean);
  const baseNoun = cleanedParts.length === 0 ? "home" : cleanedParts.join("-");
  const noun = baseNoun
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    || "flow";

  if (!calls || calls.length === 0) return `view-${noun}`;
  const verbs = new Set(calls.map((c) => c.method));
  if (verbs.size === 1) {
    const only = [...verbs][0];
    if (only === "GET" || only === "HEAD") return `view-${noun}`;
    if (only === "POST") return `create-${noun}`;
    if (only === "PATCH" || only === "PUT") return `update-${noun}`;
    if (only === "DELETE") return `delete-${noun}`;
    if (only === "ACTION") return `act-${noun}`;
    if (only.startsWith("TRPC-MUTATE")) return `update-${noun}`;
    if (only.startsWith("TRPC-QUERY")) return `view-${noun}`;
    if (only.startsWith("GQL-MUTATION")) return `update-${noun}`;
    if (only.startsWith("GQL-QUERY")) return `view-${noun}`;
  }
  return noun;
}

await main();
