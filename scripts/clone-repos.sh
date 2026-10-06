#!/usr/bin/env bash
# Clone repositories for a fresh indexing machine.
#
# Default behavior:
# - clones only organizations explicitly supplied with --org
# - clones into ./repos unless --root is provided
# - skips repos that already exist locally
# - fetches all remote branches for newly cloned repos
# - can optionally fetch all remote branches for repos that already exist locally
# - keeps going on per-repo clone/fetch failures and summarizes them at the end
# - retries failed clone/fetch operations once at the end by default
#
# Auth for private orgs:
# - preferred: existing `gh` login (script uses `gh auth token`)
# - fallback: `GH_TOKEN` / `GITHUB_TOKEN` for GitHub API listing and HTTPS clone auth
#
# Examples:
#   ./scripts/clone-repos.sh --org your-org --root /data/tirion/repos
#   ./scripts/clone-repos.sh --org your-org --root /data/tirion/repos --skip-archived --dry-run
#   ./scripts/clone-repos.sh --org your-org --root /data/tirion/repos --fetch-existing-branches
#
# Backward-compatible usage:
#   ./scripts/clone-repos.sh your-org /path/to/repos

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/clone-repos.sh --org ORG [--org ORG ...] [--root DIR] [options]
  ./scripts/clone-repos.sh <org> <target_dir>

Options:
  --root DIR          Target directory for cloned repos. Default: ./repos
  --org ORG           GitHub org to clone. Repeat to clone multiple orgs.
                      Required; no organization is selected by default.
  --protocol MODE     Clone protocol: auto, ssh, or https. Default: auto
                      auto uses HTTPS. Use ssh only if you explicitly need it.
  --depth N           Clone depth for all remote branches. Default: 1
  --full-history      Clone full history instead of a shallow clone
  --fetch-existing-branches
                      For repos that already exist locally, fetch all remote
                      branches instead of only skipping them
  --retry-failures N  Number of end-of-run retry passes for failed clone/fetch
                      operations. Default: 1
  --skip-archived     Do not clone archived repos
  --skip-forks        Do not clone fork repos
  --dry-run           Show what would be cloned without cloning
  -h, --help          Show this help

Auth:
  Preferred:
    gh auth login  (script uses `gh auth token` automatically)

  Fallback:
    export GH_TOKEN=...  # or export GITHUB_TOKEN=...
    # used for both GitHub API listing and HTTPS clone auth

Windows:
  Run this script from Git Bash.

Examples:
  ./scripts/clone-repos.sh --org your-org --root /data/tirion/repos
  ./scripts/clone-repos.sh --org your-org --root /data/tirion/repos --fetch-existing-branches
  ./scripts/clone-repos.sh your-org /data/tirion/repos
EOF
}

die() {
  echo "Error: $*" >&2
  exit 1
}

normalize_remote() {
  local url="$1"
  url="${url%.git}"
  url="${url#git@github.com:}"
  url="${url#https://github.com/}"
  url="${url#http://github.com/}"
  echo "$url"
}

ROOT=""
PROTOCOL="${GIT_PROTOCOL:-auto}"
DEPTH="1"
DRY_RUN=0
FETCH_EXISTING_BRANCHES=0
RETRY_FAILURES="${CLONE_RETRY_FAILURES:-1}"
SKIP_ARCHIVED=0
SKIP_FORKS=0
AUTH_TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
declare -a ORGS=()
declare -a FAILED_RECORDS=()

legacy_org=""
legacy_root=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      [[ $# -ge 2 ]] || die "--root requires a value"
      ROOT="$2"
      shift 2
      ;;
    --org)
      [[ $# -ge 2 ]] || die "--org requires a value"
      ORGS+=("$2")
      shift 2
      ;;
    --protocol)
      [[ $# -ge 2 ]] || die "--protocol requires a value"
      PROTOCOL="$2"
      shift 2
      ;;
    --depth)
      [[ $# -ge 2 ]] || die "--depth requires a value"
      DEPTH="$2"
      shift 2
      ;;
    --full-history)
      DEPTH=""
      shift
      ;;
    --fetch-existing-branches)
      FETCH_EXISTING_BRANCHES=1
      shift
      ;;
    --retry-failures)
      [[ $# -ge 2 ]] || die "--retry-failures requires a value"
      RETRY_FAILURES="$2"
      shift 2
      ;;
    --skip-archived)
      SKIP_ARCHIVED=1
      shift
      ;;
    --skip-forks)
      SKIP_FORKS=1
      shift
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      die "unknown option: $1"
      ;;
    *)
      if [[ -z "$legacy_org" ]]; then
        legacy_org="$1"
      elif [[ -z "$legacy_root" ]]; then
        legacy_root="$1"
      else
        die "unexpected argument: $1"
      fi
      shift
      ;;
  esac
done

if [[ -n "$legacy_org" ]]; then
  ORGS=("$legacy_org")
fi

if [[ -n "$legacy_root" && -z "$ROOT" ]]; then
  ROOT="$legacy_root"
fi

if [[ "${#ORGS[@]}" -eq 0 ]]; then
  die "at least one --org is required"
fi

if [[ -z "$ROOT" ]]; then
  ROOT="$(pwd)/repos"
fi

case "$PROTOCOL" in
  auto|ssh|https) ;;
  *)
    die "--protocol must be auto, ssh, or https"
    ;;
esac

[[ "$RETRY_FAILURES" =~ ^[0-9]+$ ]] || die "--retry-failures must be a non-negative integer"

mkdir -p "$ROOT"
ROOT="$(cd "$ROOT" && pwd)"

have_gh=0
if command -v gh >/dev/null 2>&1; then
  if gh auth status --hostname github.com >/dev/null 2>&1; then
    have_gh=1
    if [[ -z "$AUTH_TOKEN" ]]; then
      AUTH_TOKEN="$(gh auth token --hostname github.com 2>/dev/null || true)"
    fi
  fi
fi

if [[ "$have_gh" -eq 0 && -z "${AUTH_TOKEN:-}" ]]; then
  die "no GitHub auth available. Run 'gh auth login' or export GH_TOKEN / GITHUB_TOKEN."
fi

if [[ "$have_gh" -eq 0 ]] && ! command -v jq >/dev/null 2>&1; then
  die "jq is required when using token fallback."
fi

if [[ "$PROTOCOL" != "ssh" ]]; then
  export GIT_TERMINAL_PROMPT=0
fi

list_org_repos() {
  local org="$1"

  if [[ "$have_gh" -eq 1 ]]; then
    GH_TOKEN="$AUTH_TOKEN" gh repo list "$org" \
      --limit 1000 \
      --json name,sshUrl,url,isArchived,isFork \
      --jq '.[] | [.name, .sshUrl, (.url + ".git"), (.isArchived | tostring), (.isFork | tostring)] | @tsv'
    return
  fi

  local page=1
  local response
  local count

  while true; do
    response="$(printf '%s\n' "Authorization: Bearer ${AUTH_TOKEN}" | curl -fsS \
      -H "Accept: application/vnd.github+json" \
      --header @- \
      "https://api.github.com/orgs/${org}/repos?type=all&per_page=100&page=${page}")" || return 1

    count="$(printf '%s' "$response" | jq 'length')" || return 1
    if [[ "$count" -eq 0 ]]; then
      break
    fi

    printf '%s' "$response" | jq -r '.[] | [.name, .ssh_url, .clone_url, (.archived | tostring), (.fork | tostring)] | @tsv' || return 1
    page=$((page + 1))
  done
}

echo "Target root: $ROOT"
echo "Orgs: ${ORGS[*]}"
echo "Protocol: $PROTOCOL"
echo "Retry passes: $RETRY_FAILURES"
if [[ -n "$DEPTH" ]]; then
  echo "Depth: $DEPTH"
else
  echo "Depth: full history"
fi
if [[ "$FETCH_EXISTING_BRANCHES" -eq 1 ]]; then
  echo "Existing repos: fetch all remote branches"
else
  echo "Existing repos: skip"
fi
echo

total_seen=0
total_cloned=0
total_skipped=0
total_fetched=0
total_filtered=0
total_conflicts=0
total_failed=0

record_failure() {
  local org="$1"
  local repo_name="$2"
  local mode="$3"
  FAILED_RECORDS+=("$org"$'\t'"$repo_name"$'\t'"$mode")
}

git_clone_with_args() {
  local clone_url="$1"
  local repo_dir="$2"

  if [[ -n "$DEPTH" ]]; then
    git_authenticated clone --depth "$DEPTH" --no-single-branch "$clone_url" "$repo_dir"
  else
    git_authenticated clone "$clone_url" "$repo_dir"
  fi
}

git_authenticated() {
  if [[ -z "$AUTH_TOKEN" ]]; then
    git "$@"
    return
  fi
  # Command-local helper: never write the token to argv, remotes, or a keychain.
  # Only answer HTTPS GitHub requests; other remotes must use their own auth.
  TIRION_GIT_TOKEN="$AUTH_TOKEN" git -c credential.helper= -c 'credential.helper=!f() {
    test "$1" = get || exit 0
    protocol= host=
    while IFS="=" read -r key value; do
      case "$key" in
        protocol) protocol="$value" ;;
        host) host="$value" ;;
      esac
    done
    if test "$protocol" = https && test "$host" = github.com; then
      printf "%s\n" "username=x-access-token" "password=$TIRION_GIT_TOKEN"
    fi
  }; f' "$@"
}

clone_repo() {
  local org="$1"
  local repo_name="$2"
  local repo_dir="$3"
  local ssh_url="$4"
  local https_url="$5"

  local attempt label clone_url
  local -a attempts=()

  case "$PROTOCOL" in
    ssh)
      attempts=("ssh")
      ;;
    https)
      attempts=("https")
      ;;
    auto)
      attempts=("https")
      ;;
  esac

  for attempt in "${attempts[@]}"; do
    if [[ -e "$repo_dir" || -L "$repo_dir" ]]; then
      echo "  Clone destination already exists; left untouched: $repo_dir" >&2
      return 1
    fi

    if [[ "$attempt" == "https" ]]; then
      label="https"
      clone_url="$https_url"
      echo "  Attempt: $label"
      if git_clone_with_args "$clone_url" "$repo_dir"; then
        return 0
      fi
    else
      label="ssh"
      clone_url="$ssh_url"
      echo "  Attempt: $label"
      if GIT_SSH_COMMAND="${GIT_SSH_COMMAND:-ssh -o ConnectTimeout=10 -o ConnectionAttempts=1}" git_clone_with_args "$clone_url" "$repo_dir"; then
        return 0
      fi
    fi

    echo "  Attempt failed: $label"
  done

  return 1
}

fetch_repo_branches() {
  local repo_dir="$1"
  local remote_url="${2:-}"
  if [[ -n "$remote_url" ]]; then
    git -C "$repo_dir" remote set-url origin "$remote_url"
  fi
  git_authenticated -C "$repo_dir" fetch origin --prune '+refs/heads/*:refs/remotes/origin/*'
}

retry_failed_repos() {
  local round="$1"
  local current_failures=("${FAILED_RECORDS[@]}")
  local org repo_name mode repo_dir ssh_url https_url
  local retried_failed=()

  [[ "${#current_failures[@]}" -gt 0 ]] || return 0

  echo "=== Retry pass $round ==="
  FAILED_RECORDS=()

  for record in "${current_failures[@]}"; do
    IFS=$'\t' read -r org repo_name mode <<<"$record"
    repo_dir="$ROOT/$repo_name"
    ssh_url="git@github.com:${org}/${repo_name}.git"
    https_url="https://github.com/${org}/${repo_name}.git"

    if [[ "$mode" == "fetch" ]]; then
      echo "RETRY FETCH: $org/$repo_name"
      local retry_remote="$https_url"
      [[ "$PROTOCOL" != ssh ]] || retry_remote="$ssh_url"
      if fetch_repo_branches "$repo_dir" "$retry_remote"; then
        total_fetched=$((total_fetched + 1))
        total_failed=$((total_failed - 1))
      else
        echo "RETRY FAIL: $org/$repo_name (fetch)"
        retried_failed+=("$record")
      fi
      continue
    fi

    echo "RETRY CLONE: $org/$repo_name"
    if clone_repo "$org" "$repo_name" "$repo_dir" "$ssh_url" "$https_url"; then
      total_cloned=$((total_cloned + 1))
      total_failed=$((total_failed - 1))
    else
      echo "RETRY FAIL: $org/$repo_name (clone)"
      retried_failed+=("$record")
    fi
  done

  FAILED_RECORDS=("${retried_failed[@]}")
}

for org in "${ORGS[@]}"; do
  echo "=== $org ==="
  repo_rows="$(list_org_repos "$org")" || die "could not list repositories for $org"

  org_seen=0
  org_cloned=0
  org_skipped=0
  org_fetched=0
  org_filtered=0
  org_conflicts=0
  org_failed=0

  while IFS=$'\t' read -r repo_name ssh_url https_url archived forked; do
    [[ -n "$repo_name" ]] || continue

    org_seen=$((org_seen + 1))
    total_seen=$((total_seen + 1))

    if [[ "$SKIP_ARCHIVED" -eq 1 && "$archived" == "true" ]]; then
      echo "FILTER: $org/$repo_name (archived)"
      org_filtered=$((org_filtered + 1))
      total_filtered=$((total_filtered + 1))
      continue
    fi

    if [[ "$SKIP_FORKS" -eq 1 && "$forked" == "true" ]]; then
      echo "FILTER: $org/$repo_name (fork)"
      org_filtered=$((org_filtered + 1))
      total_filtered=$((total_filtered + 1))
      continue
    fi

    repo_dir="$ROOT/$repo_name"
    desired_remote="${org}/${repo_name}"
    fetch_remote="$ssh_url"
    if [[ "$PROTOCOL" != "ssh" ]]; then
      fetch_remote="$https_url"
    fi
    if [[ -e "$repo_dir" || -L "$repo_dir" ]]; then
      existing_remote="$(git -C "$repo_dir" config --get remote.origin.url 2>/dev/null || true)"
      if [[ -L "$repo_dir" || ! -d "$repo_dir/.git" || -z "$existing_remote" || "$(normalize_remote "$existing_remote")" != "$desired_remote" ]]; then
        echo "CONFLICT: $repo_name exists but is not a dedicated clone with origin $desired_remote; left untouched"
        org_conflicts=$((org_conflicts + 1))
        total_conflicts=$((total_conflicts + 1))
      elif [[ "$FETCH_EXISTING_BRANCHES" -eq 1 ]]; then
        if [[ "$DRY_RUN" -eq 1 ]]; then
          echo "FETCH: $org/$repo_name (all remote branches)"
        else
          echo "FETCH: $org/$repo_name (all remote branches)"
          if ! fetch_repo_branches "$repo_dir" "$fetch_remote"; then
            echo "FAIL: $org/$repo_name (fetch)"
            record_failure "$org" "$repo_name" "fetch"
            org_failed=$((org_failed + 1))
            total_failed=$((total_failed + 1))
            continue
          fi
        fi
        org_fetched=$((org_fetched + 1))
        total_fetched=$((total_fetched + 1))
      else
        echo "SKIP: $org/$repo_name (exists)"
        org_skipped=$((org_skipped + 1))
        total_skipped=$((total_skipped + 1))
      fi
      continue
    fi

    if [[ "$DRY_RUN" -eq 1 ]]; then
      echo "CLONE: $org/$repo_name -> $repo_dir"
      org_cloned=$((org_cloned + 1))
      total_cloned=$((total_cloned + 1))
      continue
    fi

    echo "CLONE: $org/$repo_name"
    if clone_repo "$org" "$repo_name" "$repo_dir" "$ssh_url" "$https_url"; then
      org_cloned=$((org_cloned + 1))
      total_cloned=$((total_cloned + 1))
    else
      echo "FAIL: $org/$repo_name"
      record_failure "$org" "$repo_name" "clone"
      org_failed=$((org_failed + 1))
      total_failed=$((total_failed + 1))
    fi
  done <<< "$repo_rows"

  echo "Summary for $org: seen=$org_seen cloned=$org_cloned fetched=$org_fetched skipped=$org_skipped filtered=$org_filtered conflicts=$org_conflicts failed=$org_failed"
  echo
done

echo "=== Done ==="
echo "Seen:      $total_seen"
echo "Cloned:    $total_cloned"
echo "Fetched:   $total_fetched"
echo "Skipped:   $total_skipped"
echo "Filtered:  $total_filtered"
echo "Conflicts: $total_conflicts"
echo "Failed:    $total_failed"

if [[ "$DRY_RUN" -eq 0 && "$RETRY_FAILURES" -gt 0 && "${#FAILED_RECORDS[@]}" -gt 0 ]]; then
  echo
  for round in $(seq 1 "$RETRY_FAILURES"); do
    retry_failed_repos "$round"
    [[ "${#FAILED_RECORDS[@]}" -eq 0 ]] && break
  done

  echo
  echo "=== After retries ==="
  echo "Cloned:    $total_cloned"
  echo "Fetched:   $total_fetched"
  echo "Failed:    $total_failed"
fi

if [[ "${#FAILED_RECORDS[@]}" -gt 0 ]]; then
  echo
  echo "Failures:"
  for record in "${FAILED_RECORDS[@]}"; do
    IFS=$'\t' read -r org repo_name mode <<<"$record"
    echo "  - $org/$repo_name [$mode]"
  done
  exit 1
fi

if [[ "$total_conflicts" -gt 0 ]]; then
  echo "Resolve the existing-path conflicts before retrying; no conflicting path was changed." >&2
  exit 1
fi
