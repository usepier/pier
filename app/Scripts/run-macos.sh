#!/bin/sh
set -eu

app_path=${1:?app path is required}
logs_dir=${2:?logs directory is required}

if [ ! -d "$app_path" ]; then
  echo "Pier.app was not built at $app_path" >&2
  exit 1
fi

mkdir -p "$logs_dir"
log_file="$logs_dir/macos-$(date '+%Y%m%d-%H%M%S').log"

open -n "$app_path"
echo "Pier launched. Streaming logs to $log_file"
/usr/bin/log stream \
  --style compact \
  --level debug \
  --predicate 'process == "Pier"' 2>&1 | tee "$log_file"
