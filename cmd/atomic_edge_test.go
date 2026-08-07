package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestQuotaInspectReportsConfirmedExhaustionFromStructuredCodexSignal(t *testing.T) {
	const resetAt = "2026-08-05T12:30:00Z"
	var apiCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			assertCommandManagementRequest(t, r, http.MethodGet, r.URL.Path)
			writeCommandJSON(t, w, map[string]any{"files": []any{
				map[string]any{
					"id": "codex-1", "auth_index": "idx-1", "name": "exhausted.json", "provider": "codex",
					"id_token": map[string]any{"chatgpt_account_id": "acct-1"},
				},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/api-call":
			apiCalls.Add(1)
			assertCommandManagementRequest(t, r, http.MethodPost, r.URL.Path)
			var request struct {
				AuthIndex string            `json:"auth_index"`
				Method    string            `json:"method"`
				URL       string            `json:"url"`
				Header    map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode api-call request: %v", err)
			}
			if request.AuthIndex != "idx-1" || request.Method != http.MethodGet || request.URL != codexUsageURL {
				t.Fatalf("api-call request = %#v", request)
			}
			if request.Header["Authorization"] != "Bearer $TOKEN$" || request.Header["Chatgpt-Account-Id"] != "acct-1" {
				t.Fatalf("api-call headers = %#v", request.Header)
			}
			writeCommandJSON(t, w, map[string]any{
				"status_code": http.StatusOK,
				"header":      map[string][]string{"Content-Type": {"application/json"}},
				"body": `{
					"rate_limit": {
						"allowed": false,
						"primary_window": {
							"used_percent": 100,
							"reset_at": "` + resetAt + `"
						}
					}
				}`,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, stderr := runCommand(t, "quota", "inspect", "--provider", "codex", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if apiCalls.Load() != 1 {
		t.Fatalf("api-call count = %d, want 1", apiCalls.Load())
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 1 || data["count"] != float64(1) {
		t.Fatalf("data = %#v", data)
	}
	item := items[0].(map[string]any)
	if item["state"] != "confirmed_exhausted" || item["reset_at"] != resetAt || item["used_percent"] != float64(100) {
		t.Fatalf("item = %#v", item)
	}
	evidence := item["evidence"].(map[string]any)
	rateLimit := evidence["rate_limit"].(map[string]any)
	if rateLimit["allowed"] != false {
		t.Fatalf("evidence = %#v", evidence)
	}
	primary := rateLimit["primary_window"].(map[string]any)
	if primary["used_percent"] != float64(100) || primary["reset_at"] != resetAt {
		t.Fatalf("primary evidence = %#v", primary)
	}
}

func TestAuthFileSetStatusDryRunRejectsAmbiguousNameBeforePatch(t *testing.T) {
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			assertCommandManagementRequest(t, r, http.MethodGet, r.URL.Path)
			writeCommandJSON(t, w, map[string]any{"files": []any{
				map[string]any{"id": "one", "auth_index": "idx-1", "name": "shared.json", "provider": "codex", "disabled": false},
				map[string]any{"id": "two", "auth_index": "idx-2", "name": "shared.json", "provider": "codex", "disabled": false},
			}})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			patches.Add(1)
			writeCommandJSON(t, w, map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, _ := runCommand(t, "auth-file", "set-status", "--name", "shared.json", "--disabled=true", "--dry-run", "--compact")
	if exit != 6 {
		t.Fatalf("exit=%d stdout=%s, want 6", exit, stdout)
	}
	errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errorObject["code"] != "E_CONFLICT" {
		t.Fatalf("error = %#v", errorObject)
	}
	if patches.Load() != 0 {
		t.Fatalf("ambiguous dry-run made %d PATCH requests", patches.Load())
	}
}
