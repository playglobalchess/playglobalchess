#!/usr/bin/env python3
"""
docs-check: validate the planning doc set.
Runs in CI (and locally: `python3 platform/ci/check_docs.py`).

Checks:
  1. Every spec under docs/specs/**/ (excluding _shared templates and READMEs)
     has a 'Status' header with a valid lifecycle value.
  2. Master-ID references (F-xx / X-x / ADR-xx / G-x / NFR-x) that look malformed.
  3. Relative markdown links point at files that exist.

Exit non-zero on any failure. Zero external dependencies (stdlib only).
"""
from __future__ import annotations
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
DOCS = REPO / "docs"
VALID_STATUS = {"DRAFT", "REVIEW", "RATIFIED", "BUILDING", "DONE", "SUPERSEDED"}

errors: list[str] = []
warnings: list[str] = []


def is_spec(p: Path) -> bool:
    # A "spec" is a .md under docs/specs that is not a README or a _shared template.
    if p.suffix != ".md":
        return False
    rel = p.relative_to(DOCS)
    parts = rel.parts
    if "_shared" in parts:
        return False
    if p.name.upper() == "README.MD":
        return False
    return parts[0] == "specs"


def check_status_headers() -> None:
    for p in sorted(DOCS.rglob("*.md")):
        if not is_spec(p):
            continue
        text = p.read_text(encoding="utf-8", errors="replace")
        m = re.search(r"(?im)^\|?\s*Status\s*\|?\s*([A-Za-z]+)", text)
        if not m:
            errors.append(f"{p.relative_to(REPO)}: spec has no 'Status' header")
            continue
        status = m.group(1).upper()
        if status not in VALID_STATUS:
            errors.append(
                f"{p.relative_to(REPO)}: invalid Status '{m.group(1)}' "
                f"(expected one of {sorted(VALID_STATUS)})"
            )


def check_relative_links() -> None:
    link_re = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
    for p in sorted(DOCS.rglob("*.md")):
        text = p.read_text(encoding="utf-8", errors="replace")
        for mobj in link_re.finditer(text):
            target = mobj.group(1).split("#")[0].strip()
            if not target or target.startswith(("http://", "https://", "mailto:")):
                continue
            resolved = (p.parent / target).resolve()
            if not resolved.exists():
                errors.append(
                    f"{p.relative_to(REPO)}: broken relative link -> {target}"
                )


def check_id_shapes() -> None:
    # Flag obviously malformed IDs, e.g. 'F -01', 'ADR 1', 'G--A'. Advisory (warnings).
    bad = re.compile(r"\b(F|X|NFR|ADR)\s-\s?\d|\bADR\s+\d\b")
    for p in sorted(DOCS.rglob("*.md")):
        for i, line in enumerate(p.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
            if bad.search(line):
                warnings.append(f"{p.relative_to(REPO)}:{i}: possibly malformed ID reference")


def main() -> int:
    if not DOCS.exists():
        print("docs/ not found — nothing to check", file=sys.stderr)
        return 0
    check_status_headers()
    check_relative_links()
    check_id_shapes()

    for w in warnings:
        print(f"::warning::{w}")
    for e in errors:
        print(f"::error::{e}")

    spec_count = sum(1 for p in DOCS.rglob("*.md") if is_spec(p))
    print(f"docs-check: scanned docs/, {spec_count} spec file(s), "
          f"{len(errors)} error(s), {len(warnings)} warning(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
