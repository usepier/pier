# pier is a single binary that embeds the in-VM supervisor (both Linux
# architectures), so build the supervisors first.
REPO_ROOT := $(abspath $(CURDIR)/..)
ASSETS := pier/assets

# git describe in a checkout; release tarballs have no .git, so the brew
# formula passes VERSION explicitly.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN ?= $(REPO_ROOT)/pier

.DEFAULT_GOAL := help

.PHONY: help build supervisors install test clean

build: supervisors
	go build -ldflags "-X main.version=$(VERSION)" -o "$(BIN)" ./pier

supervisors:
	GOOS=linux GOARCH=arm64 go build -o $(ASSETS)/pier-supervisor-linux-arm64 ./pier-supervisor
	GOOS=linux GOARCH=amd64 go build -o $(ASSETS)/pier-supervisor-linux-amd64 ./pier-supervisor

# install(1), not cp: it unlinks the target first (new inode), so replacing
# the binary is safe even while a pier process is running (attached ssh).
install: build
	install -m 0755 "$(BIN)" "$$(go env GOPATH)/bin/pier"

test:
	cd "$(REPO_ROOT)" && go vet ./... && go test ./...

clean:
	rm -f "$(BIN)" $(ASSETS)/pier-supervisor-linux-*

help:
	@echo "make build        Build the CLI and embedded supervisors"
	@echo "make supervisors  Build the embedded Linux supervisors"
	@echo "make install      Install the CLI into GOPATH/bin"
	@echo "make test         Vet and test the Go code"
	@echo "make clean        Remove CLI build artifacts"
