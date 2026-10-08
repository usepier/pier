package api

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// AdoptLoginEnv gives this process the environment a login shell would
// have. An app launched from the Dock inherits launchd's minimal PATH, so
// the cloud CLIs, gh and ssh helpers installed by a package manager would be
// invisible to every command pier runs. Values already set win; a shell
// that fails or hangs leaves PATH with the usual package-manager locations
// appended.
func AdoptLoginEnv() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", "env -0")
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err == nil {
		for _, kv := range bytes.Split(out, []byte{0}) {
			k, v, ok := strings.Cut(string(kv), "=")
			if !ok || k == "" || !adoptable(k) {
				continue
			}
			if k == "PATH" || os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
	path := os.Getenv("PATH")
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/homebrew/sbin", "/usr/local/sbin"} {
		if !strings.Contains(":"+path+":", ":"+dir+":") {
			path += ":" + dir
		}
	}
	os.Setenv("PATH", path)
}

// adoptable keeps to what pier's tools read: PATH, cloud and GitHub
// configuration, ssh's agent, locale. Shell internals (PWD, SHLVL, _) and
// anything terminal-specific stay out.
func adoptable(k string) bool {
	switch k {
	case "PATH", "SSH_AUTH_SOCK", "LANG", "LC_ALL", "LC_CTYPE", "HOME", "USER":
		return true
	}
	for _, prefix := range []string{"AWS_", "CLOUDSDK_", "GOOGLE_", "GH_", "GITHUB_", "GIT_"} {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
