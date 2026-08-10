#!/bin/sh
set -eu

project=${1:?project path is required}
scheme=${2:?scheme is required}
derived_data=${3:?derived data path is required}
logs_dir=${4:?logs directory is required}
bundle_id=com.pier.client
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
env_file="$script_dir/../.env.local"

if [ -f "$env_file" ]; then
  set -a
  . "$env_file"
  set +a
fi

device_id=$(xcrun simctl list devices available | awk -F '[()]' '/iPhone/ && /(Booted|Shutdown)/ { print $2; exit }')
if [ -z "$device_id" ]; then
  echo "No iPhone Simulator runtime is installed." >&2
  echo "Install one in Xcode > Settings > Components, then run make ios again." >&2
  exit 1
fi

xcrun simctl boot "$device_id" >/dev/null 2>&1 || true
xcrun simctl bootstatus "$device_id" -b
open -a Simulator --args -CurrentDeviceUDID "$device_id"

xcodebuild -project "$project" \
  -scheme "$scheme" \
  -destination "platform=iOS Simulator,id=$device_id" \
  -derivedDataPath "$derived_data" \
  build

app_path="$derived_data/Build/Products/Debug-iphonesimulator/Pier.app"
xcrun simctl install "$device_id" "$app_path"

mkdir -p "$logs_dir"
log_file="$logs_dir/ios-$(date '+%Y%m%d-%H%M%S').log"
echo "Launching Pier and streaming logs to $log_file"
export SIMCTL_CHILD_PIER_IOS_START_URL="${PIER_IOS_START_URL:-}"
export SIMCTL_CHILD_PIER_IOS_SSO_REGION="${PIER_IOS_SSO_REGION:-}"
export SIMCTL_CHILD_PIER_IOS_AWS_REGION="${PIER_IOS_AWS_REGION:-}"
export SIMCTL_CHILD_PIER_IOS_ACCOUNT_ID="${PIER_IOS_ACCOUNT_ID:-}"
export SIMCTL_CHILD_PIER_IOS_ROLE_NAME="${PIER_IOS_ROLE_NAME:-}"
xcrun simctl launch --terminate-running-process --console "$device_id" "$bundle_id" 2>&1 | tee "$log_file"
