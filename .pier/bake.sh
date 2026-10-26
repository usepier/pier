#!/usr/bin/env bash
# pier bake hook: runs once on the bake instance (agent user, passwordless
# sudo, NO repo checkout). Toolchains only; repo state goes in .pier/setup.sh.
set -euo pipefail

# go.mod's `go` line is a minimum; any newer toolchain builds it without a
# GOTOOLCHAIN download. Bump the version and both checksums together
# (https://go.dev/dl/?mode=json).
GO_VERSION=1.27.3
case "$(dpkg --print-architecture)" in
  arm64) GO_SHA256=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49 ;;
  amd64) GO_SHA256=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 ;;
  *) echo "no pinned go checksum for $(dpkg --print-architecture)" >&2; exit 1 ;;
esac
tarball="go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz"
curl -fsSL --retry 3 -o "/tmp/$tarball" "https://go.dev/dl/$tarball"
echo "$GO_SHA256  /tmp/$tarball" | sha256sum -c -
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "/tmp/$tarball"
rm -f "/tmp/$tarball"
# /usr/local/bin, not a profile PATH edit: setup.sh and ssh exec channels run
# non-login shells.
sudo ln -sf /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/

# `make test` runs with -race, which needs cgo and a C compiler.
sudo DEBIAN_FRONTEND=noninteractive apt-get update -y
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends gcc libc6-dev
