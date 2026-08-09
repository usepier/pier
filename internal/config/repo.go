package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RepoConfig contains the optional, repository-local pier settings. It is
// intentionally separate from Config: ~/.config/pier/config.toml describes a
// developer's cloud account, while .pier.toml travels with one repository.
type RepoConfig struct {
	Docker RepoDockerConfig `toml:"docker"`
}

type RepoDockerConfig struct {
	// Pointer preserves the distinction between the default (true) and an
	// explicit opt-out.
	AutoUp *bool `toml:"auto_up"`
}

// LoadRepo reads <root>/.pier.toml. A missing file is the default config.
func LoadRepo(root string) (RepoConfig, error) {
	var cfg RepoConfig
	path := filepath.Join(root, ".pier.toml")
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, nil
}

// AutoCompose reports whether a repository's Compose project should be
// started after its setup hook. Automatic startup is on unless opted out.
func (c RepoConfig) AutoCompose() bool {
	return c.Docker.AutoUp == nil || *c.Docker.AutoUp
}
