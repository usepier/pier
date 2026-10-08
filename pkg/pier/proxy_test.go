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
	if err := InstallCLI("relative/pier"); err == nil {
		t.Error("a relative path was linked")
	}
}
