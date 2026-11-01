# pier for iOS. The Xcode project is generated from project.yml; the two
# frameworks it links are built here: PierCore from ios/core (Go, through
# gomobile) and GhosttyKit from Ghostty's source with Patches/ applied.
PROJECT := Pier.xcodeproj
SCHEME := Pier
DERIVED_DATA ?= .derived-data
LOGS_DIR ?= logs
DIST_DIR ?= ../dist
APP_VERSION ?= $(shell awk '/MARKETING_VERSION:/ { gsub(/"/, "", $$2); print $$2; exit }' project.yml)
BUILD_NUMBER ?= $(shell date -u '+%Y%m%d%H%M')
TEAM_ID ?= JD26ZWJ4WW
SIMULATOR ?= $(shell xcrun simctl list devices available | awk -F '[()]' '/iPhone/ { print $$2; exit }')
RELEASE_DIR := $(abspath $(DIST_DIR)/ios/Pier-$(APP_VERSION)-$(BUILD_NUMBER))
ARCHIVE := $(RELEASE_DIR)/Pier.xcarchive
EXPORT_DIR := $(RELEASE_DIR)/export
.DEFAULT_GOAL := help

.PHONY: help generate core ghostty frameworks run build test core-test testflight check-team-id clean

generate:
	@command -v xcodegen >/dev/null || { echo "xcodegen is required: brew install xcodegen"; exit 1; }
	xcodegen generate

core:
	./Scripts/build-mobile-core.sh

ghostty:
	./Scripts/build-ghostty.sh

frameworks: core ghostty

run: frameworks generate
	./Scripts/run-ios.sh "$(PROJECT)" "$(SCHEME)" "$(DERIVED_DATA)/simulator" "$(LOGS_DIR)"

build: frameworks generate
	xcodebuild -project "$(PROJECT)" -scheme "$(SCHEME)" \
		-destination 'generic/platform=iOS' \
		-derivedDataPath "$(DERIVED_DATA)/device" \
		CODE_SIGNING_ALLOWED=NO build

test: frameworks generate core-test
	xcodebuild -project "$(PROJECT)" -scheme "$(SCHEME)" \
		-destination "platform=iOS Simulator,id=$(SIMULATOR)" \
		-derivedDataPath "$(DERIVED_DATA)/simulator" \
		CODE_SIGNING_ALLOWED=NO test

core-test:
	cd core && go vet ./... && go test ./...

check-team-id:
	@test -n "$(TEAM_ID)" || { echo "Pass your Apple team: TEAM_ID=XXXXXXXXXX" >&2; exit 1; }
	@security find-identity -v -p codesigning | grep -q '"Apple Distribution:' || { \
		echo "No Apple Distribution identity (certificate and private key) is installed." >&2; exit 1; }

testflight: check-team-id frameworks generate
	mkdir -p "$(RELEASE_DIR)"
	rm -rf "$(ARCHIVE)" "$(EXPORT_DIR)"
	xcodebuild -project "$(PROJECT)" -scheme "$(SCHEME)" -configuration Release \
		-destination 'generic/platform=iOS' -archivePath "$(ARCHIVE)" -allowProvisioningUpdates \
		DEVELOPMENT_TEAM="$(TEAM_ID)" MARKETING_VERSION="$(APP_VERSION)" CURRENT_PROJECT_VERSION="$(BUILD_NUMBER)" \
		archive
	xcodebuild -exportArchive -archivePath "$(ARCHIVE)" -exportPath "$(EXPORT_DIR)" \
		-exportOptionsPlist Config/TestFlightExportOptions.plist -allowProvisioningUpdates
	@echo "TestFlight IPA: $(EXPORT_DIR)/Pier.ipa"

clean:
	rm -rf "$(DERIVED_DATA)" "$(PROJECT)" Frameworks

help:
	@echo "make run         build the frameworks and the app, and run it in the Simulator"
	@echo "make build       build for a device, unsigned"
	@echo "make test        the core's Go tests and the app's unit tests"
	@echo "make core        PierCore.xcframework, from ios/core"
	@echo "make ghostty     GhosttyKit.xcframework, patched (Zig 0.16+ and Xcode's Metal toolchain)"
	@echo "make testflight  a signed App Store Connect IPA (TEAM_ID=…)"
	@echo "make clean       remove the generated project, frameworks and build data"
