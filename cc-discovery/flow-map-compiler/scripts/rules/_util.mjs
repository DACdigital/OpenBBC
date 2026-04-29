// Shared helpers used by every rule pack.
// Underscore prefix keeps the loader from treating this as a pack.

// Mask out *comments only* so regex scanners don't surface example
// code inside JSDoc / // notes. String and template-literal contents
// are preserved verbatim — we need them for path extraction. The minor
// downside (an `axios.get('/x')` literal embedded inside a string would
// still match) is acceptable; that pattern is vanishingly rare in real
// code.
//
// Output preserves length and newlines, so character offsets remain
// valid for line-index lookup.
export function maskCommentsAndStrings(src) {
  const out = src.split("");
  const len = src.length;
  let i = 0;
  while (i < len) {
    const ch = src[i];
    const next = src[i + 1];
    // Line comment
    if (ch === "/" && next === "/") {
      out[i] = " "; out[i + 1] = " ";
      i += 2;
      while (i < len && src[i] !== "\n") { out[i] = " "; i++; }
      continue;
    }
    // Block comment
    if (ch === "/" && next === "*") {
      out[i] = " "; out[i + 1] = " ";
      i += 2;
      while (i < len && !(src[i] === "*" && src[i + 1] === "/")) {
        out[i] = src[i] === "\n" ? "\n" : " ";
        i++;
      }
      if (i < len) { out[i] = " "; out[i + 1] = " "; i += 2; }
      continue;
    }
    // String literal "..." or '...' — skip past, leave contents.
    if (ch === '"' || ch === "'") {
      const quote = ch;
      i++;
      while (i < len && src[i] !== quote) {
        if (src[i] === "\\") { i += 2; continue; }
        if (src[i] === "\n") break;
        i++;
      }
      if (i < len) i++;
      continue;
    }
    // Template literal — skip past, leave contents (substitutions and all).
    if (ch === "`") {
      i++;
      while (i < len && src[i] !== "`") {
        if (src[i] === "\\") { i += 2; continue; }
        if (src[i] === "$" && src[i + 1] === "{") {
          // Skip over the expression; nested templates/strings inside
          // re-enter the same logic by virtue of the brace tracker
          // calling no special path — we just match depth.
          let depth = 1;
          i += 2;
          while (i < len && depth > 0) {
            const c = src[i];
            if (c === "\\") { i += 2; continue; }
            if (c === '"' || c === "'") {
              const q = c; i++;
              while (i < len && src[i] !== q) {
                if (src[i] === "\\") { i += 2; continue; }
                if (src[i] === "\n") break;
                i++;
              }
              if (i < len) i++;
              continue;
            }
            if (c === "`") {
              i++;
              while (i < len && src[i] !== "`") {
                if (src[i] === "\\") { i += 2; continue; }
                if (src[i] === "$" && src[i + 1] === "{") {
                  // Recurse one more level — fragment composition often
                  // does this. Keep it simple: accept up to one more
                  // level then bail; deeper nesting is rare.
                  let d2 = 1;
                  i += 2;
                  while (i < len && d2 > 0) {
                    if (src[i] === "{") d2++;
                    else if (src[i] === "}") d2--;
                    i++;
                  }
                  continue;
                }
                i++;
              }
              if (i < len) i++;
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
      if (i < len) i++;
      continue;
    }
    i++;
  }
  return out.join("");
}

// Builds a precomputed line-offset table. lineOf(offsets, idx) is O(log n).
export function makeLineIndex(text) {
  const offsets = [0];
  for (let i = 0; i < text.length; i++) if (text[i] === "\n") offsets.push(i + 1);
  return offsets;
}
export function lineFromIndex(offsets, idx) {
  let lo = 0, hi = offsets.length - 1, ans = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (offsets[mid] <= idx) { ans = mid; lo = mid + 1; } else hi = mid - 1;
  }
  return ans + 1;
}

// Slice the args between an opening `(` and its matching `)`, starting
// at `from` (position immediately after the `(`). Tracks nesting through
// nested calls, brackets, braces, strings, and template literals.
// Returns null if no matching paren is found.
export function sliceCallArgs(text, from) {
  let depth = 1;
  let i = from;
  let inStr = null;
  let inTpl = false;
  let tplBrace = 0;
  for (; i < text.length; i++) {
    const ch = text[i];
    if (inStr) {
      if (ch === "\\") { i++; continue; }
      if (ch === inStr) inStr = null;
      continue;
    }
    if (inTpl) {
      if (ch === "\\") { i++; continue; }
      if (ch === "$" && text[i + 1] === "{") { tplBrace++; i++; continue; }
      if (tplBrace > 0) {
        if (ch === "{") tplBrace++;
        else if (ch === "}") tplBrace--;
        continue;
      }
      if (ch === "`") inTpl = false;
      continue;
    }
    if (ch === '"' || ch === "'") { inStr = ch; continue; }
    if (ch === "`") { inTpl = true; continue; }
    if (ch === "(" || ch === "[" || ch === "{") depth++;
    else if (ch === ")" || ch === "]" || ch === "}") {
      depth--;
      if (ch === ")" && depth === 0) return text.slice(from, i);
    }
  }
  return null;
}

// Split top-level args. Returns an array of trimmed parts.
export function splitArgs(args) {
  const parts = [];
  let depth = 0;
  let start = 0;
  let inStr = null;
  let inTpl = false;
  let tplBrace = 0;
  for (let i = 0; i < args.length; i++) {
    const ch = args[i];
    if (inStr) {
      if (ch === "\\") { i++; continue; }
      if (ch === inStr) inStr = null;
      continue;
    }
    if (inTpl) {
      if (ch === "\\") { i++; continue; }
      if (ch === "$" && args[i + 1] === "{") { tplBrace++; i++; continue; }
      if (tplBrace > 0) {
        if (ch === "{") tplBrace++;
        else if (ch === "}") tplBrace--;
        continue;
      }
      if (ch === "`") inTpl = false;
      continue;
    }
    if (ch === '"' || ch === "'") { inStr = ch; continue; }
    if (ch === "`") { inTpl = true; continue; }
    if (ch === "(" || ch === "[" || ch === "{") depth++;
    else if (ch === ")" || ch === "]" || ch === "}") depth--;
    else if (ch === "," && depth === 0) {
      parts.push(args.slice(start, i).trim());
      start = i + 1;
    }
  }
  parts.push(args.slice(start).trim());
  return parts;
}

// Returns the literal value if `expr` is a simple string/template
// literal we can treat as a static path. ${var} → {var}. Otherwise null.
export function parsePathLiteral(expr) {
  let s = (expr || "").trim();
  if (!s) return null;
  if (s.startsWith("`") && s.endsWith("`")) s = s.slice(1, -1);
  else if (s.startsWith('"') && s.endsWith('"')) s = s.slice(1, -1);
  else if (s.startsWith("'") && s.endsWith("'")) s = s.slice(1, -1);
  else return null;
  return s.replace(/\$\{\s*([^}]+?)\s*\}/g, (_, inner) => {
    // Pick a clean identifier for the placeholder; fall back to "param".
    const tail = inner.split(/[?.]/).pop().trim();
    const name = (tail.match(/^[A-Za-z_$][\w$]*/) || [])[0];
    return `{${name || "param"}}`;
  });
}

// Detect auth from a free-form options expression. We only claim
// auth=bearer when we see something that looks like a bearer header;
// otherwise "unknown" — never "none" — because absence of evidence is
// not evidence of absence.
export function inferAuth(optsText) {
  if (!optsText) return "unknown";
  if (/['"`]Authorization['"`]\s*[:,]/i.test(optsText)) return "bearer";
  if (/Authorization\s*:/i.test(optsText)) return "bearer";
  if (/['"`]Cookie['"`]\s*[:,]/i.test(optsText) || /Cookie\s*:/i.test(optsText)) return "cookie";
  if (/['"`]X-Api-Key['"`]\s*[:,]/i.test(optsText)) return "apikey";
  return "unknown";
}

export function depPresent(pkg, name) {
  const all = { ...(pkg.dependencies || {}), ...(pkg.devDependencies || {}) };
  return Boolean(all[name]);
}
