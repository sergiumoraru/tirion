#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PUBLIC_ROOT="${2:-${RPG_PUBLIC_ROOT:-}}"
DB_URL="${1:-${DATABASE_URL:-}}"
: "${DB_URL:?Pass a database URL or set DATABASE_URL}"
PARSE_BIN="${PARSE_BIN:-$ROOT_DIR/bin/parse}"
if [[ -z "$PUBLIC_ROOT" ]]; then
  echo "Set RPG_PUBLIC_ROOT or supply the corpus directory as the second argument." >&2
  exit 1
fi
if [[ ! -x "$PARSE_BIN" ]]; then
  echo "Missing parser: $PARSE_BIN. Run bash scripts/build-local.sh first." >&2
  exit 1
fi

repos=(
  "$PUBLIC_ROOT/IBM-i-RPG-Free-CLP-Code"
  "$PUBLIC_ROOT/QshOni"
  "$PUBLIC_ROOT/httpapi"
  "$PUBLIC_ROOT/optionsForConsuming"
  "$PUBLIC_ROOT/ftpapi"
)

for repo in "${repos[@]}"; do
  if [[ ! -d "$repo" ]]; then
    echo "missing corpus repo: $repo" >&2
    exit 1
  fi
done

for repo in "${repos[@]}"; do
  "$PARSE_BIN" -db "$DB_URL" "$repo"
done
