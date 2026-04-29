// Rule pack: Next.js server actions.
//
// A server action is either:
//   1. Any exported async function in a module whose first non-comment,
//      non-blank source line is `"use server"` (or `'use server'`).
//   2. Any function/arrow whose own body opens with `"use server"`.
//
// Path is `action:<exported-name>`. Method is `ACTION`.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  depPresent,
} from "./_util.mjs";

export const meta = {
  id: "server-actions",
  client: "server-actions",
};

export function detect(pkg) {
  return depPresent(pkg, "next");
}

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const calls = [];

  const fileLevelAction = hasFileLevelDirective(rawText);

  if (fileLevelAction) {
    // export async function name(
    for (const m of text.matchAll(/\bexport\s+async\s+function\s+([A-Za-z_$][\w$]*)/g)) {
      pushAction(calls, filePath, rawText, offsets, m.index, m[1]);
    }
    // export default async function [name]() — synthesize "default" if anon.
    for (const m of text.matchAll(/\bexport\s+default\s+async\s+function\s*([A-Za-z_$][\w$]*)?/g)) {
      pushAction(calls, filePath, rawText, offsets, m.index, m[1] || "default");
    }
    // export const foo = async ( | async function (
    for (const m of text.matchAll(/\bexport\s+(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*async\s*(?:function\b[^(]*)?\s*\(/g)) {
      pushAction(calls, filePath, rawText, offsets, m.index, m[1]);
    }
    // export default async (
    for (const m of text.matchAll(/\bexport\s+default\s+async\s*\(/g)) {
      pushAction(calls, filePath, rawText, offsets, m.index, "default");
    }
  }

  // Inline-directive forms — declared regardless of file-level directive.
  // function name(...) { "use server"; ... }
  for (const m of rawText.matchAll(/\bfunction\s+([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*\{\s*['"]use server['"]/g)) {
    pushAction(calls, filePath, rawText, offsets, m.index, m[1]);
  }
  // const name = async (...) => { "use server"; ... }
  for (const m of rawText.matchAll(/\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?\([^)]*\)\s*=>\s*\{\s*['"]use server['"]/g)) {
    pushAction(calls, filePath, rawText, offsets, m.index, m[1]);
  }

  // Dedupe by (path, line)
  const seen = new Set();
  const out = [];
  for (const c of calls) {
    const key = `${c.path}|${c.line}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(c);
  }
  return { calls: out, unresolved: [] };
}

function hasFileLevelDirective(rawText) {
  // BOM + leading comments + blank lines ok; the first source statement
  // must be the directive.
  let i = 0;
  if (rawText.charCodeAt(0) === 0xFEFF) i = 1;
  while (i < rawText.length) {
    // Skip whitespace including \r
    while (i < rawText.length && /\s/.test(rawText[i])) i++;
    // Line comment
    if (rawText.startsWith("//", i)) {
      while (i < rawText.length && rawText[i] !== "\n") i++;
      continue;
    }
    // Block comment
    if (rawText.startsWith("/*", i)) {
      const end = rawText.indexOf("*/", i + 2);
      if (end < 0) return false;
      i = end + 2;
      continue;
    }
    break;
  }
  const slice = rawText.slice(i, i + 64);
  return /^['"]use server['"]\s*;?/.test(slice);
}

function pushAction(calls, filePath, rawText, offsets, idx, name) {
  calls.push({
    file: filePath,
    line: lineFromIndex(offsets, idx),
    method: "ACTION",
    path: `action:${name}`,
    auth: "unknown",
    client: "server-actions",
    confidence: "high",
    raw_snippet: rawText.slice(idx, idx + 200).replace(/\s+/g, " "),
  });
}
