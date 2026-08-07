// Package credential stores management credentials outside the CLI config file.
package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

const ServiceName = "cliproxyapi-cli"

var ErrNotFound = errors.New("credential not found")

// Store is the minimal credential persistence boundary used by commands.
type Store interface {
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// OSStore persists credentials in the current user's operating-system keyring.
type OSStore struct{}

func NewOSStore() Store {
	return OSStore{}
}

func (OSStore) Get(account string) (string, error) {
	secret, err := keyring.Get(ServiceName, account)
	if err != nil {
		return "", mapKeyringError("get", err)
	}
	return secret, nil
}

func (OSStore) Set(account, secret string) error {
	return mapKeyringError("set", keyring.Set(ServiceName, account, secret))
}

func (OSStore) Delete(account string) error {
	return mapKeyringError("delete", keyring.Delete(ServiceName, account))
}

// Account derives a non-identifying keyring account from a normalized base URL.
func Account(baseURL string) string {
	sum := sha256.Sum256([]byte(baseURL))
	return "management:" + hex.EncodeToString(sum[:])
}

func mapKeyringError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("%s credential: %w", operation, err)
}
