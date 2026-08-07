package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveAndLoadProfileUsesZeroSecretSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	profile := Profile{
		Version:           ProfileVersion,
		BaseURL:           "https://example.com/v0/management/",
		CredentialBackend: CredentialBackendKeyring,
	}
	if err := SaveProfile(dir, profile); err != nil {
		t.Fatalf("SaveProfile() error = %v", err)
	}

	raw, err := os.ReadFile(ProfilePath(dir))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("saved profile is invalid JSON: %v", err)
	}
	if len(document) != 3 {
		t.Fatalf("saved fields = %#v, want exactly version/base_url/credential_backend", document)
	}
	for _, field := range []string{"version", "base_url", "credential_backend"} {
		if _, ok := document[field]; !ok {
			t.Fatalf("saved profile is missing %q: %#v", field, document)
		}
	}
	got, exists, err := LoadProfile(dir)
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if !exists {
		t.Fatal("LoadProfile() exists = false")
	}
	if got.Version != ProfileVersion || got.BaseURL != "https://example.com/v0/management" || got.CredentialBackend != CredentialBackendKeyring {
		t.Fatalf("LoadProfile() = %#v", got)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(ProfilePath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("profile mode = %o, want 600", gotMode)
		}
	}
}

func TestSaveProfileAtomicallyReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	first := Profile{Version: ProfileVersion, BaseURL: "https://one.example/v0/management", CredentialBackend: CredentialBackendKeyring}
	second := Profile{Version: ProfileVersion, BaseURL: "https://two.example/v0/management", CredentialBackend: CredentialBackendKeyring}
	if err := SaveProfile(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(dir, second); err != nil {
		t.Fatalf("replace profile: %v", err)
	}
	got, exists, err := LoadProfile(dir)
	if err != nil || !exists || got != second {
		t.Fatalf("LoadProfile() = %#v, %t, %v; want second profile", got, exists, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".config.json.tmp-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %#v, error = %v", matches, err)
	}
}

func TestLoadProfileMissingAndDeleteProfileIdempotency(t *testing.T) {
	dir := t.TempDir()
	if got, exists, err := LoadProfile(dir); err != nil || exists || got != (Profile{}) {
		t.Fatalf("missing LoadProfile() = %#v, %t, %v", got, exists, err)
	}
	profile := Profile{Version: ProfileVersion, BaseURL: "https://example.com/v0/management", CredentialBackend: CredentialBackendKeyring}
	if err := SaveProfile(dir, profile); err != nil {
		t.Fatal(err)
	}
	if deleted, err := DeleteProfile(dir); err != nil || !deleted {
		t.Fatalf("DeleteProfile() = %t, %v; want true, nil", deleted, err)
	}
	if deleted, err := DeleteProfile(dir); err != nil || deleted {
		t.Fatalf("second DeleteProfile() = %t, %v; want false, nil", deleted, err)
	}
}

func TestProfileRejectsInvalidOrSecretBearingDocuments(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "unsupported version", body: `{"version":99,"base_url":"https://example.com/v0/management","credential_backend":"keyring"}`},
		{name: "invalid URL", body: `{"version":1,"base_url":"https://user:pass@example.com/v0/management","credential_backend":"keyring"}`},
		{name: "unsupported backend", body: `{"version":1,"base_url":"https://example.com/v0/management","credential_backend":"plaintext"}`},
		{name: "unknown account field", body: `{"version":1,"base_url":"https://example.com/v0/management","credential_backend":"keyring","account":"attacker-selected"}`},
		{name: "trailing JSON", body: `{"version":1,"base_url":"https://example.com/v0/management","credential_backend":"keyring"}{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(ProfilePath(dir), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, exists, err := LoadProfile(dir); err == nil || !exists {
				t.Fatalf("LoadProfile() exists = %t, error = %v; want true and an error", exists, err)
			}
		})
	}
}

func TestSaveProfileRejectsInvalidInput(t *testing.T) {
	valid := Profile{Version: ProfileVersion, BaseURL: "https://example.com/v0/management", CredentialBackend: CredentialBackendKeyring}
	tests := []struct {
		name    string
		dir     string
		profile Profile
	}{
		{name: "empty directory", dir: "", profile: valid},
		{name: "unsupported version", dir: t.TempDir(), profile: Profile{Version: 2, BaseURL: valid.BaseURL, CredentialBackend: valid.CredentialBackend}},
		{name: "invalid URL", dir: t.TempDir(), profile: Profile{Version: ProfileVersion, BaseURL: "localhost:8317", CredentialBackend: valid.CredentialBackend}},
		{name: "unsupported backend", dir: t.TempDir(), profile: Profile{Version: ProfileVersion, BaseURL: valid.BaseURL, CredentialBackend: "file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := SaveProfile(tt.dir, tt.profile); err == nil {
				t.Fatal("SaveProfile() error = nil")
			}
		})
	}
}

func TestProfilePath(t *testing.T) {
	dir := filepath.Join("root", "state")
	if got, want := ProfilePath(dir), filepath.Join(dir, "config.json"); got != want {
		t.Fatalf("ProfilePath() = %q, want %q", got, want)
	}
}

func TestLoadProfileReadErrorPreservesExistence(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(ProfilePath(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	_, exists, err := LoadProfile(dir)
	if err == nil || !exists || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadProfile() exists = %t, error = %v", exists, err)
	}
}
