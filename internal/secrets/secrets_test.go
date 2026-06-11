package secrets

import (
	"errors"
	"testing"
)

type fakeStore struct {
	value     string
	getErr    error
	getCalled bool
}

func (f *fakeStore) Get() (string, error) {
	f.getCalled = true
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.value, nil
}

func (f *fakeStore) Set(value string) error {
	f.value = value
	return nil
}

func (f *fakeStore) Delete() error {
	f.value = ""
	return nil
}

func TestValidateAPIKeyFormat(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{name: "valid", key: "sk-aetherapi-abcdefghijklmnopqrstuvwxyz123456", ok: true},
		{name: "missing prefix", key: "sk-openai-abcdefghijklmnopqrstuvwxyz123456", ok: false},
		{name: "empty suffix", key: "sk-aetherapi-", ok: false},
		{name: "contains whitespace", key: "sk-aetherapi-abc def", ok: false},
		{name: "empty", key: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAPIKey(tt.key)
			if tt.ok && err != nil {
				t.Fatalf("ValidateAPIKey returned error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("ValidateAPIKey returned nil error for invalid key")
			}
		})
	}
}

func TestResolveAPIKeyPrefersEnvironment(t *testing.T) {
	const envKey = "sk-aetherapi-from-env"
	store := &fakeStore{value: "sk-aetherapi-from-keychain"}
	t.Setenv("AETHER_API_KEY", envKey)

	got, err := ResolveAPIKey(store)
	if err != nil {
		t.Fatalf("ResolveAPIKey returned error: %v", err)
	}
	if got != envKey {
		t.Fatalf("ResolveAPIKey = %q, want env key", got)
	}
	if store.getCalled {
		t.Fatal("ResolveAPIKey read keychain even though env var was set")
	}
}

func TestResolveAPIKeyFallsBackToStore(t *testing.T) {
	store := &fakeStore{value: "sk-aetherapi-from-keychain"}

	got, err := ResolveAPIKey(store)
	if err != nil {
		t.Fatalf("ResolveAPIKey returned error: %v", err)
	}
	if got != store.value {
		t.Fatalf("ResolveAPIKey = %q, want store key", got)
	}
	if !store.getCalled {
		t.Fatal("ResolveAPIKey did not read the keychain store")
	}
}

func TestResolveAPIKeyReportsMissingKey(t *testing.T) {
	store := &fakeStore{getErr: ErrNotFound}

	_, err := ResolveAPIKey(store)
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("ResolveAPIKey error = %v, want ErrMissingAPIKey", err)
	}
}
