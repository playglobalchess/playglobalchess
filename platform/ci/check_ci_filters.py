#!/usr/bin/env python3
"""
check_ci_filters: every path-filtered CI gate must run when ANY file it reads
changes (#86, #87).

Runs in CI (the `platform` job) and locally:
`python3 platform/ci/check_ci_filters.py`.

Why: `.github/workflows/ci.yml` gates jobs with dorny/paths-filter. A filter
that lists only the folder a job *owns* lets a change to a file the job *reads*
merge green with the job skipped, so its check is hollow:
- #86: the REST contract audit reads two spec files under docs/ that no filter
  covered, so a docs-only contract change merged green and broke the nightly.
- #87: the game-server links protocol/codec-cgo and engine/engine-cgo, and the
  web client builds protocol/codec-wasm, but `services` and `web` filtered only
  their own folders.

How: the inputs of each gate are DERIVED from the files that define them, not
hand-listed, so the check cannot drift from the build:

  services       services/**, every `*_DIR := ...` in services/*/Makefile, and
                 the Cargo path dependencies of those crates (transitively)
  web            clients/web/**, every local `../../...` path in
                 clients/web/package.json (resolved to its crate), and that
                 crate's Cargo path dependencies (transitively)
  web-e2e        the union of web and services (it builds both)
  rest-contract  every `REPO / "..."` file check_rest_contract.py reads, plus
                 the script itself
  engine         engine/**
  protocol       protocol/**
  platform       platform/** and .github/workflows/**
  every gate     .github/workflows/ci.yml (a step change must run that job)

For each gate, the union of the filter outputs referenced in the job's `if:`
must cover every input. A filter covers a directory when it matches a probe
file inside it.

Fails closed: a missing workflow, filter block, job, Makefile or package.json,
an unparseable filter line, or a job with no recognisable gate is an error,
never a silent pass. Stdlib only. Unit tests: platform/ci/test_check_ci_filters.py.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
CI_YML = REPO / ".github" / "workflows" / "ci.yml"
CI_YML_REL = ".github/workflows/ci.yml"


# --- workflow parsing ---------------------------------------------------------

_FILTER_LINE = re.compile(r"^\s+([A-Za-z0-9_-]+):\s*\[(.*)\]\s*$")
_QUOTED = re.compile(r"'([^']*)'|\"([^\"]*)\"")


def parse_filters(text: str) -> tuple[dict[str, list[str]], list[str]]:
    """Return ({output: [patterns]}, problems) from the paths-filter block."""
    lines = text.splitlines()
    start = next((i for i, l in enumerate(lines) if re.match(r"^\s+filters:\s*\|\s*$", l)), None)
    if start is None:
        return {}, ["no `filters: |` block found"]
    indent = len(lines[start]) - len(lines[start].lstrip())
    filters: dict[str, list[str]] = {}
    problems: list[str] = []
    for line in lines[start + 1:]:
        if not line.strip():
            continue
        if len(line) - len(line.lstrip()) <= indent:
            break  # left the block
        m = _FILTER_LINE.match(line)
        if not m:
            problems.append(f"unparseable filter line: {line.strip()!r}")
            continue
        items = [a or b for a, b in _QUOTED.findall(m.group(2))]
        if not items:
            problems.append(f"filter {m.group(1)!r} has no patterns")
        filters[m.group(1)] = items
    if not filters:
        problems.append("filters block is empty")
    return filters, problems


def parse_job_gates(text: str) -> dict[str, set[str]]:
    """Return {job: {filter outputs its `if:` references}} for top-level jobs."""
    gates: dict[str, set[str]] = {}
    in_jobs = False
    job = None
    for line in text.splitlines():
        if re.match(r"^jobs:\s*$", line):
            in_jobs = True
            continue
        if not in_jobs:
            continue
        if re.match(r"^\S", line):
            break  # a new top-level key ends `jobs:`
        m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
        if m:
            job = m.group(1)
            gates.setdefault(job, set())
            continue
        if job and re.match(r"^    if:", line):
            gates[job].update(re.findall(r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)", line))
    return gates


# --- glob matching (dorny/paths-filter uses picomatch) -----------------------

def glob_to_regex(pattern: str) -> re.Pattern[str]:
    out = []
    i = 0
    while i < len(pattern):
        c = pattern[i]
        if pattern.startswith("**", i):
            out.append(".*")
            i += 2
            if i < len(pattern) and pattern[i] == "/":
                i += 1  # `**/` also matches zero directories
            continue
        if c == "*":
            out.append("[^/]*")
        elif c == "?":
            out.append("[^/]")
        elif c == "[":
            j = pattern.find("]", i)
            if j == -1:
                out.append(re.escape(c))
            else:
                out.append(pattern[i:j + 1])
                i = j
        else:
            out.append(re.escape(c))
        i += 1
    return re.compile("^" + "".join(out) + "$")


def covers(patterns: list[str], required: str) -> bool:
    """True if any pattern matches `required` (a file) or a probe inside it (a dir)."""
    probes = [required] if Path(required).suffix else [required.rstrip("/") + "/__probe__/__probe__.x"]
    return any(glob_to_regex(p).match(probe) for p in patterns for probe in probes)


# --- deriving each gate's inputs from the build files ------------------------

def _rel(path: Path, repo: Path) -> str:
    return path.resolve().relative_to(repo.resolve()).as_posix()


def cargo_path_deps(crate_dir: Path, repo: Path, seen: set[str] | None = None) -> set[str]:
    """Directories of the crate and of its Cargo `path = "..."` dependencies, transitively."""
    seen = set() if seen is None else seen
    rel = _rel(crate_dir, repo)
    if rel in seen:
        return seen
    seen.add(rel)
    manifest = crate_dir / "Cargo.toml"
    if manifest.exists():
        for target in re.findall(r"\{[^}]*\bpath\s*=\s*\"([^\"]+)\"[^}]*\}", manifest.read_text(encoding="utf-8")):
            dep = (crate_dir / target).resolve()
            if (dep / "Cargo.toml").exists():
                cargo_path_deps(dep, repo, seen)
    return seen


def nearest_crate(path: Path, repo: Path) -> Path:
    """Walk up from path to the first directory holding a Cargo.toml (e.g. pkg/ -> crate)."""
    p = path.resolve()
    while p != repo.resolve() and p != p.parent:
        if (p / "Cargo.toml").exists():
            return p
        p = p.parent
    return path.resolve()


_GO_REL_PATH = re.compile(r"\"((?:\.\./)+[A-Za-z0-9_./-]+)\"")


def services_inputs(repo: Path, problems: list[str]) -> set[str]:
    """services/**, Makefile native deps (with their Cargo deps), and any file a
    Go source or test reads outside services/ by relative path (e.g. the golden
    vectors)."""
    inputs = {"services"}
    makefiles = sorted((repo / "services").glob("*/Makefile"))
    if not makefiles:
        problems.append("no services/*/Makefile found: services inputs cannot be derived")
    for mk in makefiles:
        for target in re.findall(r"^[A-Z0-9_]+_DIR\s*:=\s*(\S+)", mk.read_text(encoding="utf-8"), re.M):
            dep = (mk.parent / target).resolve()
            if not dep.exists():
                problems.append(f"{_rel(mk, repo)}: {target} does not exist")
                continue
            inputs |= cargo_path_deps(dep, repo)
    services_root = (repo / "services").resolve()
    for src in sorted((repo / "services").rglob("*.go")):
        for target in _GO_REL_PATH.findall(src.read_text(encoding="utf-8", errors="replace")):
            dep = (src.parent / target).resolve()
            if dep == services_root or services_root in dep.parents:
                continue  # inside services/: already covered
            if not dep.exists():
                problems.append(f"{_rel(src, repo)}: {target} does not exist")
                continue
            inputs.add(_rel(dep, repo))
    return inputs


def web_inputs(repo: Path, problems: list[str]) -> set[str]:
    """clients/web/** plus every local `file:` dependency in package.json (and
    that crate's Cargo deps). Script paths are not inputs of the `web` job: it
    does not run the e2e script (web-e2e does, and covers services itself)."""
    inputs = {"clients/web"}
    pkg = repo / "clients" / "web" / "package.json"
    if not pkg.exists():
        problems.append("clients/web/package.json not found: web inputs cannot be derived")
        return inputs
    try:
        data = json.loads(pkg.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        problems.append(f"clients/web/package.json is not valid JSON: {e}")
        return inputs
    for section in ("dependencies", "devDependencies", "optionalDependencies"):
        for name, spec in (data.get(section) or {}).items():
            if not isinstance(spec, str) or not spec.startswith("file:"):
                continue
            dep = (pkg.parent / spec[len("file:"):]).resolve()
            if not dep.exists() and not dep.parent.exists():
                problems.append(f"clients/web/package.json: {name} -> {spec} does not exist")
                continue
            inputs |= cargo_path_deps(nearest_crate(dep, repo), repo)
    return inputs


_REPO_ASSIGN = re.compile(r"^[A-Z0-9_]+\s*=\s*REPO\b.*$", re.M)
_REPO_CHAIN = re.compile(r"^[A-Z0-9_]+\s*=\s*REPO((?:\s*/\s*\"[^\"]+\")+)\s*$")


def rest_contract_inputs(repo: Path, problems: list[str]) -> set[str]:
    """Every `NAME = REPO / "..."` file check_rest_contract.py reads. Fails closed:
    a REPO-based assignment the pattern cannot read is a problem, not a skip."""
    script = repo / "platform" / "ci" / "check_rest_contract.py"
    if not script.exists():
        problems.append("platform/ci/check_rest_contract.py not found")
        return set()
    files = {"platform/ci/check_rest_contract.py"}
    for line in _REPO_ASSIGN.findall(script.read_text(encoding="utf-8")):
        m = _REPO_CHAIN.match(line)
        if not m:
            problems.append(f"check_rest_contract.py: cannot read input path from {line.strip()!r}")
            continue
        files.add("/".join(re.findall(r"\"([^\"]+)\"", m.group(1))))
    if len(files) == 1:
        problems.append("check_rest_contract.py: no `NAME = REPO / ...` inputs found")
    return files


def required_inputs(repo: Path, problems: list[str]) -> dict[str, set[str]]:
    web = web_inputs(repo, problems)
    services = services_inputs(repo, problems)
    req = {
        "engine": {"engine"},
        "protocol": {"protocol"},
        "services": services,
        "web": web,
        "web-e2e": web | services,
        "rest-contract": rest_contract_inputs(repo, problems),
        "platform": {"platform", ".github/workflows"},
    }
    for job in req:
        req[job] = req[job] | {CI_YML_REL}
    return req


# --- the check ---------------------------------------------------------------

def check(repo: Path, ci_text: str) -> list[str]:
    errors: list[str] = []
    filters, problems = parse_filters(ci_text)
    errors += [f"{CI_YML_REL}: {p}" for p in problems]
    gates = parse_job_gates(ci_text)
    derive_problems: list[str] = []
    required = required_inputs(repo, derive_problems)
    errors += derive_problems
    for job, inputs in sorted(required.items()):
        if job not in gates:
            errors.append(f"{CI_YML_REL}: job {job!r} not found (a gate this check requires)")
            continue
        outputs = gates[job]
        if not outputs:
            errors.append(f"{CI_YML_REL}: job {job!r} has no `if:` on needs.changes.outputs.* to check")
            continue
        unknown = sorted(o for o in outputs if o not in filters)
        if unknown:
            errors.append(f"{CI_YML_REL}: job {job!r} references undefined filter output(s) {unknown}")
        patterns = [p for o in outputs if o in filters for p in filters[o]]
        for path in sorted(inputs):
            if not covers(patterns, path):
                errors.append(
                    f"{CI_YML_REL}: job {job!r} reads {path} but its filter(s) {sorted(outputs)} "
                    f"do not match it, so a change there would skip the job (hollow gate)"
                )
    return errors


def main() -> int:
    if not CI_YML.exists():
        print(f"::error::{CI_YML_REL} not found", file=sys.stderr)
        return 1
    errors = check(REPO, CI_YML.read_text(encoding="utf-8"))
    for e in errors:
        print(f"::error::{e}")
    print(f"check_ci_filters: {len(errors)} error(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
