#!/usr/bin/env bash
# Build all native Go commands together so indexer helpers remain discoverable.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [[ $# -gt 1 ]]; then
  echo "Usage: bash scripts/build-local.sh [output-directory]" >&2
  exit 1
fi

OUTPUT_DIR="${1:-$REPO_ROOT/bin}"
command -v go >/dev/null 2>&1 || { echo "Go is required; see go.mod for the version." >&2; exit 1; }

# tree-sitter links native parsers; this is a native build, not cross-compilation.
export CGO_ENABLED=1
export GOOS="$(go env GOHOSTOS)"
export GOARCH="$(go env GOHOSTARCH)"
mkdir -p "$OUTPUT_DIR"
go build -trimpath -o "$OUTPUT_DIR/" ./cmd/...

printf 'Native commands built in %s\n' "$OUTPUT_DIR"
printf 'Keep tirion, parse, and extract-* binaries together. See SETUP.md for database and UI setup.\n'
