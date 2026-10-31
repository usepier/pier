package pier

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppleString(t *testing.T) {
	if got := appleString(`printf 'a\nb' > "/x"`); got != `"printf 'a\\nb' > \"/x\""` {
		t.Errorf("appleString = %s", got)
	}
}

func TestCLIInstallStatus(t *testing.T) {
	self := filepath.Join(t.TempDir(), "pier")
	os.WriteFile(self, []byte("#!/bin/sh\n"), 0o755)
	if st := CLIInstallStatus(self); st.Link != CLILink || st.Installed {
		t.Errorf("a fresh binary reads installed: %+v", st)
	}
	if err := InstallCLI("relative/pier", false); err == nil {
		t.Error("a relative path was linked")
	}
}

// A link left by an app that moved reads stale and is put right; a pier
// Homebrew links to the app's own is the app's.
func TestCLILinkMovedAndProvided(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Applications", "Pier.app", "Contents", "MacOS")
	os.MkdirAll(app, 0o755)
	self := filepath.Join(app, "pier")
	os.WriteFile(self, []byte("#!/bin/sh\n"), 0o755)
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	defer func(old string) { CLILink = old }(CLILink)
	CLILink = filepath.Join(bin, "pier")
	t.Setenv("PATH", bin)

	// The app was in Downloads when the command went in.
	os.Symlink(filepath.Join(root, "Downloads", "Pier.app", "Contents", "MacOS", "pier"), CLILink)
	if st := CLIInstallStatus(self); !st.Stale || st.Installed {
		t.Errorf("moved = %+v", st)
	}
	if err := InstallCLI(self, false); err != nil {
		t.Fatal(err)
	}
	if st := CLIInstallStatus(self); !st.Installed || st.Stale {
		t.Errorf("repaired = %+v", st)
	}

	// Homebrew's link, first on PATH, is the app's pier: nothing to do.
	os.Remove(CLILink)
	brew := filepath.Join(root, "brew")
	os.MkdirAll(brew, 0o755)
	os.Symlink(self, filepath.Join(brew, "pier"))
	t.Setenv("PATH", brew+string(os.PathListSeparator)+bin)
	if st := CLIInstallStatus(self); st.Provided != filepath.Join(brew, "pier") || st.Other != "" || st.Installed {
		t.Errorf("provided = %+v", st)
	}
	// Someone else's pier first is another.
	other := filepath.Join(root, "other")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "pier"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", other+string(os.PathListSeparator)+bin)
	if st := CLIInstallStatus(self); st.Other != filepath.Join(other, "pier") || st.Provided != "" {
		t.Errorf("other = %+v", st)
	}
}
