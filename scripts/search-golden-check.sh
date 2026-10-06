#!/bin/bash
set -euo pipefail

MANIFEST="${1:-${SEARCH_GOLDEN_MANIFEST:-}}"
SEARCH_API_URL="${SEARCH_API_URL:-http://localhost:8080/api/search}"
HTTP_CLIENT="${HTTP_CLIENT:-curl}"

if [ "$#" -gt 1 ] || [ -z "$MANIFEST" ]; then
	echo "Usage: scripts/search-golden-check.sh /path/to/queries.tsv" >&2
	echo "Or set SEARCH_GOLDEN_MANIFEST. The manifest must match your indexed estate." >&2
	exit 1
fi

if [ ! -f "$MANIFEST" ]; then
	echo "Manifest not found: $MANIFEST" >&2
	exit 1
fi

if ! command -v "$HTTP_CLIENT" >/dev/null 2>&1; then
	echo "$HTTP_CLIENT is required" >&2
	exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
	echo "jq is required" >&2
	exit 1
fi

# Token lookup order: TIRION_API_TOKEN, TIRION_API_TOKEN_FILE, then the local
# file written by `tirion serve` (TIRION_HOME, else ~/.tirion; ~/.codebase-intel
# on older installs). The local file is read only for a loopback API.
resolve_token() {
	if [ "${TIRION_API_TOKEN+set}" = set ]; then
		printf '%s' "$TIRION_API_TOKEN" | tr -d '[:space:]'
		return
	fi
	if [ -n "${TIRION_API_TOKEN_FILE:-}" ]; then
		case "$TIRION_API_TOKEN_FILE" in
		/*) ;;
		*)
			echo "TIRION_API_TOKEN_FILE must be absolute" >&2
			exit 1
			;;
		esac
		tr -d '[:space:]' <"$TIRION_API_TOKEN_FILE"
		return
	fi
	local host="${SEARCH_API_URL#*://}"
	host="${host%%/*}"
	# Userinfo (user@host) can disguise a remote host as loopback; never send the
	# local token to such a URL.
	case "$host" in
	*@*) return ;;
	esac
	case "$host" in
	"[::1]"*) host="[::1]" ;;
	*) host="${host%%:*}" ;;
	esac
	case "$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')" in
	localhost|127.0.0.1|"[::1]") ;;
	*) return ;;
	esac
	local candidates=()
	if [ -n "${TIRION_HOME:-}" ]; then
		candidates=("$TIRION_HOME/api-token")
	else
		candidates=("$HOME/.tirion/api-token" "$HOME/.codebase-intel/api-token")
	fi
	local candidate
	for candidate in "${candidates[@]}"; do
		if [ -f "$candidate" ]; then
			tr -d '[:space:]' <"$candidate"
			return
		fi
	done
}

# The token reaches the HTTP client through a private config file, never argv.
TOKEN="$(resolve_token)"
CURL_CONFIG=()
if [ -n "$TOKEN" ]; then
	if printf '%s' "$TOKEN" | grep -q '[^A-Za-z0-9._~+/=-]'; then
		echo "API token contains unsupported characters" >&2
		exit 1
	fi
	CONFIG_FILE="$(mktemp)"
	trap 'rm -f "$CONFIG_FILE"' EXIT
	printf 'header = "X-Tirion-Token: %s"\n' "$TOKEN" >"$CONFIG_FILE"
	CURL_CONFIG=(-K "$CONFIG_FILE")
else
	echo "warning: no API token found (set TIRION_API_TOKEN or TIRION_API_TOKEN_FILE, or start tirion serve); the API will answer 401" >&2
fi

bucket_field() {
	case "$1" in
	functions|classes)
		echo "name"
		;;
	endpoints)
		echo "path"
		;;
	schedules)
		echo "ruleName"
		;;
	*)
		return 1
		;;
	esac
}

total=0
passed=0
failed=0

while IFS=$'\t' read -r id mode query bucket target max_rank; do
	case "$id" in
	""|\#*)
		continue
		;;
	esac

	field="$(bucket_field "$bucket")"
	total=$((total + 1))

	response="$("$HTTP_CLIENT" -fsS ${CURL_CONFIG[@]+"${CURL_CONFIG[@]}"} --get "$SEARCH_API_URL" \
		--data-urlencode "q=$query" \
		--data-urlencode "mode=$mode" \
		--data-urlencode "limit=$max_rank")"

	rank="$(printf '%s' "$response" | jq -r --arg bucket "$bucket" --arg field "$field" --arg target "$target" '
		(.results[$bucket] // [])
		| [ .[] | .[$field] ]
		| to_entries[]
		| select(.value == $target)
		| (.key + 1)
	' | head -n 1)"

	if [ -n "$rank" ]; then
		echo "PASS $id [$bucket<=${max_rank}] $query -> $target (rank $rank)"
		passed=$((passed + 1))
		continue
	fi

	top="$(printf '%s' "$response" | jq -r --arg bucket "$bucket" --arg field "$field" '
		(.results[$bucket] // [])
		| .[0:5]
		| map(.[$field])
		| join(" | ")
	')"
	echo "FAIL $id [$bucket<=${max_rank}] $query -> $target"
	echo "     top ${bucket}: ${top:-<none>}"
	failed=$((failed + 1))
done < "$MANIFEST"

echo
echo "Summary: $passed/$total passed, $failed failed"

if [ "$failed" -ne 0 ]; then
	exit 1
fi
