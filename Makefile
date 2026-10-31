# pier is a single binary that embeds the in-VM supervisor (both linux
# arches), so build the supervisors first.
ASSETS := cmd/pier/assets

# git describe in a checkout; release tarballs have no .git, so the brew
# formula passes VERSION explicitly.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build: supervisors
	go build -ldflags "-X main.version=$(VERSION)" -o pier ./cmd/pier

supervisors:
	GOOS=linux GOARCH=arm64 go build -o $(ASSETS)/pier-supervisor-linux-arm64 ./cmd/pier-supervisor
	GOOS=linux GOARCH=amd64 go build -o $(ASSETS)/pier-supervisor-linux-amd64 ./cmd/pier-supervisor

# install(1), not cp: it unlinks the target first (new inode), so replacing
# the binary is safe even while a pier process is running (attached ssh).
install: build
	install -m 0755 pier $$(go env GOPATH)/bin/pier

test:
	go vet ./... && go test -race ./...

# The Mac app's TypeScript twin of the `pier api` protocol (pkg/pier/wire).
api-types:
	@mkdir -p macos/src/api
	go run ./internal/api/tsgen -o macos/src/api/protocol.gen.ts

# The Mac app bundles this exact pier as its sidecar, one per CPU arch,
# named by target triple the way the app's bundler expects.
MAC_BIN := macos/src-tauri/binaries
mac-sidecar: supervisors
	@mkdir -p $(MAC_BIN)
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o $(MAC_BIN)/pier-aarch64-apple-darwin ./cmd/pier
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(MAC_BIN)/pier-x86_64-apple-darwin ./cmd/pier

# Run the app against this checkout's pier (rebuilt on every reload).
mac-dev: build mac-sidecar
	cd macos && pnpm install && VITE_PIER_API=bridge pnpm tauri dev

# A local, unsigned build of the app for this Mac's architecture.
mac: mac-sidecar
	cd macos && pnpm install --frozen-lockfile && pnpm tauri build --bundles app

clean:
	rm -f pier $(ASSETS)/pier-supervisor-linux-*
