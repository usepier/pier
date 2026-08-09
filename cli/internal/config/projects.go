package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// ProjectPaths is the user's explicit local project list. It is kept outside
// config.toml because these machine-specific paths are UI metadata, just like
// the per-instance workspace registry.
func ProjectPaths() []string {
	var paths []string
	b, err := os.ReadFile(projectsPath())
	if err == nil {
		_ = json.Unmarshal(b, &paths)
	}
	return paths
}

func RememberProject(repo string) error {
	path, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	paths := ProjectPaths()
	for _, existing := range paths {
		if existing == path {
			return nil
		}
	}
	paths = append(paths, path)
	sort.Strings(paths)
	b, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(projectsPath(), append(b, '\n'), 0o600)
}

func projectsPath() string {
	return filepath.Join(Dir(), "projects.json")
}
