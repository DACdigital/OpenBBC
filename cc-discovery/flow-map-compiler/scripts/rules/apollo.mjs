// Rule pack: Apollo Client.
//
// Extracts every gql`...` tagged template, regardless of binding form
// (top-level const, object property, function arg, default export).
// Operation name from `query|mutation|subscription <name>` inside the
// body. Brace-aware scanning lets nested `${ ... }` interpolations
// (fragment composition) survive.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  depPresent,
} from "./_util.mjs";

export const meta = {
  id: "apollo",
  client: "apollo",
};

export function detect(pkg) {
  return depPresent(pkg, "@apollo/client") ||
         depPresent(pkg, "apollo-client") ||
         depPresent(pkg, "@apollo/client-react-streaming");
}

export function extract(filePath, rawText) {
  return extractGql(filePath, rawText, "apollo");
}

export function extractGql(filePath, rawText, client) {
  // We keep the raw text here so we can read inside templates;
  // comment masking would erase backtick contents.
  const offsets = makeLineIndex(rawText);
  const calls = [];

  // Locate every `gql\`...\`` site by hand-scanning to handle nested
  // templates from fragment interpolation.
  let i = 0;
  while (i < rawText.length) {
    const at = rawText.indexOf("gql", i);
    if (at < 0) break;
    // Word-boundary check before
    const before = at === 0 ? "" : rawText[at - 1];
    if (/[A-Za-z0-9_$]/.test(before)) { i = at + 3; continue; }
    let j = at + 3;
    // Allow whitespace then optional `<TypeArgs>` then backtick
    while (j < rawText.length && /\s/.test(rawText[j])) j++;
    if (j < rawText.length && rawText[j] === "<") {
      // Skip generic args
      let depth = 1; j++;
      while (j < rawText.length && depth > 0) {
        if (rawText[j] === "<") depth++;
        else if (rawText[j] === ">") depth--;
        j++;
      }
      while (j < rawText.length && /\s/.test(rawText[j])) j++;
    }
    if (j >= rawText.length || rawText[j] !== "`") { i = at + 3; continue; }
    // Found gql template at position j (backtick).
    const bodyStart = j + 1;
    const bodyEnd = scanTemplateEnd(rawText, bodyStart);
    if (bodyEnd < 0) { i = j + 1; continue; }
    const body = rawText.slice(bodyStart, bodyEnd);
    const op = body.match(/\b(query|mutation|subscription)\s+([A-Za-z_$][\w$]*)/);
    const line = lineFromIndex(offsets, at);
    if (op) {
      const kind = op[1].toUpperCase();
      const name = op[2];
      calls.push({
        file: filePath, line,
        method: `GQL-${kind}`,
        path: `graphql:${name}`,
        auth: "unknown",
        client,
        confidence: "high",
        raw_snippet: rawText.slice(at, at + 200).replace(/\s+/g, " "),
      });
    } else {
      // Anonymous operation. Synthesize a stable id from a context
      // identifier if one is bindable (const NAME = gql`...`); else skip.
      const id = bindableIdentBefore(rawText, at);
      if (id) {
        calls.push({
          file: filePath, line,
          method: "GQL-QUERY",
          path: `graphql:${id}`,
          auth: "unknown",
          client,
          confidence: "low",
          raw_snippet: rawText.slice(at, at + 200).replace(/\s+/g, " "),
        });
      }
    }
    i = bodyEnd + 1;
  }
  return { calls, unresolved: [] };
}

// Find the backtick that closes the template starting at `from`,
// honoring nested `${...}` (which itself may contain strings/templates).
function scanTemplateEnd(text, from) {
  let i = from;
  while (i < text.length) {
    const ch = text[i];
    if (ch === "\\") { i += 2; continue; }
    if (ch === "`") return i;
    if (ch === "$" && text[i + 1] === "{") {
      let depth = 1;
      i += 2;
      while (i < text.length && depth > 0) {
        const c = text[i];
        if (c === "\\") { i += 2; continue; }
        if (c === '"' || c === "'") {
          const q = c; i++;
          while (i < text.length && text[i] !== q) {
            if (text[i] === "\\") { i += 2; continue; }
            if (text[i] === "\n") break;
            i++;
          }
          i++;
          continue;
        }
        if (c === "`") {
          i++;
          const innerEnd = scanTemplateEnd(text, i);
          if (innerEnd < 0) return -1;
          i = innerEnd + 1;
          continue;
        }
        if (c === "{") depth++;
        else if (c === "}") depth--;
        i++;
      }
      continue;
    }
    i++;
  }
  return -1;
}

function bindableIdentBefore(text, gqlIdx) {
  // Scan back through whitespace and `=` to find an identifier in
  // forms: `const X = gql\`...\``, `X = gql\`...\``, `: gql\`...\``
  // (object property). For property form we capture the property name.
  const head = text.slice(Math.max(0, gqlIdx - 200), gqlIdx);
  // const X =
  let m = head.match(/(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*$/);
  if (m) return m[1];
  // X =
  m = head.match(/\b([A-Za-z_$][\w$]*)\s*=\s*$/);
  if (m) return m[1];
  // prop: gql`...`
  m = head.match(/\b([A-Za-z_$][\w$]*)\s*:\s*$/);
  if (m) return m[1];
  return null;
}
