package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingConfigReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
	if cfg.DefaultModel != "" {
		t.Fatalf("DefaultModel = %q, want empty", cfg.DefaultModel)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{
		BaseURL:      "https://example.test/v1",
		DefaultModel: "claude-sonnet-4-6",
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("config mode = %v, want 0600", gotMode)
	}
}

func TestLoadBackfillsMissingBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(Config{DefaultModel: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
}

func TestValidateCredentialTransport(t *testing.T) {
	for _, tc := range []struct {
		url   string
		valid bool
	}{
		{"https://api.aetherapi.dev/v1", true}, {"http://127.0.0.1:1234/v1", true}, {"http://[::1]:1234/v1", true}, {"http://localhost:1234/v1", true},
		{"http://remote.example/v1", false}, {"https://user:password@example.com/v1", false}, {"https://example.com/v1?key=secret", false}, {"https://example.com/v1#fragment", false}, {"file:///tmp/key", false}, {"https://example.com/v1/%2e%2e", false},
	} {
		if err := Validate(Config{BaseURL: tc.url}); (err == nil) != tc.valid {
			t.Errorf("url=%s error=%v", tc.url, err)
		}
	}
}
func TestSavePreservesFutureFieldsAndUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"base_url":"https://example.test/v1","future":{"mode":"enabled"}}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultModel = "model"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if string(fields["future"]) != "{\n    \"mode\": \"enabled\"\n  }" {
		var future map[string]string
		if json.Unmarshal(fields["future"], &future) != nil || future["mode"] != "enabled" {
			t.Fatalf("future field lost: %s", data)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", info.Mode())
	}
}
func TestForeignAndInvalidConfigsAreNeverOverwritten(t *testing.T) {
	for _, raw := range []string{`{"mcpServers":{"foreign":{}}}`, `[1,2]`, `null`, `{broken`} {
		path := filepath.Join(t.TempDir(), "foreign.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("foreign config accepted: %s", raw)
		}
		if err := Save(path, Config{BaseURL: DefaultBaseURL}); err == nil {
			t.Fatalf("foreign config overwritten: %s", raw)
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatalf("file changed: %s", after)
		}
	}
}
