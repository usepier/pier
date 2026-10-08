# The Homebrew cask for the Mac app, rendered for each release by
# .github/workflows/macos-release.yml into usepier/homebrew-tap.
cask "pier" do
  arch arm: "aarch64", intel: "x64"

  version "{{VERSION}}"
  sha256 arm:   "{{SHA_ARM}}",
         intel: "{{SHA_INTEL}}"

  url "https://github.com/usepier/pier/releases/download/{{TAG}}/Pier_#{version}_#{arch}.dmg"
  name "Pier"
  desc "Coding-agent sessions on your own cloud, one VM each"
  homepage "https://github.com/usepier/pier"

  auto_updates true
  depends_on macos: ">= :sonoma"
  conflicts_with formula: "pier"

  app "Pier.app"
  # The app bundles the pier command of the same release.
  binary "#{appdir}/Pier.app/Contents/MacOS/pier"

  # pier's own settings and state (~/.config/pier) are the CLI's too, and
  # stay; zap removes only what the app keeps.
  zap trash: [
    "~/Library/Application Support/com.usepier.pier",
    "~/Library/Caches/com.usepier.pier",
    "~/Library/Caches/pier",
    "~/Library/Preferences/com.usepier.pier.plist",
    "~/Library/Saved Application State/com.usepier.pier.savedState",
    "~/Library/WebKit/com.usepier.pier",
  ]
end
