// Rule pack: openapi-fetch.
//
// Calls look like:
//   client.GET("/users/{id}", { params: { path: { id } } });
//   client.POST("/users", { body: {...} });
//   const { GET, POST } = client; GET("/users");                  // destructured
//   client.GET(`/users/${id}`, ...);                              // template path
//
// Allowlist `<ident>` against either a `createClient(...)` binding or
// a destructure from one. Falls back to allowing the conventional
// identifier `client` so single-file demos work.

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
  id: "openapi-fetch",
  client: "openapi-fetch",
};

export function detect(pkg) {
  return depPresent(pkg, "openapi-fetch");
}

const VERBS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const calls = [];
  const unresolved = [];

  const clients = collectClientIdents(text);

  const memberRe = new RegExp(`\\b([A-Za-z_$][\\w$]*)\\.(${VERBS.join("|")})\\s*\\(`, "g");
  for (const m of text.matchAll(memberRe)) {
    const ident = m[1];
    if (!clients.has(ident)) continue;
    pushCall(text, rawText, offsets, calls, unresolved, filePath, m, m[2]);
  }

  // Destructured shorthand: bare `GET("/path")` only when GET was
  // destructured from a known client.
  const verbRe = new RegExp(`(^|[^A-Za-z0-9_$.])(${VERBS.join("|")})\\s*\\(`, "g");
  for (const m of text.matchAll(verbRe)) {
    const verb = m[2];
    if (!clients.has(verb)) continue;
    pushCall(text, rawText, offsets, calls, unresolved, filePath, m, verb, m[1].length);
  }

  return { calls, unresolved };
}

function pushCall(text, rawText, offsets, calls, unresolved, filePath, m, verb, leadOffset = 0) {
  const callStart = m.index + m[0].length;
  const args = sliceCallArgs(text, callStart);
  if (args == null) return;
  const [pathArg] = splitArgs(args);
  const path = parsePathLiteral(pathArg);
  const startIdx = m.index + leadOffset;
  const line = lineFromIndex(offsets, startIdx);
  if (!path) {
    unresolved.push({
      file: filePath, line,
      snippet: rawText.slice(startIdx, startIdx + 200).replace(/\s+/g, " "),
      reason: `openapi-fetch ${verb}() path is not a static literal`,
    });
    return;
  }
  calls.push({
    file: filePath,
    line,
    method: verb,
    path,
    auth: inferAuth(args),
    client: "openapi-fetch",
    confidence: "high",
    raw_snippet: rawText.slice(startIdx, startIdx + 200).replace(/\s+/g, " "),
  });
}

function collectClientIdents(text) {
  const idents = new Set();
  for (const m of text.matchAll(/\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*createClient\b/g)) idents.add(m[1]);
  for (const m of text.matchAll(/\b(?:const|let|var)\s*\{\s*([^}]+)\s*\}\s*=\s*([A-Za-z_$][\w$]*)/g)) {
    const sourceIdent = m[2];
    if (!idents.has(sourceIdent) && sourceIdent !== "client") continue;
    for (const piece of m[1].split(",")) {
      const name = piece.trim().split(/\s*:\s*|\s+as\s+/).pop().trim();
      if (VERBS.includes(name)) idents.add(name);
    }
  }
  idents.add("client");
  return idents;
}
