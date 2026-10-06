#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DB_URL="${1:-${DATABASE_URL:-}}"
: "${DB_URL:?Pass a database URL or set DATABASE_URL (use a disposable database)}"
PUBLIC_ROOT="${2:-${RPG_PUBLIC_ROOT:-}}"
export PARSE_BIN="${PARSE_BIN:-$ROOT_DIR/bin/parse}"
if [[ -z "$PUBLIC_ROOT" ]]; then
  echo "Set RPG_PUBLIC_ROOT or supply the corpus directory as the second argument." >&2
  exit 1
fi

mkdir -p "$(dirname "$PARSE_BIN")"
go build -o "$PARSE_BIN" "$ROOT_DIR/cmd/parse"

"$ROOT_DIR/scripts/index-rpg-public-corpus.sh" "$DB_URL" "$PUBLIC_ROOT"
"$ROOT_DIR/scripts/index-rpg-custom-estate.sh" "$DB_URL"
"$ROOT_DIR/scripts/index-rpg-member-export.sh" "$DB_URL"

psql "$DB_URL" -v ON_ERROR_STOP=1 <<'SQL'
\echo 'rpg_hardening_stats'
SELECT
  (SELECT COUNT(*) FROM repositories) AS repos,
  (SELECT COUNT(*) FROM files) AS files,
  (SELECT COUNT(*) FROM functions) AS functions,
  (SELECT COUNT(*) FROM function_calls) AS function_calls,
  (SELECT COUNT(*) FROM ibmi_exports) AS ibmi_exports,
  (SELECT COUNT(*) FROM ibmi_bindings) AS ibmi_bindings;

\echo 'qshonisrv_no_main'
SELECT fi.path,
       COUNT(*) FILTER (WHERE f.name = '_MAIN') AS synthetic_main_count
FROM files fi
JOIN repositories r ON r.id = fi.repo_id
LEFT JOIN functions f ON f.file_id = fi.id
WHERE r.name = 'QshOni'
  AND fi.path IN ('QSHONISRV.RPGLE', 'QSHONISRVH.RPGLE')
GROUP BY fi.path
ORDER BY fi.path;

\echo 'httpapi_external_symbols'
SELECT fc.callee_name,
       COUNT(*) AS total_calls,
       COUNT(*) FILTER (WHERE fc.callee_function_id IS NULL) AS unresolved_calls
FROM function_calls fc
JOIN functions cf ON cf.id = fc.caller_function_id
JOIN files fi ON fi.id = cf.file_id
JOIN repositories r ON r.id = fi.repo_id
WHERE r.name = 'httpapi'
  AND fi.path ILIKE '%EXAMPLE%'
  AND fc.callee_name ~ '^[A-Z0-9_]+$'
GROUP BY fc.callee_name
ORDER BY total_calls DESC, fc.callee_name
LIMIT 10;

\echo 'synthetic_estate_cross_repo'
SELECT COUNT(*) AS cross_repo_calls
FROM trace_call_edges
WHERE caller_id LIKE 'cbi-rpg-%:%'
  AND callee_id LIKE 'cbi-rpg-%:%'
  AND split_part(caller_id, ':', 1) <> split_part(callee_id, ':', 1);

\echo 'synthetic_copy_alias_resolution'
SELECT split_part(te.caller_id, ':', 1) AS caller_repo,
       te.callee_name,
       te.callee_repo,
       te.callee_func,
       te.callee_resolution_source
FROM trace_call_edges te
WHERE te.caller_id IN (
  'cbi-rpg-order-entry:src/CBIORD100.RPGLE:SubmitOrder',
  'cbi-rpg-billing-core:src/CBIBILLP.RPGLE:CBIBILLP'
)
ORDER BY caller_repo, te.callee_name;

\echo 'member_export_program_chain'
SELECT te.caller_id, te.callee_name, te.callee_id, te.callee_resolution_source
FROM trace_call_edges te
WHERE te.caller_id LIKE 'cbi-ibmi-member-export:%'
ORDER BY te.caller_id, te.callee_name;
SQL
