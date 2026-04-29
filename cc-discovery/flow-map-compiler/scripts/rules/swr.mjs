// Rule pack: SWR.
//
// Wrapper-only: emits annotations, not endpoints. Endpoint comes from
// the fetcher's underlying client pack.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  depPresent,
} from "./_util.mjs";

export const meta = {
  id: "swr",
  client: "swr",
};

export function detect(pkg) {
  return depPresent(pkg, "swr");
}

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const wrappers = [];
  for (const m of text.matchAll(/\buse(SWR|SWRMutation|SWRInfinite|SWRImmutable|SWRSubscription)\s*\(/g)) {
    wrappers.push({
      file: filePath,
      line: lineFromIndex(offsets, m.index),
      hook: `use${m[1]}`,
      kind: m[1].includes("Mutation") ? "mutation" : "query",
    });
  }
  return { calls: [], unresolved: [], wrappers };
}
