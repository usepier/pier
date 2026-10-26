#!/usr/bin/env bash
# pier setup: runs async in the session's "setup" tmux window on first
# boot, cwd = repo root. Logs to ~/.pier-setup.log.
set -euo pipefail

go mod download
# make, not go build: it embeds the cross-compiled supervisors.
make build
# Compile, but don't run, the race-instrumented tests so the first
# `make test` in a session starts from a warm build cache.
go vet ./...
go test -race -run '^$' ./... >/dev/null
