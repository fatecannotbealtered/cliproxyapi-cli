package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/credential"
)

type fakeCredentialStore struct {
	secrets map[string]string
	gets    []string
}

func (s *fakeCredentialStore) Get(account string) (string, error) {
	s.gets = append(s.gets, account)
	secret, ok := s.secrets[account]
	if !ok {
		return "", credential.ErrNotFound
	}
	return secret, nil
}

func (s *fakeCredentialStore) Set(account, secret string) error {
	if s.secrets == nil {
		s.secrets = map[string]string{}
	}
	s.secrets[account] = secret
	return nil
}

func (s *fakeCredentialStore) Delete(account string) error {
	if _, ok := s.secrets[account]; !ok {
		return credential.ErrNotFound
	}
	delete(s.secrets, account)
	return nil
}

func TestLoadUsesSafeDefaultsAndEnvironmentKey(t *testing.T) {
	env := map[string]string{EnvManagementKey: "secret-value", EnvStateDir: t.TempDir()}
	cfg, err := Load(LoadOptions{Getenv: func(key string) string { return env[key] }})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL || cfg.ManagementKey != "secret-value" || cfg.CredentialSource != "env" {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %s", cfg.Timeout)
	}
	if fingerprint := cfg.CredentialFingerprint(); fingerprint == "" || strings.Contains(fingerprint, "secret") {
		t.Fatalf("unsafe fingerprint %q", fingerprint)
	}
}

func TestLoadStdinOverridesEnvironmentWithoutMultilineSecrets(t *testing.T) {
	cfg, err := Load(LoadOptions{
		Getenv:           func(string) string { return "from-env" },
		ReadKeyFromStdin: true,
		Stdin:            strings.NewReader("from-stdin\n"),
		BaseURL:          "https://example.com/v0/management/",
		Timeout:          3 * time.Second,
		StateDir:         t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ManagementKey != "from-stdin" || cfg.CredentialSource != "stdin" {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.BaseURL != "https://example.com/v0/management" {
		t.Fatalf("base URL = %q", cfg.BaseURL)
	}
	if _, err := Load(LoadOptions{ReadKeyFromStdin: true, Stdin: strings.NewReader("one\ntwo\n"), StateDir: t.TempDir()}); err == nil {
		t.Fatal("multiline management key was accepted")
	}
}

func TestLoadRejectsUnsafeBaseURL(t *testing.T) {
	for _, raw := range []string{"localhost:8317", "ftp://example.com/v0/management", "https://user:pass@example.com/v0/management", "https://example.com/v0/management?q=x"} {
		t.Run(raw, func(t *testing.T) {
			_, err := Load(LoadOptions{BaseURL: raw, StateDir: t.TempDir()})
			if err == nil {
				t.Fatalf("Load(%q) error = nil", raw)
			}
		})
	}
}

func TestLoadFallsBackToSavedProfileAndKeyring(t *testing.T) {
	stateDir := t.TempDir()
	baseURL := "https://saved.example/v0/management"
	if err := SaveProfile(stateDir, Profile{Version: ProfileVersion, BaseURL: baseURL, CredentialBackend: CredentialBackendKeyring}); err != nil {
		t.Fatalf("SaveProfile() error = %v", err)
	}
	store := &fakeCredentialStore{secrets: map[string]string{credential.Account(baseURL): "saved-secret"}}

	cfg, err := Load(LoadOptions{
		StateDir:        stateDir,
		Getenv:          func(string) string { return "" },
		CredentialStore: store,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BaseURL != baseURL || cfg.ManagementKey != "saved-secret" || cfg.CredentialSource != "keyring" {
		t.Fatalf("config = %#v", cfg)
	}
	if len(store.gets) != 1 || store.gets[0] != credential.Account(baseURL) {
		t.Fatalf("keyring gets = %#v", store.gets)
	}
}

func TestLoadCredentialPrecedenceIsStdinThenEnvironmentThenKeyring(t *testing.T) {
	stateDir := t.TempDir()
	baseURL := "https://saved.example/v0/management"
	if err := SaveProfile(stateDir, Profile{Version: ProfileVersion, BaseURL: baseURL, CredentialBackend: CredentialBackendKeyring}); err != nil {
		t.Fatalf("SaveProfile() error = %v", err)
	}
	store := &fakeCredentialStore{secrets: map[string]string{credential.Account(baseURL): "saved-secret"}}

	envCfg, err := Load(LoadOptions{
		StateDir: stateDir,
		Getenv: func(key string) string {
			if key == EnvManagementKey {
				return "environment-secret"
			}
			return ""
		},
		CredentialStore: store,
	})
	if err != nil {
		t.Fatalf("Load(env) error = %v", err)
	}
	if envCfg.ManagementKey != "environment-secret" || envCfg.CredentialSource != "env" || len(store.gets) != 0 {
		t.Fatalf("env config = %#v gets=%#v", envCfg, store.gets)
	}

	stdinCfg, err := Load(LoadOptions{
		StateDir: stateDir,
		Getenv: func(key string) string {
			if key == EnvManagementKey {
				return "environment-secret"
			}
			return ""
		},
		CredentialStore:  store,
		ReadKeyFromStdin: true,
		Stdin:            strings.NewReader("stdin-secret\n"),
	})
	if err != nil {
		t.Fatalf("Load(stdin) error = %v", err)
	}
	if stdinCfg.ManagementKey != "stdin-secret" || stdinCfg.CredentialSource != "stdin" || len(store.gets) != 0 {
		t.Fatalf("stdin config = %#v gets=%#v", stdinCfg, store.gets)
	}
}

func TestLoadNeverReusesSavedKeyForAnotherBaseURL(t *testing.T) {
	stateDir := t.TempDir()
	savedBaseURL := "https://saved.example/v0/management"
	if err := SaveProfile(stateDir, Profile{Version: ProfileVersion, BaseURL: savedBaseURL, CredentialBackend: CredentialBackendKeyring}); err != nil {
		t.Fatalf("SaveProfile() error = %v", err)
	}
	store := &fakeCredentialStore{secrets: map[string]string{credential.Account(savedBaseURL): "saved-secret"}}

	for name, options := range map[string]LoadOptions{
		"flag": {
			BaseURL:         "https://other.example/v0/management",
			StateDir:        stateDir,
			Getenv:          func(string) string { return "" },
			CredentialStore: store,
		},
		"environment": {
			StateDir: stateDir,
			Getenv: func(key string) string {
				if key == EnvBaseURL {
					return "https://other.example/v0/management"
				}
				return ""
			},
			CredentialStore: store,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store.gets = nil
			cfg, err := Load(options)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.ManagementKey != "" || cfg.CredentialSource != "" {
				t.Fatalf("config = %#v", cfg)
			}
			if len(store.gets) != 0 {
				t.Fatalf("saved key was read for another base URL: %#v", store.gets)
			}
		})
	}
}

func TestLoadPropagatesKeyringFailureWithoutLeakingSecret(t *testing.T) {
	stateDir := t.TempDir()
	baseURL := "https://saved.example/v0/management"
	if err := SaveProfile(stateDir, Profile{Version: ProfileVersion, BaseURL: baseURL, CredentialBackend: CredentialBackendKeyring}); err != nil {
		t.Fatalf("SaveProfile() error = %v", err)
	}
	store := failingCredentialStore{err: errors.New("keyring unavailable")}
	if _, err := Load(LoadOptions{StateDir: stateDir, Getenv: func(string) string { return "" }, CredentialStore: store}); err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("Load() error = %v", err)
	}
}

type failingCredentialStore struct{ err error }

func (s failingCredentialStore) Get(string) (string, error) { return "", s.err }
func (s failingCredentialStore) Set(string, string) error   { return s.err }
func (s failingCredentialStore) Delete(string) error        { return s.err }
