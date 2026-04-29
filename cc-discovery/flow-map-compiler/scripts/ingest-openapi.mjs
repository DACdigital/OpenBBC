#!/usr/bin/env node
// Phase 1.5 - OpenAPI spec ingestion (optional).
//
// If `recon.json` lists any `openapi_specs`, parse each one and inject
// every operation into `callsites.ndjson` as a synthetic call site with
// client="openapi-spec". Idempotent: re-running this step replaces any
// previous openapi-spec records, never duplicates.
//
// JSON specs are parsed natively. YAML specs are parsed with a minimal
// in-tree reader covering the subset OpenAPI uses (block-style mappings
// and sequences, no anchors, no flow style). For exotic specs, convert
// to JSON first: `npx js-yaml openapi.yaml > openapi.json`.

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { resolve, join, extname } from "node:path";

const HTTP_VERBS = new Set(["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]);

const repoRoot = resolve(process.argv[2] ?? ".");
const cacheDir = join(repoRoot, ".flow-map", ".cache");
const reconPath = join(cacheDir, "recon.json");
if (!existsSync(reconPath)) {
  console.error("ingest-openapi: recon.json missing — run scripts/recon.mjs first");
  process.exit(1);
}
const recon = JSON.parse(readFileSync(reconPath, "utf8"));
const specs = recon.openapi_specs ?? [];

const ndjsonPath = join(cacheDir, "callsites.ndjson");
const existingLines = existsSync(ndjsonPath)
  ? readFileSync(ndjsonPath, "utf8").split("\n").filter(Boolean)
  : [];
// Drop any prior openapi-spec records so re-runs are byte-identical.
const kept = existingLines.filter((l) => {
  try { return JSON.parse(l).client !== "openapi-spec"; } catch { return true; }
});

if (specs.length === 0) {
  if (existingLines.length !== kept.length) {
    writeFileSync(ndjsonPath, kept.length ? kept.join("\n") + "\n" : "");
    process.stdout.write("ingest-openapi: no specs in recon.json — cleared previous spec records\n");
  } else {
    process.stdout.write("ingest-openapi: no specs in recon.json — skipping\n");
  }
  process.exit(0);
}

const synthetic = [];
for (const specPath of specs) {
  const abs = join(repoRoot, specPath);
  let raw;
  try { raw = readFileSync(abs, "utf8"); } catch (e) {
    console.error(`ingest-openapi: cannot read ${abs}: ${e.message}`);
    continue;
  }
  let doc;
  try {
    if (extname(specPath) === ".json") doc = JSON.parse(raw);
    else doc = parseMinimalYaml(raw);
  } catch (e) {
    console.error(`ingest-openapi: parse error in ${specPath}: ${e.message}`);
    continue;
  }
  const paths = doc?.paths ?? {};
  // Sort path keys + method order so output is deterministic regardless
  // of object insertion order (which varies across YAML/JSON encoders).
  const pathKeys = Object.keys(paths).sort();
  for (const path of pathKeys) {
    const ops = paths[path];
    if (!ops || typeof ops !== "object") continue;
    const methodKeys = Object.keys(ops).sort();
    for (const method of methodKeys) {
      if (!HTTP_VERBS.has(method.toUpperCase())) continue;
      const op = ops[method];
      synthetic.push({
        file: specPath,
        line: 0,
        method: method.toUpperCase(),
        path,
        auth: detectAuth(op),
        client: "openapi-spec",
        confidence: "high",
        raw_snippet: `${method.toUpperCase()} ${path} (${op?.operationId ?? "no operationId"})`,
        entry: null,
        rule_pack: "openapi-spec",
        operation_id: op?.operationId ?? null,
        summary: op?.summary ?? null,
      });
    }
  }
}

const merged = [...kept, ...synthetic.map((c) => JSON.stringify(c))];
writeFileSync(ndjsonPath, merged.length ? merged.join("\n") + "\n" : "");

process.stdout.write(
  `ingest-openapi: ${synthetic.length} operations from ${specs.length} spec(s)\n`,
);

// ---- helpers ----

function detectAuth(op) {
  if (!op || !op.security) return "unknown";
  if (Array.isArray(op.security) && op.security.length === 0) return "none";
  const first = op.security?.[0];
  if (!first) return "unknown";
  const schemes = Object.keys(first);
  if (schemes.some((s) => /bearer|jwt|oauth/i.test(s))) return "bearer";
  if (schemes.some((s) => /cookie/i.test(s))) return "cookie";
  return "unknown";
}

// Minimal YAML reader. Supports:
//  - block-style mappings, block-style sequences
//  - scalar values: bare, single-quoted, double-quoted
//  - any consistent indent ≥ parent + 1 (the first child sets the unit
//    for that level — most OpenAPI specs are 2-space, some are 4)
// Does NOT support anchors, flow style ({} or []), block scalars
// (|, >), or multiline-folded scalars.
function parseMinimalYaml(text) {
  const lines = text.split("\n").map((l) => l.replace(/\r$/, ""));
  let i = 0;

  function isBlankOrComment(l) {
    return l === "" || /^\s*#/.test(l);
  }

  // Look-ahead: indent of next non-blank line, without moving the cursor.
  function peekIndent() {
    let j = i;
    while (j < lines.length && isBlankOrComment(lines[j])) j++;
    if (j >= lines.length) return -1;
    return lines[j].match(/^( *)/)[0].length;
  }

  function parseBlock(indent) {
    while (i < lines.length && isBlankOrComment(lines[i])) i++;
    if (i >= lines.length) return null;
    const m = lines[i].match(/^( *)(.*)$/);
    const ind = m[1].length;
    if (ind < indent) return null;
    if (m[2].startsWith("- ") || m[2] === "-") return parseSequence(indent);
    return parseMapping(indent);
  }

  function parseMapping(indent) {
    const out = {};
    while (i < lines.length) {
      if (isBlankOrComment(lines[i])) { i++; continue; }
      const m = lines[i].match(/^( *)(.*)$/);
      const ind = m[1].length;
      const body = m[2];
      if (ind < indent) break;
      if (ind > indent) {
        // The previous key declared a child block; we're already past it.
        // This means the next key should be at the same indent. If it
        // isn't, the doc is malformed.
        throw new Error(`yaml: unexpected indent ${ind} (expected ${indent}) at line ${i + 1}`);
      }
      const split = splitKeyValue(body);
      if (!split) throw new Error(`yaml: expected key:value at line ${i + 1}: ${body}`);
      const key = unquote(split.key);
      const rest = split.value;
      i++;
      if (rest === "") {
        const childInd = peekIndent();
        if (childInd > indent) out[key] = parseBlock(childInd);
        else out[key] = null;
      } else {
        out[key] = parseScalar(rest);
      }
    }
    return out;
  }

  function parseSequence(indent) {
    const out = [];
    while (i < lines.length) {
      if (isBlankOrComment(lines[i])) { i++; continue; }
      const m = lines[i].match(/^( *)(.*)$/);
      const ind = m[1].length;
      const body = m[2];
      if (ind < indent) break;
      if (ind > indent) break; // belongs to a deeper block parsed elsewhere
      if (!body.startsWith("- ") && body !== "-") break;
      const rest = body.replace(/^- ?/, "");
      i++;
      if (rest === "") {
        const childInd = peekIndent();
        if (childInd > indent) out.push(parseBlock(childInd));
        else out.push(null);
      } else if (looksLikeMappingHead(rest)) {
        // Inline mapping start: "- key: value" then sibling keys at the
        // same column as the inline key (which is indent + 2 for "- ").
        const childIndent = indent + 2;
        const split = splitKeyValue(rest);
        const obj = {};
        if (split) {
          const key = unquote(split.key);
          if (split.value === "") {
            const childInd = peekIndent();
            if (childInd > childIndent) obj[key] = parseBlock(childInd);
            else obj[key] = null;
          } else {
            obj[key] = parseScalar(split.value);
          }
        }
        const more = parseMapping(childIndent);
        Object.assign(obj, more ?? {});
        out.push(obj);
      } else {
        out.push(parseScalar(rest));
      }
    }
    return out;
  }

  function looksLikeMappingHead(s) {
    // "key: value" — but not a URL scalar like "http://example.com".
    const split = splitKeyValue(s);
    if (!split) return false;
    // If the "key" itself contains URL scheme indicators, treat as scalar.
    if (/^(https?|ftp|s3|file|mailto)$/i.test(split.key.trim())) return false;
    if (/^[A-Za-z_][\w$. -]*$/.test(split.key.trim())) return true;
    if (split.key.trim().startsWith('"') || split.key.trim().startsWith("'")) return true;
    return false;
  }

  // Split "key: value" honoring quotes around the key. The colon must be
  // unquoted. Returns { key, value } or null.
  function splitKeyValue(line) {
    let i2 = 0;
    let inStr = null;
    while (i2 < line.length) {
      const c = line[i2];
      if (inStr) {
        if (c === inStr) inStr = null;
        i2++;
        continue;
      }
      if (c === '"' || c === "'") { inStr = c; i2++; continue; }
      if (c === "#") return null; // comment
      if (c === ":" && (i2 + 1 === line.length || /\s/.test(line[i2 + 1]))) {
        const key = line.slice(0, i2).trim();
        let value = line.slice(i2 + 1).trim();
        // Strip trailing comment on value (only if outside quotes)
        value = stripTrailingComment(value);
        return { key, value };
      }
      i2++;
    }
    return null;
  }

  function stripTrailingComment(s) {
    let inStr = null;
    for (let k = 0; k < s.length; k++) {
      const c = s[k];
      if (inStr) {
        if (c === inStr) inStr = null;
        continue;
      }
      if (c === '"' || c === "'") { inStr = c; continue; }
      if (c === "#" && (k === 0 || /\s/.test(s[k - 1]))) return s.slice(0, k).trimEnd();
    }
    return s;
  }

  function parseScalar(s) {
    s = stripTrailingComment(s).trim();
    if (s === "" || s === "null" || s === "~") return null;
    if (s === "true") return true;
    if (s === "false") return false;
    if (/^-?\d+$/.test(s)) return Number(s);
    if (/^-?\d+\.\d+$/.test(s)) return Number(s);
    return unquote(s);
  }

  function unquote(s) {
    if (s.length >= 2 && ((s[0] === '"' && s.endsWith('"')) || (s[0] === "'" && s.endsWith("'")))) {
      return s.slice(1, -1);
    }
    return s;
  }

  return parseBlock(0);
}
