// Generic React adapter (Vite + react-router-dom). Probes for `react`
// without `next`. Enumerates entries from src/App.tsx route declarations
// (statically parsed) plus src/pages/*.

import { readFileSync, existsSync } from "node:fs";
import { join, relative, dirname, resolve, sep } from "node:path";
import { glob } from "./glob.mjs";

function toPosix(p) {
  return sep === "/" ? p : p.split(sep).join("/");
}

export const meta = {
  id: "react",
  language_default: "ts",
};

export function probe(repoRoot) {
  const pkgPath = join(repoRoot, "package.json");
  if (!existsSync(pkgPath)) return 0;
  let pkg;
  try { pkg = JSON.parse(readFileSync(pkgPath, "utf8")); } catch { return 0; }
  const deps = { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) };
  if (deps.next) return 0; // Next.js wins this match
  if (!deps.react) return 0;
  return 0.9;
}

export function detectRouter(repoRoot) {
  const pkgPath = join(repoRoot, "package.json");
  let pkg;
  try { pkg = JSON.parse(readFileSync(pkgPath, "utf8")); } catch { return "none"; }
  const deps = { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) };
  if (deps["react-router-dom"] || deps["react-router"]) return "react-router-dom";
  if (deps["@tanstack/react-router"]) return "tanstack-router";
  return "none";
}

export async function enumerateEntries(repoRoot) {
  // Strategy: scan src/pages/* as page files. Also parse src/App.tsx for
  // <Route path="..." element={<Component />} /> declarations, mapping the
  // component back to a file in src/.
  const pages = await glob("src/pages/**/*.{ts,tsx,js,jsx}", { cwd: repoRoot });
  const entries = pages.map((f) => ({
    file: f,
    kind: "page",
    route: routeFromPagePath(f),
  }));

  const appPath = await findAppRoot(repoRoot);
  if (appPath) {
    const text = readFileSync(appPath, "utf8");
    for (const r of parseRoutesFromApp(text)) {
      // Try to resolve the component import to a file path.
      const mapped = resolveImportInApp(text, r.component, repoRoot);
      if (mapped && !entries.some((e) => e.file === mapped)) {
        entries.push({ file: mapped, kind: "page", route: r.path });
      }
    }
  }

  return entries
    .map((e) => ({ ...e, file: toPosix(relative(repoRoot, join(repoRoot, e.file))) }))
    .sort((a, b) => a.file.localeCompare(b.file));
}

async function findAppRoot(repoRoot) {
  for (const cand of ["src/App.tsx", "src/App.jsx", "src/App.ts", "src/App.js"]) {
    if (existsSync(join(repoRoot, cand))) return join(repoRoot, cand);
  }
  return null;
}

function routeFromPagePath(file) {
  // Drop src/pages/ prefix, drop extension, drop trailing /index.
  let r = file
    .replace(/^src\/pages\//, "")
    .replace(/\.(t|j)sx?$/, "")
    .replace(/\/index$/, "");
  if (r === "index") r = "";
  return "/" + r;
}

function parseRoutesFromApp(text) {
  const routes = [];
  for (const m of text.matchAll(/<Route\s+path=(?:"([^"]+)"|\{([^}]+)\})\s+element=\{<\s*([A-Za-z0-9_]+)\s*\/?\s*>\s*\}/g)) {
    const path = m[1] ?? m[2];
    routes.push({ path, component: m[3] });
  }
  return routes;
}

function resolveImportInApp(appText, component, repoRoot) {
  // Match either `import Comp from "./x"` (default) or
  // `import { Comp } from "./x"` (named).
  const defaultRe = new RegExp(`\\bimport\\s+${component}\\s+from\\s+["']([^"']+)["']`);
  const namedRe = new RegExp(`\\bimport\\s*\\{[^}]*\\b${component}\\b[^}]*\\}\\s+from\\s+["']([^"']+)["']`);
  const m = appText.match(defaultRe) || appText.match(namedRe);
  if (!m) return null;
  const spec = m[1];
  if (!spec.startsWith(".")) return null; // bare package — out of source

  // Resolve relative to src/App.tsx (we tried each App candidate; assume
  // src/App.tsx by convention — same dir for resolution purposes).
  const fromDir = "src";
  const exts = [".tsx", ".ts", ".jsx", ".js"];
  const tries = [];
  tries.push(spec);
  for (const ext of exts) tries.push(spec + ext);
  for (const ext of exts) tries.push(spec + "/index" + ext);
  for (const cand of tries) {
    const abs = resolve(join(repoRoot, fromDir), cand);
    if (existsSync(abs)) return toPosix(relative(repoRoot, abs));
  }
  return null;
}

// Same grouping rule as Next.js: entry-file ownership of subtree calls.
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
    let attached = c.entry ? flowsByEntry.get(c.entry) : null;
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
