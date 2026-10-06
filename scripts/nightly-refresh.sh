#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_ROOT="${INSTALL_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"

ROOT="${TIRION_REPOS_ROOT:-${REPOS_ROOT:-/data/tirion/repos}}"
TIRION_BIN="${TIRION_BIN:-$INSTALL_ROOT/tirion}"
PROTOCOL="${GIT_PROTOCOL:-https}"
BACKUP_ON_FAIL="${TIRION_BACKUP_ON_FAIL:-1}"
BACKUP_DIR="${TIRION_BACKUP_DIR:-/tmp}"
MAX_ATTEMPTS="${TIRION_REFRESH_MAX_ATTEMPTS:-3}"
STATUS_FILE="${TIRION_REFRESH_STATUS_FILE:-/var/log/tirion/refresh-status.env}"
STOP_API_DURING_REFRESH="${TIRION_REFRESH_STOP_API_DURING_REFRESH:-1}"
API_SERVICE_NAME="${TIRION_REFRESH_API_SERVICE_NAME:-tirion.service}"
SKIP_UNCHANGED="${TIRION_REFRESH_SKIP_UNCHANGED:-true}"
REFRESH_WORKSPACE="${TIRION_REFRESH_WORKSPACE:-default-main}"
# Space-separated organizations; unset means index existing repos only.
GITHUB_ORGS="${TIRION_GITHUB_ORGS:-}"

# Environment comes from systemd via EnvironmentFile= directives on
# tirion-refresh.service (loads /opt/tirion/.env and /etc/tirion/github.env
# as root before dropping privileges to the service user). Do NOT source
# these files from within the script: service-user permissions on the env
# files may deny reads after deploy.

: "${DATABASE_URL:?Set DATABASE_URL to the Tirion database before refreshing}"
DB_URL="$DATABASE_URL"

if [[ ! -x "$TIRION_BIN" ]]; then
  echo "tirion binary not found or not executable: $TIRION_BIN" >&2
  exit 1
fi

BACKUP_FILE=""
STARTED_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
LAST_SUCCESS_AT=""
API_WAS_STOPPED=0

cleanup_backup() {
  if [[ -n "$BACKUP_FILE" && -f "$BACKUP_FILE" ]]; then
    rm -f "$BACKUP_FILE"
  fi
}

api_service_exists() {
  command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files "$API_SERVICE_NAME" >/dev/null 2>&1
}

stop_api_service() {
  if [[ "${STOP_API_DURING_REFRESH:-1}" != "1" ]]; then
    return 0
  fi
  if ! api_service_exists; then
    return 0
  fi
  if systemctl is-active --quiet "$API_SERVICE_NAME"; then
    echo "Stopping $API_SERVICE_NAME to avoid DB deadlocks during refresh"
    if ! sudo -n systemctl stop "$API_SERVICE_NAME"; then
      echo "Non-interactive permission to stop $API_SERVICE_NAME is required during refresh" >&2
      exit 1
    fi
    API_WAS_STOPPED=1
  fi
}

start_api_service() {
  if [[ "$API_WAS_STOPPED" != "1" ]]; then
    return 0
  fi
  if api_service_exists; then
    echo "Starting $API_SERVICE_NAME after refresh"
    if ! sudo -n systemctl start "$API_SERVICE_NAME"; then
      echo "Failed to restart $API_SERVICE_NAME; restore the service before retrying refresh" >&2
      return 1
    fi
  fi
  API_WAS_STOPPED=0
}

sanitize_status_value() {
  printf '%s' "$1" | tr '\n' ' ' | sed 's/[[:cntrl:]]//g'
}

load_last_success_at() {
  if [[ -f "$STATUS_FILE" ]]; then
    LAST_SUCCESS_AT="$(grep -E '^LAST_SUCCESS_AT=' "$STATUS_FILE" | tail -n 1 | cut -d= -f2- || true)"
  fi
}

write_status() {
  local status="$1"
  local attempt="$2"
  local error_message="${3:-}"
  local updated_at
  updated_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  mkdir -p "$(dirname "$STATUS_FILE")"
  cat >"$STATUS_FILE" <<EOF
STATUS=$(sanitize_status_value "$status")
WORKSPACE=$(sanitize_status_value "$REFRESH_WORKSPACE")
ATTEMPT=$attempt
MAX_ATTEMPTS=$MAX_ATTEMPTS
STARTED_AT=$(sanitize_status_value "$STARTED_AT")
UPDATED_AT=$(sanitize_status_value "$updated_at")
LAST_SUCCESS_AT=$(sanitize_status_value "$LAST_SUCCESS_AT")
ERROR=$(sanitize_status_value "$error_message")
EOF
}

create_db_backup() {
  if [[ "${BACKUP_ON_FAIL:-1}" != "1" ]]; then
    return 0
  fi
  if ! command -v pg_dump >/dev/null 2>&1 || ! command -v psql >/dev/null 2>&1; then
    echo "pg_dump/psql not available; cannot protect refresh with DB rollback" >&2
    exit 1
  fi

  mkdir -p "${BACKUP_DIR:-/tmp}"
  BACKUP_FILE="$(mktemp "${BACKUP_DIR:-/tmp}/tirion-refresh-backup.XXXXXX.sql")"
  echo "Creating pre-refresh DB snapshot at $BACKUP_FILE"
  pg_dump \
    --dbname="$DB_URL" \
    --clean \
    --if-exists \
    --no-owner \
    --no-privileges \
    --format=plain \
    --file="$BACKUP_FILE"
}

restore_db_backup() {
  if [[ -z "$BACKUP_FILE" || ! -f "$BACKUP_FILE" ]]; then
    echo "No DB snapshot available to restore" >&2
    return 1
  fi

  echo "Refresh failed. Restoring previous DB snapshot from $BACKUP_FILE" >&2
  psql "$DB_URL" -v ON_ERROR_STOP=1 -f "$BACKUP_FILE"
}

trap 'start_api_service; cleanup_backup' EXIT

load_last_success_at

if [[ "$REFRESH_WORKSPACE" == "default-main" ]]; then
  if [[ -n "$GITHUB_ORGS" ]]; then
    read -r -a orgs <<< "$GITHUB_ORGS"
    for org in "${orgs[@]}"; do
      "$SCRIPT_DIR/clone-repos.sh" --org "$org" --root "$ROOT" --protocol "$PROTOCOL" --fetch-existing-branches
    done
  fi
fi

create_db_backup
stop_api_service

run_step() {
  local step_name="$1"
  shift
  local capture_file
  capture_file="$(mktemp "${BACKUP_DIR:-/tmp}/tirion-refresh-step.XXXXXX.log")"
  if "$@" > >(tee "$capture_file") 2> >(tee -a "$capture_file" >&2); then
    rm -f "$capture_file"
    return 0
  fi
  local last_line
  last_line="$(grep -v '^[[:space:]]*$' "$capture_file" | tail -n 1 || true)"
  rm -f "$capture_file"
  if [[ -n "$last_line" ]]; then
    printf '%s failed: %s' "$step_name" "$last_line"
  else
    printf '%s failed' "$step_name"
  fi
  return 1
}

attempt=1
while (( attempt <= MAX_ATTEMPTS )); do
  write_status "running" "$attempt" ""

  if [[ "$REFRESH_WORKSPACE" == "default-main" ]]; then
    if ! sync_error="$(run_step "sync-mainline" "$TIRION_BIN" sync-branches -db "$DB_URL" --mainline "$ROOT")"; then
      restore_db_backup || true
      if (( attempt == MAX_ATTEMPTS )); then
        write_status "failed" "$attempt" "$sync_error"
        printf '%s\n' "$sync_error" >&2
        exit 1
      fi
      printf '%s\nRetrying refresh (%d/%d)...\n' "$sync_error" "$((attempt + 1))" "$MAX_ATTEMPTS" >&2
      attempt=$((attempt + 1))
      continue
    fi
  fi

  if [[ "$REFRESH_WORKSPACE" == "default-main" ]]; then
    index_cmd=("$TIRION_BIN" index -db "$DB_URL" -workspace "$REFRESH_WORKSPACE" -skip-unchanged="$SKIP_UNCHANGED" "$ROOT")
  else
    index_cmd=("$TIRION_BIN" workspace index -db "$DB_URL" -skip-unchanged="$SKIP_UNCHANGED" "$REFRESH_WORKSPACE")
  fi

  if ! index_error="$(run_step "index" "${index_cmd[@]}")"; then
    restore_db_backup || true
    if (( attempt == MAX_ATTEMPTS )); then
      write_status "failed" "$attempt" "$index_error"
      printf '%s\n' "$index_error" >&2
      exit 1
    fi
    printf '%s\nRetrying refresh (%d/%d)...\n' "$index_error" "$((attempt + 1))" "$MAX_ATTEMPTS" >&2
    attempt=$((attempt + 1))
    continue
  fi

  LAST_SUCCESS_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  write_status "ok" "$attempt" ""
  start_api_service
  exit 0
done
