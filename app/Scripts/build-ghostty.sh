#!/bin/sh
set -eu

APP_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
GHOSTTY_COMMIT=fea378e565c8ddb7f49808c4f2e36a4a932e35ff
PATCH="$APP_ROOT/Patches/libghostty-external-transport.patch"
CACHE_ROOT="$APP_ROOT/.ghostty-build"
SOURCE="$CACHE_ROOT/source"
OUTPUT="$APP_ROOT/Frameworks/GhosttyKit.xcframework"
PATCH_HASH=$(shasum -a 256 "$PATCH" | awk '{ print $1 }')
EXPECTED_STAMP="$GHOSTTY_COMMIT:$PATCH_HASH"

if [ -f "$OUTPUT/.pier-build" ] && [ "$(sed -n '1p' "$OUTPUT/.pier-build")" = "$EXPECTED_STAMP" ]; then
  echo "GhosttyKit is current."
  exit 0
fi

command -v git >/dev/null || { echo "git is required to build GhosttyKit." >&2; exit 1; }
command -v zig >/dev/null || { echo "Zig 0.16+ is required: brew install zig" >&2; exit 1; }
/usr/bin/xcrun -sdk macosx -find metal >/dev/null 2>&1 || {
  echo "Apple's Metal toolchain is required:" >&2
  echo "  xcodebuild -downloadComponent MetalToolchain" >&2
  exit 1
}

if [ -d "$SOURCE/.git" ]; then
  CURRENT_COMMIT=$(git -C "$SOURCE" rev-parse HEAD)
else
  CURRENT_COMMIT=
fi

if [ "$CURRENT_COMMIT" != "$GHOSTTY_COMMIT" ]; then
  rm -rf "$SOURCE"
  mkdir -p "$CACHE_ROOT"
  git clone --filter=blob:none --no-checkout https://github.com/ghostty-org/ghostty.git "$SOURCE"
  git -C "$SOURCE" checkout --detach "$GHOSTTY_COMMIT"
fi

if git -C "$SOURCE" apply --reverse --check "$PATCH" >/dev/null 2>&1; then
  :
else
  git -C "$SOURCE" reset --hard "$GHOSTTY_COMMIT"
  git -C "$SOURCE" clean -fd
  git -C "$SOURCE" apply --check "$PATCH"
  git -C "$SOURCE" apply "$PATCH"
fi

(
  cd "$SOURCE"
  zig build -Demit-xcframework -Demit-macos-app=false -Doptimize=ReleaseFast
)

mkdir -p "$APP_ROOT/Frameworks"
rm -rf "$OUTPUT"
/usr/bin/ditto "$SOURCE/macos/GhosttyKit.xcframework" "$OUTPUT"
printf '%s\n' "$EXPECTED_STAMP" > "$OUTPUT/.pier-build"
echo "Built $OUTPUT"
