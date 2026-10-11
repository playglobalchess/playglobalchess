#!/usr/bin/env bash
# postToolUse hook: after a write to a Go service, run gofmt + go vet as a
# fast quality gate (engineering-conventions.md: gofmt/vet/golangci-lint).
#
# Same contract as engine-rust-check.sh: reads the Kiro JSON event on STDIN,
# acts only when the written path is a *.go file under services/, that
# service has a go.mod, and the go toolchain is installed. Otherwise a silent
# no-op. FAILS OPEN on any infra/parse trouble; reports real findings via
# exit 2 (STDERR → LLM) without undoing the write.
#
# Uses golangci-lint when available (preferred by conventions), else falls
# back to gofmt -l + go vet.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
event="$(cat 2>/dev/null || true)"

path=""
if command -v python3 >/dev/null 2>&1; then
  path="$(printf '%s' "$event" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    ti = d.get("tool_input", {}) or {}
    print(ti.get("path") or ti.get("file") or ti.get("filePath") or "")
except Exception:
    print("")
' 2>/dev/null || true)"
fi

[ -n "$path" ] || exit 0

# Only *.go files under services/.
case "$path" in
  *services/*.go) : ;;
  *) exit 0 ;;
esac

# Resolve the service dir = the nearest ancestor of the file that has go.mod.
abs="$path"
case "$abs" in
  /*) : ;;
  *) abs="$repo_root/$path" ;;
esac
dir="$(dirname "$abs")"
mod_dir=""
while [ "$dir" != "/" ] && [ "$dir" != "$repo_root" ]; do
  if [ -f "$dir/go.mod" ]; then mod_dir="$dir"; break; fi
  dir="$(dirname "$dir")"
done
[ -n "$mod_dir" ] || exit 0

if ! command -v go >/dev/null 2>&1; then
  echo "services-go-check: go not installed; skipping gofmt/vet. Install the Go toolchain to enable this gate." >&2
  exit 0
fi

cd "$mod_dir" || exit 0

problems=""

if command -v golangci-lint >/dev/null 2>&1; then
  if ! lint_out="$(golangci-lint run ./... 2>&1)"; then
    problems+="golangci-lint findings:\n${lint_out}\n"
  fi
else
  # gofmt -l lists files that need formatting (empty = clean).
  if fmt_list="$(gofmt -l . 2>/dev/null)" && [ -n "$fmt_list" ]; then
    problems+="gofmt: these files need formatting (run 'gofmt -w .'):\n${fmt_list}\n\n"
  fi
  if ! vet_out="$(go vet ./... 2>&1)"; then
    problems+="go vet findings:\n${vet_out}\n"
  fi
fi

if [ -n "$problems" ]; then
  printf 'services-go-check found issues in %s:\n\n%b' "$path" "$problems" >&2
  exit 2
fi

exit 0
