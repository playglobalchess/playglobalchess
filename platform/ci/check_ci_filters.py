#!/usr/bin/env python3
"""
check_ci_filters: every path-filtered CI gate must run when ANY file it reads
changes (#86, #87).

Runs in CI as its own UNFILTERED job (`ci-filter-audit`, no `if:`), so it runs on
every PR and push: its own inputs (Makefiles, Go sources, Cargo.toml files,
package.json, the workflow) live everywhere, and a filtered audit would itself be
a hollow gate. Locally: `python3 platform/ci/check_ci_filters.py`.

Why: `.github/workflows/ci.yml` gates jobs with dorny/paths-filter. A filter
that lists only the folder a job *owns* lets a change to a file the job *reads*
merge green with the job skipped, so its check is hollow (#86: the REST audit
read docs/ specs no filter covered; #87: the game-server and web builds read
protocol/ and engine/ crates their filters did not cover).

How: for EVERY job gated on `needs.changes.outputs.*`, its inputs are DERIVED,
never hand-listed:
  1. repo paths named in the job's own steps (run:, working-directory:), with
     shell scripts they call scanned the same way, recursively;
  2. for each Rust crate reached: its Cargo `path` dependencies, transitively
     (parsed with tomllib: dependencies, dev-, build-, target.*, workspace);
  3. `make -C <dir>`: every Makefile variable whose expanded value is a `../`
     path (and that crate's Cargo deps);
  4. Go sources under a reached Go module: relative string paths, filepath.Join
     of literals, and `#cgo` -I/-L paths that point outside the module;
  5. `npm` in clients/web: package.json local dependencies (file:, link:,
     portal:, ./, ../) resolved to their crate;
  6. `./gradlew`: the root Gradle files and every projectDir in settings.gradle.kts;
  7. `python3 platform/ci/<x>.py`: every `NAME = REPO / "..."` the script reads;
  8. the workflow file itself, so editing a job's steps runs that job.
web-e2e additionally gets the union of the web and services inputs (it builds
both). For each gated job, the union of the filter outputs in its `if:` must
match every input.

Fails closed: a missing file, unparseable filter line or TOML, unsupported glob
syntax (`!`, braces), a gate using `&&` or `!=`, an unexpanded Makefile variable,
a non-literal path in filepath.Join, a local dependency outside the repo, or a
REPO-based assignment it cannot read is an ERROR, never a silent pass.

Known limits (stated, not hidden): paths constructed at runtime in other ways
(environment variables, string concatenation in Go, computed paths in shell)
are not derived; jobs not gated on the changes outputs (schedule-only, or no
`if:`) are not filtered and so not audited.

Stdlib only (Python >= 3.11 for tomllib). Tests: platform/ci/test_check_ci_filters.py.
"""
from __future__ import annotations

import json
import re
import sys
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
CI_YML_REL = ".github/workflows/ci.yml"
AUDIT_SCRIPT_REL = "platform/ci/check_ci_filters.py"
# Gated jobs that must exist; a rename or removal is an error, not a silent pass.
EXPECTED_GATED_JOBS = {"engine", "protocol", "rest-contract", "protocol-parity",
                       "services", "web", "web-e2e", "mobile", "platform"}
SKIP_DIRS = {"target", "node_modules", "pkg", "build", "dist", ".gradle"}


class Problems(list):
    """A list of problem strings (fail-closed findings during derivation)."""


def rel(path: Path, repo: Path, problems: Problems, where: str) -> str | None:
    try:
        return path.resolve().relative_to(repo.resolve()).as_posix()
    except ValueError:
        problems.append(f"{where}: {path} is outside the repository")
        return None


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
            break
        m = _FILTER_LINE.match(line)
        if not m:
            problems.append(f"unparseable filter line: {line.strip()!r}")
            continue
        items = [a or b for a, b in _QUOTED.findall(m.group(2))]
        if not items:
            problems.append(f"filter {m.group(1)!r} has no patterns")
        for p in items:
            if p.startswith("!") or "{" in p:
                problems.append(f"filter {m.group(1)!r}: pattern {p!r} uses syntax this audit "
                                f"does not model (negation or braces); extend check_ci_filters.py")
        filters[m.group(1)] = items
    if not filters:
        problems.append("filters block is empty")
    return filters, problems


def split_jobs(text: str) -> dict[str, list[str]]:
    """Return {job: [lines of its block]} for top-level jobs."""
    jobs: dict[str, list[str]] = {}
    in_jobs = False
    job = None
    for line in text.splitlines():
        if re.match(r"^jobs:\s*$", line):
            in_jobs = True
            continue
        if not in_jobs:
            continue
        if re.match(r"^\S", line):
            break
        m = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
        if m:
            job = m.group(1)
            jobs[job] = []
            continue
        if job:
            jobs[job].append(line)
    return jobs


def job_if(lines: list[str]) -> str | None:
    """The job-level `if:` expression, including a multi-line block value."""
    for i, line in enumerate(lines):
        m = re.match(r"^    if:\s*(.*)$", line)
        if not m:
            continue
        value = m.group(1).strip()
        if value in (">", ">-", "|", "|-", ""):
            parts = []
            for nxt in lines[i + 1:]:
                if nxt.strip() and len(nxt) - len(nxt.lstrip()) <= 4:
                    break
                parts.append(nxt.strip())
            value = " ".join(parts)
        return value
    return None


def parse_job_gates(text: str) -> tuple[dict[str, set[str]], list[str]]:
    """Return ({job: {filter outputs its `if:` is gated on}}, problems)."""
    gates: dict[str, set[str]] = {}
    problems: list[str] = []
    for job, lines in split_jobs(text).items():
        expr = job_if(lines) or ""
        outputs = set(re.findall(r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)", expr))
        if outputs and ("&&" in expr or "!=" in expr):
            problems.append(f"job {job!r}: gate {expr!r} uses && or !=; the audit only models "
                            f"OR-of-outputs gates (== 'true' joined by ||)")
        gates[job] = outputs
    return gates, problems


def has_unconditional_audit(text: str) -> bool:
    """True if some job with no `if:` runs the audit script."""
    for lines in split_jobs(text).values():
        # Comments do not count: a comment naming the script runs nothing.
        if job_if(lines) is None and re.search(r"\bpython3?\s+" + re.escape(AUDIT_SCRIPT_REL), step_text(lines)):
            return True
    return False


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
                i += 1
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
                body = pattern[i + 1:j]
                if body.startswith("!"):
                    body = "^" + body[1:]  # glob negation -> regex negation
                out.append("[" + body + "]")
                i = j
        else:
            out.append(re.escape(c))
        i += 1
    return re.compile("^" + "".join(out) + "$")


def covers(patterns: list[str], required: str, is_dir: bool) -> bool:
    probe = required.rstrip("/") + "/__probe__/__probe__.x" if is_dir else required
    return any(glob_to_regex(p).match(probe) for p in patterns)


# --- derivation helpers -------------------------------------------------------

def cargo_path_deps(crate_dir: Path, repo: Path, problems: Problems, seen: set[str]) -> None:
    r = rel(crate_dir, repo, problems, "Cargo dependency")
    if r is None or r in seen:
        return
    seen.add(r)
    manifest = crate_dir / "Cargo.toml"
    if not manifest.exists():
        return
    try:
        data = tomllib.loads(manifest.read_text(encoding="utf-8"))
    except tomllib.TOMLDecodeError as e:
        problems.append(f"{r}/Cargo.toml: cannot parse: {e}")
        return
    tables = [data.get(k, {}) for k in ("dependencies", "dev-dependencies", "build-dependencies")]
    for tgt in (data.get("target") or {}).values():
        tables += [tgt.get(k, {}) for k in ("dependencies", "dev-dependencies", "build-dependencies")]
    ws = data.get("workspace") or {}
    tables.append(ws.get("dependencies", {}))
    targets = [spec["path"] for t in tables for spec in t.values() if isinstance(spec, dict) and "path" in spec]
    targets += list(ws.get("members", []))
    for target in targets:
        dep = (crate_dir / target).resolve()
        if not (dep / "Cargo.toml").exists():
            problems.append(f"{r}/Cargo.toml: path dependency {target!r} has no Cargo.toml")
            continue
        cargo_path_deps(dep, repo, problems, seen)


def nearest_crate(path: Path, repo: Path) -> Path | None:
    p = path.resolve()
    root = repo.resolve()
    while p != root and root in p.parents:
        if (p / "Cargo.toml").exists():
            return p
        p = p.parent
    return None


_MAKE_ASSIGN = re.compile(r"^([A-Za-z0-9_]+)\s*(?::=|\?=|=)\s*(.*?)\s*$", re.M)
_MAKE_REF = re.compile(r"\$[({]([A-Za-z0-9_]+)[)}]")


def makefile_deps(make_dir: Path, repo: Path, problems: Problems, out: set[str]) -> None:
    mk = make_dir / "Makefile"
    where = rel(mk, repo, problems, "Makefile") or str(mk)
    if not mk.exists():
        problems.append(f"{where}: not found (job runs make -C on it)")
        return
    text = mk.read_text(encoding="utf-8")
    raw = dict(_MAKE_ASSIGN.findall(text))

    def expand(value: str, depth: int = 0) -> str | None:
        if depth > 10:
            return None
        def sub(m: re.Match[str]) -> str:
            v = raw.get(m.group(1))
            return "\x00" if v is None else (expand(v, depth + 1) or "\x00")
        res = _MAKE_REF.sub(sub, value)
        return None if "\x00" in res else res

    found = 0
    for name, value in raw.items():
        if "../" not in value and "$" not in value:
            continue
        val = expand(value)
        if val is None:
            if "../" in value:
                problems.append(f"{where}: {name} = {value!r} uses a variable this audit cannot expand")
            continue
        if not val.startswith("../"):
            continue
        dep = (make_dir / val).resolve()
        crate = nearest_crate(dep, repo)
        if not dep.exists() and crate is None:
            # A build output under a crate (e.g. .../target/release) resolves
            # to that crate; anything else missing is a real problem.
            problems.append(f"{where}: {name} = {val!r} does not exist")
            continue
        found += 1
        crate = crate or dep
        seen: set[str] = set()
        cargo_path_deps(crate, repo, problems, seen)
        out |= seen
    if found == 0 and re.search(r"\bcargo\b", text):
        problems.append(f"{where}: runs cargo but declares no ../ dependency paths the audit can read")


_GO_STR = re.compile(r"\"((?:[^\"\\]|\\.)*)\"|`([^`]*)`")
_GO_JOIN = re.compile(r"\b(?:filepath|path)\.Join\(([^)]*)\)")
# `#cgo` directives appear in a /* */ preamble or as `// #cgo` lines.
_GO_CGO = re.compile(r"^\s*(?://\s*)?#cgo\b.*$", re.M)


def go_deps(module_dir: Path, repo: Path, problems: Problems, out: set[str]) -> None:
    root = module_dir.resolve()
    for src in sorted(module_dir.rglob("*.go")):
        text = src.read_text(encoding="utf-8", errors="replace")
        where = rel(src, repo, problems, "Go source") or str(src)
        candidates: list[str] = []
        for m in _GO_JOIN.finditer(text):
            args = [a.strip() for a in m.group(1).split(",") if a.strip()]
            lits = [_GO_STR.fullmatch(a) for a in args]
            if any(a == '".."' or a.startswith('"../') for a in args):
                if not all(lits):
                    problems.append(f"{where}: {m.group(0)!r} builds a ../ path from a non-literal; "
                                    f"use string literals so the audit can resolve it")
                    continue
                candidates.append("/".join((l.group(1) or l.group(2)) for l in lits))
        for m in _GO_STR.finditer(text):
            s = m.group(1) if m.group(1) is not None else m.group(2)
            if s.startswith("../"):
                candidates.append(s)
        for line in _GO_CGO.findall(text):
            for p in re.findall(r"-[IL]\s*\$\{SRCDIR\}/(\S+)", line):
                candidates.append(p)
        for c in candidates:
            dep = (src.parent / c).resolve()
            if dep == root or root in dep.parents:
                continue
            if not dep.exists():
                problems.append(f"{where}: {c!r} does not exist")
                continue
            r = rel(dep, repo, problems, where)
            if r:
                out.add(r)


def npm_deps(pkg_dir: Path, repo: Path, problems: Problems, out: set[str]) -> None:
    pkg = pkg_dir / "package.json"
    where = rel(pkg, repo, problems, "package.json") or str(pkg)
    if not pkg.exists():
        problems.append(f"{where}: not found")
        return
    try:
        data = json.loads(pkg.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        problems.append(f"{where}: not valid JSON: {e}")
        return
    for section in ("dependencies", "devDependencies", "optionalDependencies", "peerDependencies"):
        for name, spec in (data.get(section) or {}).items():
            if not isinstance(spec, str):
                continue
            m = re.match(r"^(?:file:|link:|portal:)?(\.{1,2}/.*)$", spec)
            if not m:
                continue
            dep = (pkg_dir / m.group(1)).resolve()
            if not dep.exists() and not dep.parent.exists():
                problems.append(f"{where}: {name} -> {spec} does not exist")
                continue
            if rel(dep, repo, problems, f"{where}: {name}") is None:
                continue
            crate = nearest_crate(dep, repo)
            if crate is None:
                r = rel(dep, repo, problems, where)
                if r:
                    out.add(r)
                continue
            seen: set[str] = set()
            cargo_path_deps(crate, repo, problems, seen)
            out |= seen


def gradle_deps(repo: Path, problems: Problems, out: set[str]) -> None:
    for f in ("settings.gradle.kts", "build.gradle.kts", "gradle.properties", "gradlew"):
        if (repo / f).exists():
            out.add(f)
    if (repo / "gradle").is_dir():
        out.add("gradle")
    settings = repo / "settings.gradle.kts"
    if not settings.exists():
        problems.append("settings.gradle.kts: not found (job runs ./gradlew)")
        return
    for d in re.findall(r"projectDir\s*=\s*file\(\"([^\"]+)\"\)", settings.read_text(encoding="utf-8")):
        if not (repo / d).is_dir():
            problems.append(f"settings.gradle.kts: projectDir {d!r} does not exist")
            continue
        out.add(d)


_REPO_ASSIGN = re.compile(r"^[A-Z0-9_]+\s*=\s*REPO\b.*$", re.M)
_REPO_CHAIN = re.compile(r"^[A-Z0-9_]+\s*=\s*REPO((?:\s*/\s*\"[^\"]+\")+)\s*$")


def python_script_deps(script: Path, repo: Path, problems: Problems, out: set[str]) -> None:
    where = rel(script, repo, problems, "script") or str(script)
    for line in _REPO_ASSIGN.findall(script.read_text(encoding="utf-8")):
        m = _REPO_CHAIN.match(line)
        if not m:
            problems.append(f"{where}: cannot read input path from {line.strip()!r}")
            continue
        out.add("/".join(re.findall(r"\"([^\"]+)\"", m.group(1))))


# --- deriving a job's inputs from its steps ----------------------------------

def top_level_dirs(repo: Path) -> list[str]:
    return sorted(p.name for p in repo.iterdir()
                  if p.is_dir() and not p.name.startswith(".") and p.name not in SKIP_DIRS)


def path_tokens(text: str, tops: list[str]) -> list[str]:
    alt = "|".join(re.escape(t) for t in tops)
    return [m.group(1).rstrip(".,;:)'\"") for m in
            re.finditer(r"(?<![\w./-])((?:" + alt + r")/[A-Za-z0-9_./-]*)", text)]


def step_text(lines: list[str]) -> str:
    """The job's lines minus YAML/shell comment lines and `uses:` lines (an
    action reference such as gradle/actions/setup-gradle is not a repo path)."""
    return "\n".join(l for l in lines
                     if not l.lstrip().startswith("#") and not re.match(r"^\s*-?\s*uses:", l))


def derive_job_inputs(job_lines: list[str], repo: Path, problems: Problems) -> set[str]:
    tops = top_level_dirs(repo)
    text = step_text(job_lines)
    inputs: set[str] = {CI_YML_REL}
    queue = path_tokens(text, tops)
    scanned: set[str] = set()
    while queue:
        tok = queue.pop()
        if tok in scanned:
            continue
        scanned.add(tok)
        p = repo / tok
        if not p.exists():
            # A build output (e.g. .../pkg, target/...) resolves to its crate.
            crate = nearest_crate(p, repo)
            if crate is None:
                problems.append(f"{CI_YML_REL}: step path {tok!r} does not exist")
                continue
            p = crate
        r = rel(p, repo, problems, CI_YML_REL)
        if r is None:
            continue
        inputs.add(r)
        crate = nearest_crate(p if p.is_dir() else p.parent, repo)
        if crate is not None:
            seen: set[str] = set()
            cargo_path_deps(crate, repo, problems, seen)
            inputs |= seen
        if p.is_file() and p.suffix == ".sh":
            queue += path_tokens("\n".join(l for l in p.read_text(encoding="utf-8").splitlines()
                                           if not l.lstrip().startswith("#")), tops)
        if p.is_file() and p.suffix == ".py":
            python_script_deps(p, repo, problems, inputs)
    for d in re.findall(r"make\s+(?:[^\n]*?\s)?-C\s+(\S+)", text):
        makefile_deps(repo / d, repo, problems, inputs)
        go_root = repo / d
        if (go_root / "go.mod").exists():
            go_deps(go_root, repo, problems, inputs)
    for d in re.findall(r"working-directory:\s*(\S+)", text):
        if re.search(r"\bnpm\b", text) and (repo / d / "package.json").exists():
            npm_deps(repo / d, repo, problems, inputs)
    if re.search(r"(?:^|\s)\./gradlew\b", text):
        gradle_deps(repo, problems, inputs)
    return inputs


# --- the check ---------------------------------------------------------------

def check(repo: Path, ci_text: str) -> list[str]:
    errors: list[str] = []
    filters, fprob = parse_filters(ci_text)
    errors += [f"{CI_YML_REL}: {p}" for p in fprob]
    gates, gprob = parse_job_gates(ci_text)
    errors += [f"{CI_YML_REL}: {p}" for p in gprob]
    jobs = split_jobs(ci_text)
    if not has_unconditional_audit(ci_text):
        errors.append(f"{CI_YML_REL}: no job without an `if:` runs {AUDIT_SCRIPT_REL}; "
                      f"a filtered audit is itself a hollow gate")
    for job in sorted(EXPECTED_GATED_JOBS):
        if job not in jobs:
            errors.append(f"{CI_YML_REL}: job {job!r} not found (a gate this check requires)")
        elif not gates.get(job):
            errors.append(f"{CI_YML_REL}: job {job!r} is not gated on needs.changes.outputs.*")
    derived: dict[str, set[str]] = {}
    for job, outputs in gates.items():
        if not outputs:
            continue  # unfiltered or schedule-only: not a path gate
        problems = Problems()
        derived[job] = derive_job_inputs(jobs[job], repo, problems)
        errors += problems
    if "web-e2e" in derived:
        derived["web-e2e"] |= derived.get("web", set()) | derived.get("services", set())
    for job, inputs in sorted(derived.items()):
        outputs = gates[job]
        unknown = sorted(o for o in outputs if o not in filters)
        if unknown:
            errors.append(f"{CI_YML_REL}: job {job!r} references undefined filter output(s) {unknown}")
        patterns = [p for o in outputs if o in filters for p in filters[o]]
        for path in sorted(inputs):
            if not covers(patterns, path, (repo / path).is_dir()):
                errors.append(f"{CI_YML_REL}: job {job!r} reads {path} but its filter(s) {sorted(outputs)} "
                              f"do not match it, so a change there would skip the job (hollow gate)")
    return sorted(set(errors))


def main() -> int:
    ci = REPO / CI_YML_REL
    if not ci.exists():
        print(f"::error::{CI_YML_REL} not found", file=sys.stderr)
        return 1
    errors = check(REPO, ci.read_text(encoding="utf-8"))
    for e in errors:
        print(f"::error::{e}")
    print(f"check_ci_filters: {len(errors)} error(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
