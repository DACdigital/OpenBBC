#!/usr/bin/env node
// Phase 2.2 - propose-tools. Reads flows.json, derives a proposed tool
// name per unique endpoint, groups endpoints into capabilities (one per
// first path segment, or by OpenAPI tag if available), writes
// .flow-map/.cache/tools.json (the intermediate Phase 2 artefact that
// compile.mjs consumes).
//
// Default convention: dotted lower-camel `<resource>.<verb>`. Override
// via env FLOWMAP_NAMING (one of: dotted-lower-camel, snake_case,
// dotted-snake).

import { readFileSync, writeFileSync } from "node:fs";
import { resolve, join } from "node:path";

const NAMING = process.env.FLOWMAP_NAMING || "dotted-lower-camel";

async function main() {
  const repoRoot = resolve(process.argv[2] ?? ".");
  const cacheDir = join(repoRoot, ".flow-map", ".cache");
  const grouped = JSON.parse(readFileSync(join(cacheDir, "flows.json"), "utf8"));

  const seen = new Map();
  const toolsByCapability = new Map();
  for (const flow of grouped.flows) {
    for (const c of flow.calls) {
      const key = `${c.method} ${c.path}`;
      if (seen.has(key)) {
        seen.get(key).used_by_flows.add(flow.id);
        continue;
      }
      const capability = capabilityFromPath(c.path);
      const verb = verbFromMethod(c.method, c.path);
      const tool = applyNaming(`${capability}.${verb}`);
      const anchor = tool.replace(/\./g, "-");

      const entry = {
        proposed_name: tool,
        method: c.method,
        path: c.path,
        path_params: c.path_params,
        query_params: c.query_params,
        body_shape: c.body_shape,
        response_shape: c.response_shape,
        auth: c.auth,
        capability,
        anchor,
        source: c.source,
        used_by_flows: new Set([flow.id]),
        confidence: c.confidence,
        openapi_operation_id: null,
      };
      seen.set(key, entry);
      if (!toolsByCapability.has(capability)) toolsByCapability.set(capability, []);
      toolsByCapability.get(capability).push(entry);
    }
  }

  // Serialise sets to arrays. Sort everything by stable keys so output
  // doesn't depend on Map insertion order.
  const tools = [...seen.values()]
    .map((t) => ({ ...t, used_by_flows: [...t.used_by_flows].sort() }))
    .sort((a, b) => a.proposed_name.localeCompare(b.proposed_name));

  const capabilities = [...toolsByCapability.entries()]
    .map(([name, list]) => ({
      name,
      tool_count: list.length,
      tools: list.map((t) => t.proposed_name).sort(),
    }))
    .sort((a, b) => a.name.localeCompare(b.name));

  // Intent index — sorted so glossary table rows are stable.
  const intents = tools
    .map((t) => ({
      intent: intentFromTool(t),
      proposed_tool: t.proposed_name,
      capability: t.capability,
      capability_anchor: t.anchor,
      role: roleFromMethod(t.method),
      used_by_flows: [...t.used_by_flows].sort(),
    }))
    .sort((a, b) => a.intent.localeCompare(b.intent));

  const out = {
    schema_version: 1,
    generated_by: "flow-map-compiler/propose-tools",
    naming_convention: NAMING,
    tools,
    capabilities,
    intents,
  };
  writeFileSync(join(cacheDir, "tools.json"), JSON.stringify(out, null, 2) + "\n");
  process.stdout.write(`propose-tools: ${tools.length} tools across ${capabilities.length} capabilities\n`);
}

function capabilityFromPath(path) {
  // Special prefixes carrying their own meaning.
  if (path.startsWith("trpc:")) {
    const proc = path.slice("trpc:".length).split(".")[0];
    return slugify(proc);
  }
  if (path.startsWith("graphql:")) {
    const op = path.slice("graphql:".length);
    return slugify(op).slice(0, 32) || "graphql";
  }
  if (path.startsWith("action:")) {
    return "actions";
  }
  // Skip versioned prefixes (`/api/v1`) and OpenAPI path-template
  // segments (`{tenant}`) when picking the capability noun.
  const segs = path.split("/").filter(Boolean).filter((s) => !/^\{[^}]+\}$/.test(s));
  let i = 0;
  if (segs[i] === "api") i++;
  if (segs[i] && /^v\d+$/.test(segs[i])) i++;
  return slugify(segs[i] ?? "root");
}

function slugify(s) {
  return (s ?? "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "root";
}

function verbFromMethod(method, path) {
  const hasIdParam = /\{[^}]+\}/.test(path);
  switch (method) {
    case "GET":     return hasIdParam ? "get" : "list";
    case "POST":    return "create";
    case "PUT":     return "replace";
    case "PATCH":   return "update";
    case "DELETE":  return "delete";
    case "HEAD":    return "head";
    case "OPTIONS": return "options";
    default:        return method.toLowerCase();
  }
}

function applyNaming(dotted) {
  switch (NAMING) {
    case "snake_case":
      // Lowercase + insert _ between camelCase boundaries, then dot → _.
      return dotted
        .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
        .replace(/[.\-]+/g, "_")
        .toLowerCase();
    case "dotted-snake":
      return dotted
        .split(".")
        .map((seg) => seg.replace(/([a-z0-9])([A-Z])/g, "$1_$2").toLowerCase())
        .join(".");
    case "dotted-lower-camel":
    default:
      return dotted;
  }
}

function intentFromTool(tool) {
  // tool = "users.update", "users.list", "users.delete"
  // intent = "update-user-record", "list-users", "delete-user"
  // Heuristic: <verb>-<resource-singular>-<extras>.
  const [resource, verb] = tool.proposed_name.split(".");
  // Singularise trivially (drop trailing 's').
  const singular = resource.endsWith("s") ? resource.slice(0, -1) : resource;
  if (verb === "list") return `list-${resource}`;
  if (verb === "get") return `view-${singular}`;
  if (verb === "update") return `update-${singular}-record`;
  if (verb === "create") return `create-${singular}`;
  if (verb === "delete") return `delete-${singular}`;
  if (verb === "replace") return `replace-${singular}`;
  return `${verb}-${singular}`;
}

function roleFromMethod(method) {
  if (method === "GET" || method === "HEAD") return "read";
  if (method === "POST" || method === "PUT" || method === "PATCH" || method === "DELETE") return "write";
  return "read";
}

await main();
