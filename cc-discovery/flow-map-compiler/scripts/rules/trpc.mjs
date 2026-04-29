// Rule pack: tRPC.
//
// Procedure path is the dotted chain before the terminal method.
// Supports optional chaining (`trpc?.users.update?.useMutation()`).
// Aliased imports from any `@trpc/*` module are honored.

import {
  maskCommentsAndStrings,
  makeLineIndex,
  lineFromIndex,
  depPresent,
} from "./_util.mjs";

export const meta = {
  id: "trpc",
  client: "trpc",
};

export function detect(pkg) {
  return depPresent(pkg, "@trpc/client") ||
         depPresent(pkg, "@trpc/react-query") ||
         depPresent(pkg, "@trpc/server") ||
         depPresent(pkg, "@trpc/next");
}

const TERMINAL = new Set([
  "query", "mutate",
  "useQuery", "useMutation", "useInfiniteQuery", "useSuspenseQuery",
  "useSubscription", "fetchQuery", "prefetchQuery", "ensureQueryData",
]);

const DEFAULT_ROOTS = new Set(["trpc", "trpcClient", "api"]);

export function extract(filePath, rawText) {
  const text = maskCommentsAndStrings(rawText);
  const offsets = makeLineIndex(text);
  const calls = [];

  const roots = new Set(DEFAULT_ROOTS);
  for (const m of text.matchAll(/\bimport\s+\{([^}]+)\}\s+from\s+['"]@trpc\/[^'"]+['"]/g)) {
    for (const piece of m[1].split(",")) {
      const part = piece.trim();
      const aliasMatch = part.match(/\bas\s+([A-Za-z_$][\w$]*)\b/);
      if (aliasMatch) roots.add(aliasMatch[1]);
      else {
        const name = part.split(/\s+/).pop();
        if (name) roots.add(name);
      }
    }
  }

  const rootAlt = [...roots].map((r) => r.replace(/[$]/g, "\\$")).join("|");
  if (!rootAlt) return { calls, unresolved: [] };
  const chainRe = new RegExp(
    `\\b(${rootAlt})((?:\\??\\.[A-Za-z_$][\\w$]*)+)\\s*\\(`,
    "g",
  );
  for (const m of text.matchAll(chainRe)) {
    const chain = m[2].split(/\??\./).filter(Boolean);
    if (chain.length < 2) continue;
    const terminal = chain[chain.length - 1];
    if (!TERMINAL.has(terminal)) continue;
    const procedure = chain.slice(0, -1).join(".");
    const isMutate = /mutation|mutate/i.test(terminal);
    const line = lineFromIndex(offsets, m.index);
    calls.push({
      file: filePath,
      line,
      method: isMutate ? "TRPC-MUTATE" : "TRPC-QUERY",
      path: `trpc:${procedure}`,
      auth: "unknown",
      client: "trpc",
      confidence: "high",
      raw_snippet: rawText.slice(m.index, m.index + 200).replace(/\s+/g, " "),
    });
  }
  return { calls, unresolved: [] };
}
