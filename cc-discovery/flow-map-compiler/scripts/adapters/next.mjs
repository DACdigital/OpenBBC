// Next.js adapter. Probes for `next` in package.json. Enumerates entry
// points from app/ (App Router) and pages/ (Pages Router). Groups flows
// by entry-point ancestor (App Router uses nearest layout; Pages Router
// uses the page file itself).

import { readFileSync, existsSync } from "node:fs";
import { join, relative, dirname, sep } from "node:path";
import { glob } from "./glob.mjs";

function toPosix(p) {
  return sep === "/" ? p : p.split(sep).join("/");
}

export const meta = {
  id: "next",
  language_default: "ts",
};

export function probe(repoRoot) {
  const pkgPath = join(repoRoot, "package.json");
  if (!existsSync(pkgPath)) return 0;
  let pkg;
  try { pkg = JSON.parse(readFileSync(pkgPath, "utf8")); } catch { return 0; }
  const deps = { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) };
  if (!deps.next) return 0;
  const hasApp = existsSync(join(repoRoot, "app")) || existsSync(join(repoRoot, "src/app"));
  const hasPages = existsSync(join(repoRoot, "pages")) || existsSync(join(repoRoot, "src/pages"));
  if (!hasApp && !hasPages) return 0.5; // dependency present but no router dir; weak signal
  return 1.0;
}

export function detectRouter(repoRoot) {
  const hasApp = existsSync(join(repoRoot, "app")) || existsSync(join(repoRoot, "src/app"));
  const hasPages = existsSync(join(repoRoot, "pages")) || existsSync(join(repoRoot, "src/pages"));
  if (hasApp && hasPages) return "both";
  if (hasApp) return "app";
  if (hasPages) return "pages";
  return "none";
}

export async function enumerateEntries(repoRoot) {
  const patterns = [
    "app/**/page.{ts,tsx,js,jsx}",
    "src/app/**/page.{ts,tsx,js,jsx}",
    "app/**/route.{ts,tsx,js,jsx}",
    "src/app/**/route.{ts,tsx,js,jsx}",
    "pages/**/*.{ts,tsx,js,jsx}",
    "src/pages/**/*.{ts,tsx,js,jsx}",
  ];
  const all = [];
  for (const p of patterns) {
    for (const f of await glob(p, { cwd: repoRoot })) {
      // Skip pages/_app, pages/_document, pages/api/* in this enumeration.
      // pages/api/* are server endpoints, not entry points for client flows.
      if (/(^|\/)_app\.(t|j)sx?$/.test(f)) continue;
      if (/(^|\/)_document\.(t|j)sx?$/.test(f)) continue;
      if (/(^|\/)pages\/api\//.test(f)) continue;
      all.push(f);
    }
  }
  return all
    .map((f) => ({
      file: toPosix(relative(repoRoot, join(repoRoot, f))),
      kind: f.includes("/route.") ? "route-handler" : "page",
      route: routeFromEntry(f),
    }))
    .sort((a, b) => a.file.localeCompare(b.file));
}

function routeFromEntry(file) {
  // app/profile/page.tsx -> /profile
  // app/(group)/x/page.tsx -> /x
  // pages/profile.tsx -> /profile
  // pages/profile/index.tsx -> /profile
  let p = file
    .replace(/^src\//, "")
    .replace(/^app\//, "")
    .replace(/^pages\//, "")
    .replace(/\/page\.(t|j)sx?$/, "")
    .replace(/\/route\.(t|j)sx?$/, "")
    .replace(/\.(t|j)sx?$/, "")
    .replace(/\/index$/, "");
  // Remove route groups (parens).
  p = p.replace(/\([^/]*\)\/?/g, "");
  return "/" + p;
}

// Group call sites into flows. For Next.js App Router, the ancestor
// directory of a layout.tsx file is a flow boundary. For Pages Router,
// each page file is its own flow.
export function groupFlows(callsites, entries, repoRoot) {
  const flowsByEntry = new Map();
  for (const e of entries) {
    flowsByEntry.set(e.file, {
      id: flowIdFromEntry(e),
      entry: e.file,
      route: e.route,
      calls: [],
      unresolved: [],
    });
  }
  for (const c of callsites) {
    // Attach to nearest entry by file path prefix; for App Router, the
    // entry's directory subtree owns it.
    let attached = null;
    for (const [entryFile, flow] of flowsByEntry) {
      const dir = dirname(entryFile);
      if (c.file === entryFile || c.file.startsWith(dir + "/")) {
        attached = flow;
        break;
      }
    }
    // Fallback: callsites in shared lib/ get attached to all entries that
    // import them. Phase-1 trace gives us callsites reachable from each
    // entry; the trace output should have already filtered.
    if (!attached && c.entry) attached = flowsByEntry.get(c.entry);
    if (attached) {
      if (c.unresolved) attached.unresolved.push(c);
      else attached.calls.push(c);
    }
  }
  return [...flowsByEntry.values()].filter((f) => f.calls.length > 0 || f.unresolved.length > 0);
}

function flowIdFromEntry(e) {
  const parts = (e.route || "/").split("/").map((p) => p.replace(/[\[\]{}]/g, "")).filter(Boolean);
  if (parts.length === 0) return "home";
  return parts.join("-").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
}
