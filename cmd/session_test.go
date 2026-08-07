package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/config"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/credential"
)

type memoryCredentialStore struct {
	secrets   map[string]string
	setErr    error
	deleteErr error
}

func (s *memoryCredentialStore) Get(account string) (string, error) {
	secret, ok := s.secrets[account]
	if !ok {
		return "", credential.ErrNotFound
	}
	return secret, nil
}

func (s *memoryCredentialStore) Set(account, secret string) error {
	if s.setErr != nil {
		return s.setErr
	}
	if s.secrets == nil {
		s.secrets = map[string]string{}
	}
	s.secrets[account] = secret
	return nil
}

func (s *memoryCredentialStore) Delete(account string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if _, ok := s.secrets[account]; !ok {
		return credential.ErrNotFound
	}
	delete(s.secrets, account)
	return nil
}

func runCommandWithStore(t *testing.T, store credential.Store, stdin string, args ...string) (int, []byte, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := executeArgsWithCredentialStore(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, store)
	return exit, stdout.Bytes(), stderr.String()
}

func TestLoginPersistsKeyringSessionAndLogoutRemovesIt(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(config.EnvStateDir, stateDir)
	t.Setenv(config.EnvBaseURL, "")
	t.Setenv(config.EnvManagementKey, "")

	const managementKey = "one-time-session-secret"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/v0/management/auth-files" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+managementKey {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(response, `{"files":[]}`)
	}))
	defer server.Close()

	store := &memoryCredentialStore{secrets: map[string]string{}}
	baseURL := server.URL + "/v0/management"
	exit, stdout, stderr := runCommandWithStore(t, store, managementKey+"\n", "login", "--base-url", baseURL, "--management-key-stdin", "--dry-run", "--compact")
	if exit != 0 || stderr != "" || bytes.Contains(stdout, []byte(managementKey)) {
		t.Fatalf("dry-run exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if len(store.secrets) != 0 {
		t.Fatalf("dry-run wrote keyring: %#v", store.secrets)
	}
	if _, err := os.Stat(config.ProfilePath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run profile stat error = %v", err)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)

	exit, stdout, stderr = runCommandWithStore(t, store, managementKey+"\n", "login", "--base-url", baseURL, "--management-key-stdin", "--confirm", token, "--compact")
	if exit != 0 || stderr != "" || bytes.Contains(stdout, []byte(managementKey)) {
		t.Fatalf("confirm exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["configured"] != true || data["verified"] != true || data["credential_backend"] != "keyring" || data["base_url"] != baseURL {
		t.Fatalf("login data = %#v", data)
	}
	if store.secrets[credential.Account(baseURL)] != managementKey {
		t.Fatalf("stored secret = %#v", store.secrets)
	}
	profileBytes, err := os.ReadFile(config.ProfilePath(stateDir))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	for _, forbidden := range []string{managementKey, "fingerprint", "account"} {
		if bytes.Contains(profileBytes, []byte(forbidden)) {
			t.Fatalf("profile contains %q: %s", forbidden, profileBytes)
		}
	}

	exit, stdout, stderr = runCommandWithStore(t, store, "", "context", "--compact")
	if exit != 0 || stderr != "" || bytes.Contains(stdout, []byte(managementKey)) {
		t.Fatalf("context exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	contextData := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if contextData["target"].(map[string]any)["base_url"] != baseURL {
		t.Fatalf("context target = %#v", contextData["target"])
	}
	credentials := contextData["credentials"].(map[string]any)
	if credentials["configured"] != true || credentials["source"] != "keyring" {
		t.Fatalf("credentials = %#v", credentials)
	}

	exit, stdout, _ = runCommandWithStore(t, store, "", "context", "--base-url", "https://other.example/v0/management", "--compact")
	if exit != 0 {
		t.Fatalf("override context exit=%d stdout=%s", exit, stdout)
	}
	overrideCredentials := decodeEnvelope(t, stdout)["data"].(map[string]any)["credentials"].(map[string]any)
	if overrideCredentials["configured"] != false || overrideCredentials["source"] != "" {
		t.Fatalf("override credentials = %#v", overrideCredentials)
	}

	exit, stdout, stderr = runCommandWithStore(t, store, "", "doctor", "--compact")
	if exit != 0 || stderr != "" || bytes.Contains(stdout, []byte(managementKey)) {
		t.Fatalf("doctor exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if requests != 3 {
		t.Fatalf("management requests = %d, want 3", requests)
	}

	exit, stdout, stderr = runCommandWithStore(t, store, "", "logout", "--dry-run", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("logout dry-run exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	logoutToken := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	exit, stdout, stderr = runCommandWithStore(t, store, "", "logout", "--confirm", logoutToken, "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("logout confirm exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if decodeEnvelope(t, stdout)["data"].(map[string]any)["removed"] != true {
		t.Fatalf("logout data = %#v", decodeEnvelope(t, stdout)["data"])
	}
	if len(store.secrets) != 0 {
		t.Fatalf("logout left keyring entries: %#v", store.secrets)
	}
	if _, err := os.Stat(config.ProfilePath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logout profile stat error = %v", err)
	}

	exit, stdout, _ = runCommandWithStore(t, store, "", "context", "--compact")
	if exit != 0 {
		t.Fatalf("post-logout context exit=%d stdout=%s", exit, stdout)
	}
	postLogoutCredentials := decodeEnvelope(t, stdout)["data"].(map[string]any)["credentials"].(map[string]any)
	if postLogoutCredentials["configured"] != false || postLogoutCredentials["source"] != "" {
		t.Fatalf("post-logout credentials = %#v", postLogoutCredentials)
	}

	exit, stdout, _ = runCommandWithStore(t, store, "", "logout", "--dry-run", "--compact")
	if exit != 0 {
		t.Fatalf("idempotent logout dry-run exit=%d stdout=%s", exit, stdout)
	}
	idempotentToken := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	exit, stdout, _ = runCommandWithStore(t, store, "", "logout", "--confirm", idempotentToken, "--compact")
	if exit != 0 || decodeEnvelope(t, stdout)["data"].(map[string]any)["removed"] != false {
		t.Fatalf("idempotent logout confirm exit=%d stdout=%s", exit, stdout)
	}
}

func TestLoginRequiresGateAndRejectsInvalidCredentialWithoutPersistence(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(config.EnvStateDir, stateDir)
	t.Setenv(config.EnvBaseURL, "")
	t.Setenv(config.EnvManagementKey, "")
	store := &memoryCredentialStore{secrets: map[string]string{}}

	exit, stdout, _ := runCommandWithStore(t, store, "", "login", "--compact")
	if exit != 5 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFIRMATION_REQUIRED" {
		t.Fatalf("ungated login exit=%d stdout=%s", exit, stdout)
	}

	const rejectedKey = "rejected-management-secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(response, `{"error":"`+rejectedKey+`"}`)
	}))
	defer server.Close()
	exit, stdout, _ = runCommandWithStore(t, store, rejectedKey+"\n", "login", "--base-url", server.URL+"/v0/management", "--management-key-stdin", "--dry-run", "--compact")
	if exit != 4 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_AUTH" || bytes.Contains(stdout, []byte(rejectedKey)) {
		t.Fatalf("invalid login exit=%d stdout=%s", exit, stdout)
	}
	if len(store.secrets) != 0 {
		t.Fatalf("invalid login wrote keyring: %#v", store.secrets)
	}
	if _, err := os.Stat(config.ProfilePath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid login profile stat error = %v", err)
	}
}

func TestLoginConfirmationBindsManagementKey(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(config.EnvStateDir, stateDir)
	t.Setenv(config.EnvBaseURL, "")
	t.Setenv(config.EnvManagementKey, "")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"files":[]}`)
	}))
	defer server.Close()
	store := &memoryCredentialStore{secrets: map[string]string{}}
	baseURL := server.URL + "/v0/management"

	exit, stdout, _ := runCommandWithStore(t, store, "first-secret\n", "login", "--base-url", baseURL, "--management-key-stdin", "--dry-run", "--compact")
	if exit != 0 {
		t.Fatalf("dry-run exit=%d stdout=%s", exit, stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	exit, stdout, _ = runCommandWithStore(t, store, "second-secret\n", "login", "--base-url", baseURL, "--management-key-stdin", "--confirm", token, "--compact")
	if exit != 6 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFLICT" {
		t.Fatalf("mismatched confirm exit=%d stdout=%s", exit, stdout)
	}
	if len(store.secrets) != 0 {
		t.Fatalf("mismatched confirm wrote keyring: %#v", store.secrets)
	}
}

func TestLoginKeyringFailureDoesNotWriteProfile(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(config.EnvStateDir, stateDir)
	t.Setenv(config.EnvBaseURL, "")
	t.Setenv(config.EnvManagementKey, "")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"files":[]}`)
	}))
	defer server.Close()
	const managementKey = "keyring-failure-secret"
	store := &memoryCredentialStore{secrets: map[string]string{}, setErr: errors.New("credential manager unavailable")}
	baseURL := server.URL + "/v0/management"

	exit, stdout, _ := runCommandWithStore(t, store, managementKey+"\n", "login", "--base-url", baseURL, "--management-key-stdin", "--dry-run", "--compact")
	if exit != 0 {
		t.Fatalf("dry-run exit=%d stdout=%s", exit, stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	exit, stdout, _ = runCommandWithStore(t, store, managementKey+"\n", "login", "--base-url", baseURL, "--management-key-stdin", "--confirm", token, "--compact")
	if exit != 4 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFIG" || bytes.Contains(stdout, []byte(managementKey)) {
		t.Fatalf("confirm exit=%d stdout=%s", exit, stdout)
	}
	if _, err := os.Stat(config.ProfilePath(stateDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed keyring write left a profile: %v", err)
	}
}
