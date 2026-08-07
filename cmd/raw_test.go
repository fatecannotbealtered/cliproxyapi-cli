package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRawGetOmitsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v0/management/debug" {
			t.Fatalf("request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"debug":false}`)
	}))
	defer server.Close()

	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	exit, stdout, stderr := runConfirmedRawCommand(t, "--base-url", server.URL+"/v0/management", "--path", "/debug")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status_code"] != float64(http.StatusOK) || data["response_body_omitted"] != true {
		t.Fatalf("data = %#v", data)
	}
	if strings.Contains(string(stdout), `"debug":false`) {
		t.Fatalf("response body leaked: %s", stdout)
	}
}

func TestRawRequestRequiresDangerousAndConfirmation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `{"status":"ok"}`)
	}))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")

	args := []string{"raw", "request", "--base-url", server.URL + "/v0/management", "--path", "/debug", "--compact"}
	exit, stdout, _ := runCommand(t, args...)
	if exit != 2 || calls.Load() != 0 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_USAGE" {
		t.Fatalf("without dangerous exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}

	args = append(args, "--dangerous")
	exit, stdout, _ = runCommand(t, args...)
	if exit != 5 || calls.Load() != 0 || decodeEnvelope(t, stdout)["error"].(map[string]any)["code"] != "E_CONFIRMATION_REQUIRED" {
		t.Fatalf("without confirm exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
}

func TestRawNonGetRequiresDryRunThenSingleUseConfirm(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPatch || request.URL.Path != "/v0/management/debug" || string(body) != `{"value":true}` {
			t.Fatalf("request = %s %s body=%s", request.Method, request.URL.String(), body)
		}
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"status":"ok"}`)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	baseArgs := []string{"raw", "request", "--base-url", server.URL + "/v0/management", "--state-dir", stateDir, "--method", "PATCH", "--path", "/debug", "--body", `{"value":true}`, "--dangerous", "--compact"}
	exit, stdout, _ := runCommand(t, baseArgs...)
	if exit != 5 {
		t.Fatalf("missing confirm exit=%d stdout=%s", exit, stdout)
	}

	dryArgs := append(append([]string{}, baseArgs...), "--dry-run")
	exit, stdout, _ = runCommand(t, dryArgs...)
	if exit != 0 || calls.Load() != 0 {
		t.Fatalf("dry-run exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	confirmArgs := append(append([]string{}, baseArgs...), "--confirm", token)
	exit, stdout, _ = runCommand(t, confirmArgs...)
	if exit != 0 || calls.Load() != 1 {
		t.Fatalf("confirm exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
	if decodeEnvelope(t, stdout)["data"].(map[string]any)["status_code"] != float64(http.StatusCreated) {
		t.Fatalf("stdout=%s", stdout)
	}

	exit, stdout, _ = runCommand(t, confirmArgs...)
	if exit != 6 || calls.Load() != 1 {
		t.Fatalf("replay exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
}

func TestRawConfirmBindsPathAndBody(t *testing.T) {
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	stateDir := t.TempDir()
	args := []string{"raw", "request", "--base-url", "http://127.0.0.1:1/v0/management", "--state-dir", stateDir, "--method", "POST", "--path", "/api-call", "--body", `{}`, "--dangerous", "--dry-run", "--compact"}
	exit, stdout, _ := runCommand(t, args...)
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	exit, stdout, _ = runCommand(t, "raw", "request", "--base-url", "http://127.0.0.1:1/v0/management", "--state-dir", stateDir, "--method", "POST", "--path", "/api-call", "--body", `{"changed":true}`, "--dangerous", "--confirm", token, "--compact")
	if exit != 6 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
}

func TestRawDryRunBindsButDoesNotEchoQueryValues(t *testing.T) {
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	secretValue := "query-secret-value"
	exit, stdout, _ := runCommand(t, "raw", "request", "--base-url", "http://127.0.0.1:1/v0/management", "--state-dir", t.TempDir(), "--method", "DELETE", "--path", "/api-keys?value="+secretValue, "--dangerous", "--dry-run", "--compact")
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	if strings.Contains(string(stdout), secretValue) {
		t.Fatalf("query value leaked in preview: %s", stdout)
	}
	preview := decodeEnvelope(t, stdout)["data"].(map[string]any)["preview"].(map[string]any)
	if preview["path"] != "/api-keys" || preview["query_present"] != true {
		t.Fatalf("preview=%#v", preview)
	}
}

func TestRawUsageQueueWarnsThatGetPops(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `[{"api_key":"queue-secret"}]`)
	}))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	stateDir := t.TempDir()
	baseArgs := []string{"raw", "request", "--base-url", server.URL + "/v0/management", "--state-dir", stateDir, "--path", "/usage-queue?count=1", "--dangerous", "--compact"}

	dryArgs := append(append([]string{}, baseArgs...), "--dry-run")
	exit, stdout, _ := runCommand(t, dryArgs...)
	if exit != 0 || calls.Load() != 0 {
		t.Fatalf("dry-run exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if _, ok := data["notices"]; !ok {
		t.Fatalf("dry-run notice missing: %#v", data)
	}
	token := data["confirm_token"].(string)
	confirmArgs := append(append([]string{}, baseArgs...), "--confirm", token)
	exit, stdout, _ = runCommand(t, confirmArgs...)
	if exit != 0 || calls.Load() != 1 || strings.Contains(string(stdout), "queue-secret") {
		t.Fatalf("confirm exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
	data = decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["response_body_omitted"] != true {
		t.Fatalf("response omission missing: %#v", data)
	}
}

func TestRawFormatIsRejectedBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, "plain\nbytes")
	}))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	exit, stdout, stderr := runCommand(t, "raw", "request", "--base-url", server.URL+"/v0/management", "--path", "/config.yaml", "--dangerous", "--format", "raw")
	if exit != 2 || calls.Load() != 0 || stderr != "" || strings.Contains(string(stdout), "plain") {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr, stdout)
	}
}

func TestRawFieldsCannotExposeResponseBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `{"access_token":"top-secret"}`)
	}))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "test-key")
	exit, stdout, _ := runCommand(t, "raw", "request", "--base-url", server.URL+"/v0/management", "--state-dir", t.TempDir(), "--path", "/config", "--dangerous", "--dry-run", "--fields", "body", "--compact")
	if exit != 2 || calls.Load() != 0 || strings.Contains(string(stdout), "top-secret") {
		t.Fatalf("exit=%d calls=%d stdout=%s", exit, calls.Load(), stdout)
	}
}

func TestRawBodyStdinRejectsManagementKeyStdin(t *testing.T) {
	var stdout, stderr strings.Builder
	exit := ExecuteArgs(t.Context(), []string{"raw", "request", "--path", "/debug", "--method", "PATCH", "--body-stdin", "--management-key-stdin", "--dry-run", "--compact"}, strings.NewReader("secret"), &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &envelope); err != nil {
		t.Fatal(err)
	}
}

func runConfirmedRawCommand(t *testing.T, args ...string) (int, []byte, string) {
	t.Helper()
	baseArgs := append([]string{"raw", "request", "--state-dir", t.TempDir(), "--dangerous"}, args...)
	dryArgs := append(append([]string{}, baseArgs...), "--dry-run", "--compact")
	exit, stdout, stderr := runCommand(t, dryArgs...)
	if exit != 0 || stderr != "" {
		t.Fatalf("raw dry-run exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	token := decodeEnvelope(t, stdout)["data"].(map[string]any)["confirm_token"].(string)
	confirmArgs := append(append([]string{}, baseArgs...), "--confirm", token, "--compact")
	return runCommand(t, confirmArgs...)
}
