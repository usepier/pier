PROJECT := Pier.xcodeproj
MACOS_SCHEME := Pier macOS
IOS_SCHEME := Pier iOS
DERIVED_DATA ?= .derived-data
PACKAGES_DIR ?= .swift-packages
LOGS_DIR ?= logs
DIST_DIR ?= ../dist
APP_VERSION ?= $(shell awk '/MARKETING_VERSION:/ { gsub(/"/, "", $$2); print $$2; exit }' project.yml)
BUILD_NUMBER ?= $(shell date -u '+%Y%m%d%H%M')
TEAM_ID ?= $(shell security find-certificate -c "Developer ID Application" -p 2>/dev/null | openssl x509 -noout -subject -nameopt RFC2253 2>/dev/null | awk -F'OU=' 'NF > 1 { split($$2, fields, ","); print fields[1]; exit }')
NOTARY_PROFILE ?= pier-notary
SPARKLE_ACCOUNT ?= usepier
SPARKLE_BIN := $(abspath $(PACKAGES_DIR)/artifacts/sparkle/Sparkle/bin)
SPARKLE_GENERATE_KEYS := $(SPARKLE_BIN)/generate_keys
SPARKLE_SIGN_UPDATE := $(SPARKLE_BIN)/sign_update
SPARKLE_PUBLIC_KEY := $(shell /usr/libexec/PlistBuddy -c 'Print :SUPublicEDKey' Config/PierMacOS-Info.plist 2>/dev/null)
ifdef SPARKLE_PRIVATE_KEY_FILE
SPARKLE_SIGNING_KEY_ARGS := --ed-key-file "$(SPARKLE_PRIVATE_KEY_FILE)"
else
SPARKLE_SIGNING_KEY_ARGS := --account "$(SPARKLE_ACCOUNT)"
endif
IOS_RELEASE_DIR := $(abspath $(DIST_DIR)/ios/Pier-$(APP_VERSION)-$(BUILD_NUMBER))
IOS_ARCHIVE := $(IOS_RELEASE_DIR)/Pier-iOS.xcarchive
IOS_EXPORT_DIR := $(IOS_RELEASE_DIR)/export
MACOS_RELEASE_DIR := $(abspath $(DIST_DIR)/macos/Pier-$(APP_VERSION)-$(BUILD_NUMBER))
MACOS_ARCHIVE := $(MACOS_RELEASE_DIR)/Pier-macOS.xcarchive
MACOS_APP := $(MACOS_ARCHIVE)/Products/Applications/Pier.app
MACOS_NOTARY_ZIP := $(MACOS_RELEASE_DIR)/Pier-macOS-notary-upload.zip
MACOS_RELEASE_ZIP := $(MACOS_RELEASE_DIR)/Pier-macOS-$(APP_VERSION)-$(BUILD_NUMBER).zip
.DEFAULT_GOAL := help

.PHONY: generate resolve-packages ios ios-core ghostty-core macos clean test ios-build macos-build \
	ios-testflight macos-signed-archive macos-release notary-setup \
	check-team-id check-ios-distribution check-developer-id check-notary-profile \
	check-sparkle-key help

generate:
	command -v xcodegen >/dev/null || { echo "xcodegen is required: brew install xcodegen"; exit 1; }
	xcodegen generate

resolve-packages: generate
	xcodebuild -resolvePackageDependencies \
		-project "$(PROJECT)" \
		-scheme "$(MACOS_SCHEME)" \
		-clonedSourcePackagesDirPath "$(PACKAGES_DIR)"
	@test -x "$(SPARKLE_GENERATE_KEYS)" -a -x "$(SPARKLE_SIGN_UPDATE)" || { \
		echo "Sparkle command-line tools were not resolved." >&2; \
		exit 1; \
	}

macos: macos-build
	./Scripts/run-macos.sh "$(DERIVED_DATA)/macos/Build/Products/Debug/Pier.app" "$(LOGS_DIR)"

ios: ios-core generate
	./Scripts/run-ios.sh "$(PROJECT)" "$(IOS_SCHEME)" "$(DERIVED_DATA)/ios-simulator" "$(LOGS_DIR)"

ios-core:
	./Scripts/build-mobile-core.sh

ghostty-core:
	./Scripts/build-ghostty.sh

clean:
	rm -rf "$(DERIVED_DATA)"
	rm -rf "$(PROJECT)"
	rm -rf "Frameworks/PierCore.xcframework"
	rm -rf "Frameworks/GhosttyKit.xcframework"

# Non-launching targets used by CI.
test: ghostty-core generate
	xcodebuild -project "$(PROJECT)" \
		-scheme "$(MACOS_SCHEME)" \
		-destination 'platform=macOS' \
		-derivedDataPath "$(DERIVED_DATA)/macos" \
		-clonedSourcePackagesDirPath "$(PACKAGES_DIR)" \
		CODE_SIGNING_ALLOWED=NO \
		test

macos-build: ghostty-core generate
	xcodebuild -project "$(PROJECT)" \
		-scheme "$(MACOS_SCHEME)" \
		-destination 'platform=macOS' \
		-derivedDataPath "$(DERIVED_DATA)/macos" \
		-clonedSourcePackagesDirPath "$(PACKAGES_DIR)" \
		CODE_SIGNING_ALLOWED=NO \
		build

ios-build: ghostty-core ios-core generate
	xcodebuild -project "$(PROJECT)" \
		-scheme "$(IOS_SCHEME)" \
		-destination 'generic/platform=iOS' \
		-derivedDataPath "$(DERIVED_DATA)/ios-device" \
		-clonedSourcePackagesDirPath "$(PACKAGES_DIR)" \
		CODE_SIGNING_ALLOWED=NO \
		build

check-team-id:
	@test -n "$(TEAM_ID)" || { \
		echo "No Apple team ID found. Pass TEAM_ID=XXXXXXXXXX." >&2; \
		exit 1; \
	}

check-ios-distribution: check-team-id
	@security find-identity -v -p codesigning | grep -q '"Apple Distribution:' || { \
		echo "No usable Apple Distribution identity is installed (the certificate and its private key are both required)." >&2; \
		exit 1; \
	}

check-developer-id: check-team-id
	@security find-identity -v -p codesigning | grep -q '"Developer ID Application:' || { \
		echo "No usable Developer ID Application identity is installed." >&2; \
		exit 1; \
	}

check-notary-profile:
	@xcrun notarytool history --keychain-profile "$(NOTARY_PROFILE)" >/dev/null || { \
		echo "Notary profile '$(NOTARY_PROFILE)' is missing or invalid. Run: make notary-setup APPLE_ID=you@example.com" >&2; \
		exit 1; \
	}

check-sparkle-key: resolve-packages
ifdef SPARKLE_PRIVATE_KEY_FILE
	@test -r "$(SPARKLE_PRIVATE_KEY_FILE)" || { \
		echo "Sparkle private key file '$(SPARKLE_PRIVATE_KEY_FILE)' is not readable." >&2; \
		exit 1; \
	}
else
	@actual_key="$$($(SPARKLE_GENERATE_KEYS) --account "$(SPARKLE_ACCOUNT)" -p)" || { \
		echo "Import Pier's Sparkle private key into the login Keychain before releasing." >&2; \
		exit 1; \
	}; \
	test "$$actual_key" = "$(SPARKLE_PUBLIC_KEY)" || { \
		echo "The Sparkle private key in account '$(SPARKLE_ACCOUNT)' does not match Pier's embedded public key." >&2; \
		exit 1; \
	}
endif

ios-testflight: check-ios-distribution ghostty-core ios-core generate
	mkdir -p "$(IOS_RELEASE_DIR)"
	rm -rf "$(IOS_ARCHIVE)" "$(IOS_EXPORT_DIR)"
	xcodebuild -project "$(PROJECT)" \
		-scheme "$(IOS_SCHEME)" \
		-configuration Release \
		-destination 'generic/platform=iOS' \
		-archivePath "$(IOS_ARCHIVE)" \
		-clonedSourcePackagesDirPath "$(PACKAGES_DIR)" \
		-allowProvisioningUpdates \
		DEVELOPMENT_TEAM="$(TEAM_ID)" \
		MARKETING_VERSION="$(APP_VERSION)" \
		CURRENT_PROJECT_VERSION="$(BUILD_NUMBER)" \
		archive
	xcodebuild -exportArchive \
		-archivePath "$(IOS_ARCHIVE)" \
		-exportPath "$(IOS_EXPORT_DIR)" \
		-exportOptionsPlist Config/TestFlightExportOptions.plist \
		-allowProvisioningUpdates
	@echo "TestFlight IPA: $(IOS_EXPORT_DIR)/Pier.ipa"

macos-signed-archive: ghostty-core generate check-developer-id
	mkdir -p "$(MACOS_RELEASE_DIR)"
	rm -rf "$(MACOS_ARCHIVE)"
	xcodebuild -project "$(PROJECT)" \
		-scheme "$(MACOS_SCHEME)" \
		-configuration Release \
		-destination 'generic/platform=macOS' \
		-archivePath "$(MACOS_ARCHIVE)" \
		-clonedSourcePackagesDirPath "$(PACKAGES_DIR)" \
		DEVELOPMENT_TEAM="$(TEAM_ID)" \
		CODE_SIGN_STYLE=Manual \
		CODE_SIGN_IDENTITY="Developer ID Application" \
		MARKETING_VERSION="$(APP_VERSION)" \
		CURRENT_PROJECT_VERSION="$(BUILD_NUMBER)" \
		archive
	/usr/bin/codesign --verify --deep --strict --verbose=2 "$(MACOS_APP)"
	@echo "Developer ID archive: $(MACOS_ARCHIVE)"

macos-release: check-notary-profile check-sparkle-key macos-signed-archive
	rm -f "$(MACOS_NOTARY_ZIP)" "$(MACOS_RELEASE_ZIP)" "$(MACOS_RELEASE_ZIP).sparkle"
	/usr/bin/ditto -c -k --keepParent "$(MACOS_APP)" "$(MACOS_NOTARY_ZIP)"
	xcrun notarytool submit "$(MACOS_NOTARY_ZIP)" \
		--keychain-profile "$(NOTARY_PROFILE)" \
		--wait
	xcrun stapler staple "$(MACOS_APP)"
	xcrun stapler validate "$(MACOS_APP)"
	/usr/bin/codesign --verify --deep --strict --verbose=2 "$(MACOS_APP)"
	/usr/sbin/spctl --assess --type execute --verbose=2 "$(MACOS_APP)"
	/usr/bin/ditto -c -k --sequesterRsrc --keepParent "$(MACOS_APP)" "$(MACOS_RELEASE_ZIP)"
	@metadata="$$($(SPARKLE_SIGN_UPDATE) $(SPARKLE_SIGNING_KEY_ARGS) "$(MACOS_RELEASE_ZIP)")" || exit 1; \
		printf '%s\n' "$$metadata" | tee "$(MACOS_RELEASE_ZIP).sparkle"
	rm -f "$(MACOS_NOTARY_ZIP)"
	@echo "Notarized GitHub artifact: $(MACOS_RELEASE_ZIP)"
	@echo "Sparkle enclosure metadata: $(MACOS_RELEASE_ZIP).sparkle"

notary-setup: check-team-id
	@test -n "$(APPLE_ID)" || { \
		echo "Pass the Apple account email as APPLE_ID=you@example.com." >&2; \
		exit 1; \
	}
	xcrun notarytool store-credentials "$(NOTARY_PROFILE)" \
		--apple-id "$(APPLE_ID)" \
		--team-id "$(TEAM_ID)"

help:
	@echo "make generate          Generate the Xcode project"
	@echo "make resolve-packages  Resolve Swift packages and Sparkle tools"
	@echo "make ios               Build and run unsigned in the iOS Simulator"
	@echo "make ios-core          Build the embedded Go XCFramework"
	@echo "make ghostty-core      Build the patched libghostty XCFramework"
	@echo "make macos             Build and run unsigned on this Mac"
	@echo "make ios-testflight    Export a signed App Store Connect IPA"
	@echo "make macos-release     Sign, notarize, staple, and zip for GitHub"
	@echo "make notary-setup      Save notarization credentials in Keychain"
	@echo "make clean             Remove generated project and build data"
