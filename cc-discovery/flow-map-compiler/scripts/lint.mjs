#!/usr/bin/env node
// flow-map-compiler lint - Step 1 subset (rules 3, 4, 5, 7, 9, 10, 13).
// Step 2 wires the rest. See references/lint-contract.md.
//
// Usage: node scripts/lint.mjs <flow-map-dir>
// Exits 0 (silent) on success, 1 with messages on failure.

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve, dirname } from "node:path";

const HTTP_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const HTTP_METHOD_LINE_RE = new RegExp(`^(${HTTP_METHODS.join("|")})\\s`);
const HTTP_PARTICIPANT_RE = new RegExp(`^(${HTTP_METHODS.join("|")}) /`);
const TOOL_NAME_RE = /^[a-z][a-zA-Z0-9]*\.[a-z][a-zA-Z0-9]*$/;

const failures = [];
function fail(file, rule, msg) {
  failures.push(`${file}: rule ${rule} - ${msg}`);
}

function listMd(dir) {
  try {
    return readdirSync(dir)
      .filter((n) => n.endsWith(".md"))
      .map((n) => join(dir, n));
  } catch {
    return [];
  }
}

// Split frontmatter from body.
function splitFrontmatter(text) {
  const lines = text.split("\n");
  if (lines[0] !== "---") return { frontmatter: "", body: text };
  const end = lines.indexOf("---", 1);
  if (end === -1) return { frontmatter: "", body: text };
  return {
    frontmatter: lines.slice(1, end).join("\n"),
    body: lines.slice(end + 1).join("\n"),
  };
}

// Tiny YAML subset: scalars, single-line strings, arrays of scalars
// (- "..."), and arrays of mappings (- key: value). Sufficient for the
// frontmatter contract.
function parseFrontmatter(fm) {
  const out = {};
  const lines = fm.split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "" || line.trim().startsWith("#")) { i++; continue; }
    const m = line.match(/^([A-Za-z_][A-Za-z0-9_-]*)\s*:\s*(.*)$/);
    if (!m) { i++; continue; }
    const key = m[1];
    const rest = m[2];
    if (rest === "" || rest === ">" || rest === "|") {
      const items = [];
      let j = i + 1;
      while (j < lines.length) {
        const nxt = lines[j];
        if (/^\s*-\s+/.test(nxt)) {
          const itemLine = nxt.replace(/^\s*-\s+/, "");
          const mapMatch = itemLine.match(/^([A-Za-z_][A-Za-z0-9_-]*)\s*:\s*(.*)$/);
          if (mapMatch) {
            const obj = {};
            obj[mapMatch[1]] = stripQuotes(mapMatch[2]);
            j++;
            while (j < lines.length && /^\s+[A-Za-z_][A-Za-z0-9_-]*\s*:/.test(lines[j]) && !/^\s*-\s+/.test(lines[j])) {
              const subM = lines[j].match(/^\s+([A-Za-z_][A-Za-z0-9_-]*)\s*:\s*(.*)$/);
              if (subM) obj[subM[1]] = stripQuotes(subM[2]);
              j++;
            }
            items.push(obj);
          } else {
            items.push(stripQuotes(itemLine));
            j++;
          }
        } else if (nxt.trim() === "" || /^\s+/.test(nxt)) {
          j++;
        } else {
          break;
        }
      }
      out[key] = items;
      i = j;
    } else {
      out[key] = stripQuotes(rest);
      i++;
    }
  }
  return out;
}

function stripQuotes(s) {
  s = s.trim();
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1);
  }
  return s;
}

// --- rule implementations ---

function checkRule3_flowLineCount(file, text) {
  const total = text.split("\n").length;
  if (total < 60 || total > 400) {
    fail(file, 3, `flow file is ${total} lines; must be between 60 and 400`);
  }
}

function checkRule4_flowFrontmatterFields(file, fm) {
  const required = ["description", "user_phrases", "intents_used", "preconditions"];
  for (const k of required) {
    const v = fm[k];
    if (v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0)) {
      fail(file, 4, `frontmatter field "${k}" missing or empty`);
    }
  }
}

function checkRule5_descriptionPrefix(file, fm) {
  const d = fm.description;
  if (typeof d !== "string" || !d.startsWith("Use when")) {
    fail(file, 5, `description must start with "Use when"`);
  }
}

// Rule 7: in each sequenceDiagram block, classify participant-like names
// as HTTP-style or tool-name-style. The two conventions must not mix.
function checkRule7_sequenceParticipants(file, body) {
  const blocks = extractFencedBlocks(body, "mermaid").filter((b) =>
    /sequenceDiagram/.test(b),
  );
  for (const block of blocks) {
    const participants = new Set();
    for (const line of block.split("\n")) {
      const p = line.match(/^\s*participant\s+(\S+)(?:\s+as\s+(.+))?$/);
      if (p) participants.add((p[2] ?? p[1]).trim());
      const a = line.match(/^\s*actor\s+(\S+)(?:\s+as\s+(.+))?$/);
      if (a) participants.add((a[2] ?? a[1]).trim());
    }
    let httpStyle = false;
    let toolStyle = false;
    for (const p of participants) {
      if (HTTP_PARTICIPANT_RE.test(p)) httpStyle = true;
      else if (TOOL_NAME_RE.test(p)) toolStyle = true;
    }
    if (httpStyle && toolStyle) {
      fail(file, 7, `sequenceDiagram mixes HTTP-style and tool-name-style participants`);
    }
  }
}

function extractFencedBlocks(body, lang) {
  const re = new RegExp("```" + lang + "\\n([\\s\\S]*?)```", "g");
  return [...body.matchAll(re)].map((m) => m[1]);
}

function checkRule9_noHttpInFlow(file, body) {
  const lines = body.split("\n");
  let inFence = false;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (/^\s*```/.test(line)) { inFence = !inFence; continue; }
    if (inFence) continue; // ignore code fences (mermaid, ts, sh, …)
    if (/^\s*#{1,6}\s/.test(line)) continue; // markdown headings
    if (HTTP_METHOD_LINE_RE.test(line)) {
      fail(file, 9, `line ${i + 1} starts with an HTTP method`);
    }
    if (line.includes("fetch(")) {
      fail(file, 9, `line ${i + 1} contains "fetch("`);
    }
    if (line.includes("axios.")) {
      fail(file, 9, `line ${i + 1} contains "axios."`);
    }
    if (/\/api\//.test(line)) {
      fail(file, 9, `line ${i + 1} contains a "/api/" URL path`);
    }
  }
}

function checkRule10_blockBalance(file, text) {
  const tokens = [];
  const re = /<!--\s*(\/?)(HUMAN|AGENT)(?:\s+id="([^"]*)")?\s*-->/g;
  for (const m of text.matchAll(re)) {
    const isClose = m[1] === "/";
    const kind = m[2];
    const id = m[3];
    if (!isClose) {
      if (id === undefined) {
        fail(file, 10, `${kind} open block missing id attribute`);
      }
      tokens.push({ kind, id });
    } else {
      const top = tokens.pop();
      if (!top) {
        fail(file, 10, `${kind} close has no matching open`);
      } else if (top.kind !== kind) {
        fail(file, 10, `mismatched close: expected /${top.kind} got /${kind}`);
      }
    }
  }
  for (const unclosed of tokens) {
    fail(file, 10, `${unclosed.kind} block id="${unclosed.id}" not closed`);
  }
}

// Hop 1: flow -> glossary anchor exists.
function checkRule13_flowToGlossary(file, body, flowMapRoot) {
  const re = /\(([^)\s]*glossary\.md)#([^)\s]+)\)/g;
  const glossaryPath = join(flowMapRoot, "glossary.md");
  let glossaryText;
  try {
    glossaryText = readFileSync(glossaryPath, "utf8");
  } catch {
    glossaryText = null;
  }
  for (const m of body.matchAll(re)) {
    const linkPath = m[1];
    const anchor = m[2];
    const flowDir = dirname(file);
    const target = resolve(flowDir, linkPath);
    let txt = glossaryText;
    if (resolve(target) !== resolve(glossaryPath)) {
      try {
        txt = readFileSync(target, "utf8");
      } catch {
        fail(file, 13, `linked glossary file not found: ${linkPath}`);
        continue;
      }
    } else if (txt === null) {
      fail(file, 13, `linked glossary file not found: ${linkPath}`);
      continue;
    }
    if (!anchorExists(txt, anchor)) {
      fail(file, 13, `intent #${anchor} not in ${linkPath}`);
    }
  }
}

// Hop 2: glossary -> capability anchor exists.
function checkRule13_glossaryToCapabilities(flowMapRoot) {
  const glossaryPath = join(flowMapRoot, "glossary.md");
  let text;
  try {
    text = readFileSync(glossaryPath, "utf8");
  } catch {
    return;
  }
  const re = /\(([^)\s]*capabilities\/([^)\s/]+)\.md)#([^)\s]+)\)/g;
  for (const m of text.matchAll(re)) {
    const linkPath = m[1];
    const capName = m[2];
    const anchor = m[3];
    const target = resolve(dirname(glossaryPath), linkPath);
    let capText;
    try {
      capText = readFileSync(target, "utf8");
    } catch {
      const alt = join(flowMapRoot, "capabilities", `${capName}.md`);
      try {
        capText = readFileSync(alt, "utf8");
      } catch {
        fail(glossaryPath, 13, `linked capability file not found: ${linkPath}`);
        continue;
      }
    }
    if (!anchorExists(capText, anchor)) {
      fail(glossaryPath, 13, `capability anchor #${anchor} not in ${linkPath}`);
    }
  }
}

function anchorExists(capText, anchor) {
  if (capText.includes(`{#${anchor}}`)) return true;
  for (const line of capText.split("\n")) {
    const h = line.match(/^#{1,6}\s+(.+?)\s*$/);
    if (!h) continue;
    const slug = h[1]
      .toLowerCase()
      .replace(/[^a-z0-9\s-]/g, "")
      .trim()
      .replace(/\s+/g, "-");
    if (slug === anchor) return true;
  }
  return false;
}

// --- rules 1, 2 ---
// Body length is measured after the frontmatter is stripped.

function checkRule1_agentsLineCount(file) {
  let text;
  try { text = readFileSync(file, "utf8"); } catch { return; }
  const { body } = splitFrontmatter(text);
  const n = body.split("\n").length;
  if (n > 500) fail(file, 1, `AGENTS.md body is ${n} lines; must be <= 500`);
}

function checkRule2_appLineCount(file) {
  let text;
  try { text = readFileSync(file, "utf8"); } catch { return; }
  const { body } = splitFrontmatter(text);
  const n = body.split("\n").length;
  if (n > 300) fail(file, 2, `APP.md body is ${n} lines; must be <= 300`);
}

// --- rule 6 (adapted to glossary indirection) ---
// Every entry in a flow's intents_used[] must have a matching anchor in
// glossary.md. Rule 13 hop 1 also catches body-link violations; this
// catches frontmatter violations the body might not surface.

function checkRule6_flowIntentsExistInGlossary(file, fm, glossaryText) {
  const list = fm.intents_used;
  if (!Array.isArray(list)) return;
  for (const entry of list) {
    // Accept either string-form ("update-user-record") or object-form
    // ({ intent: "...", role: "...", glossary_ref: "..." }).
    const intentId = typeof entry === "string" ? entry : entry?.intent;
    if (!intentId) continue;
    if (!glossaryText) {
      fail(file, 6, `intents_used references "${intentId}" but glossary.md is missing`);
      continue;
    }
    if (!anchorExists(glossaryText, intentId)) {
      fail(file, 6, `intent "${intentId}" in frontmatter has no anchor in glossary.md`);
    }
  }
}

// --- rule 8 (minimal mermaid syntax) ---
// Real parser comes later (mermaid-cli). For now: fenced ```mermaid blocks
// must start with a recognised diagram type. Catches obvious typos and
// truncated paste errors.

const MERMAID_HEADERS = [
  /^sequenceDiagram\b/,
  /^flowchart\s+(TB|TD|BT|RL|LR)\b/,
  /^graph\s+(TB|TD|BT|RL|LR)\b/,
  /^classDiagram\b/,
  /^stateDiagram(-v2)?\b/,
  /^erDiagram\b/,
  /^journey\b/,
  /^gantt\b/,
  /^pie\b/,
  /^mindmap\b/,
];

function checkRule8_mermaidSyntax(file, body) {
  const blocks = extractFencedBlocks(body, "mermaid");
  for (const [idx, block] of blocks.entries()) {
    const firstNonEmpty = block.split("\n").map((l) => l.trim()).find((l) => l !== "");
    if (!firstNonEmpty) {
      fail(file, 8, `mermaid block #${idx + 1} is empty`);
      continue;
    }
    if (!MERMAID_HEADERS.some((re) => re.test(firstNonEmpty))) {
      fail(file, 8, `mermaid block #${idx + 1} does not start with a recognised diagram type`);
    }
  }
}

// --- rule 11 ---
// Every flow id in AGENTS.md "Flows" table must appear in the
// "Intent -> flow" table.

function checkRule11_agentsIntentTableCoverage(file) {
  let text;
  try { text = readFileSync(file, "utf8"); } catch { return; }
  const { body } = splitFrontmatter(text);
  const flows = extractTableLinkedIds(body, /^##\s+Flows\s*$/m);
  const intents = extractTableLinkedIds(body, /^##\s+Intent\s*(?:→|->)\s*flow\s*$/m);
  for (const id of flows) {
    if (!intents.has(id)) {
      fail(file, 11, `flow "${id}" not present in "Intent -> flow" table`);
    }
  }
}

// Pull markdown link target ids from the table immediately following a
// header. We extract the substring of <linkpath>#<frag> shape; for the
// flows table the linkpath is "flows/<id>.md".
function extractTableLinkedIds(body, headerRe) {
  const lines = body.split("\n");
  const idx = lines.findIndex((l) => headerRe.test(l));
  if (idx === -1) return new Set();
  const ids = new Set();
  for (let i = idx + 1; i < lines.length; i++) {
    const l = lines[i];
    if (/^##\s/.test(l)) break;
    if (l.trim() === "") continue;
    // Accept absolute (`flows/foo.md`), explicit-relative (`./flows/foo.md`),
    // or parent-relative (`../flows/foo.md`) link forms; with optional
    // `#anchor` suffix.
    for (const m of l.matchAll(/\((?:\.{1,2}\/)?(?:[^)\s]*\/)?flows\/([^)#\s]+)\.md(?:#[^)\s]*)?\)/g)) {
      ids.add(m[1]);
    }
  }
  return ids;
}

// --- rule 12 ---
// Every intent referenced in glossary.md's lookup table must have a
// matching {#<intent>} anchor in the same file.

function checkRule12_glossaryIntentAnchors(file) {
  let text;
  try { text = readFileSync(file, "utf8"); } catch { return; }
  // Find table-cell self-links of shape (#intent) in the lookup table.
  const intents = new Set();
  for (const m of text.matchAll(/\(#([a-z0-9][a-z0-9-]*)\)/g)) {
    intents.add(m[1]);
  }
  for (const intent of intents) {
    if (!text.includes(`{#${intent}}`)) {
      fail(file, 12, `lookup-table intent "${intent}" has no matching anchor`);
    }
  }
}

// --- rule 14 ---
// Bidirectional consistency between tools-proposed.json and the union of
// every capability's tools[].tool frontmatter list.

function checkRule14_toolsBidirectional(flowMapRoot, capFiles) {
  const toolsJsonPath = join(flowMapRoot, "tools-proposed.json");
  let toolsJson;
  try {
    toolsJson = JSON.parse(readFileSync(toolsJsonPath, "utf8"));
  } catch {
    fail(toolsJsonPath, 14, `tools-proposed.json missing or unparseable`);
    return;
  }
  const jsonNames = new Set((toolsJson.tools ?? []).map((t) => t.proposed_name));

  const capNames = new Set();
  for (const c of capFiles) {
    let text;
    try { text = readFileSync(c, "utf8"); } catch { continue; }
    const { frontmatter } = splitFrontmatter(text);
    const fm = parseFrontmatter(frontmatter);
    if (!Array.isArray(fm.tools)) continue;
    for (const t of fm.tools) {
      if (typeof t === "object" && t.tool) capNames.add(t.tool);
    }
  }

  for (const n of jsonNames) {
    if (!capNames.has(n)) {
      fail(toolsJsonPath, 14, `tool "${n}" not present in any capability frontmatter`);
    }
  }
  for (const n of capNames) {
    if (!jsonNames.has(n)) {
      fail(toolsJsonPath, 14, `tool "${n}" present in capabilities but missing from tools-proposed.json`);
    }
  }
}

// --- rule 15 ---
// Every capability tools[] entry carries proposed: true. The skill must
// never emit proposed: false.

function checkRule15_proposedFlag(file, fm) {
  const list = fm.tools;
  if (!Array.isArray(list)) return;
  for (const entry of list) {
    if (typeof entry !== "object") continue;
    if (entry.proposed === undefined) {
      fail(file, 15, `tool "${entry.tool ?? "?"}" missing proposed flag`);
    } else if (String(entry.proposed) !== "true") {
      fail(file, 15, `tool "${entry.tool ?? "?"}" has proposed=${entry.proposed}; must be true`);
    }
  }
}

// --- main ---

function main() {
  const arg = process.argv[2];
  if (!arg) {
    console.error("usage: node scripts/lint.mjs <flow-map-dir>");
    process.exit(2);
  }
  const root = resolve(arg);
  let s;
  try {
    s = statSync(root);
  } catch {
    console.error(`not a directory: ${root}`);
    process.exit(2);
  }
  if (!s.isDirectory()) {
    console.error(`not a directory: ${root}`);
    process.exit(2);
  }

  const flowsDir = join(root, "flows");
  const capsDir = join(root, "capabilities");
  const flowFiles = listMd(flowsDir);
  const capFiles = listMd(capsDir);
  const glossaryPath = join(root, "glossary.md");
  const agentsPath = join(root, "AGENTS.md");
  const appPath = join(root, "APP.md");

  let glossaryText = null;
  try { glossaryText = readFileSync(glossaryPath, "utf8"); } catch {}

  for (const f of flowFiles) {
    const text = readFileSync(f, "utf8");
    const { frontmatter, body } = splitFrontmatter(text);
    const fm = parseFrontmatter(frontmatter);
    checkRule3_flowLineCount(f, text);
    checkRule4_flowFrontmatterFields(f, fm);
    checkRule5_descriptionPrefix(f, fm);
    checkRule6_flowIntentsExistInGlossary(f, fm, glossaryText);
    checkRule7_sequenceParticipants(f, body);
    checkRule8_mermaidSyntax(f, body);
    checkRule9_noHttpInFlow(f, body);
    checkRule10_blockBalance(f, text);
    checkRule13_flowToGlossary(f, body, root);
  }

  checkRule13_glossaryToCapabilities(root);

  if (glossaryText !== null) {
    checkRule10_blockBalance(glossaryPath, glossaryText);
    checkRule12_glossaryIntentAnchors(glossaryPath);
  }

  for (const c of capFiles) {
    const text = readFileSync(c, "utf8");
    const { frontmatter, body } = splitFrontmatter(text);
    const fm = parseFrontmatter(frontmatter);
    checkRule8_mermaidSyntax(c, body);
    checkRule10_blockBalance(c, text);
    checkRule15_proposedFlag(c, fm);
  }

  checkRule1_agentsLineCount(agentsPath);
  checkRule11_agentsIntentTableCoverage(agentsPath);
  // Reuse rule 10 + rule 8 on AGENTS.md.
  try {
    const atext = readFileSync(agentsPath, "utf8");
    const { body: abody } = splitFrontmatter(atext);
    checkRule8_mermaidSyntax(agentsPath, abody);
    checkRule10_blockBalance(agentsPath, atext);
  } catch {}

  checkRule2_appLineCount(appPath);
  try {
    const ptext = readFileSync(appPath, "utf8");
    const { body: pbody } = splitFrontmatter(ptext);
    checkRule8_mermaidSyntax(appPath, pbody);
    checkRule10_blockBalance(appPath, ptext);
  } catch {}

  checkRule14_toolsBidirectional(root, capFiles);

  if (failures.length === 0) {
    process.exit(0);
  }
  for (const f of failures) console.error(f);
  process.exit(1);
}

main();
