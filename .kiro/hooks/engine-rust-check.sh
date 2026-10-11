#!/usr/bin/env bash
# postToolUse hook: after a write to the Rust engine, run fmt + clippy as a
# fast quality gate (engineering-conventions.md: clippy clean, rustfmt).
#
# Kiro delivers the hook event as JSON on STDIN. We read it, extract the
# written file path, and only act when ALL of these hold:
#   - the path is under engine/
#   - engine/Cargo.toml exists (there is actually a crate to check)
#   - cargo is installed on this machine
# Otherwise this is a silent no-op (exit 0) — so it sits dormant and harmless
# until the engine has real code and a toolchain.
#
# Philosophy matches guard-protected-branch.sh: FAIL OPEN on any infra/parse
# trouble (missing tool, bad JSON) so the hook never wedges the agent. It only
# reports genuine fmt/clippy findings; it does NOT hard-block (postToolUse
# runs after the write anyway) — the message goes back to the LLM to fix.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
event="$(cat 2>/dev/null || true)"

# --- extract the written path from the event JSON (python3, like the guard) ---
path=""
if command -v python3 >/dev/null 2>&1; then
  path="$(printf '%s' "$event" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    ti = d.get("tool_input", {}) or {}
    # file-write tool shapes vary; try the common keys.
    print(ti.get("path") or ti.get("file") or ti.get("filePath") or "")
except Exception:
    print("")
' 2>/dev/null || true)"
fi

# No path resolved → nothing to check. Fail open.
[ -n "$path" ] || exit 0

# Only care about the Rust engine.
case "$path" in
  *engine/*.rs|engine/*.rs|*/engine/*.rs) : ;;
  *) exit 0 ;;
esac

# Need an actual crate.
[ -f "$repo_root/engine/Cargo.toml" ] || exit 0

# Need the toolchain. If cargo is absent, say so once (info) but don't block.
if ! command -v cargo >/dev/null 2>&1; then
  echo "engine-rust-check: cargo not installed; skipping fmt/clippy. Install the Rust toolchain to enable this gate." >&2
  exit 0
fi

cd "$repo_root/engine" || exit 0

problems=""

# rustfmt (check-only; never rewrites files from a hook).
if ! fmt_out="$(cargo fmt --all -- --check 2>&1)"; then
  problems+="rustfmt: formatting issues (run 'cargo fmt --all' in engine/):\n${fmt_out}\n\n"
fi

# clippy, warnings as errors per conventions.
if ! clippy_out="$(cargo clippy --all-targets --all-features -- -D warnings 2>&1)"; then
  problems+="clippy: lint findings (engineering-conventions.md requires clippy-clean):\n${clippy_out}\n"
fi

if [ -n "$problems" ]; then
  # exit 2 forwards STDERR to the LLM to fix; it does not undo the write.
  printf 'engine-rust-check found issues in %s:\n\n%b' "$path" "$problems" >&2
  exit 2
fi

exit 0
