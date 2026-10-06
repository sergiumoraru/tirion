#!/bin/bash

set -euo pipefail

VERSION="${1:-vdev}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_VERSION="$(awk '$1 == "go" { print $2; exit }' "$REPO_ROOT/go.mod")"
IMAGE="${DOCKER_IMAGE:-golang:$GO_VERSION}"
PLATFORM="${DOCKER_PLATFORM:-linux/amd64}"
DOCKER_CONTEXT_NAME="${DOCKER_CONTEXT_NAME:-}"

if ! command -v docker >/dev/null 2>&1; then
    echo "docker is required but not installed" >&2
    exit 1
fi

docker_cmd=(docker)
if [[ -n "$DOCKER_CONTEXT_NAME" ]]; then
    docker_cmd+=(--context "$DOCKER_CONTEXT_NAME")
fi

if ! "${docker_cmd[@]}" info >/dev/null 2>&1; then
    echo "Docker is not available in the selected context. Start your Docker engine or select a running context." >&2
    exit 1
fi

"${docker_cmd[@]}" run --rm \
    --platform "$PLATFORM" \
    -v "$REPO_ROOT:/app" \
    -w /app \
    "$IMAGE" \
    bash -lc '
        set -euo pipefail
        export PATH="/usr/local/go/bin:$PATH"
        apt-get update
        apt-get install -y gcc g++ zip
        export GOTOOLCHAIN=local
        bash scripts/build-dist.sh "$1"
    ' bash "$VERSION"
