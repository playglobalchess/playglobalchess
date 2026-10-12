#!/usr/bin/env python3
"""
check_rest_contract: audit the frozen REST contract (SPEC-PE-API-CONTRACT) and
its machine-readable projection protocol/rest/openapi.yaml.

Runs in CI (platform path) and locally:  python3 platform/ci/check_rest_contract.py

Checks (R1 — the static, server-free half of the §8 conformance suite):
  1. openapi.yaml parses and is a minimal-valid OpenAPI 3.1 doc.
  2. BOUNDARY (§3.3): no WebSocket operation (SPEC-S2-PROTOCOL §4A.2 message
     table) is exposed as a REST path, and the §3.3 "live" operations are
     WS-only. A REST path that mutates live game truth is a contract bug.
  3. CATALOG ↔ OPENAPI: every /v1 path listed in SPEC-PE-API-CONTRACT §3.2 that
     carries a frozen Appendix A schema appears in openapi.yaml, and every
     openapi.yaml path appears in the spec catalog (no drift either way).
  4. ERROR REGISTRY: every Appendix B error `type` suffix appears in the
     openapi.yaml Problem.type enum, and vice-versa (closed v1 registry).

Zero external deps: a tiny YAML subset reader is NOT needed — we parse the two
machine-relevant parts of openapi.yaml (paths and the Problem enum) with simple
line scanning, which is robust for this generated, regular file and keeps the
check stdlib-only like check_docs.py. Exit non-zero on any failure.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OPENAPI = REPO / "protocol" / "rest" / "openapi.yaml"
PE_SPEC = REPO / "docs" / "specs" / "PE-platform-eng" / "API-CONTRACT.md"
S2_SPEC = REPO / "docs" / "specs" / "S2-realtime" / "PROTOCOL.md"

errors: list[str] = []


def fail(msg: str) -> None:
    errors.append(msg)


# --- read openapi.yaml paths + Problem enum (line-scan, no yaml dep) ---------
def read_openapi() -> tuple[set[str], set[str], bool]:
    if not OPENAPI.exists():
        fail(f"missing {OPENAPI.relative_to(REPO)}")
        return set(), set(), False
    text = OPENAPI.read_text()
    is_31 = bool(re.search(r"^openapi:\s*3\.1\.\d+", text, re.M))
    if not is_31:
        fail("openapi.yaml: not an OpenAPI 3.1.x document")
    # Paths: top-level keys under `paths:` are 2-space-indented and start with /.
    paths: set[str] = set()
    in_paths = False
    for line in text.splitlines():
        if re.match(r"^paths:\s*$", line):
            in_paths = True
            continue
        if in_paths:
            if re.match(r"^\S", line):  # dedented to col 0 → left paths:
                break
            m = re.match(r"^  (/[^:]+):\s*$", line)
            if m:
                paths.add("/v1" + m.group(1))
    # Problem.type enum values (full URIs under .../problems/<suffix>).
    enum_suffixes: set[str] = set()
    for m in re.finditer(r"problems/([a-z0-9-]+)", text):
        enum_suffixes.add(m.group(1))
    return paths, enum_suffixes, is_31


# --- read the spec's §3.2 catalog /v1 paths (those with frozen schemas) ------
# The Appendix A §A.2 block is the authoritative "frozen for the F-01 slice"
# path set (tournaments/clubs are placeholders without schemas, excluded).
def read_spec_catalog_paths() -> set[str]:
    text = PE_SPEC.read_text()
    # Scope to Appendix A.2 (per-endpoint schemas) — the frozen set.
    start = text.find("### A.2 Per-endpoint schemas")
    end = text.find("## 4B.")
    if start == -1 or end == -1:
        fail("PE spec: could not locate Appendix A.2 block for the frozen path set")
        return set()
    block = text[start:end]
    paths: set[str] = set()
    for line in block.splitlines():
        # A frozen endpoint is a schema line: an HTTP method, then a /v1 path.
        # Prose notes (e.g. the tournaments/clubs placeholder paragraph) are not
        # method-prefixed and their paths carry a `…` ellipsis — both excluded.
        m = re.match(r"\s*(?:GET|POST|PUT|DELETE|PATCH)\s+(/v1/[A-Za-z0-9/{}._-]+)", line)
        if m and "…" not in m.group(1):
            paths.add(m.group(1))
    return paths


# --- read the frozen WS operation names (SPEC-S2-PROTOCOL §4A.2) -------------
def read_ws_ops() -> set[str]:
    text = S2_SPEC.read_text()
    ops: set[str] = set()
    # §4A.2 rows look like: | 0x03 | C | MOVE | | 0x83 | S | MOVE_ACCEPTED |
    for m in re.finditer(r"\|\s*0x[0-9A-Fa-f]{2}\s*\|\s*[CS]\s*\|\s*([A-Z_]+)\s*\|", text):
        ops.add(m.group(1))
    return ops


# --- read Appendix B error suffixes -----------------------------------------
def read_error_registry() -> set[str]:
    text = PE_SPEC.read_text()
    start = text.find("## 4B.")
    end = text.find("## 5.", start if start != -1 else 0)
    if start == -1 or end == -1:
        fail("PE spec: could not locate Appendix B error registry")
        return set()
    block = text[start:end]
    # rows: | `unauthenticated` | 401 | ... — grab the backticked suffix.
    return set(re.findall(r"\|\s*`([a-z0-9-]+)`\s*\|\s*\d{3}\s*\|", block))


def main() -> int:
    oa_paths, oa_errs, _ = read_openapi()
    cat_paths = read_spec_catalog_paths()
    ws_ops = read_ws_ops()
    reg_errs = read_error_registry()

    # Check 2 — boundary: no WS op name appears as a REST path segment.
    # (Live mutations are WS-only; a REST path like /v1/move would be the bug.)
    ws_as_rest = {
        p for p in oa_paths
        for op in ws_ops
        if op.lower() in [seg.lower() for seg in p.strip("/").split("/")]
    }
    if ws_as_rest:
        fail(f"BOUNDARY (§3.3): WS operations exposed as REST paths: {sorted(ws_as_rest)}")
    # The specific live verbs must never be REST paths.
    forbidden = {"move", "seek", "resign", "abort", "takeback", "rematch", "offer_draw"}
    leaked = {p for p in oa_paths if any(f in p.lower() for f in forbidden)}
    if leaked:
        fail(f"BOUNDARY (§3.3): live-game verb in a REST path (must be WS): {sorted(leaked)}")

    # Check 3 — catalog <-> openapi path parity (frozen F-01-slice set).
    missing_in_oa = cat_paths - oa_paths
    extra_in_oa = oa_paths - cat_paths
    if missing_in_oa:
        fail(f"CATALOG: §3.2/App-A paths absent from openapi.yaml: {sorted(missing_in_oa)}")
    if extra_in_oa:
        fail(f"CATALOG: openapi.yaml paths absent from the spec catalog: {sorted(extra_in_oa)}")

    # Check 4 — error registry parity.
    missing_errs = reg_errs - oa_errs
    extra_errs = oa_errs - reg_errs
    if missing_errs:
        fail(f"ERRORS: Appendix B types absent from openapi.yaml Problem enum: {sorted(missing_errs)}")
    if extra_errs:
        fail(f"ERRORS: openapi.yaml Problem types absent from Appendix B: {sorted(extra_errs)}")

    n = f"openapi paths={len(oa_paths)}, catalog paths={len(cat_paths)}, ws ops={len(ws_ops)}, errors={len(reg_errs)}"
    if errors:
        print(f"check_rest_contract: FAIL ({n})", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        return 1
    print(f"check_rest_contract: OK — {n}; boundary clean, catalog + error registry in sync")
    return 0


if __name__ == "__main__":
    sys.exit(main())
