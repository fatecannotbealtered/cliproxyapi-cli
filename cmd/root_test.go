package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runCommand(t *testing.T, args ...string) (int, []byte, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := ExecuteArgs(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return exit, stdout.Bytes(), stderr.String()
}

func decodeEnvelope(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, raw)
	}
	return envelope
}

func TestReferenceCommandHasCanonicalSelfDescription(t *testing.T) {
	exit, stdout, stderr := runCommand(t, "reference", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	data := envelope["data"].(map[string]any)
	for _, field := range []string{"tool", "version", "risk_tier", "release_readiness", "commands", "schemas", "exit_codes", "error_codes"} {
		if _, ok := data[field]; !ok {
			t.Errorf("reference missing %q", field)
		}
	}
	commands := data["commands"].([]any)
	if len(commands) < 4 {
		t.Fatalf("commands = %#v", commands)
	}
	for _, raw := range commands {
		command := raw.(map[string]any)
		if command["output_schema"] == "" || len(command["examples"].([]any)) == 0 || command["write_gate"] == "" || command["state_verification"] == "" || command["retry_semantics"] == "" {
			t.Errorf("leaf has incomplete contract: %#v", command)
		}
	}
}

func TestContextCommandDoesNotExposeManagementKey(t *testing.T) {
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "super-secret-value")
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", t.TempDir())
	exit, stdout, stderr := runCommand(t, "context", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	if bytes.Contains(stdout, []byte("super-secret-value")) {
		t.Fatalf("management key leaked: %s", stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	credentials := envelope["data"].(map[string]any)["credentials"].(map[string]any)
	if credentials["configured"] != true || credentials["source"] != "env" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestDoctorCommandIncludesReleaseReadiness(t *testing.T) {
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "")
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", t.TempDir())
	exit, stdout, _ := runCommand(t, "doctor", "--compact")
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	checks := decodeEnvelope(t, stdout)["data"].(map[string]any)["checks"].([]any)
	found := false
	for _, raw := range checks {
		check := raw.(map[string]any)
		if check["check"] == "release_readiness" {
			found = true
			if check["status"] != "warn" {
				t.Fatalf("release_readiness check = %#v, want beta warning", check)
			}
		}
	}
	if !found {
		t.Fatalf("release_readiness check missing: %#v", checks)
	}
	readiness := decodeEnvelope(t, stdout)["data"].(map[string]any)["release_readiness"].(map[string]any)
	if readiness["level"] != "beta" || readiness["fcc_status"] != "verified" || readiness["mock_upstream_status"] != "verified" || readiness["live_smoke_status"] != "missing" {
		t.Fatalf("release_readiness = %#v", readiness)
	}
}

func TestChangelogCommandFiltersSince(t *testing.T) {
	exit, stdout, _ := runCommand(t, "changelog", "--since", "1.0.0", "--compact")
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["current_version"] != "1.0.0" || data["since"] != "1.0.0" {
		t.Fatalf("changelog = %#v", data)
	}
	if len(data["entries"].([]any)) != 0 {
		t.Fatalf("entries = %#v, want empty", data["entries"])
	}
}

func TestInvalidFormatReturnsUsageEnvelope(t *testing.T) {
	exit, stdout, _ := runCommand(t, "context", "--format", "xml")
	if exit != 2 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	errorObject := envelope["error"].(map[string]any)
	if errorObject["code"] != "E_USAGE" || errorObject["retryable"] != false {
		t.Fatalf("error = %#v", errorObject)
	}
}

func TestJSONAliasAndFieldValidation(t *testing.T) {
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", t.TempDir())
	exit, stdout, stderr := runCommand(t, "context", "--json", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if decodeEnvelope(t, stdout)["ok"] != true {
		t.Fatalf("stdout=%s", stdout)
	}

	exit, stdout, _ = runCommand(t, "context", "--fields", "does_not_exist", "--compact")
	if exit != 2 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errorObject["code"] != "E_VALIDATION" {
		t.Fatalf("error=%#v", errorObject)
	}

	exit, stdout, _ = runCommand(t, "context", "--json", "--format", "text")
	if exit != 2 {
		t.Fatalf("conflict exit=%d stdout=%s", exit, stdout)
	}
}

func TestDoctorChecksManagementAPIWithoutLeakingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer doctor-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(response, `{"files":[]}`)
	}))
	defer server.Close()
	t.Setenv("CLIPROXYAPI_CLI_MANAGEMENT_KEY", "doctor-secret")
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", t.TempDir())
	exit, stdout, stderr := runCommand(t, "doctor", "--base-url", server.URL+"/v0/management", "--compact")
	if exit != 0 || stderr != "" || bytes.Contains(stdout, []byte("doctor-secret")) {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	checks := decodeEnvelope(t, stdout)["data"].(map[string]any)["checks"].([]any)
	found := false
	for _, raw := range checks {
		check := raw.(map[string]any)
		if check["check"] == "management_api" && check["status"] == "pass" {
			found = true
		}
	}
	if !found {
		t.Fatalf("management_api pass missing: %#v", checks)
	}
}
