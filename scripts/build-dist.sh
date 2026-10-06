#!/usr/bin/env bash
# Package the native platform. Use native CI runners or the Linux Docker wrapper.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
VERSION="${1:-dev}"
if [[ $# -gt 1 || ! "$VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]]; then
    echo 'Usage: bash scripts/build-dist.sh [version-without-path-separators]' >&2
    exit 1
fi

DIST_DIR="$REPO_ROOT/dist"

SERVER_COMMANDS=(tirion parse extract-http extract-sqs extract-java-intel extract-java-calls extract-spring extract-ts-intel extract-eventbridge)
MCP_COMMANDS=(mcp-intel mcp-guard)
TOOLS_COMMANDS=(impact-report contracts-report audit-index)
COMMANDS=("${SERVER_COMMANDS[@]}" "${MCP_COMMANDS[@]}" "${TOOLS_COMMANDS[@]}")

export GOOS="$(go env GOHOSTOS)"
export GOARCH="$(go env GOHOSTARCH)"
export CGO_ENABLED=1
PLATFORM="$GOOS-$GOARCH"
EXT=""
[[ "$GOOS" != windows ]] || EXT='.exe'
MODULE="$(go list -m)"
LDFLAGS="-s -w -X $MODULE/internal/buildinfo.Version=$VERSION"

# dist contains generated output only. Never silently publish a partial platform.
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR/$PLATFORM"
for cmd in "${COMMANDS[@]}"; do
    echo "Building $cmd for $PLATFORM"
    go build -trimpath -ldflags="$LDFLAGS" -o "$DIST_DIR/$PLATFORM/$cmd$EXT" "./cmd/$cmd"
done

copy_docs() {
    local destination="$1"
    # Package only the public manifest, never untracked local documents.
    local document
    while IFS= read -r document; do
        [[ -n "$document" ]] || continue
        mkdir -p "$destination/$(dirname "$document")"
        cp "$document" "$destination/$document"
    done < scripts/release-documents.txt
    cp LICENSE THIRD_PARTY_NOTICES.md "$destination/"
    local notice
    for notice in NOTICE; do
        if [[ -f "$notice" ]]; then
            cp "$notice" "$destination/"
        fi
    done
}

for bundle in server mcp tools; do
    stage="$DIST_DIR/_stage/$bundle"
    mkdir -p "$stage"
    case "$bundle" in
        server)
            selected=("${SERVER_COMMANDS[@]}")
            mkdir -p "$stage/bin"
            cp scripts/nightly-refresh.sh scripts/clone-repos.sh "$stage/bin/"
            chmod +x "$stage/bin/"*.sh
            mkdir -p "$stage/scripts"
            cp scripts/tirion.service scripts/tirion-refresh.service "$stage/scripts/"
            cp .env.example "$stage/"
            ;;
        mcp) selected=("${MCP_COMMANDS[@]}") ;;
        tools) selected=("${TOOLS_COMMANDS[@]}") ;;
    esac
    for cmd in "${selected[@]}"; do
        cp "$DIST_DIR/$PLATFORM/$cmd$EXT" "$stage/"
    done
    copy_docs "$stage"
    archive="$DIST_DIR/tirion-$bundle-$VERSION-$PLATFORM"
    if [[ "$GOOS" == windows ]]; then
        # Recursive zip includes the server's helper scripts and bundled docs.
        if command -v zip >/dev/null 2>&1; then
            (cd "$stage" && zip -qr "$archive.zip" .)
        else
            STAGE_PATH="$(cygpath -w "$stage")" ARCHIVE_PATH="$(cygpath -w "$archive.zip")" \
                powershell.exe -NoProfile -Command 'Compress-Archive -Path (Join-Path $env:STAGE_PATH "*") -DestinationPath $env:ARCHIVE_PATH -Force'
        fi
    else
        # BSD tar otherwise includes macOS extended attributes as AppleDouble files.
        COPYFILE_DISABLE=1 tar -czf "$archive.tar.gz" -C "$stage" .
    fi
    echo "Packaged $bundle for $PLATFORM"
done
rm -rf "$DIST_DIR/_stage"
printf 'Native bundles: %s\n' "$DIST_DIR"
