package awsec2

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFollowSetupRemoteReplaysCompletedLogAndExit(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".pier-setup.log"), []byte("make install\nmake build\npier setup: done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".pier-setup.finished"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", followSetupRemote)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("follow script: %v: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"make install\n", "make build\n", setupExitPrefix + "0\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("follow output missing %q: %q", want, got)
		}
	}
}
