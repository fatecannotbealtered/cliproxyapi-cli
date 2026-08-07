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
		if payload.AuthIndex != "idx-1" || payload.URL != codexUsageURL || payload.Header["Chatgpt-Account-Id"] != f.accountID || payload.Header["Authorization"] != "Bearer $TOKEN$" {
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

func TestGuardRunOnceObservesThenAppliesAndRestoresOwnedAccount(t *testing.T) {
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
	if data["apply"] != false || data["summary"].(map[string]any)["suggested"] != float64(1) || len(fixture.patches) != 0 {
		t.Fatalf("observe data=%#v patches=%#v", data, fixture.patches)
	}

	apply := append(append([]string{}, base...), "--apply")
	exit, stdout, _ = runCommand(t, apply...)
	if exit != 0 || len(fixture.patches) != 1 || !fixture.patches[0] {
		t.Fatalf("disable exit=%d patches=%#v stdout=%s", exit, fixture.patches, stdout)
	}
	if _, err := os.Stat(filepath.Join(stateDir, guardStateFile)); err != nil {
		t.Fatalf("guard state was not persisted: %v", err)
	}

	fixture.mu.Lock()
	fixture.quotaBody = `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":50,"reset_at":"` + reset + `"}}}`
	fixture.mu.Unlock()
	exit, stdout, _ = runCommand(t, apply...)
	if exit != 0 || len(fixture.patches) != 2 || fixture.patches[1] {
		t.Fatalf("restore exit=%d patches=%#v stdout=%s", exit, fixture.patches, stdout)
	}

	exit, stdout, _ = runCommand(t, "guard", "state", "--state-dir", stateDir, "--compact")
	if exit != 0 || decodeEnvelope(t, stdout)["data"].(map[string]any)["count"] != float64(0) {
		t.Fatalf("state exit=%d stdout=%s", exit, stdout)
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
	exit, stdout, _ := runCommand(t, "guard", "run-once", "--base-url", server.URL+"/v0/management", "--state-dir", t.TempDir(), "--apply", "--compact")
	if exit != 0 || fixture.apiCallCount != 0 || len(fixture.patches) != 0 {
		t.Fatalf("exit=%d api_calls=%d patches=%#v stdout=%s", exit, fixture.apiCallCount, fixture.patches, stdout)
	}
	decisions := decodeEnvelope(t, stdout)["data"].(map[string]any)["decisions"].([]any)
	if len(decisions) != 1 || decisions[0].(map[string]any)["reason"] != "missing_chatgpt_account_id" {
		t.Fatalf("decisions=%#v", decisions)
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

func TestGuardLockConflictIsStructured(t *testing.T) {
	stateDir := t.TempDir()
	lease, err := guard.NewFileLock(filepath.Join(stateDir, guardLockFile)).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			t.Errorf("release lock: %v", err)
		}
	}()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	exit, stdout, _ := runCommand(t, "guard", "run-once", "--base-url", "http://127.0.0.1:1/v0/management", "--state-dir", stateDir, "--compact")
	if exit != 6 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errorObject["code"] != "E_CONFLICT" {
		t.Fatalf("error=%#v", errorObject)
	}
}
