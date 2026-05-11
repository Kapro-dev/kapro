// Package config manages the kapro CLI configuration file (~/.kapro/config.yaml).
// Stores hub cluster details, selected registries, and defaults so users don't
// need to pass --project/--registry flags on every command.
//
// Written by `kapro hub init` and `kapro hub registry add`.
// Read by all commands that need project/cluster/registry context.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// KaproConfig is the CLI configuration persisted at ~/.kapro/config.yaml.
type KaproConfig struct {
	// Hub holds the hub cluster connection details.
	Hub HubConfig `json:"hub"`
	// Registries maps a role (bundles, charts, images) to an OCI URL.
	Registries map[string]string `json:"registries,omitempty"`
}

// HubConfig holds the hub cluster identity.
type HubConfig struct {
	Project  string `json:"project"`
	Cluster  string `json:"cluster"`
	Location string `json:"location"`
}

// DefaultPath returns ~/.kapro/config.yaml.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kapro", "config.yaml")
}

// Load reads the config from disk. Returns zero config if file doesn't exist.
func Load() (*KaproConfig, error) {
	return LoadFrom(DefaultPath())
}

// LoadFrom reads the config from a specific path.
func LoadFrom(path string) (*KaproConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &KaproConfig{Registries: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg KaproConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Registries == nil {
		cfg.Registries = map[string]string{}
	}
	return &cfg, nil
}

// Save writes the config to disk, creating ~/.kapro/ if needed.
func (c *KaproConfig) Save() error {
	return c.SaveTo(DefaultPath())
}

// SaveTo writes the config to a specific path.
func (c *KaproConfig) SaveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// Project returns the hub project, or the override if non-empty.
func (c *KaproConfig) Project(override string) string {
	if override != "" {
		return override
	}
	return c.Hub.Project
}

// Registry returns the OCI URL for a given role (e.g. "bundles", "charts").
// Falls back to the "default" registry if the role isn't found.
func (c *KaproConfig) Registry(role string) string {
	if url, ok := c.Registries[role]; ok {
		return url
	}
	return c.Registries["default"]
}

// SetRegistry sets a registry URL for a role.
func (c *KaproConfig) SetRegistry(role, url string) {
	c.Registries[role] = url
}
