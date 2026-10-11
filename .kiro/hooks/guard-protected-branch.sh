#!/usr/bin/env bash
# preToolUse guard: block `git push` to the protected main branch.
# Kiro delivers the hook event as JSON on STDIN (not env vars). We read it,
# extract the shell command, and if it is a push to main, exit 2 to BLOCK
# (STDERR is returned to the LLM). Any other command: exit 0 (allow).
#
# Fails OPEN on parse trouble (exit 0) so a malformed event never wedges the
# agent — the Kiro Crew policy + GitHub protection remain the real backstops.

set -euo pipefail

event="$(cat 2>/dev/null || true)"

# Pull the command string out of tool_input. Prefer python (always present);
# fall back to grep if python is missing.
cmd=""
if command -v python3 >/dev/null 2>&1; then
  cmd="$(printf '%s' "$event" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    ti = d.get("tool_input", {}) or {}
    # shell tool input shape: {"command": "..."} (and possibly nested)
    print(ti.get("command", "") or "")
except Exception:
    print("")
' 2>/dev/null || true)"
else
  cmd="$event"
fi

# Normalize whitespace for matching.
norm="$(printf '%s' "$cmd" | tr '\n\t' '  ')"

# Match a git push at all.
if printf '%s' "$norm" | grep -Eq 'git([[:space:]]+-[^[:space:]]+)*[[:space:]]+push([[:space:]]|$)'; then
  # Explicit protected target → BLOCK.
  if printf '%s' "$norm" | grep -Eq '(^|[[:space:]:])(main|master|mainline)([[:space:]]|$|:)'; then
    echo "BLOCKED by preToolUse guard: do not push to a protected branch (main/master/mainline). Push a feature branch and open a PR instead (see CONTRIBUTING.md §3)." >&2
    exit 2
  fi
  # Determine whether an EXPLICIT branch/refspec was named after 'push'.
  # Strip everything up to and including 'push', drop flags (-u, --tags, etc.)
  # and a leading remote name; if a ref token remains it is an explicit branch.
  rest="$(printf '%s' "$norm" | sed -E 's/.*push//')"
  # remove flags
  rest="$(printf '%s' "$rest" | sed -E 's/(^|[[:space:]])-{1,2}[^[:space:]]+//g')"
  # tokens left
  set -- $rest
  # $1 is typically the remote (origin); $2+ would be the refspec(s)
  if [ "$#" -ge 2 ]; then
    # An explicit non-protected ref was named (protected already handled above) → ALLOW.
    exit 0
  fi
  # Bare 'git push' / 'git push origin' with no explicit branch → ambiguous;
  # it may push the current branch to a protected upstream. Be conservative: BLOCK.
  echo "BLOCKED by preToolUse guard: ambiguous 'git push' with no explicit feature branch could target a protected branch. Name the branch explicitly, e.g. 'git push -u origin <feature-branch>' (see CONTRIBUTING.md §3)." >&2
  exit 2
fi

exit 0
