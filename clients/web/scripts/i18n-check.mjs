// NFR-11 guard: no hard-coded user-facing strings. Heuristic, intentionally
// conservative — it flags JSX text nodes and a few string-valued attributes
// (title, placeholder, aria-label, alt) that contain a letter and are NOT a
// t(...) call or a bare expression. The real contract is "display text comes
// from t()"; this catches the common regressions in CI without a full parser.
//
// Run: node scripts/i18n-check.mjs  (exits non-zero on a finding).

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const SRC = new URL("../src", import.meta.url).pathname;

/** Recursively collect .tsx files under a dir. */
function tsxFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...tsxFiles(p));
    else if (name.endsWith(".tsx")) out.push(p);
  }
  return out;
}

// A JSX text node with a letter that is not inside {...}. e.g. `>Hello<`.
const JSX_TEXT = />\s*([A-Za-z][^<{}]*?)\s*</g;
// A localizable attribute with a bare string literal.
const ATTR = /\b(title|placeholder|aria-label|alt)\s*=\s*"([^"]*[A-Za-z][^"]*)"/g;

let findings = 0;
for (const file of tsxFiles(SRC)) {
  const text = readFileSync(file, "utf8");
  for (const m of text.matchAll(JSX_TEXT)) {
    const s = m[1].trim();
    // Allow punctuation-only / entity fragments and the version word scaffold.
    if (s.length > 1 && /[A-Za-z]{2,}/.test(s)) {
      findings++;
      console.error(`${file}: hard-coded JSX text "${s}" — use t(id) (NFR-11)`);
    }
  }
  for (const m of text.matchAll(ATTR)) {
    findings++;
    console.error(`${file}: hard-coded ${m[1]}="${m[2]}" — use t(id) (NFR-11)`);
  }
}

if (findings > 0) {
  console.error(`\ni18n-check: ${findings} hard-coded string(s) found (NFR-11).`);
  process.exit(1);
}
console.log("i18n-check: no hard-coded user-facing strings found.");
