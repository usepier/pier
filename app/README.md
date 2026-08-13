# Pier native app

Pier uses one shared SwiftUI codebase with native iOS and macOS targets. The
Xcode project is generated from `project.yml`; do not edit `Pier.xcodeproj`
directly.

## Architecture

- `Sources/Shared` owns the models, app state, and SwiftUI interface used by
  both platforms.
- macOS builds and embeds the Go CLI, then talks to its versioned `pier app`
  JSON interface. The native app never scrapes human-readable terminal output.
- iOS links a gomobile-generated `PierCore.xcframework` built from
  `mobile/piercore`. PierCore uses only the modular Go AWS clients it needs
  (SSO OIDC, SSO, STS, EC2, and EC2 Instance Connect), plus Go SSH. Swift owns
  the UI and stores PierCore's opaque session JSON in the iOS Keychain.
- Interactive terminals use libghostty's native Metal renderer. macOS lets
  libghostty own the local `pier app attach` PTY; iOS uses Pier's small
  external-transport patch to connect the renderer to PierCore's Go SSH
  stream. Each screen attaches to a cloned tmux session.

The product model stays low-level: instances contain ordinary tmux tabs. The
Shell, Claude, and Codex choices are command presets, not special agent types.

```sh
cd app
make          # show available commands
make generate # generate the Xcode project
make ios-core # regenerate the embedded Go XCFramework
make ghostty-core # build the pinned, patched libghostty XCFramework
make macos    # build, launch, and stream runtime logs into logs/
make ios      # build, install, launch, and stream Simulator logs into logs/
make ios-testflight # archive and export an App Store Connect IPA
make macos-release  # Developer ID sign, notarize, staple, and zip for GitHub
make clean    # remove generated project and build output
```

These app commands can also be run from the repository root. Use
`make app-test` and `make app-clean` there to distinguish them from the CLI
targets.

Requirements: Xcode 26+, its Metal toolchain, XcodeGen, Zig 0.16+, Go 1.25+,
and gomobile. Install the Metal toolchain with
`xcodebuild -downloadComponent MetalToolchain`. Install the binder once with
`go install golang.org/x/mobile/cmd/gomobile@latest`; the matching
`gobind` version is pinned as a Go tool dependency. The generated Xcode project
and XCFramework are ignored by Git and can always be recreated.

## Distribution

The iOS and macOS bundle ID is `com.pier.client`.
Local `make ios` and `make macos` builds remain unsigned. Distribution artifacts
are written under the repository-level `dist/` directory.

An installed Apple Distribution identity is required for the TestFlight IPA:

```sh
make ios-testflight
```

The version defaults to `MARKETING_VERSION` in `project.yml`, and the build
number defaults to a UTC timestamp. Override either when needed:

```sh
make ios-testflight APP_VERSION=0.2.0 BUILD_NUMBER=42
```

The macOS GitHub artifact uses Developer ID, Hardened Runtime, Apple
notarization, stapling, and Sparkle signing. Store notarization credentials
once; the command prompts securely for an app-specific password and saves it
in Keychain:

```sh
make notary-setup APPLE_ID=developer@example.com
make macos-release
```

Use `NOTARY_PROFILE=name` or `TEAM_ID=XXXXXXXXXX` to override their inferred
defaults. `make macos-signed-archive` performs the local Developer ID archive
and signature verification without submitting anything to Apple.

Sparkle checks `https://pier.kak.dev/appcast.xml`. Its private Ed25519 key is
stored in the login Keychain under the `usepier` account and its public key is
embedded in `Config/PierMacOS-Info.plist`. Do not create a replacement key for
an existing release channel. Resolve the tools, then export the current key and
import it on another Mac with:

```sh
make resolve-packages
./.swift-packages/artifacts/sparkle/Sparkle/bin/generate_keys --account usepier -x /secure/path/pier-sparkle-key
./.swift-packages/artifacts/sparkle/Sparkle/bin/generate_keys --account usepier -f /secure/path/pier-sparkle-key
```

`make macos-release` writes the notarized ZIP and a neighboring `.zip.sparkle`
file under `dist/macos/`. The sidecar contains the `sparkle:edSignature` and
`length` attributes the website must put on the appcast enclosure. The appcast
must use the ZIP's monotonically increasing build number as `sparkle:version`
and the marketing version as `sparkle:shortVersionString`. For CI, pass an
exported key with `SPARKLE_PRIVATE_KEY_FILE=/secure/path` instead of using the
Keychain; never commit that file.

The run commands remain attached while the app is open and mirror logs to a
timestamped file under the git-ignored `logs/` directory. Press Control-C to
stop streaming.

## Current slice

The shared UI contains onboarding/readiness, an instance list, the macOS split
view/iOS navigation stack, a pinned Info tab, and one closable workspace tab
per tmux window. On macOS, the tabs live in the titlebar and host a native
terminal connected to the selected tmux window. The `+` tab opens an inline
Shell, Claude, or Codex launcher; one click creates and enters the ordinary
tmux window. iOS includes native IAM Identity Center device authorization,
Keychain-backed token refresh, EC2 instance discovery/removal, EC2 Instance
Connect, tmux inspection and tab management, and interactive SSH terminals.

Creating the first VM for a repository still happens on macOS because the
current Pier create flow uploads a local repository snapshot. Once an instance
exists, it is manageable from iOS. Managed port viewers and container
inspection remain later vertical slices.

The single app-icon source is `Resources/Anchor.icon`, created with Apple's
Icon Composer. Xcode renders the iOS and macOS appearances and legacy sizes at
build time; no app-icon PNGs or app-icon asset catalog live in the repository.
