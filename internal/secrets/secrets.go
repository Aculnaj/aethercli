package secrets

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/zalando/go-keyring"
)

const (
	DefaultService = "aether"
	DefaultAccount = "api-key"
	EnvAPIKey      = "AETHER_API_KEY"
	keyPrefix      = "sk-aetherapi-"
)

var keyPattern = regexp.MustCompile("sk-aetherapi-[^\\s\"'`]+")

var (
	ErrNotFound      = errors.New("secret not found")
	ErrMissingAPIKey = errors.New("missing AetherAPI key")
)

type Store interface {
	Get() (string, error)
	Set(value string) error
	Delete() error
}

type KeyringStore struct {
	Service string
	Account string
}

func DefaultStore() Store {
	return KeyringStore{
		Service: DefaultService,
		Account: DefaultAccount,
	}
}

func (s KeyringStore) Get() (string, error) {
	value, err := keyring.Get(s.service(), s.account())
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

func (s KeyringStore) Set(value string) error {
	return keyring.Set(s.service(), s.account(), value)
}

func (s KeyringStore) Delete() error {
	err := keyring.Delete(s.service(), s.account())
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func (s KeyringStore) service() string {
	if s.Service == "" {
		return DefaultService
	}
	return s.Service
}

func (s KeyringStore) account() string {
	if s.Account == "" {
		return DefaultAccount
	}
	return s.Account
}

func ValidateAPIKey(key string) error {
	key, _ = normalizeAPIKeyValue(key)
	if key == "" {
		return fmt.Errorf("API key is empty")
	}
	if !strings.HasPrefix(key, keyPrefix) || len(key) == len(keyPrefix) {
		return fmt.Errorf("API key must start with %q", keyPrefix)
	}
	for _, r := range key {
		if unicode.IsSpace(r) {
			return fmt.Errorf("API key must not contain whitespace")
		}
	}
	return nil
}

func NormalizeAPIKey(key string) (string, error) {
	key, found := normalizeAPIKeyValue(key)
	if !found {
		return "", fmt.Errorf("API key must start with %q", keyPrefix)
	}
	if err := ValidateAPIKey(key); err != nil {
		return "", err
	}
	return key, nil
}

func ResolveAPIKey(store Store) (string, error) {
	if key := strings.TrimSpace(os.Getenv(EnvAPIKey)); key != "" {
		normalized, err := NormalizeAPIKey(key)
		if err != nil {
			return "", err
		}
		return normalized, nil
	}

	key, err := store.Get()
	if errors.Is(err, ErrNotFound) {
		return "", ErrMissingAPIKey
	}
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ErrMissingAPIKey
	}
	normalized, err := NormalizeAPIKey(key)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func normalizeAPIKeyValue(key string) (string, bool) {
	key = strings.TrimSpace(stripInvisiblePasteRunes(key))
	key = strings.Trim(key, `"'`+"`")
	key = strings.TrimSpace(key)

	if strings.HasPrefix(key, "export ") {
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
	}
	if strings.HasPrefix(key, EnvAPIKey+"=") {
		key = strings.TrimSpace(strings.TrimPrefix(key, EnvAPIKey+"="))
		key = strings.Trim(key, `"'`+"`")
	}
	if strings.HasPrefix(strings.ToLower(key), "authorization:") {
		key = strings.TrimSpace(key[len("authorization:"):])
	}
	if strings.HasPrefix(strings.ToLower(key), "bearer ") {
		key = strings.TrimSpace(key[len("bearer "):])
	}
	key = strings.Trim(key, `"'`+"`")

	if strings.HasPrefix(key, keyPrefix) {
		return key, true
	}
	if match := keyPattern.FindString(key); match != "" {
		return match, true
	}
	return key, false
}

func stripInvisiblePasteRunes(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\uFEFF', '\u200B', '\u200C', '\u200D':
			return -1
		default:
			return r
		}
	}, value)
}
