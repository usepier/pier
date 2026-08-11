#!/bin/sh
set -eu

repo_root="$SRCROOT/.."
output_dir="$TARGET_BUILD_DIR/$UNLOCALIZED_RESOURCES_FOLDER_PATH"
cli_output="$DERIVED_FILE_DIR/pier"
cli_arm64="$DERIVED_FILE_DIR/pier-arm64"
cli_x86_64="$DERIVED_FILE_DIR/pier-x86_64"

GOOS=darwin GOARCH=arm64 make -C "$repo_root" build BIN="$cli_arm64"
GOOS=darwin GOARCH=amd64 make -C "$repo_root" build BIN="$cli_x86_64"
/usr/bin/lipo -create "$cli_arm64" "$cli_x86_64" -output "$cli_output"
mkdir -p "$output_dir"
install -m 0755 "$cli_output" "$output_dir/pier"

if [ "${CODE_SIGNING_ALLOWED:-NO}" = "YES" ] && [ -n "${EXPANDED_CODE_SIGN_IDENTITY:-}" ] && [ "${EXPANDED_CODE_SIGN_IDENTITY}" != "-" ]; then
  /usr/bin/codesign --force --sign "$EXPANDED_CODE_SIGN_IDENTITY" --options runtime "$output_dir/pier"
fi
