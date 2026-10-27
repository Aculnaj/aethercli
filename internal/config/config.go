package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const DefaultBaseURL = "https://api.aetherapi.dev/v1"

type Config struct {
	BaseURL      string       `json:"base_url"`
	DefaultModel string       `json:"default_model,omitempty"`
	Update       *UpdateState `json:"update,omitempty"`
}

type UpdateState struct {
	LastCheckedAt   string `json:"last_checked_at,omitempty"`
	LastSeenVersion string `json:"last_seen_version,omitempty"`
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

	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return Config{}, fmt.Errorf("config must be a JSON object")
	}
	if len(fields) > 0 && fields["base_url"] == nil && fields["default_model"] == nil && fields["update"] == nil {
		return Config{}, fmt.Errorf("file does not contain aethercli configuration")
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
	if err := Validate(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Refuse symlinks and foreign schemas; preserve future aethercli fields.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink config")
	}
	if old, err := os.ReadFile(path); err == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(old, &fields); err != nil || fields == nil {
			return fmt.Errorf("existing config is not a JSON object")
		}
		if len(fields) > 0 && fields["base_url"] == nil && fields["default_model"] == nil && fields["update"] == nil {
			return fmt.Errorf("existing file is not an aethercli config")
		}
		var updated map[string]json.RawMessage
		_ = json.Unmarshal(data, &updated)
		for _, key := range []string{"base_url", "default_model", "update"} {
			delete(fields, key)
		}
		for key, value := range updated {
			fields[key] = value
		}
		data, err = json.MarshalIndent(fields, "", "  ")
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".aethercli-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// HTTPS protects credentials. Plain HTTP is supported only for local test and
// development servers; URLs cannot carry embedded credentials or routing data.
func Validate(cfg Config) error {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base-url must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return fmt.Errorf("base-url requires HTTPS (HTTP is allowed only on loopback)")
		}
	}
	if strings.Contains(u.Path, "..") || u.RawPath != "" {
		return fmt.Errorf("base-url must not contain encoded paths or traversal")
	}
	for _, value := range []string{cfg.BaseURL, cfg.DefaultModel} {
		if strings.Contains(value, "sk-aetherapi-") || strings.Contains(value, "aeth_dev_") {
			return fmt.Errorf("configuration must not contain credentials")
		}
	}
	return nil
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
