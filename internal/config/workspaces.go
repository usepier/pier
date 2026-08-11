package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WorkspacePaths is local-only UI metadata: instance ID -> source checkout.
// Absolute laptop paths never belong in cloud tags, so this registry stays
// beside the user's pier config.
func WorkspacePaths() map[string]string {
	paths := map[string]string{}
	b, err := os.ReadFile(workspacePath())
	if err == nil {
		_ = json.Unmarshal(b, &paths)
	}
	return paths
}

func RememberWorkspace(instanceID, repo string) error {
	path, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	paths := WorkspacePaths()
	paths[instanceID] = path
	b, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(workspacePath(), append(b, '\n'), 0o600)
}

func ForgetWorkspace(instanceID string) error {
	paths := WorkspacePaths()
	delete(paths, instanceID)
	b, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(workspacePath(), append(b, '\n'), 0o600)
}

func workspacePath() string {
	return filepath.Join(Dir(), "workspaces.json")
}
