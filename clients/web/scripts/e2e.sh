#!/usr/bin/env bash
# Build the Go e2e-server binary (real stack, in-memory pool) and run the web
# client's real-server end-to-end test against it. Keeps the e2e out of the fast
# unit suite — this is the only path that boots a real server + real socket.
#
# Usage: npm run e2e   (from clients/web)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WEB_DIR="$(cd "$HERE/.." && pwd)"
GS_DIR="$(cd "$WEB_DIR/../../services/game-server" && pwd)"
BIN="$GS_DIR/bin/e2e-server"

echo "==> staging cgo staticlibs (make lib)"
make -C "$GS_DIR" lib

echo "==> building e2e-server -> $BIN"
mkdir -p "$GS_DIR/bin"
( cd "$GS_DIR" && go build -o "$BIN" ./cmd/e2e-server )

echo "==> running web real-server e2e"
cd "$WEB_DIR"
PGC_E2E_SERVER="$BIN" npx vitest run --config vitest.e2e.config.ts
