// Rule pack: axios.
//
// Allowlist-driven: we only treat `<ident>.<verb>(...)` as an axios
// call when `<ident>` is statically traceable to an axios import or an
// `axios.create()` result. Otherwise a repo with both axios and Express
// would have every `app.get('/users', cb)` flagged as an HTTP client
// call.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  sliceCallArgs,
  splitArgs,
  parsePathLiteral,
  inferAuth,
  depPresent,
} from "./_util.mjs";

export const meta = {
  id: "axios",
  client: "axios",
};

export function detect(pkg) {
  return depPresent(pkg, "axios");
}

const VERBS = ["get", "post", "put", "patch", "delete", "head", "options", "request"];

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const calls = [];
  const unresolved = [];

  const axiosIdents = collectAxiosIdents(text);
  axiosIdents.add("axios");

  const verbRe = new RegExp(`\\b([A-Za-z_$][\\w$]*)\\.(${VERBS.join("|")})\\s*\\(`, "g");
  for (const m of text.matchAll(verbRe)) {
    const ident = m[1];
    const verb = m[2];
    if (!axiosIdents.has(ident)) continue;
    const callStart = m.index + m[0].length;
    const args = sliceCallArgs(text, callStart);
    if (args == null) continue;
    const parts = splitArgs(args);
    const urlExpr = parts[0];
    const restExpr = parts.slice(1).join(", ");
    const line = lineFromIndex(offsets, m.index);

    if (verb === "request" || (parts.length >= 1 && looksLikeConfigObject(parts[0]))) {
      const cfg = parts[0];
      const method = (cfg.match(/method\s*:\s*['"`]([A-Za-z]+)['"`]/) || [, "GET"])[1].toUpperCase();
      const url = cfg.match(/url\s*:\s*(['"`])([^'"`]+)\1/);
      if (!url) {
        unresolved.push({
          file: filePath, line,
          snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
          reason: `${ident}.${verb}() url is not a static literal`,
        });
        continue;
      }
      calls.push({
        file: filePath, line, method,
        path: normalizeTemplate(url[2]),
        auth: inferAuth(cfg),
        client: "axios", confidence: "high",
        raw_snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
      });
      continue;
    }

    const path = parsePathLiteral(urlExpr);
    if (!path) {
      unresolved.push({
        file: filePath, line,
        snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
        reason: `${ident}.${verb}() URL is not a static literal`,
      });
      continue;
    }
    calls.push({
      file: filePath, line,
      method: verb.toUpperCase(),
      path,
      auth: inferAuth(restExpr),
      client: "axios",
      confidence: ident === "axios" ? "high" : "medium",
      raw_snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
    });
  }

  // Bare-call form: axios({method, url, ...})
  for (const m of text.matchAll(/\baxios\s*\(/g)) {
    const start = m.index + m[0].length;
    const args = sliceCallArgs(text, start);
    if (args == null) continue;
    const parts = splitArgs(args);
    if (!parts.length || !looksLikeConfigObject(parts[0])) continue;
    const cfg = parts[0];
    const method = (cfg.match(/method\s*:\s*['"`]([A-Za-z]+)['"`]/) || [, "GET"])[1].toUpperCase();
    const url = cfg.match(/url\s*:\s*(['"`])([^'"`]+)\1/);
    const line = lineFromIndex(offsets, m.index);
    if (!url) {
      unresolved.push({
        file: filePath, line,
        snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
        reason: "axios({...}) url is not a static literal",
      });
      continue;
    }
    calls.push({
      file: filePath, line, method,
      path: normalizeTemplate(url[2]),
      auth: inferAuth(cfg),
      client: "axios", confidence: "high",
      raw_snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
    });
  }

  return { calls, unresolved };
}

function collectAxiosIdents(text) {
  const idents = new Set();
  for (const m of text.matchAll(/\bimport\s+([A-Za-z_$][\w$]*)\s+from\s+['"]axios['"]/g)) idents.add(m[1]);
  for (const m of text.matchAll(/\bimport\s+\*\s+as\s+([A-Za-z_$][\w$]*)\s+from\s+['"]axios['"]/g)) idents.add(m[1]);
  for (const m of text.matchAll(/\bimport\s+\{[^}]*\bdefault\s+as\s+([A-Za-z_$][\w$]*)[^}]*\}\s+from\s+['"]axios['"]/g)) idents.add(m[1]);
  for (const m of text.matchAll(/\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*axios\.create\s*\(/g)) idents.add(m[1]);
  for (const m of text.matchAll(/\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*require\s*\(\s*['"]axios['"]\s*\)/g)) idents.add(m[1]);
  return idents;
}

function looksLikeConfigObject(expr) {
  return expr && expr.trim().startsWith("{") && /\b(method|url)\s*:/.test(expr);
}

function normalizeTemplate(s) {
  return s.replace(/\$\{\s*([^}]+?)\s*\}/g, (_, inner) => {
    const tail = inner.split(/[?.]/).pop().trim();
    const name = (tail.match(/^[A-Za-z_$][\w$]*/) || [])[0];
    return `{${name || "param"}}`;
  });
}
