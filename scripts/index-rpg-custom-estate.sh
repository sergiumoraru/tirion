#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DB_URL="${1:-${DATABASE_URL:-}}"
: "${DB_URL:?Pass a database URL or set DATABASE_URL}"
PARSE_BIN="${PARSE_BIN:-$ROOT_DIR/bin/parse}"
if [[ ! -x "$PARSE_BIN" ]]; then
  echo "Missing parser: $PARSE_BIN. Run bash scripts/build-local.sh first." >&2
  exit 1
fi

repos=(
  "$ROOT_DIR/testdata/rpg-estate/cbi-rpg-shared-services"
  "$ROOT_DIR/testdata/rpg-estate/cbi-rpg-ar-ledger"
  "$ROOT_DIR/testdata/rpg-estate/cbi-rpg-billing-core"
  "$ROOT_DIR/testdata/rpg-estate/cbi-rpg-order-entry"
)

for repo in "${repos[@]}"; do
  "$PARSE_BIN" -db "$DB_URL" "$repo"
done
