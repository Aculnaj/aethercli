package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const DefaultBaseURL = "https://api.aetherapi.dev/v1"

type Config struct {
	BaseURL      string `json:"base_url"`
	DefaultModel string `json:"default_model,omitempty"`
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aether", "config.json"), nil
}

func Load(path string) (Config, error) {
	if path == "" {
		defaultPath, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = defaultPath
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{BaseURL: DefaultBaseURL}, nil
	}
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return withDefaults(cfg), nil
}

func Save(path string, cfg Config) error {
	if path == "" {
		defaultPath, err := DefaultPath()
		if err != nil {
			return err
		}
		path = defaultPath
	}

	cfg = withDefaults(cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func Delete(path string) error {
	if path == "" {
		defaultPath, err := DefaultPath()
		if err != nil {
			return err
		}
		path = defaultPath
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func withDefaults(cfg Config) Config {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return cfg
}
