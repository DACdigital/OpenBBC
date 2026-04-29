// Minimal glob replacement so the skill ships zero dependencies.
// Supports `**`, `*`, and brace expansion `{ts,tsx}`. Sufficient for our
// patterns; if we outgrow it, swap in `fast-glob` later.

import { readdirSync, statSync } from "node:fs";
import { join, sep } from "node:path";

export async function glob(pattern, { cwd } = { cwd: process.cwd() }) {
  const expanded = expandBraces(pattern);
  const results = new Set();
  for (const p of expanded) {
    const re = patternToRegex(p);
    walk(cwd, "", re, results);
  }
  return [...results].sort();
}

function expandBraces(p) {
  const m = p.match(/\{([^}]+)\}/);
  if (!m) return [p];
  const head = p.slice(0, m.index);
  const tail = p.slice(m.index + m[0].length);
  const out = [];
  for (const opt of m[1].split(",")) {
    out.push(...expandBraces(head + opt + tail));
  }
  return out;
}

function patternToRegex(p) {
  // Convert `**` -> match any segments; `*` -> match within a segment.
  // Escape regex special chars except '*' and '/'.
  let r = "";
  let i = 0;
  while (i < p.length) {
    const ch = p[i];
    if (ch === "*" && p[i + 1] === "*") {
      // Could be `**/` (match zero or more segments) or just `**` at end.
      if (p[i + 2] === "/") {
        r += "(?:.*/)?";
        i += 3;
      } else {
        r += ".*";
        i += 2;
      }
    } else if (ch === "*") {
      r += "[^/]*";
      i++;
    } else if (ch === ".") {
      r += "\\.";
      i++;
    } else if (ch === "/") {
      r += "/";
      i++;
    } else if (/[a-zA-Z0-9_-]/.test(ch)) {
      r += ch;
      i++;
    } else {
      r += "\\" + ch;
      i++;
    }
  }
  return new RegExp("^" + r + "$");
}

function walk(absCwd, rel, re, results) {
  const abs = rel === "" ? absCwd : join(absCwd, rel);
  let entries;
  try { entries = readdirSync(abs); } catch { return; }
  for (const name of entries) {
    if (name === "node_modules" || name === ".git" || name === ".flow-map" || name.startsWith(".")) continue;
    const childRel = rel === "" ? name : rel + "/" + name;
    const full = join(abs, name);
    let s;
    try { s = statSync(full); } catch { continue; }
    if (s.isDirectory()) {
      walk(absCwd, childRel, re, results);
    } else if (s.isFile()) {
      const norm = childRel.split(sep).join("/");
      if (re.test(norm)) results.add(norm);
    }
  }
}
