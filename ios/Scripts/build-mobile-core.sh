#!/bin/sh
# Builds ios/core (Go) into PierCore.xcframework for devices and the
# Simulator. gomobile and gobind are the core module's own tools, so their
# versions are pinned in ios/core/go.mod.
set -eu

APP_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
OUTPUT="$APP_ROOT/Frameworks/PierCore.xcframework"

export DEVELOPER_DIR="${DEVELOPER_DIR:-$(xcode-select -p)}"
mkdir -p "$APP_ROOT/Frameworks"
cd "$APP_ROOT/core"
# gomobile runs gobind from PATH: put the pinned one there.
TOOLS=$(mktemp -d)
trap 'rm -rf "$TOOLS"' EXIT
go build -o "$TOOLS/gobind" golang.org/x/mobile/cmd/gobind
PATH="$TOOLS:$PATH" go tool gomobile bind \
  -target=ios,iossimulator \
  -iosversion=18.0 \
  -prefix Pier \
  -trimpath \
  -o "$OUTPUT" \
  .

echo "Built $OUTPUT"
