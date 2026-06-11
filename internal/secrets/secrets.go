package secrets

import (
	"errors"
	"fmt"
	"os"
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

func ResolveAPIKey(store Store) (string, error) {
	if key := strings.TrimSpace(os.Getenv(EnvAPIKey)); key != "" {
		if err := ValidateAPIKey(key); err != nil {
			return "", err
		}
		return key, nil
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
	if err := ValidateAPIKey(key); err != nil {
		return "", err
	}
	return key, nil
}
