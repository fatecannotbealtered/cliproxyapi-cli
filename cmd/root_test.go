package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func testNewerVersion(t *testing.T) string {
	t.Helper()
	parsed, ok := parseUpdateSemver(version)
	if !ok {
		t.Fatalf("runtime version %q is not semantic", version)
	}
	parsed.Patch++
	return parsed.String()
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
	schemas := data["schemas"].(map[string]any)
	updateSchema, ok := schemas["update"].(map[string]any)
	if !ok {
		t.Fatalf("reference schemas missing update: %#v", schemas)
	}
	updateFields := updateSchema["fields"].([]any)
	for _, required := range []string{"command", "checksum_available", "skill_sync_supported"} {
		if !hasField(updateFields, required) {
			t.Fatalf("update schema missing %q: %#v", required, updateFields)
		}
	}
	if hasField(updateFields, "recommended_command") {
		t.Fatalf("update schema should use actual command field, not recommended_command: %#v", updateFields)
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
	var contextParams []any
	var rawReference map[string]any
	var quotaReference map[string]any
	var updateReference map[string]any
	for _, raw := range commands {
		command := raw.(map[string]any)
		switch command["path"] {
		case "cliproxyapi-cli context":
			contextParams = command["params"].([]any)
		case "cliproxyapi-cli quota inspect":
			quotaReference = command
		case "cliproxyapi-cli raw request":
			rawReference = command
		case "cliproxyapi-cli update":
			updateReference = command
		}
	}
	for _, required := range []string{"compact", "fields", "format", "state-dir", "timeout"} {
		found := false
		for _, raw := range contextParams {
			if raw.(map[string]any)["name"] == required {
				found = true
			}
		}
		if !found {
			t.Errorf("context params missing inherited flag %q: %#v", required, contextParams)
		}
	}
	if rawReference == nil || rawReference["permission_tier"] != "dangerous" || rawReference["write_gate"] != "dangerous_dry_run_confirm" {
		t.Errorf("raw request contract = %#v", rawReference)
	}
	if quotaReference == nil || !strings.Contains(quotaReference["blast_radius"].(string), "used and remaining percentages") {
		t.Errorf("quota inspect contract = %#v", quotaReference)
	}
	if updateReference == nil || updateReference["output_schema"] != "update" || len(updateReference["examples"].([]any)) == 0 {
		t.Errorf("update contract = %#v", updateReference)
	} else {
		for _, raw := range updateReference["params"].([]any) {
			if raw.(map[string]any)["name"] == "confirm" {
				t.Errorf("update reference must not advertise --confirm: %#v", updateReference["params"])
			}
		}
	}
}

func TestVersionAndHelpSurface(t *testing.T) {
	exit, stdout, stderr := runCommand(t, "--version")
	if exit != 0 || stderr != "" || string(stdout) != "cliproxyapi-cli version "+version+"\n" {
		t.Fatalf("version exit=%d stderr=%q stdout=%q", exit, stderr, stdout)
	}

	exit, stdout, stderr = runCommand(t, "--help")
	if exit != 0 || stderr != "" || !bytes.Contains(stdout, []byte("Available Commands:")) {
		t.Fatalf("help exit=%d stderr=%q stdout=%q", exit, stderr, stdout)
	}

	exit, stdout, stderr = runCommand(t, "quota", "inspect", "--help")
	if exit != 0 || stderr != "" || !bytes.Contains(stdout, []byte("used and remaining quota percentages")) {
		t.Fatalf("quota help exit=%d stderr=%q stdout=%q", exit, stderr, stdout)
	}

	exit, stdout, stderr = runCommand(t, "update", "--help")
	if exit != 0 || stderr != "" || bytes.Contains(stdout, []byte("--confirm")) || !bytes.Contains(stdout, []byte("without changing the installation or issuing a confirmation token")) {
		t.Fatalf("update help exit=%d stderr=%q stdout=%q", exit, stderr, stdout)
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
	meta := envelope["meta"].(map[string]any)
	if _, ok := meta["notices"]; ok {
		t.Fatalf("context meta should omit empty notices: %#v", meta)
	}
	credentials := envelope["data"].(map[string]any)["credentials"].(map[string]any)
	if credentials["configured"] != true || credentials["source"] != "env" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestContextAndDoctorAttachCachedUpdateNotice(t *testing.T) {
	stateDir := t.TempDir()
	notice := map[string]any{
		"type":                "update_available",
		"severity":            "info",
		"current_version":     version,
		"latest_version":      testNewerVersion(t),
		"update_available":    true,
		"install_method":      "binary",
		"recommended_command": "cliproxyapi-cli update --compact",
	}
	writeTestUpdateNotice(t, stateDir, notice["latest_version"].(string), notice["severity"].(string))
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)

	for _, command := range []string{"context", "doctor"} {
		exit, stdout, stderr := runCommand(t, command, "--compact")
		if exit != 0 || stderr != "" {
			t.Fatalf("%s exit=%d stderr=%q stdout=%s", command, exit, stderr, stdout)
		}
		envelope := decodeEnvelope(t, stdout)
		meta := envelope["meta"].(map[string]any)
		notices, ok := meta["notices"].([]any)
		if !ok || len(notices) != 1 {
			t.Fatalf("%s meta.notices = %#v", command, meta["notices"])
		}
		dataNotices, ok := envelope["data"].(map[string]any)["notices"].([]any)
		if !ok || len(dataNotices) != 1 {
			t.Fatalf("%s data.notices = %#v", command, envelope["data"])
		}
	}
}

type failUpdateNoticeNetworkTransport struct{ t *testing.T }

func (f failUpdateNoticeNetworkTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.t.Fatalf("ordinary command must not check update network; requested %s", request.URL)
	return nil, nil
}

func TestHelpUsesCachedUpdateNoticeWithoutNetwork(t *testing.T) {
	stateDir := t.TempDir()
	latest := testNewerVersion(t)
	writeTestUpdateNotice(t, stateDir, latest, "info")
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)

	originalClient := updateHTTPClient
	updateHTTPClient = &http.Client{Transport: failUpdateNoticeNetworkTransport{t: t}}
	t.Cleanup(func() { updateHTTPClient = originalClient })

	exit, stdout, stderr := runCommand(t, "--help")
	if exit != 0 || stderr != "" {
		t.Fatalf("help exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	want := "Update available: cliproxyapi-cli " + version + " -> " + latest + ". Run: cliproxyapi-cli update --compact"
	if !bytes.Contains(stdout, []byte(want)) {
		t.Fatalf("help missing cached update notice %q: %s", want, stdout)
	}
}

func TestOrdinaryCommandUsesCachedUpdateNoticeWithoutNetwork(t *testing.T) {
	stateDir := t.TempDir()
	notice := map[string]any{
		"type":             "update_available",
		"severity":         "info",
		"current_version":  version,
		"latest_version":   testNewerVersion(t),
		"update_available": true,
		"install_method":   "binary",
	}
	writeTestUpdateNotice(t, stateDir, notice["latest_version"].(string), notice["severity"].(string))
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)

	origClient := updateHTTPClient
	updateHTTPClient = &http.Client{Transport: failUpdateNoticeNetworkTransport{t: t}}
	t.Cleanup(func() { updateHTTPClient = origClient })

	exit, stdout, stderr := runCommand(t, "changelog", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("changelog exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	meta := decodeEnvelope(t, stdout)["meta"].(map[string]any)
	notices, ok := meta["notices"].([]any)
	if !ok || len(notices) != 1 {
		t.Fatalf("meta.notices = %#v, want one cached notice", meta["notices"])
	}
}

func TestExpiredUpdateNoticeCacheIsOmitted(t *testing.T) {
	stateDir := t.TempDir()
	checkedAt := time.Now().Add(-noticeCacheTTL - time.Minute).UTC().Format(time.RFC3339)
	document := noticeCacheDocument{
		Version:   noticeCacheVersion,
		CheckedAt: checkedAt,
		Notices: []updateNotice{{
			Type:            "update_available",
			Severity:        "info",
			CurrentVersion:  version,
			LatestVersion:   testNewerVersion(t),
			UpdateAvailable: true,
			CheckedAt:       checkedAt,
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, noticeCacheFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)

	exit, stdout, stderr := runCommand(t, "context", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("context exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	meta := decodeEnvelope(t, stdout)["meta"].(map[string]any)
	if _, ok := meta["notices"]; ok {
		t.Fatalf("expired cache must be omitted from meta.notices: %#v", meta)
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
			if check["status"] != "warn" || !strings.Contains(check["fix"].(string), "update E2E") {
				t.Fatalf("release_readiness check = %#v, want candidate beta warning", check)
			}
		}
	}
	if !found {
		t.Fatalf("release_readiness check missing: %#v", checks)
	}
	meta := decodeEnvelope(t, stdout)["meta"].(map[string]any)
	if _, ok := meta["notices"]; ok {
		t.Fatalf("doctor meta should omit empty notices: %#v", meta)
	}
	readiness := decodeEnvelope(t, stdout)["data"].(map[string]any)["release_readiness"].(map[string]any)
	if readiness["level"] != "beta" || readiness["fcc_status"] != "verified" || readiness["mock_upstream_status"] != "verified" || readiness["live_smoke_status"] != "missing" {
		t.Fatalf("release_readiness = %#v", readiness)
	}
	if !strings.Contains(readiness["reason"].(string), "clean candidate commit") {
		t.Fatalf("release_readiness reason = %q, want clean-candidate rerun requirement", readiness["reason"])
	}
	if !strings.Contains(readiness["reason"].(string), "self-update E2E passed for the development tree") {
		t.Fatalf("release_readiness reason = %q, want completed development-E2E status", readiness["reason"])
	}
	if !strings.Contains(readiness["reason"].(string), version) {
		t.Fatalf("release_readiness reason = %q, want current version %q", readiness["reason"], version)
	}
}

func TestChangelogCommandFiltersSince(t *testing.T) {
	exit, stdout, _ := runCommand(t, "changelog", "--since", version, "--compact")
	if exit != 0 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["current_version"] != version || data["since"] != version {
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

func hasField(fields []any, want string) bool {
	for _, raw := range fields {
		if raw == want {
			return true
		}
	}
	return false
}
