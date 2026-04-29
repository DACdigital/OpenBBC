// Loads all rule packs dynamically. Adding a new client library means
// dropping a new .mjs file in this directory; loader picks it up. The
// underscore prefix keeps this loader and any non-pack siblings out of
// the auto-discovery scan.

import { readdirSync } from "node:fs";
import { fileURLToPath, pathToFileURL } from "node:url";
import { dirname, join, basename } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));

export async function loadRulePacks() {
  const files = readdirSync(here)
    .filter((n) => n.endsWith(".mjs"))
    .filter((n) => !n.startsWith("_"));
  const packs = [];
  for (const f of files) {
    const mod = await import(pathToFileURL(join(here, f)).href);
    if (!mod.meta || typeof mod.detect !== "function" || typeof mod.extract !== "function") {
      console.error(`rule pack ${f} missing meta/detect/extract export; skipping`);
      continue;
    }
    packs.push({ ...mod, file: basename(f) });
  }
  return packs;
}
