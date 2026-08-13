.DEFAULT_GOAL := help

BIN ?= pier

CLI_TARGETS := build supervisors install test clean
APP_TARGETS := generate resolve-packages ios ios-core ghostty-core macos ios-build macos-build \
	ios-testflight macos-signed-archive macos-release notary-setup
PREFIXED_APP_TARGETS := app-test app-clean

.PHONY: help $(CLI_TARGETS) $(APP_TARGETS) $(PREFIXED_APP_TARGETS)

$(CLI_TARGETS):
	@$(MAKE) --no-print-directory -C cmd $@ BIN="$(abspath $(BIN))"

$(APP_TARGETS):
	@$(MAKE) --no-print-directory -C app $@

$(PREFIXED_APP_TARGETS):
	@$(MAKE) --no-print-directory -C app $(patsubst app-%,%,$@)

help:
	@echo "CLI commands:"
	@echo "  make build                 Build the CLI and embedded supervisors"
	@echo "  make supervisors           Build the embedded Linux supervisors"
	@echo "  make install               Install the CLI into GOPATH/bin"
	@echo "  make test                  Vet and test the Go code"
	@echo "  make clean                 Remove CLI build artifacts"
	@echo ""
	@echo "App commands:"
	@echo "  make generate              Generate the Xcode project"
	@echo "  make resolve-packages      Resolve Swift packages and Sparkle tools"
	@echo "  make ios                   Build and run in the iOS Simulator"
	@echo "  make macos                 Build and run on this Mac"
	@echo "  make ios-core              Build the embedded Go XCFramework"
	@echo "  make ghostty-core          Build the libghostty XCFramework"
	@echo "  make ios-build             Build unsigned for an iOS device"
	@echo "  make macos-build           Build unsigned for macOS"
	@echo "  make ios-testflight        Export an App Store Connect IPA"
	@echo "  make macos-signed-archive  Create a Developer ID archive"
	@echo "  make macos-release         Sign, notarize, staple, and zip"
	@echo "  make notary-setup          Save notarization credentials"
	@echo "  make app-test              Run the native app tests"
	@echo "  make app-clean             Remove native app build artifacts"
