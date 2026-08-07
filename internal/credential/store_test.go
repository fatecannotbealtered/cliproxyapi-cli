package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

func TestAccountHashesNormalizedBaseURL(t *testing.T) {
	baseURL := "https://example.com/v0/management"
	sum := sha256.Sum256([]byte(baseURL))
	want := "management:" + hex.EncodeToString(sum[:])

	got := Account(baseURL)
	if got != want {
		t.Fatalf("Account() = %q, want %q", got, want)
	}
	if strings.Contains(got, "example.com") {
		t.Fatalf("Account() exposes base URL: %q", got)
	}
}

func TestOSStoreRoundTripAndNotFoundMapping(t *testing.T) {
	keyring.MockInit()
	store := NewOSStore()
	account := Account("https://example.com/v0/management")

	if err := store.Set(account, "management-secret"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	secret, err := store.Get(account)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if secret != "management-secret" {
		t.Fatalf("Get() = %q", secret)
	}
	if err := store.Delete(account); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after Delete error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete() error = %v, want ErrNotFound", err)
	}
}

func TestOSStoreImplementsStore(t *testing.T) {
	var _ Store = OSStore{}
}
