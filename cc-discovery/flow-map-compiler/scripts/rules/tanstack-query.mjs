// Rule pack: @tanstack/react-query / react-query.
//
// Wrapper-only: emits annotations, never new endpoints. The endpoint
// comes from the fetch / axios / openapi-fetch pack scanning the
// queryFn / mutationFn body.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  depPresent,
} from "./_util.mjs";

export const meta = {
  id: "tanstack-query",
  client: "tanstack-query",
};

export function detect(pkg) {
  return depPresent(pkg, "@tanstack/react-query") || depPresent(pkg, "react-query");
}

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const wrappers = [];
  for (const m of text.matchAll(/\buse(Query|Mutation|InfiniteQuery|SuspenseQuery|Queries|MutationState)\s*\(/g)) {
    wrappers.push({
      file: filePath,
      line: lineFromIndex(offsets, m.index),
      hook: `use${m[1]}`,
      kind: m[1].includes("Mutation") ? "mutation" : "query",
    });
  }
  return { calls: [], unresolved: [], wrappers };
}
