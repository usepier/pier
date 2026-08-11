#!/bin/sh
set -eu

APP_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
REPO_ROOT=$(CDPATH= cd -- "$APP_ROOT/.." && pwd)
OUTPUT="$APP_ROOT/Frameworks/PierCore.xcframework"

if command -v gomobile >/dev/null 2>&1; then
  GOMOBILE=$(command -v gomobile)
else
  GOPATH_VALUE=$(go env GOPATH)
  GOMOBILE="$GOPATH_VALUE/bin/gomobile"
fi

GOPATH_VALUE=$(go env GOPATH)
PATH="$GOPATH_VALUE/bin:$PATH"
export PATH

if [ ! -x "$GOMOBILE" ]; then
  echo "gomobile is required; install it with:" >&2
  echo "  go install golang.org/x/mobile/cmd/gomobile@latest" >&2
  exit 1
fi

mkdir -p "$APP_ROOT/Frameworks"
cd "$REPO_ROOT"
"$GOMOBILE" bind \
  -target=ios,iossimulator \
  -prefix Pier \
  -trimpath \
  -o "$OUTPUT" \
  ./mobile/piercore

echo "Built $OUTPUT"
