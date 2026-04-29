#!/usr/bin/env node
// Smoke test for ingest-openapi.mjs.
// Covers JSON ingestion, YAML ingestion (2-space + 4-space), idempotent
// re-runs, and quoted-value-with-colon edge cases.

import { mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";

const HERE = dirname(fileURLToPath(import.meta.url));
const SCRIPT = join(HERE, "..", "..", "scripts", "ingest-openapi.mjs");

const YAML_2 = `openapi: 3.0.0
paths:
  /items/{id}:
    get:
      operationId: getItem
      summary: "Fetch one: a colon in summary"
    delete:
      operationId: removeItem
      security:
        - bearerAuth: []
  /items:
    post:
      operationId: createItem
`;

const YAML_4 = `openapi: 3.0.0
paths:
    /items/{id}:
        get:
            operationId: getItem
            summary: "Fetch one: a colon in summary"
        delete:
            operationId: removeItem
    /items:
        post:
            operationId: createItem
`;

let failed = 0;

runJsonCase();
runYamlCase("yaml-2space", YAML_2);
runYamlCase("yaml-4space", YAML_4);
runIdempotenceCase();

if (failed > 0) {
  console.error(`\n${failed} case(s) failed`);
  process.exit(1);
}
console.log(`\nopenapi ingestion: ok`);

function runJsonCase() {
  const tmp = mkScratch("openapi-json");
  try {
    writeRecon(tmp, ["openapi.json"]);
    writeFileSync(join(tmp, ".flow-map", ".cache", "callsites.ndjson"), "");
    writeFileSync(join(tmp, "openapi.json"), JSON.stringify({
      openapi: "3.0.0",
      paths: {
        "/users/{id}": {
          get: { operationId: "getUserById", summary: "Fetch a user" },
          patch: { operationId: "updateUser", security: [{ bearerAuth: [] }] },
        },
        "/users": { post: { operationId: "createUser" } },
      },
    }));
    execFileSync("node", [SCRIPT, tmp], { encoding: "utf8" });
    const lines = readNdjson(tmp);
    expect(lines.length === 3, `json: expected 3 ops, got ${lines.length}`);
    const patch = lines.find((l) => l.method === "PATCH");
    expect(patch?.auth === "bearer", `json: PATCH /users/{id} auth should be bearer`);
    console.log("  ok  json: 3 ops with bearer auth detected");
  } finally { rmSync(tmp, { recursive: true, force: true }); }
}

function runYamlCase(label, yaml) {
  const tmp = mkScratch(label);
  try {
    writeRecon(tmp, ["openapi.yaml"]);
    writeFileSync(join(tmp, ".flow-map", ".cache", "callsites.ndjson"), "");
    writeFileSync(join(tmp, "openapi.yaml"), yaml);
    let out;
    try {
      out = execFileSync("node", [SCRIPT, tmp], { encoding: "utf8" });
    } catch (e) {
      expect(false, `${label}: ingest threw: ${(e.stderr || e.message).toString().slice(0, 300)}`);
      return;
    }
    const lines = readNdjson(tmp);
    expect(lines.length >= 2, `${label}: expected ≥ 2 ops, got ${lines.length}; out=${out}`);
    const get = lines.find((l) => l.method === "GET" && l.path === "/items/{id}");
    expect(!!get, `${label}: missing GET /items/{id}`);
    expect(get?.summary === "Fetch one: a colon in summary",
      `${label}: quoted-value-with-colon was lost (got: ${JSON.stringify(get?.summary)})`);
    console.log(`  ok  ${label}: ${lines.length} ops, quoted-colon summary preserved`);
  } finally { rmSync(tmp, { recursive: true, force: true }); }
}

function runIdempotenceCase() {
  const tmp = mkScratch("openapi-idempotent");
  try {
    writeRecon(tmp, ["openapi.json"]);
    writeFileSync(join(tmp, ".flow-map", ".cache", "callsites.ndjson"), "");
    writeFileSync(join(tmp, "openapi.json"), JSON.stringify({
      paths: { "/x": { get: { operationId: "getX" } } },
    }));
    execFileSync("node", [SCRIPT, tmp], { encoding: "utf8" });
    const first = readFileSync(join(tmp, ".flow-map", ".cache", "callsites.ndjson"), "utf8");
    execFileSync("node", [SCRIPT, tmp], { encoding: "utf8" });
    const second = readFileSync(join(tmp, ".flow-map", ".cache", "callsites.ndjson"), "utf8");
    expect(first === second,
      `idempotent: re-run produced different output\nfirst=${first}\nsecond=${second}`);
    console.log("  ok  idempotent: re-run is byte-identical");
  } finally { rmSync(tmp, { recursive: true, force: true }); }
}

function expect(cond, msg) {
  if (!cond) { console.error(`  FAIL ${msg}`); failed++; }
}

function mkScratch(label) {
  const tmp = join(tmpdir(), `flow-map-${label}-${process.pid}-${Date.now()}`);
  mkdirSync(join(tmp, ".flow-map", ".cache"), { recursive: true });
  return tmp;
}

function writeRecon(tmp, specs) {
  writeFileSync(join(tmp, ".flow-map", ".cache", "recon.json"), JSON.stringify({
    schema_version: 1,
    framework: { adapter: "react", router: null, language: "ts" },
    api_clients: [],
    openapi_specs: specs,
  }));
}

function readNdjson(tmp) {
  const text = readFileSync(join(tmp, ".flow-map", ".cache", "callsites.ndjson"), "utf8").trim();
  return text === "" ? [] : text.split("\n").map((l) => JSON.parse(l));
}

