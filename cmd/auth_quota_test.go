package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAuthFileListFiltersSortsAndMarksUntrusted(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assertCommandManagementRequest(t, r, http.MethodGet, "/v0/management/auth-files")
		writeCommandJSON(t, w, map[string]any{"files": []any{
			map[string]any{"id": "z", "auth_index": "idx-z", "name": "Zulu.json", "provider": "codex", "disabled": true, "account": "external-z", "email": "z@example.test", "status_message": "external status z"},
			map[string]any{"id": "a", "auth_index": "idx-a", "name": "alpha.json", "provider": "CODEX", "disabled": true, "account": "external-a", "email": "a@example.test", "status_message": "external status a"},
			map[string]any{"id": "enabled", "auth_index": "idx-enabled", "name": "enabled.json", "provider": "codex", "disabled": false},
			map[string]any{"id": "claude", "auth_index": "idx-claude", "name": "claude.json", "provider": "claude", "disabled": true},
		}})
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, stderr := runCommand(t, "auth-file", "list", "--provider", "codex", "--disabled=true", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["name"] != "alpha.json" || items[1].(map[string]any)["name"] != "Zulu.json" {
		t.Fatalf("items = %#v, want filtered case-insensitive name order", items)
	}
	if data["count"] != float64(2) || data["has_more"] != false || data["next_cursor"] != nil {
		t.Fatalf("list shape = %#v", data)
	}
	untrusted := stringSliceFromAny(t, data["_untrusted"])
	for _, field := range []string{"items.name", "items.account", "items.email", "items.status_message"} {
		if !containsString(untrusted, field) {
			t.Errorf("_untrusted = %#v, missing %q", untrusted, field)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestAuthFileListPreservesMissingDisabledState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommandManagementRequest(t, r, http.MethodGet, "/v0/management/auth-files")
		writeCommandJSON(t, w, map[string]any{"files": []any{
			map[string]any{"name": "fallback.json", "type": "codex", "email": "fallback@example.test"},
			map[string]any{"id": "enabled", "auth_index": "idx-enabled", "name": "enabled.json", "provider": "codex", "disabled": false},
		}})
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, stderr := runCommand(t, "auth-file", "list", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	items := decodeEnvelope(t, stdout)["data"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	fallback := items[1].(map[string]any)
	if fallback["name"] != "fallback.json" {
		t.Fatalf("fallback = %#v", fallback)
	}
	if _, exists := fallback["disabled"]; exists {
		t.Fatalf("fallback = %#v, missing disabled was fabricated", fallback)
	}

	exit, stdout, stderr = runCommand(t, "auth-file", "list", "--disabled=false", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("filtered exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	items = data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "enabled.json" {
		t.Fatalf("filtered items = %#v, fallback state must remain unknown", items)
	}
}

func TestAuthFileSetStatusRequiresConfirmationBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, _ := runCommand(t, "auth-file", "set-status", "--name", "account.json", "--disabled=true", "--compact")
	if exit != 5 {
		t.Fatalf("exit=%d stdout=%s, want 5", exit, stdout)
	}
	errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errorObject["code"] != "E_CONFIRMATION_REQUIRED" {
		t.Fatalf("error = %#v", errorObject)
	}
	if requests.Load() != 0 {
		t.Fatalf("missing confirmation made %d requests", requests.Load())
	}
}

func TestAuthFileSetStatusDryRunConfirmVerifyAndRejectReplay(t *testing.T) {
	serverState := newAuthStatusServerState([]authStatusFixture{
		{ID: "id-1", AuthIndex: "idx-1", Name: "account.json", Provider: "codex", UpdatedAt: "2026-08-05T12:00:00Z"},
		{ID: "id-2", AuthIndex: "idx-2", Name: "account.json", Provider: "codex", UpdatedAt: "2026-08-05T12:00:00Z"},
	})
	server := httptest.NewServer(serverState)
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, stderr := runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--dry-run", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("dry-run exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	dryData := decodeEnvelope(t, stdout)["data"].(map[string]any)
	token, _ := dryData["confirm_token"].(string)
	if token == "" {
		t.Fatalf("dry-run data = %#v, missing token", dryData)
	}
	preview := dryData["preview"].(map[string]any)
	if preview["name"] != "account.json" || preview["auth_index"] != "idx-1" || preview["current_disabled"] != false || preview["current_disabled_present"] != true || preview["target_disabled"] != true {
		t.Fatalf("preview = %#v", preview)
	}
	version := preview["version"].(map[string]any)
	if version["id"] != "id-1" || version["id_present"] != true || version["updated_at"] != "2026-08-05T12:00:00Z" || version["updated_at_present"] != true {
		t.Fatalf("version = %#v", version)
	}

	exit, stdout, stderr = runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--confirm", token, "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("confirm exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	confirmed := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if confirmed["name"] != "account.json" || confirmed["auth_index"] != "idx-1" || confirmed["disabled"] != true || confirmed["verified"] != true {
		t.Fatalf("confirmed = %#v", confirmed)
	}
	if serverState.patchCount() != 1 || serverState.lastPatchedAuthIndex() != "idx-1" {
		t.Fatalf("patch count=%d auth_index=%q", serverState.patchCount(), serverState.lastPatchedAuthIndex())
	}
	serverState.setState("idx-1", false, "2026-08-05T12:00:00Z")

	exit, stdout, _ = runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--confirm", token, "--compact")
	if exit != 6 {
		t.Fatalf("replay exit=%d stdout=%s, want 6", exit, stdout)
	}
	if code := decodeEnvelope(t, stdout)["error"].(map[string]any)["code"]; code != "E_CONFLICT" {
		t.Fatalf("replay error code = %v", code)
	}
	if serverState.patchCount() != 1 {
		t.Fatalf("replay made another PATCH; count=%d", serverState.patchCount())
	}
}

func TestAuthFileSetStatusRejectsMissingCurrentState(t *testing.T) {
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			writeCommandJSON(t, w, map[string]any{"files": []any{
				map[string]any{"name": "fallback.json", "type": "codex", "email": "fallback@example.test"},
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

	exit, stdout, _ := runCommand(t, "auth-file", "set-status", "--name", "fallback.json", "--disabled=true", "--dry-run", "--compact")
	if exit != 6 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFLICT" {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	if patches.Load() != 0 {
		t.Fatalf("missing state made %d PATCH requests", patches.Load())
	}
}

func TestAuthFileSetStatusBindsVersionFieldPresence(t *testing.T) {
	var gets atomic.Int32
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			file := map[string]any{"auth_index": "idx-1", "name": "account.json", "provider": "codex", "disabled": false}
			if gets.Add(1) > 1 {
				file["id"] = ""
				file["updated_at"] = ""
			}
			writeCommandJSON(t, w, map[string]any{"files": []any{file}})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			patches.Add(1)
			writeCommandJSON(t, w, map[string]any{"status": "ok", "disabled": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, _ := runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--dry-run", "--compact")
	if exit != 0 {
		t.Fatalf("dry-run exit=%d stdout=%s", exit, stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)

	exit, stdout, _ = runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--confirm", token, "--compact")
	if exit != 6 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFLICT" {
		t.Fatalf("presence drift exit=%d stdout=%s", exit, stdout)
	}
	if patches.Load() != 0 {
		t.Fatalf("presence drift made %d PATCH requests", patches.Load())
	}
}

func TestAuthFileSetStatusRejectsStateDriftBeforePatch(t *testing.T) {
	serverState := newAuthStatusServerState([]authStatusFixture{{
		ID: "id-1", AuthIndex: "idx-1", Name: "account.json", Provider: "codex", UpdatedAt: "2026-08-05T12:00:00Z",
	}})
	server := httptest.NewServer(serverState)
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, _ := runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--dry-run", "--compact")
	if exit != 0 {
		t.Fatalf("dry-run exit=%d stdout=%s", exit, stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	serverState.setUpdatedAt("idx-1", "2026-08-05T12:01:00Z")

	exit, stdout, _ = runCommand(t, "auth-file", "set-status", "--name", "account.json", "--auth-index", "idx-1", "--disabled=true", "--confirm", token, "--compact")
	if exit != 6 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFLICT" {
		t.Fatalf("drift exit=%d stdout=%s", exit, stdout)
	}
	if serverState.patchCount() != 0 {
		t.Fatalf("drift made PATCH; count=%d", serverState.patchCount())
	}
}

func TestQuotaInspectUsesFixedCodexProbeAndUnknownIsSuccess(t *testing.T) {
	var apiCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			assertCommandManagementRequest(t, r, http.MethodGet, r.URL.Path)
			writeCommandJSON(t, w, map[string]any{"files": []any{
				map[string]any{
					"id": "codex-1", "auth_index": "idx-1", "name": "one.json", "provider": "codex",
					"account": "must-not-be-used", "email": "must-not-be-used@example.test",
					"id_token": map[string]any{"chatgpt_account_id": "acct-from-claims"},
				},
				map[string]any{
					"id": "codex-2", "auth_index": "idx-2", "name": "missing-claims.json", "provider": "codex",
					"account": "do-not-guess-this",
				},
				map[string]any{"id": "claude", "auth_index": "idx-c", "name": "claude.json", "provider": "claude"},
			}})
			return
		}
		if r.URL.Path == "/v0/management/api-call" {
			apiCalls.Add(1)
			assertCommandManagementRequest(t, r, http.MethodPost, r.URL.Path)
			var request struct {
				AuthIndex string            `json:"auth_index"`
				Method    string            `json:"method"`
				URL       string            `json:"url"`
				Header    map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode api-call: %v", err)
			}
			if request.AuthIndex != "idx-1" || request.Method != http.MethodGet || request.URL != codexUsageURL {
				t.Errorf("api-call request = %#v", request)
			}
			if request.Header["Authorization"] != "Bearer $TOKEN$" || request.Header["Chatgpt-Account-Id"] != "acct-from-claims" {
				t.Errorf("api-call headers = %#v", request.Header)
			}
			writeCommandJSON(t, w, map[string]any{
				"status_code": http.StatusTooManyRequests,
				"header":      map[string][]string{"Content-Type": {"application/json"}},
				"body":        `{"message":"ordinary rate limit"}`,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, stderr := runCommand(t, "quota", "inspect", "--provider", "codex", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 2 || data["count"] != float64(2) {
		t.Fatalf("data = %#v", data)
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["state"] != "unknown" {
			t.Fatalf("item = %#v, want unknown", item)
		}
	}
	if apiCalls.Load() != 1 {
		t.Fatalf("api-call count = %d, missing claims must not be probed", apiCalls.Load())
	}
	untrusted := stringSliceFromAny(t, data["_untrusted"])
	if !containsString(untrusted, "items.evidence") {
		t.Fatalf("_untrusted = %#v", untrusted)
	}
}

func TestQuotaInspectRejectsUnsupportedProviderBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	configureCommandTest(t, server.URL)

	exit, stdout, _ := runCommand(t, "quota", "inspect", "--provider", "claude", "--compact")
	if exit != 2 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_VALIDATION" {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	if requests.Load() != 0 {
		t.Fatalf("unsupported provider made %d requests", requests.Load())
	}
}

func configureCommandTest(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("CLIPROXYAPI_CLI_BASE_URL", serverURL+"/v0/management")
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "management-secret")
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", t.TempDir())
}

func assertCommandManagementRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method || r.URL.Path != path {
		t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.Path, method, path)
	}
	if r.Header.Get("Authorization") != "Bearer management-secret" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %#v", r.Header)
	}
}

func writeCommandJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func stringSliceFromAny(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %#v, want []any", value)
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("item = %#v, want string", item)
		}
		result = append(result, text)
	}
	return result
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type authStatusFixture struct {
	ID        string
	AuthIndex string
	Name      string
	Provider  string
	Disabled  bool
	UpdatedAt string
}

type authStatusServerState struct {
	mu          sync.Mutex
	items       []authStatusFixture
	patches     int
	lastPatched string
}

func newAuthStatusServerState(items []authStatusFixture) *authStatusServerState {
	return &authStatusServerState{items: append([]authStatusFixture(nil), items...)}
}

func (s *authStatusServerState) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer management-secret" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
		files := make([]any, 0, len(s.items))
		for _, item := range s.items {
			files = append(files, map[string]any{
				"id": item.ID, "auth_index": item.AuthIndex, "name": item.Name, "provider": item.Provider,
				"disabled": item.Disabled, "updated_at": item.UpdatedAt,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
		var payload struct {
			Name      string `json:"name"`
			AuthIndex string `json:"auth_index"`
			Disabled  bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i := range s.items {
			if s.items[i].Name == payload.Name && s.items[i].AuthIndex == payload.AuthIndex {
				s.items[i].Disabled = payload.Disabled
				s.items[i].UpdatedAt = "2026-08-05T12:02:00Z"
			}
		}
		s.patches++
		s.lastPatched = payload.AuthIndex
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "disabled": payload.Disabled})
	default:
		http.NotFound(w, r)
	}
}

func (s *authStatusServerState) setUpdatedAt(authIndex, updatedAt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].AuthIndex == authIndex {
			s.items[i].UpdatedAt = updatedAt
		}
	}
}

func (s *authStatusServerState) setState(authIndex string, disabled bool, updatedAt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].AuthIndex == authIndex {
			s.items[i].Disabled = disabled
			s.items[i].UpdatedAt = updatedAt
		}
	}
}

func (s *authStatusServerState) patchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patches
}

func (s *authStatusServerState) lastPatchedAuthIndex() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPatched
}
