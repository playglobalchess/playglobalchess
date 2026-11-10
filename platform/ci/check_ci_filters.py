#!/usr/bin/env python3
"""
check_ci_filters: enforce the FAIL-SAFE path-filter policy of ci.yml (#86, #87).

Runs in CI as the unconditional `ci-filter-audit` job, and locally:
  python3 -m pip install --require-hashes -r platform/ci/requirements-ci.txt
  python3 platform/ci/check_ci_filters.py

Background. A path filter that lists the folder a job OWNS, not the files it
READS, lets a change to an input skip the job, so its green check is hollow
(#86: the REST audit read docs/ specs no filter covered; #87: the game-server
and web builds read protocol/ and engine/ crates their filters missed). An
earlier version of this script DERIVED every job's inputs from Makefiles, Go,
Cargo, npm and shell. That fails open by design (any shape the parser does not
model drops an input silently), so the design was inverted. The workflow is
parsed with PyYAML (pinned, hash-verified), never with regexes over its text.

The policy:

  P1  Cross-stream consumers (services, web, e2e) use EXCLUSION-style filters:
      '**' followed only by '!' patterns from ALLOWED_EXCLUSIONS. They run on any
      change except paths they certainly do not read. Only docs and OTHER
      applications may be excluded; shared libraries and anything new stay
      included. Adding an exclusion means editing this file (a reviewed choice).
  P2  The changes job: no `if:`, no `continue-on-error`; its paths-filter step is
      dorny/paths-filter@v4 with id `filter` and predicate-quantifier
      'some-with-excludes'; its `outputs:` map is exactly
      {name: ${{ steps.filter.outputs.name }}} for every filter.
  P3  Each consumer job is gated only on its own exclusion-style output.
  P4  Leaf jobs (engine, protocol, mobile, platform, protocol-parity) keep owner
      filters, safe only while each reads nothing outside its folder:
        - engine and protocol filters include the root Rust config that Cargo
          and rustup read (Cargo.toml, Cargo.lock, rust-toolchain[.toml], .cargo/);
        - every Cargo.toml and .cargo/config.toml under engine/ and protocol/ has
          path entries (all dependency tables, [patch], [replace], workspace,
          [paths]) only inside its folder (tomllib);
        - every string literal containing '../' in a .rs file under those
          folders (include_str!, include_bytes!, CARGO_MANIFEST_DIR paths,
          build.rs) resolves inside the folder; a line marked
          `ci-audit: not-a-path` is skipped (for test data that is not a path);
        - Gradle: every module has a projectDir under clients/; Gradle files
          contain no '../', rootDir, rootProject or includeBuild; the mobile
          filter lists every root Gradle file that exists.
  P5  Owner (leaf) filters contain no '!' patterns and include ci.yml.
  P6  UNCONDITIONAL jobs (ci-filter-audit, rest-contract) run on every change:
      only the keys runs-on, steps (and name); runs-on ubuntu-latest; steps are
      exactly checkout, setup-python 3.12, (for the audit) the hash-verified
      install, and the one command; steps use only name/uses/with/run.
  P7  Triggers: on.pull_request has no restrictions (no branches, types or
      paths), on.push is exactly {branches: [main]}, no workflow-level paths.
  P8  Every expected gated job exists; a gate is exactly
      `needs.changes.outputs.X == 'true'` terms joined by `||`, plus
      `|| github.event_name == 'schedule'`, so the nightly runs every job no
      matter what the paths-filter returns on schedule events. A new gated job
      must join the policy.

Fails closed: a missing job, file, filter or step, unparseable YAML or TOML, or
a duplicate YAML key is an error.

Known limits: a leaf job's own scripts (e.g. platform/mobile/run_codec_parity.sh
for protocol-parity) are trusted to read only inside the folders their filter
lists, and paths built at runtime are not detected; the nightly (which runs
every gated job, per P8) is the backstop. Tests include an acceptance matrix
that simulates dorny's 'some-with-excludes' over the real ci.yml.

Tests: platform/ci/test_check_ci_filters.py (needs the same requirements file).
"""
from __future__ import annotations

import re
import sys
import tomllib
from pathlib import Path

try:
    import yaml
except ImportError:  # fail closed with a clear message
    print("::error::PyYAML is required: python3 -m pip install --require-hashes "
          "-r platform/ci/requirements-ci.txt", file=sys.stderr)
    raise

REPO = Path(__file__).resolve().parents[2]
CI_YML_REL = ".github/workflows/ci.yml"

#   docs: docs/** and root-level *.md. `*.md` (one star) matches root files only.
ALLOWED_EXCLUSIONS: dict[str, set[str]] = {
    "services": {"docs/**", "*.md", "clients/**", "platform/mobile/**"},
    "web": {"docs/**", "*.md", "services/**", "clients/ios/**", "clients/android/**", "platform/mobile/**"},
    "e2e": {"docs/**", "*.md", "clients/ios/**", "clients/android/**", "platform/mobile/**"},
}
CONSUMER_GATES = {"services": "services", "web": "web", "web-e2e": "e2e"}
LEAF_GATES = {"engine": {"engine"}, "protocol": {"protocol"}, "mobile": {"mobile"},
              "platform": {"platform"}, "protocol-parity": {"protocol", "mobile"}}
ROOT_RUST_CONFIG = ["Cargo.toml", "Cargo.lock", "rust-toolchain", "rust-toolchain.toml", ".cargo/**"]
LEAF_REQUIRED_PATTERNS = {"engine": ["engine/**", *ROOT_RUST_CONFIG],
                          "protocol": ["protocol/**", *ROOT_RUST_CONFIG],
                          "platform": ["platform/**", ".github/workflows/**"]}
LEAF_CRATE_ROOTS = ["engine", "protocol"]
INSTALL_CMD = "python3 -m pip install --require-hashes -r platform/ci/requirements-ci.txt"
UNCONDITIONAL = {
    "ci-filter-audit": [INSTALL_CMD, "python3 platform/ci/check_ci_filters.py"],
    "rest-contract": ["python3 platform/ci/check_rest_contract.py"],
}
SCHEDULE_TERM = "github.event_name == 'schedule'"
SKIP_DIRS = {"target", "node_modules", "pkg", "build", "dist", ".gradle"}
NOT_A_PATH = "ci-audit: not-a-path"


# --- YAML loading -------------------------------------------------------------

class _UniqueKeyLoader(yaml.SafeLoader):
    """SafeLoader that rejects duplicate mapping keys (GitHub does too)."""


def _construct_mapping(loader: _UniqueKeyLoader, node: yaml.MappingNode, deep: bool = False) -> dict:
    seen = set()
    for key_node, _ in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in seen:
            raise yaml.constructor.ConstructorError(None, None, f"duplicate key {key!r}", key_node.start_mark)
        seen.add(key)
    return loader.construct_mapping(node, deep)


_UniqueKeyLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _construct_mapping)


def load_yaml(text: str) -> object:
    return yaml.load(text, Loader=_UniqueKeyLoader)  # noqa: S506 - SafeLoader subclass


def workflow_on(wf: dict) -> object:
    # YAML 1.1 reads a bare `on` key as boolean True.
    return wf.get("on", wf.get(True))


def filters_of(wf: dict) -> tuple[dict[str, list[str]], dict | None, list[str]]:
    """({output: [patterns]}, the paths-filter step, problems)."""
    problems: list[str] = []
    steps = ((wf.get("jobs") or {}).get("changes") or {}).get("steps") or []
    pf = [s for s in steps if isinstance(s, dict) and str(s.get("uses", "")).startswith("dorny/paths-filter@")]
    if len(pf) != 1:
        return {}, None, [f"the changes job must have exactly one dorny/paths-filter step, found {len(pf)}"]
    raw = (pf[0].get("with") or {}).get("filters")
    if not isinstance(raw, str):
        return {}, pf[0], ["paths-filter `with.filters` must be an inline YAML string"]
    try:
        parsed = load_yaml(raw)
    except yaml.YAMLError as e:
        return {}, pf[0], [f"paths-filter filters do not parse: {e}"]
    if not isinstance(parsed, dict) or not parsed:
        return {}, pf[0], ["paths-filter filters must be a non-empty mapping"]
    filters: dict[str, list[str]] = {}
    for name, value in parsed.items():
        if not isinstance(value, list) or not all(isinstance(p, str) for p in value):
            problems.append(f"filter {name!r} must be a list of glob strings (no change-type dicts)")
            continue
        for p in value:
            if "{" in p:
                problems.append(f"filter {name!r}: brace pattern {p!r} is not allowed (not modelled)")
        filters[str(name)] = value
    return filters, pf[0], problems


# --- gates ----------------------------------------------------------------------

_OUT = r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)\s*==\s*'true'"
_GATE = re.compile(r"^\$\{\{\s*(?:" + _OUT + r")(?:\s*\|\|\s*(?:" + _OUT.replace("(", "(?:", 1) + r"))*"
                   r"\s*\|\|\s*github\.event_name\s*==\s*'schedule'\s*\}\}$")


def gate_outputs(expr: object) -> tuple[set[str] | None, str | None]:
    """(outputs, problem). None outputs means: not a changes gate."""
    if not isinstance(expr, str) or "needs.changes.outputs" not in expr:
        return None, None
    outs = set(re.findall(r"needs\.changes\.outputs\.([A-Za-z0-9_-]+)", expr))
    if not _GATE.match(expr.strip()):
        return outs, (f"gate {expr!r} must be ${{{{ needs.changes.outputs.X == 'true' || ... || "
                      f"{SCHEDULE_TERM} }}}}")
    return outs, None


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
    """dorny 'some-with-excludes' (src/filter.ts v4.0.3): a file counts if it
    matches an include pattern and no '!' pattern."""
    inc = [glob_to_regex(p) for p in patterns if not p.startswith("!")]
    exc = [glob_to_regex(p[1:]) for p in patterns if p.startswith("!")]
    return any(any(r.match(f) for r in inc) and not any(r.match(f) for r in exc) for f in changed)


def jobs_that_run(text: str, changed: list[str], event: str = "pull_request") -> set[str]:
    wf = load_yaml(text)
    filters, _, _ = filters_of(wf)
    outputs = {n: filter_matches(p, changed) for n, p in filters.items()}
    result = set()
    for job, spec in (wf.get("jobs") or {}).items():
        if job == "changes":
            continue
        expr = spec.get("if")
        if expr is None:
            result.add(job)
            continue
        outs, _ = gate_outputs(expr)
        if outs is not None and (any(outputs.get(o) for o in outs) or (event == "schedule" and SCHEDULE_TERM in expr)):
            result.add(job)
        elif outs is None and event == "schedule" and "schedule" in str(expr):
            result.add(job)
    return result


# --- leaf checks --------------------------------------------------------------

def _inside(path: Path, base: Path) -> bool:
    p = path.resolve()
    return p == base or base in p.parents


def _toml_paths(data: dict) -> list[str]:
    specs: list[dict] = []
    def tables(d: dict) -> None:
        for k in ("dependencies", "dev-dependencies", "build-dependencies"):
            specs.extend(v for v in (d.get(k) or {}).values() if isinstance(v, dict))
    tables(data)
    for tgt in (data.get("target") or {}).values():
        if isinstance(tgt, dict):
            tables(tgt)
    ws = data.get("workspace") or {}
    specs.extend(v for v in (ws.get("dependencies") or {}).values() if isinstance(v, dict))
    for registry in (data.get("patch") or {}).values():                # [patch.<registry>.<pkg>]
        if isinstance(registry, dict):
            specs.extend(v for v in registry.values() if isinstance(v, dict))
    specs.extend(v for v in (data.get("replace") or {}).values() if isinstance(v, dict))  # [replace."<pkg>:<ver>"]
    paths = [s["path"] for s in specs if isinstance(s.get("path"), str)]
    paths += [m for m in ws.get("members", []) if isinstance(m, str)]
    paths += [p for p in data.get("paths", []) if isinstance(p, str)]  # .cargo/config.toml [paths]
    return paths


def check_cargo_leaf(repo: Path, root: str, errors: list[str]) -> None:
    base = (repo / root).resolve()
    tomls = [p for p in sorted((repo / root).rglob("Cargo.toml")) if not set(p.parts) & SKIP_DIRS]
    tomls += [p for p in sorted((repo / root).rglob("config.toml")) if p.parent.name == ".cargo"]
    tomls += [p for p in sorted((repo / root).rglob("config")) if p.parent.name == ".cargo"]
    if not any(p.name == "Cargo.toml" for p in tomls):
        errors.append(f"{root}/: no Cargo.toml found (leaf crate check cannot run)")
    for m in tomls:
        rel = m.relative_to(repo).as_posix()
        try:
            data = tomllib.loads(m.read_text(encoding="utf-8"))
        except tomllib.TOMLDecodeError as e:
            errors.append(f"{rel}: cannot parse: {e}")
            continue
        anchor = m.parent.parent if m.parent.name == ".cargo" else m.parent
        for p in _toml_paths(data):
            if not _inside(anchor / p, base):
                errors.append(f"{rel}: path {p!r} leaves {root}/; the {root!r} job has an owner filter, so "
                              f"this would be a hollow gate. Keep it inside {root}/ or make the filter exclusion-style")


_RS_STR = re.compile(r"r(#*)\"(.*?)\"\1|\"((?:[^\"\\\n]|\\.)*)\"", re.S)


def check_rust_sources_leaf(repo: Path, root: str, errors: list[str]) -> None:
    base = (repo / root).resolve()
    for src in sorted((repo / root).rglob("*.rs")):
        if set(src.relative_to(repo).parts) & SKIP_DIRS:
            continue
        text = src.read_text(encoding="utf-8", errors="replace")
        crate = next((p for p in [src.parent, *src.parents] if (p / "Cargo.toml").exists()), src.parent)
        for m in _RS_STR.finditer(text):
            s = m.group(2) if m.group(2) is not None else m.group(3)
            if s is None or "../" not in s:
                continue
            line_start = text.rfind("\n", 0, m.start()) + 1
            line_end = text.find("\n", m.end())
            line = text[line_start:line_end if line_end != -1 else len(text)]
            if NOT_A_PATH in line:
                continue
            # A path can sit after a prefix (`cargo:rerun-if-changed=../x`) or
            # among other words, so check the whole string, the tail from the
            # first '../', and every token that holds '../'.
            candidates = {s, s[s.index("../"):]}
            candidates |= {t for t in re.split(r"[\s=,;]+", s) if "../" in t}
            # Fail safe: include_str! resolves against the file, CARGO_MANIFEST_DIR
            # joins against the crate. Each candidate must stay inside under BOTH.
            if all(_inside(src.parent / cand.lstrip("/"), base) and _inside(crate / cand.lstrip("/"), base)
                   for cand in candidates):
                continue
            lineno = text.count("\n", 0, m.start()) + 1
            errors.append(f"{src.relative_to(repo).as_posix()}:{lineno}: string {s!r} points outside {root}/ "
                          f"(include_str!/CARGO_MANIFEST_DIR paths must stay inside the leaf folder; mark test "
                          f"data that is not a path with `// {NOT_A_PATH}`)")


def check_gradle_leaf(repo: Path, mobile_patterns: list[str], errors: list[str]) -> None:
    for entry in sorted(repo.iterdir()):
        n = entry.name
        is_gradle = n.endswith((".gradle", ".gradle.kts")) or n in ("gradle.properties", "gradlew", "gradlew.bat")
        if entry.is_file() and is_gradle and n not in mobile_patterns:
            errors.append(f"{CI_YML_REL}: mobile filter must list root Gradle file {n!r}")
        if entry.is_dir() and n == "gradle" and "gradle/**" not in mobile_patterns:
            errors.append(f"{CI_YML_REL}: mobile filter must list 'gradle/**'")
    settings = repo / "settings.gradle.kts"
    if settings.exists():
        text = settings.read_text(encoding="utf-8")
        includes = re.findall(r"include\(\s*\"(:[^\"]+)\"\s*\)", text)
        dirs = dict(re.findall(r"project\(\s*\"(:[^\"]+)\"\s*\)\.projectDir\s*=\s*file\(\s*\"([^\"]+)\"\s*\)", text))
        for mod in includes:
            d = dirs.get(mod)
            if d is None:
                errors.append(f"settings.gradle.kts: module {mod} has no projectDir; declare it under clients/")
            elif not d.startswith("clients/"):
                errors.append(f"settings.gradle.kts: module {mod} projectDir {d!r} is outside clients/")
    gradle_files = [p for p in repo.iterdir() if p.is_file() and p.name.endswith((".gradle", ".gradle.kts"))]
    if (repo / "clients").is_dir():
        gradle_files += [p for p in (repo / "clients").rglob("*.gradle*") if p.suffix in (".gradle", ".kts")]
    for gf in sorted(set(gradle_files)):
        if set(gf.relative_to(repo).parts) & SKIP_DIRS:
            continue
        text = gf.read_text(encoding="utf-8")
        for token in ("../", "rootDir", "rootProject", "includeBuild"):
            if token in text and not (gf == settings and token == "rootProject" and re.search(r"rootProject\.name\s*=", text)
                                      and text.count("rootProject") == len(re.findall(r"rootProject\.name\s*=", text))):
                errors.append(f"{gf.relative_to(repo).as_posix()}: uses {token!r}; the mobile job has an owner "
                              f"filter, so reading outside it would be a hollow gate")


# --- structural checks --------------------------------------------------------

def check_unconditional(job: str, spec: object, commands: list[str], errors: list[str]) -> None:
    where = f"{CI_YML_REL}: job {job!r} must run on every change"
    if not isinstance(spec, dict):
        errors.append(f"{CI_YML_REL}: unconditional job {job!r} not found")
        return
    extra = set(spec) - {"runs-on", "steps", "name"}
    if extra:
        errors.append(f"{where}: only runs-on, steps and name are allowed, found {sorted(map(str, extra))}")
    if spec.get("runs-on") != "ubuntu-latest":
        errors.append(f"{where}: runs-on must be 'ubuntu-latest'")
    steps = spec.get("steps") or []
    expected = [{"uses": "actions/checkout@v4"},
                {"uses": "actions/setup-python@v5", "with": {"python-version": "3.12"}}]
    for i, cmd in enumerate(commands):
        expected.append({"run": cmd})
    if len(steps) != len(expected):
        errors.append(f"{where}: expected {len(expected)} steps (checkout, setup-python, "
                      f"{', '.join(commands)}), found {len(steps)}")
        return
    for i, (got, want) in enumerate(zip(steps, expected)):
        if not isinstance(got, dict):
            errors.append(f"{where}: step {i} is not a mapping")
            continue
        bad = set(got) - {"name", "uses", "with", "run"}
        if bad:
            errors.append(f"{where}: step {i} uses disallowed keys {sorted(map(str, bad))}")
        if "uses" in want:
            if got.get("uses") != want["uses"] or (got.get("with") or None) != want.get("with"):
                errors.append(f"{where}: step {i} must be {want}")
            if "run" in got:
                errors.append(f"{where}: step {i} must not have run")
        else:
            run = got.get("run")
            lines = [l.strip() for l in str(run or "").splitlines() if l.strip() and not l.strip().startswith("#")]
            if lines != [want["run"]] or "uses" in got or "with" in got:
                errors.append(f"{where}: step {i} must run exactly {want['run']!r}, found {lines!r}")


def check_triggers(wf: dict, errors: list[str]) -> None:
    on = workflow_on(wf)
    if not isinstance(on, dict):
        errors.append(f"{CI_YML_REL}: `on:` must be a mapping with pull_request and push")
        return
    if "pull_request" not in on:
        errors.append(f"{CI_YML_REL}: the pull_request trigger is required")
    elif on["pull_request"] not in (None, {}):
        errors.append(f"{CI_YML_REL}: on.pull_request must have no restrictions (branches, types, paths), "
                      f"found {on['pull_request']!r}")
    if on.get("push") != {"branches": ["main"]}:
        errors.append(f"{CI_YML_REL}: on.push must be exactly {{branches: [main]}}, found {on.get('push')!r}")
    for trig, val in on.items():
        if isinstance(val, dict) and ({"paths", "paths-ignore"} & set(val)):
            errors.append(f"{CI_YML_REL}: trigger {trig!r} has paths/paths-ignore, which would skip every job")


def check(repo: Path, text: str) -> list[str]:
    errors: list[str] = []
    try:
        wf = load_yaml(text)
    except yaml.YAMLError as e:
        return [f"{CI_YML_REL}: does not parse: {e}"]
    if not isinstance(wf, dict) or not isinstance(wf.get("jobs"), dict):
        return [f"{CI_YML_REL}: no jobs mapping"]
    jobs: dict = wf["jobs"]

    check_triggers(wf, errors)                                               # P7
    filters, pf, fprob = filters_of(wf)                                      # P2
    errors += [f"{CI_YML_REL}: {p}" for p in fprob]
    changes = jobs.get("changes")
    if not isinstance(changes, dict):
        errors.append(f"{CI_YML_REL}: job 'changes' not found")
    else:
        for key in ("if", "continue-on-error", "needs"):
            if key in changes:
                errors.append(f"{CI_YML_REL}: the changes job must not have {key!r}")
        if pf is not None:
            if pf.get("uses") != "dorny/paths-filter@v4":
                errors.append(f"{CI_YML_REL}: the changes job must use dorny/paths-filter@v4 (some-with-excludes needs v4)")
            if pf.get("id") != "filter":
                errors.append(f"{CI_YML_REL}: the paths-filter step must have id 'filter'")
            if (pf.get("with") or {}).get("predicate-quantifier") != "some-with-excludes":
                errors.append(f"{CI_YML_REL}: paths-filter must set predicate-quantifier: 'some-with-excludes'")
            if "if" in pf or "continue-on-error" in pf:
                errors.append(f"{CI_YML_REL}: the paths-filter step must not have if/continue-on-error")
        outputs = changes.get("outputs") or {}
        want_outputs = {n: f"${{{{ steps.filter.outputs.{n} }}}}" for n in filters}
        if outputs != want_outputs:
            errors.append(f"{CI_YML_REL}: changes.outputs must be exactly {want_outputs}, found {outputs}")

    for name, patterns in filters.items():                                   # P1, P5
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

    expected = {**{j: {o} for j, o in CONSUMER_GATES.items()}, **LEAF_GATES}  # P3, P8
    for job, want in sorted(expected.items()):
        spec = jobs.get(job)
        if not isinstance(spec, dict):
            errors.append(f"{CI_YML_REL}: job {job!r} not found")
            continue
        outs, prob = gate_outputs(spec.get("if"))
        if prob:
            errors.append(f"{CI_YML_REL}: job {job!r}: {prob}")
        if outs != want:
            errors.append(f"{CI_YML_REL}: job {job!r} must be gated on {sorted(want)}, found {sorted(outs or [])}")
        if spec.get("needs") != "changes":
            errors.append(f"{CI_YML_REL}: job {job!r} must have needs: changes (and nothing else)")
        if "continue-on-error" in spec:
            errors.append(f"{CI_YML_REL}: job {job!r} must not have continue-on-error")
        for o in outs or []:
            if o not in filters:
                errors.append(f"{CI_YML_REL}: job {job!r} references undefined filter output {o!r}")
    for job, spec in jobs.items():
        if isinstance(spec, dict) and job not in expected and job != "changes":
            outs, _ = gate_outputs(spec.get("if"))
            if outs is not None:
                errors.append(f"{CI_YML_REL}: new gated job {job!r} is not in the policy; add it to "
                              f"CONSUMER_GATES (exclusion-style) or LEAF_GATES (with a leaf check)")

    for out, required in LEAF_REQUIRED_PATTERNS.items():                     # P4
        for p in required:
            if p not in filters.get(out, []):
                errors.append(f"{CI_YML_REL}: leaf filter {out!r} must contain {p!r}")
    for root in LEAF_CRATE_ROOTS:
        check_cargo_leaf(repo, root, errors)
        check_rust_sources_leaf(repo, root, errors)
    check_gradle_leaf(repo, filters.get("mobile", []), errors)

    for job, commands in UNCONDITIONAL.items():                              # P6
        check_unconditional(job, jobs.get(job), commands, errors)
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
