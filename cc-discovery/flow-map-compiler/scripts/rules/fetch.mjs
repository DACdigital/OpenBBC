// Rule pack: native fetch() calls.
//
// Comments and string contents are masked before scanning so example
// code in JSDoc / template-literal docs doesn't surface. We use
// brace-aware arg slicing so nested `{ headers: { Authorization: ... } }`
// auth detection works.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  sliceCallArgs,
  splitArgs,
  parsePathLiteral,
  inferAuth,
} from "./_util.mjs";

export const meta = {
  id: "fetch",
  client: "fetch",
};

export function detect(/* pkg */) {
  return true;
}

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const calls = [];
  const unresolved = [];

  // Require non-identifier char before `fetch` so `pre.fetch(` doesn't match.
  for (const m of text.matchAll(/(^|[^A-Za-z0-9_$])fetch\s*\(/g)) {
    const callStart = m.index + m[0].length;
    const args = sliceCallArgs(text, callStart);
    if (args == null) continue;
    const parts = splitArgs(args);
    const urlExpr = parts[0];
    const optsExpr = parts.slice(1).join(", ");
    const startIdx = m.index + (m[1] ? 1 : 0);
    const line = lineFromIndex(offsets, startIdx);
    const path = parsePathLiteral(urlExpr);
    if (!path) {
      unresolved.push({
        file: filePath,
        line,
        snippet: rawText.slice(startIdx, startIdx + 200).replace(/\s+/g, " "),
        reason: "fetch() URL is not a static literal",
      });
      continue;
    }
    const method = methodFromOpts(optsExpr) ?? "GET";
    calls.push({
      file: filePath,
      line,
      method,
      path,
      auth: inferAuth(rawText.slice(callStart, callStart + args.length)),
      client: "fetch",
      confidence: "high",
      raw_snippet: rawText.slice(startIdx, startIdx + 200).replace(/\s+/g, " "),
    });
  }
  return { calls, unresolved };
}

function methodFromOpts(optsExpr) {
  if (!optsExpr) return null;
  const m = optsExpr.match(/method\s*:\s*['"`]([A-Z]+)['"`]/);
  return m ? m[1] : null;
}
