package config

import (
	"os"

	"github.com/sergiumoraru/tirion/internal/runtimeconfig"
	"gopkg.in/yaml.v3"
)

type OwnersConfig struct {
	DefaultOwners []string                  `yaml:"default_owners"`
	RepoOverrides map[string]OwnersOverride `yaml:"repo_overrides"`
}

type OwnersOverride struct {
	Owners []string `yaml:"owners"`
}

func DefaultOwnersConfig() *OwnersConfig {
	return &OwnersConfig{
		RepoOverrides: map[string]OwnersOverride{},
	}
}

// LoadOwnersConfig loads ~/.tirion/owners.yaml (optional).
func LoadOwnersConfig() (*OwnersConfig, error) {
	cfg := DefaultOwnersConfig()

	configPath, err := runtimeconfig.UserPath("owners.yaml")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.RepoOverrides == nil {
		cfg.RepoOverrides = map[string]OwnersOverride{}
	}
	return cfg, nil
}
