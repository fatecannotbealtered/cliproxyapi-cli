package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/guard"
)

type guardManagementFixture struct {
	mu            sync.Mutex
	disabled      bool
	accountID     string
	quotaBody     string
	quotaStatus   int
	probeStatus   int
	patches       []bool
	apiCallCount  int
	managementKey string
}

func (f *guardManagementFixture) handler(response http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Header.Get("Authorization") != "Bearer "+f.managementKey {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v0/management/auth-files":
		_ = json.NewEncoder(response).Encode(map[string]any{"files": []map[string]any{{
			"id": "codex-user", "auth_index": "idx-1", "name": "codex-user.json", "provider": "codex",
			"disabled": f.disabled, "runtime_only": false,
			"id_token": map[string]any{"chatgpt_account_id": f.accountID},
		}}})
	case request.Method == http.MethodPost && request.URL.Path == "/v0/management/api-call":
		f.apiCallCount++
		if f.probeStatus != 0 {
			response.WriteHeader(f.probeStatus)
			return
		}
		var payload apiCallWireRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.AuthIndex != "idx-1" || payload.URL != codexUsageURL ||
			payload.Header["Chatgpt-Account-Id"] != f.accountID ||
			payload.Header["Authorization"] != "Bearer $TOKEN$" ||
			payload.Header["User-Agent"] != "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal" {
			http.Error(response, "unexpected probe", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"status_code": f.quotaStatus,
			"header":      map[string][]string{"Content-Type": {"application/json"}},
			"body":        f.quotaBody,
		})
	case request.Method == http.MethodPatch && request.URL.Path == "/v0/management/auth-files/status":
		var payload struct {
			Disabled bool `json:"disabled"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		f.disabled = payload.Disabled
		f.patches = append(f.patches, payload.Disabled)
		_, _ = io.WriteString(response, `{"status":"ok"}`)
	default:
		http.Error(response, "not found", http.StatusNotFound)
	}
}

type apiCallWireRequest struct {
	AuthIndex string            `json:"auth_index"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header"`
}

func TestGuardRunOnceObservesWithoutWriting(t *testing.T) {
	reset := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	fixture := &guardManagementFixture{
		accountID:     "acct-1",
		quotaStatus:   http.StatusOK,
		quotaBody:     `{"rate_limit":{"allowed":false,"primary_window":{"used_percent":100,"reset_at":"` + reset + `"}}}`,
		managementKey: "test-key",
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	base := []string{"guard", "run-once", "--base-url", server.URL + "/v0/management", "--state-dir", stateDir, "--compact"}

	exit, stdout, stderr := runCommand(t, base...)
	if exit != 0 || stderr != "" {
		t.Fatalf("observe exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	assertRuntimeUntrustedMatchesReference(t, "guard_result", data)
	if data["summary"].(map[string]any)["suggested"] != float64(1) || len(fixture.patches) != 0 {
		t.Fatalf("observe data=%#v patches=%#v", data, fixture.patches)
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Fatalf("observation created local guard state: entries=%#v err=%v", entries, err)
	}
}

func TestGuardNeverProbesOrDisablesWithoutChatGPTAccountID(t *testing.T) {
	fixture := &guardManagementFixture{
		quotaStatus:   http.StatusOK,
		quotaBody:     `{"rate_limit":{"allowed":false}}`,
		managementKey: "test-key",
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	exit, stdout, _ := runCommand(t, "guard", "run-once", "--base-url", server.URL+"/v0/management", "--state-dir", t.TempDir(), "--compact")
	if exit != 0 || fixture.apiCallCount != 0 || len(fixture.patches) != 0 {
		t.Fatalf("exit=%d api_calls=%d patches=%#v stdout=%s", exit, fixture.apiCallCount, fixture.patches, stdout)
	}
	decisions := decodeEnvelope(t, stdout)["data"].(map[string]any)["decisions"].([]any)
	if len(decisions) != 1 || decisions[0].(map[string]any)["reason"] != "missing_chatgpt_account_id" {
		t.Fatalf("decisions=%#v", decisions)
	}
}

func TestGuardRunOnceIgnoresLocalOwnershipRecords(t *testing.T) {
	fixture := &guardManagementFixture{
		disabled:      true,
		accountID:     "acct-1",
		managementKey: "test-key",
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	stateDir := t.TempDir()
	legacyState := `{"version":1,"records":[{"identity":"codex:idx-1","name":"codex-user.json","auth_index":"idx-1","provider":"codex","fingerprint":"untrusted-local-state","disabled_by_tool":true,"reset_at":"2026-08-01T00:00:00Z","last_state":"confirmed_exhausted"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "guard-state.json"), []byte(legacyState), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")

	exit, stdout, stderr := runCommand(t, "guard", "run-once", "--base-url", server.URL+"/v0/management", "--state-dir", stateDir, "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	decision := decodeEnvelope(t, stdout)["data"].(map[string]any)["decisions"].([]any)[0].(map[string]any)
	if decision["decision"] != "none" || decision["reason"] != "already_disabled" || fixture.apiCallCount != 0 || len(fixture.patches) != 0 {
		t.Fatalf("decision=%#v api_calls=%d patches=%#v", decision, fixture.apiCallCount, fixture.patches)
	}
}

func TestGuardRunOnceRejectsApplyBeforeNetwork(t *testing.T) {
	fixture := &guardManagementFixture{managementKey: "test-key"}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")

	exit, stdout, _ := runCommand(t, "guard", "run-once", "--base-url", server.URL+"/v0/management", "--state-dir", t.TempDir(), "--apply", "--compact")
	if exit != 2 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_USAGE" {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	if fixture.apiCallCount != 0 || len(fixture.patches) != 0 {
		t.Fatalf("rejected apply touched upstream: api_calls=%d patches=%#v", fixture.apiCallCount, fixture.patches)
	}
}

func TestGuardRunOncePartialFailureReturnsFailureEnvelope(t *testing.T) {
	fixture := &guardManagementFixture{
		accountID:     "acct-1",
		probeStatus:   http.StatusServiceUnavailable,
		managementKey: "test-key",
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")

	exit, stdout, stderr := runCommand(t, "guard", "run-once", "--base-url", server.URL+"/v0/management", "--state-dir", t.TempDir(), "--compact")
	if exit != 7 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	if envelope["ok"] != false {
		t.Fatalf("envelope=%#v", envelope)
	}
	errorObject := envelope["error"].(map[string]any)
	if errorObject["code"] != "E_SERVER" {
		t.Fatalf("error=%#v", errorObject)
	}
	details := errorObject["details"].(map[string]any)
	if details["partial_failure"] != true {
		t.Fatalf("details=%#v", details)
	}
	summary := details["summary"].(map[string]any)
	if summary["total"] != float64(1) || summary["failed"] != float64(1) {
		t.Fatalf("summary=%#v", summary)
	}
	decisions := details["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("decisions=%#v", decisions)
	}
	decision := decisions[0].(map[string]any)
	if decision["outcome"] != "failed" || decision["reason"] != "probe_failed" {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestGuardRunOnceDoesNotUseLegacyWriteLock(t *testing.T) {
	fixture := &guardManagementFixture{
		accountID:     "acct-1",
		quotaStatus:   http.StatusOK,
		quotaBody:     `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":25}}}`,
		managementKey: "test-key",
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	stateDir := t.TempDir()
	lease, err := guard.NewFileLock(filepath.Join(stateDir, "guard.lock")).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			t.Errorf("release legacy write lock: %v", err)
		}
	}()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	exit, stdout, stderr := runCommand(t, "guard", "run-once", "--base-url", server.URL+"/v0/management", "--state-dir", stateDir, "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
}
