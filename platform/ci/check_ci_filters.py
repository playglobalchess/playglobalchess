#!/usr/bin/env python3
"""
check_ci_filters: enforce the FAIL-SAFE path-filter policy of ci.yml (#86, #87).

Runs in CI as the unconditional `ci-filter-audit` job, and locally:
`python3 platform/ci/check_ci_filters.py`.

Background. A path filter that lists the folder a job OWNS, not the files it
READS, lets a change to an input skip the job, so its green check is hollow
(#86: the REST audit read docs/ specs no filter covered; #87: the game-server
and web builds read protocol/ and engine/ crates their filters missed). An
earlier version of this script tried to DERIVE every job's inputs from
Makefiles, Go, Cargo, npm and shell. That fails open by design: any build-file
shape the parser does not model drops an input silently, and three reviews in a
row found new shapes. So the design was inverted.

The policy (what this script enforces):

  P1  Cross-stream consumers (services, web, e2e) use EXCLUSION-style filters:
      `'**'` followed only by `!` patterns from ALLOWED_EXCLUSIONS below. They
      run on any change except paths they certainly do not read. Only docs and
      OTHER applications may be excluded; shared libraries and anything new stay
      included, so a new dependency can never make these gates hollow. Adding an
      exclusion means editing this file, which is a reviewable decision.
  P2  The paths-filter step is dorny/paths-filter@v4 with predicate-quantifier
      'some-with-excludes' (without it, `!` patterns would not exclude).
  P3  Each consumer job is gated ONLY on its own exclusion-style output.
  P4  Leaf jobs (engine, protocol, mobile, platform, protocol-parity) keep owner
      filters, which is safe only while the leaf reads nothing outside its
      folder. Enforced structurally:
        - every Cargo.toml under engine/ and protocol/ has path dependencies
          (dependencies, dev-, build-, target.*, workspace, [patch], [replace])
          only inside its own top-level folder, parsed with tomllib;
        - every Gradle module is declared with a projectDir under clients/, no
          Gradle build file references `../`, and the mobile filter lists every
          root Gradle file that exists.
  P5  Positive (leaf) filters contain no `!` patterns and include ci.yml.
  P6  UNCONDITIONAL jobs (ci-filter-audit, rest-contract) really run on every
      change: no job-level `if:`, no `needs:`, no `continue-on-error`, no
      step-level `if:`, and their run lines are exactly the expected command.
  P7  The workflow has no workflow-level `paths:` / `paths-ignore:` (those would
      skip every job, including the unconditional ones).
  P8  Every expected gated job exists, and gates are only
      `needs.changes.outputs.X == 'true'` joined by `||`.

Fails closed: a missing job, file, filter or step, or an unparseable filter line
or Cargo.toml, is an error.

Known limits (stated, not hidden): leaf jobs' own scripts (for example
platform/mobile/run_codec_parity.sh for protocol-parity) are trusted to read only
inside the folders their filter lists; the scheduled nightly runs every job and
is the backstop. The unit tests include an acceptance matrix that simulates
dorny's 'some-with-excludes' semantics for the #86 and #87 scenarios.

Stdlib only (Python >= 3.11 for tomllib). Tests: platform/ci/test_check_ci_filters.py.
"""
from __future__ import annotations

import re
import sys
import tomllib
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
CI_YML_REL = ".github/workflows/ci.yml"

# P1: the only paths each consumer may exclude. Docs and OTHER applications only.
#   docs:  docs/** and root-level *.md (README, CONTRIBUTING). `*.md` has one
#          star, so it matches root files only, never a .md inside a source tree.
ALLOWED_EXCLUSIONS: dict[str, set[str]] = {
    "services": {"docs/**", "*.md", "clients/**", "platform/mobile/**"},
    "web": {"docs/**", "*.md", "services/**", "clients/ios/**", "clients/android/**", "platform/mobile/**"},
    "e2e": {"docs/**", "*.md", "clients/ios/**", "clients/android/**", "platform/mobile/**"},
}
# P3: consumer job -> the one exclusion-style output it must be gated on.
CONSUMER_GATES = {"services": "services", "web": "web", "web-e2e": "e2e"}
# P4/P8: leaf job -> exact gate outputs.
LEAF_GATES = {"engine": {"engine"}, "protocol": {"protocol"}, "mobile": {"mobile"},
              "platform": {"platform"}, "protocol-parity": {"protocol", "mobile"}}
# P4: leaf output -> folder patterns its filter must contain.
LEAF_REQUIRED_PATTERNS = {"engine": ["engine/**"], "protocol": ["protocol/**"],
                          "platform": ["platform/**", ".github/workflows/**"]}
# P4: crates under these folders may depend only inside the same folder.
LEAF_CRATE_ROOTS = ["engine", "protocol"]
# P6: unconditional job -> its exact command.
UNCONDITIONAL = {"ci-filter-audit": "python3 platform/ci/check_ci_filters.py",
                 "rest-contract": "python3 platform/ci/check_rest_contract.py"}
ROOT_GRADLE_FILES = ["settings.gradle.kts", "build.gradle.kts", "gradle.properties", "gradlew"]
SKIP_DIRS = {"target", "node_modules", "pkg", "build", "dist", ".gradle"}


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
            if "{" in p:
                problems.append(f"filter {m.group(1)!r}: brace pattern {p!r} is not allowed (not modelled)")
        filters[m.group(1)] = items
    if not filters:
        problems.append("filters block is empty")
    return filters, problems


def split_jobs(text: str) -> dict[str, list[str]]:
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


_GATE_TERM = r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)\s*==\s*'true'"
_GATE_EXPR = re.compile(r"^\$\{\{\s*" + _GATE_TERM + r"(?:\s*\|\|\s*" + _GATE_TERM.replace("(", "(?:", 1) + r")*\s*\}\}$")


def gate_outputs(expr: str | None) -> tuple[set[str] | None, str | None]:
    """(outputs, problem). None outputs = not a changes gate."""
    if expr is None or "needs.changes.outputs" not in expr:
        return None, None
    if not _GATE_EXPR.match(expr.strip()):
        return set(re.findall(r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)", expr)), \
            f"gate {expr!r} is not of the form ${{{{ needs.changes.outputs.X == 'true' || ... }}}}"
    return set(re.findall(r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)", expr)), None


def run_lines(lines: list[str]) -> list[str]:
    """Non-comment, non-empty command lines from every `run:` in a job."""
    out: list[str] = []
    i = 0
    while i < len(lines):
        m = re.match(r"^(\s*)(?:-\s*)?run:\s*(.*)$", lines[i])
        if not m:
            i += 1
            continue
        indent, value = len(m.group(1)), m.group(2).strip()
        if value in ("|", "|-", ">", ">-"):
            i += 1
            # A block scalar's content is indented deeper than the `run:` key.
            while i < len(lines) and (not lines[i].strip() or len(lines[i]) - len(lines[i].lstrip()) > indent):
                s = lines[i].strip()
                if s and not s.startswith("#"):
                    out.append(s)
                i += 1
            continue
        if value:
            out.append(value)
        i += 1
    return out


def on_block(text: str) -> list[str]:
    lines = text.splitlines()
    start = next((i for i, l in enumerate(lines) if re.match(r"^(on|\"on\"|'on'):", l)), None)
    if start is None:
        return []
    block = []
    for l in lines[start + 1:]:
        if re.match(r"^\S", l):
            break
        block.append(l)
    return block


# --- glob + simulation of dorny 'some-with-excludes' (used by the tests) ------

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
                out.append("[" + ("^" + body[1:] if body.startswith("!") else body) + "]")
                i = j
        else:
            out.append(re.escape(c))
        i += 1
    return re.compile("^" + "".join(out) + "$")


def filter_matches(patterns: list[str], changed: list[str]) -> bool:
    """dorny 'some-with-excludes': a file counts if it matches an include pattern
    and no `!` pattern; the filter is true if any changed file counts."""
    inc = [glob_to_regex(p) for p in patterns if not p.startswith("!")]
    exc = [glob_to_regex(p[1:]) for p in patterns if p.startswith("!")]
    return any(any(r.match(f) for r in inc) and not any(r.match(f) for r in exc) for f in changed)


def jobs_that_run(text: str, changed: list[str]) -> set[str]:
    """Which jobs run for a pull request changing `changed` (for the tests)."""
    filters, _ = parse_filters(text)
    outputs = {name: filter_matches(p, changed) for name, p in filters.items()}
    result = set()
    for job, lines in split_jobs(text).items():
        if job == "changes":
            continue
        expr = job_if(lines)
        outs, _ = gate_outputs(expr)
        if expr is None:
            result.add(job)
        elif outs is not None and any(outputs.get(o) for o in outs):
            result.add(job)
    return result


# --- policy checks ------------------------------------------------------------

def check_cargo_leaf(repo: Path, root: str, errors: list[str]) -> None:
    base = (repo / root).resolve()
    manifests = [m for m in sorted((repo / root).rglob("Cargo.toml")) if not (set(m.parts) & SKIP_DIRS)]
    if not manifests:
        errors.append(f"{root}/: no Cargo.toml found (leaf crate check cannot run)")
    for m in manifests:
        rel = m.relative_to(repo).as_posix()
        try:
            data = tomllib.loads(m.read_text(encoding="utf-8"))
        except tomllib.TOMLDecodeError as e:
            errors.append(f"{rel}: cannot parse: {e}")
            continue
        specs: list[dict] = []
        def tables(d: dict) -> None:
            for k in ("dependencies", "dev-dependencies", "build-dependencies"):
                specs.extend(v for v in (d.get(k) or {}).values() if isinstance(v, dict))
        tables(data)
        for tgt in (data.get("target") or {}).values():
            tables(tgt)
        ws = data.get("workspace") or {}
        specs.extend(v for v in (ws.get("dependencies") or {}).values() if isinstance(v, dict))
        for section in ("patch", "replace"):
            for registry in (data.get(section) or {}).values():
                if isinstance(registry, dict):
                    specs.extend(v for v in registry.values() if isinstance(v, dict))
        paths = [s["path"] for s in specs if "path" in s] + list(ws.get("members", []))
        for p in paths:
            dep = (m.parent / p).resolve()
            if dep != base and base not in dep.parents:
                errors.append(f"{rel}: path dependency {p!r} leaves {root}/; the {root!r} job has an owner "
                              f"filter, so this would be a hollow gate. Keep deps inside {root}/ or make the "
                              f"job's filter exclusion-style")


def check_gradle_leaf(repo: Path, mobile_patterns: list[str], errors: list[str]) -> None:
    for f in ROOT_GRADLE_FILES:
        if (repo / f).exists() and f not in mobile_patterns:
            errors.append(f"{CI_YML_REL}: mobile filter must list root Gradle file {f!r}")
    if (repo / "gradle").is_dir() and "gradle/**" not in mobile_patterns:
        errors.append(f"{CI_YML_REL}: mobile filter must list 'gradle/**'")
    settings = repo / "settings.gradle.kts"
    if not settings.exists():
        return
    text = settings.read_text(encoding="utf-8")
    includes = re.findall(r"include\(\s*\"(:[^\"]+)\"\s*\)", text)
    dirs = dict(re.findall(r"project\(\s*\"(:[^\"]+)\"\s*\)\.projectDir\s*=\s*file\(\s*\"([^\"]+)\"\s*\)", text))
    for mod in includes:
        d = dirs.get(mod)
        if d is None:
            errors.append(f"settings.gradle.kts: module {mod} has no projectDir; declare it under clients/")
        elif not d.startswith("clients/"):
            errors.append(f"settings.gradle.kts: module {mod} projectDir {d!r} is outside clients/")
    for gf in [settings, repo / "build.gradle.kts", *sorted((repo / "clients").rglob("*.gradle.kts"))]:
        if gf.exists() and not (set(gf.parts) & SKIP_DIRS) and "../" in gf.read_text(encoding="utf-8"):
            errors.append(f"{gf.relative_to(repo).as_posix()}: references '../'; the mobile job has an owner "
                          f"filter, so reading outside it would be a hollow gate")


def check_unconditional(job: str, lines: list[str] | None, command: str, errors: list[str]) -> None:
    if lines is None:
        errors.append(f"{CI_YML_REL}: unconditional job {job!r} not found")
        return
    where = f"{CI_YML_REL}: job {job!r} must run on every change"
    if any(re.match(r"^    if:", l) for l in lines):
        errors.append(f"{where}: remove its job-level `if:`")
    if any(re.match(r"^    needs:", l) for l in lines):
        errors.append(f"{where}: remove `needs:` (a skipped dependency skips it)")
    if any(re.search(r"\bcontinue-on-error\b", l) for l in lines if not l.lstrip().startswith("#")):
        errors.append(f"{where}: remove `continue-on-error` (a failure must fail CI)")
    if any(re.match(r"^\s{6,}(?:-\s+)?if:", l) for l in lines):
        errors.append(f"{where}: remove step-level `if:`")
    cmds = run_lines(lines)
    if cmds != [command]:
        errors.append(f"{where}: its run lines must be exactly [{command!r}], found {cmds!r}")


def check(repo: Path, text: str) -> list[str]:
    errors: list[str] = []
    filters, fprob = parse_filters(text)
    errors += [f"{CI_YML_REL}: {p}" for p in fprob]
    jobs = split_jobs(text)

    # P7
    for l in on_block(text):
        if re.match(r"^\s+(paths|paths-ignore):", l):
            errors.append(f"{CI_YML_REL}: workflow-level {l.strip()!r} would skip every job; remove it")

    # P2
    changes = jobs.get("changes")
    if changes is None:
        errors.append(f"{CI_YML_REL}: job 'changes' not found")
    else:
        body = "\n".join(changes)
        if not re.search(r"uses:\s*dorny/paths-filter@v4\b", body):
            errors.append(f"{CI_YML_REL}: the changes job must use dorny/paths-filter@v4 (some-with-excludes needs v4)")
        if not re.search(r"predicate-quantifier:\s*['\"]some-with-excludes['\"]", body):
            errors.append(f"{CI_YML_REL}: paths-filter must set predicate-quantifier: 'some-with-excludes'")

    # P1, P5
    for name, patterns in filters.items():
        if name in ALLOWED_EXCLUSIONS:
            if not patterns or patterns[0] != "**":
                errors.append(f"{CI_YML_REL}: filter {name!r} must start with '**' (exclusion-style)")
            for p in patterns[1:]:
                if not p.startswith("!"):
                    errors.append(f"{CI_YML_REL}: filter {name!r} may only add '!' exclusions after '**', found {p!r}")
                elif p[1:] not in ALLOWED_EXCLUSIONS[name]:
                    errors.append(f"{CI_YML_REL}: filter {name!r} excludes {p[1:]!r}, which is not in "
                                  f"ALLOWED_EXCLUSIONS (only docs and other applications may be excluded)")
        else:
            for p in patterns:
                if p.startswith("!"):
                    errors.append(f"{CI_YML_REL}: owner filter {name!r} must not use '!' ({p!r})")
            if not any(glob_to_regex(p).match(CI_YML_REL) for p in patterns):
                errors.append(f"{CI_YML_REL}: filter {name!r} must include {CI_YML_REL}")
    for name in ALLOWED_EXCLUSIONS:
        if name not in filters:
            errors.append(f"{CI_YML_REL}: exclusion-style filter {name!r} not found")

    # P3, P8 gates
    expected = {**{j: {o} for j, o in CONSUMER_GATES.items()}, **LEAF_GATES}
    for job, want in sorted(expected.items()):
        lines = jobs.get(job)
        if lines is None:
            errors.append(f"{CI_YML_REL}: job {job!r} not found")
            continue
        outs, prob = gate_outputs(job_if(lines))
        if prob:
            errors.append(f"{CI_YML_REL}: job {job!r}: {prob}")
        if outs != want:
            errors.append(f"{CI_YML_REL}: job {job!r} must be gated on {sorted(want)}, found {sorted(outs or [])}")
        for o in outs or []:
            if o not in filters:
                errors.append(f"{CI_YML_REL}: job {job!r} references undefined filter output {o!r}")
    for job, lines in jobs.items():
        outs, prob = gate_outputs(job_if(lines))
        if outs is not None and job not in expected:
            errors.append(f"{CI_YML_REL}: new gated job {job!r} is not in the policy; add it to "
                          f"CONSUMER_GATES (exclusion-style) or LEAF_GATES (with a leaf check)")

    # P4
    for out, required in LEAF_REQUIRED_PATTERNS.items():
        for p in required:
            if p not in filters.get(out, []):
                errors.append(f"{CI_YML_REL}: leaf filter {out!r} must contain {p!r}")
    for root in LEAF_CRATE_ROOTS:
        check_cargo_leaf(repo, root, errors)
    check_gradle_leaf(repo, filters.get("mobile", []), errors)

    # P6
    for job, command in UNCONDITIONAL.items():
        check_unconditional(job, jobs.get(job), command, errors)
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
