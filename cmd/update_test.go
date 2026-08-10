package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func updateMockReleaseServer(t *testing.T, ver string, includeSignature bool) *httptest.Server {
	t.Helper()
	assetName, err := updateArchiveName(ver)
	if err != nil {
		t.Fatal(err)
	}
	assets := []string{assetName, "checksums.txt"}
	if includeSignature {
		assets = append(assets, "checksums.txt.sigstore.json")
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var b strings.Builder
		b.WriteString(`{"tag_name":"v` + ver + `","html_url":"https://example.com/rel","assets":[`)
		for i, name := range assets {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"` + name + `","browser_download_url":"https://example.com/` + name + `"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
}

func updateMockReleaseServerWithAssets(t *testing.T, ver string, assets []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var b strings.Builder
		b.WriteString(`{"tag_name":"v` + ver + `","html_url":"https://example.com/rel","assets":[`)
		for i, name := range assets {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"` + name + `","browser_download_url":"https://example.com/` + name + `"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
}

func withUpdateServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	origAPI := updateGitHubAPI
	origRaw := updateGitHubRaw
	updateGitHubAPI = srv.URL
	updateGitHubRaw = srv.URL
	t.Cleanup(func() {
		updateGitHubAPI = origAPI
		updateGitHubRaw = origRaw
	})
}

func useNPMManagedExecutable(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pkgName := npmPlatformPackageName()
	if pkgName == "" {
		t.Fatal("test platform does not have an npm package")
	}
	pkgDir := filepath.Join(root, "node_modules", filepath.FromSlash(pkgName))
	if err := os.MkdirAll(filepath.Join(pkgDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"`+pkgName+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(pkgDir, "bin", updateBinaryName+".exe")
	original := updateExecutable
	updateExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { updateExecutable = original })
	return exe
}

func mockUpdateInstalledVersion(t *testing.T, observed string, err error) {
	t.Helper()
	original := updateInstalledVersion
	updateInstalledVersion = func(string) (string, error) { return observed, err }
	t.Cleanup(func() { updateInstalledVersion = original })
}

func assertNoMetaNotices(t *testing.T, stdout []byte) {
	t.Helper()
	meta := decodeEnvelope(t, stdout)["meta"].(map[string]any)
	if _, ok := meta["notices"]; ok {
		t.Fatalf("stale meta.notices present: %#v", meta)
	}
}

func assertUpdateFailureDetails(t *testing.T, stdout []byte, wantStage string, wantBinaryReplaced bool, wantSkillSyncStatus string) map[string]any {
	t.Helper()
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	details, ok := errObj["details"].(map[string]any)
	if !ok {
		t.Fatalf("update failure missing structured details: %#v", errObj)
	}
	for _, field := range []string{"stage", "current_version", "binary_replaced", "skill_sync_status"} {
		if _, ok := details[field]; !ok {
			t.Fatalf("update failure details missing %q: %#v", field, details)
		}
	}
	if wantStage != "" && details["stage"] != wantStage {
		t.Fatalf("stage=%v want %s in %#v", details["stage"], wantStage, details)
	}
	if details["binary_replaced"] != wantBinaryReplaced {
		t.Fatalf("binary_replaced=%v want %v in %#v", details["binary_replaced"], wantBinaryReplaced, details)
	}
	if wantSkillSyncStatus != "" && details["skill_sync_status"] != wantSkillSyncStatus {
		t.Fatalf("skill_sync_status=%v want %s in %#v", details["skill_sync_status"], wantSkillSyncStatus, details)
	}
	return details
}

func seedStaleUpdateNotice(t *testing.T, stateDir string) {
	t.Helper()
	writeTestUpdateNotice(t, stateDir, "9.9.9", "info")
}

func writeTestUpdateNotice(t *testing.T, stateDir, latestVersion, severity string) {
	t.Helper()
	if err := writeUpdateNoticeCache(stateDir, []updateNotice{{
		Type:            "update_available",
		Severity:        severity,
		CurrentVersion:  version,
		LatestVersion:   latestVersion,
		UpdateAvailable: true,
		InstallMethod:   "binary",
	}}); err != nil {
		t.Fatalf("seed update notice: %v", err)
	}
}

func TestUpdateBareNoOpAndCheck(t *testing.T) {
	srv := updateMockReleaseServer(t, version, true)
	defer srv.Close()
	withUpdateServer(t, srv)
	originalSkillSync := updateSkillSync
	skillSyncCalls := 0
	updateSkillSync = func(context.Context, string) error {
		skillSyncCalls++
		return nil
	}
	t.Cleanup(func() { updateSkillSync = originalSkillSync })

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("bare update exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status"] != "up_to_date" || data["update_available"] != false || data["previous_version"] != version {
		t.Fatalf("bare update data = %#v", data)
	}
	if _, ok := data["confirm_token"]; ok {
		t.Fatal("bare update must not emit a confirm_token")
	}
	if data["skill_sync_status"] != "synced" || skillSyncCalls != 1 {
		t.Fatalf("bare no-op must sync the Skill once: calls=%d data=%#v", skillSyncCalls, data)
	}

	exit, stdout, stderr = runCommand(t, "update", "--check", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("update --check exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data = decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status"] != "up_to_date" || data["signature_status"] != "not_checked" {
		t.Fatalf("check data = %#v", data)
	}
	if skillSyncCalls != 1 {
		t.Fatalf("update --check must not sync the Skill: calls=%d", skillSyncCalls)
	}
}

func TestUpdateCheckRefreshesNoticeCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	target := additionalUpdateHigherVersion(t)
	srv := updateMockReleaseServer(t, target, true)
	defer srv.Close()
	withUpdateServer(t, srv)

	exit, stdout, stderr := runCommand(t, "update", "--check", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("update --check exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	notices, ok := data["notices"].([]any)
	if !ok || len(notices) != 1 {
		t.Fatalf("update --check data.notices = %#v", data["notices"])
	}
	notice := notices[0].(map[string]any)
	if notice["type"] != "update_available" || notice["severity"] != "warning" || notice["source"] != "update_check" {
		t.Fatalf("major update notice identity = %#v", notice)
	}
	if notice["current_version"] != version || notice["latest_version"] != target || notice["update_available"] != true {
		t.Fatalf("major update notice versions = %#v", notice)
	}
	if notice["install_method"] != "binary" || notice["recommended_command"] != "cliproxyapi-cli update --compact" || notice["release_url"] != "https://example.com/rel" {
		t.Fatalf("major update notice action = %#v", notice)
	}
	if _, err := time.Parse(time.RFC3339, notice["checked_at"].(string)); err != nil {
		t.Fatalf("major update notice checked_at = %#v: %v", notice["checked_at"], err)
	}
	if nextSteps, ok := notice["next_steps"].([]any); !ok || len(nextSteps) != 4 {
		t.Fatalf("major update notice next_steps = %#v", notice["next_steps"])
	}
	cached := readUpdateNoticeCache(stateDir)
	if len(cached) != 1 || cached[0].Source != "cache" || cached[0].LatestVersion != target || cached[0].Severity != "warning" {
		t.Fatalf("cached notices = %#v", cached)
	}
}

func TestUpdateCheckFieldsAreValidatedBeforeCacheRefresh(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	server := updateMockReleaseServer(t, "9.9.9", true)
	defer server.Close()
	withUpdateServer(t, server)

	exit, stdout, stderr := runCommand(t, "update", "--check", "--fields", "status,target_version", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("update --check fields exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if len(data) != 2 || data["status"] != "available" || data["target_version"] != "9.9.9" {
		t.Fatalf("projected update data=%#v", data)
	}
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 1 || cached[0].LatestVersion != "9.9.9" {
		t.Fatalf("projected check did not refresh cache: %#v", cached)
	}
}

func TestUpdateNoOpClearsStaleNoticeCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	srv := updateMockReleaseServer(t, version, true)
	defer srv.Close()
	withUpdateServer(t, srv)
	originalSkillSync := updateSkillSync
	updateSkillSync = func(context.Context, string) error { return nil }
	t.Cleanup(func() { updateSkillSync = originalSkillSync })

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("no-op update exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	assertNoMetaNotices(t, stdout)
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 0 {
		t.Fatalf("stale notice cache remains after no-op: %#v", cached)
	}
}

func TestUpdateNoOpReportsSkillSyncFailure(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	server := updateMockReleaseServer(t, version, true)
	defer server.Close()
	withUpdateServer(t, server)

	originalSkillSync := updateSkillSync
	updateSkillSync = func(context.Context, string) error { return errors.New("npx unavailable") }
	t.Cleanup(func() { updateSkillSync = originalSkillSync })

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 7 || stderr != "" {
		t.Fatalf("no-op Skill failure exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	details := assertUpdateFailureDetails(t, stdout, stageSkillSync, false, "failed")
	if details["current_version"] != version || details["target_version"] != version || details["skill_sync_command"] == "" {
		t.Fatalf("no-op Skill failure details=%#v", details)
	}
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 0 {
		t.Fatalf("no-op Skill failure retained stale notice: %#v", cached)
	}
}

func TestUpdateNoOpCleansPreviousWindowsSwapFiles(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, updateBinaryName+".exe")
	if err := os.WriteFile(exe, []byte("CURRENT"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".old", ".new"} {
		if err := os.WriteFile(filepath.Join(dir, "."+filepath.Base(exe)+suffix), []byte("STALE"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	originalExecutable := updateExecutable
	updateExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { updateExecutable = originalExecutable })
	originalSkillSync := updateSkillSync
	updateSkillSync = func(context.Context, string) error { return nil }
	t.Cleanup(func() { updateSkillSync = originalSkillSync })

	srv := updateMockReleaseServer(t, version, true)
	defer srv.Close()
	withUpdateServer(t, srv)
	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("no-op update exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	for _, suffix := range []string{".old", ".new"} {
		path := filepath.Join(dir, "."+filepath.Base(exe)+suffix)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale swap file remains at %s: %v", path, err)
		}
	}
}

func TestUpdateDryRunIssuesNoConfirmationToken(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	exit, stdout, stderr := runCommand(t, "update", "--dry-run", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("dry-run exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status"] != "dry_run" {
		t.Fatalf("status = %v, want dry_run", data["status"])
	}
	if _, ok := data["confirm_token"]; ok {
		t.Fatal("dry-run must not emit confirm_token")
	}
	if _, ok := data["expires_at"]; ok {
		t.Fatal("dry-run must not emit expires_at")
	}
}

func TestUpdateCurrentVersionDryRunDoesNotMutate(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	server := updateMockReleaseServer(t, version, true)
	defer server.Close()
	withUpdateServer(t, server)

	originalSkillSync := updateSkillSync
	updateSkillSync = func(context.Context, string) error {
		t.Fatal("current-version dry-run must not sync the Skill")
		return nil
	}
	t.Cleanup(func() { updateSkillSync = originalSkillSync })

	exit, stdout, stderr := runCommand(t, "update", "--dry-run", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("dry-run exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status"] != "dry_run" {
		t.Fatalf("dry-run data=%#v", data)
	}
	changes := data["preview"].(map[string]any)["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["action"] != "sync skill directory" {
		t.Fatalf("current-version dry-run preview=%#v", data["preview"])
	}
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 1 {
		t.Fatalf("dry-run changed notice cache: %#v", cached)
	}
}

func TestUpdateRejectsConfirmWithoutNetwork(t *testing.T) {
	origAPI := updateGitHubAPI
	updateGitHubAPI = "http://127.0.0.1:0"
	t.Cleanup(func() { updateGitHubAPI = origAPI })

	exit, stdout, _ := runCommand(t, "update", "--confirm", "token", "--compact")
	if exit != 2 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_USAGE" {
		t.Fatalf("error = %#v", errObj)
	}
}

func TestUpdateRejectsFieldsBeforeMutation(t *testing.T) {
	forbidAdditionalUpdateMutationHooks(t)
	originalAPI := updateGitHubAPI
	updateGitHubAPI = "http://127.0.0.1:0"
	t.Cleanup(func() { updateGitHubAPI = originalAPI })

	exit, stdout, stderr := runCommand(t, "update", "--fields", "does_not_exist", "--compact")
	if exit != 2 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errorObject["code"] != "E_VALIDATION" || errorObject["retryable"] != false {
		t.Fatalf("error = %#v", errorObject)
	}
	assertUpdateFailureDetails(t, stdout, stageDiscover, false, "not_run")
}

func TestUpdateGlobalValidationFailureCarriesTerminalState(t *testing.T) {
	exit, stdout, _ := runCommand(t, "update", "--dry-run", "--confirm", "token", "--compact")
	if exit != 2 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	assertUpdateFailureDetails(t, stdout, stageDiscover, false, "not_run")
}

func TestUpdateSignatureGateFailsClosed(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", false)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	t.Cleanup(func() { updateDownloadHook = origDownload })
	updateDownloadHook = func(context.Context, string, string) error { return nil }

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit != 1 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_INTEGRITY" || errObj["retryable"] != false {
		t.Fatalf("error = %#v", errObj)
	}
	details := errObj["details"].(map[string]any)
	if details["stage"] != stageVerifySignature || details["binary_replaced"] != false {
		t.Fatalf("details = %#v", details)
	}
	assertUpdateFailureDetails(t, stdout, stageVerifySignature, false, "not_run")
}

func TestUpdateMissingReleaseAssetReportsFailureDetails(t *testing.T) {
	srv := updateMockReleaseServerWithAssets(t, "9.9.9", []string{"checksums.txt", "checksums.txt.sigstore.json"})
	defer srv.Close()
	withUpdateServer(t, srv)

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit == 0 {
		t.Fatalf("missing platform asset unexpectedly succeeded: %s", stdout)
	}
	assertUpdateFailureDetails(t, stdout, stageDiscover, false, "not_run")
}

func TestUpdateMissingChecksumReportsFailureDetails(t *testing.T) {
	assetName, err := updateArchiveName("9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	srv := updateMockReleaseServerWithAssets(t, "9.9.9", []string{assetName, "checksums.txt.sigstore.json"})
	defer srv.Close()
	withUpdateServer(t, srv)

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit == 0 {
		t.Fatalf("missing checksums.txt unexpectedly succeeded: %s", stdout)
	}
	assertUpdateFailureDetails(t, stdout, stageVerifyChecksum, false, "not_run")
}

func TestUpdateDownloadTimeoutReportsTimeoutWithDetails(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	t.Cleanup(func() { updateDownloadHook = origDownload })
	updateDownloadHook = func(context.Context, string, string) error { return context.DeadlineExceeded }

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit != 8 {
		t.Fatalf("timeout exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_TIMEOUT" || errObj["retryable"] != true {
		t.Fatalf("timeout error = %#v", errObj)
	}
	assertUpdateFailureDetails(t, stdout, stageDownload, false, "not_run")
}

func TestUpdateDownloadCancellationReportsInterruptedWithDetails(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	t.Cleanup(func() { updateDownloadHook = origDownload })
	updateDownloadHook = func(context.Context, string, string) error { return context.Canceled }

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit != 130 {
		t.Fatalf("cancellation exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_INTERRUPTED" || errObj["retryable"] != true {
		t.Fatalf("cancellation error = %#v", errObj)
	}
	assertUpdateFailureDetails(t, stdout, stageDownload, false, "not_run")
}

func TestUpdateMalformedArchiveFailsClosedWithDetails(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	origVerify := updateVerifySignature
	origChecksum := updateChecksumHook
	t.Cleanup(func() {
		updateDownloadHook = origDownload
		updateVerifySignature = origVerify
		updateChecksumHook = origChecksum
	})

	updateDownloadHook = func(_ context.Context, _, dest string) error {
		return os.WriteFile(dest, []byte("not an archive"), 0o600)
	}
	updateVerifySignature = func(context.Context, string, string, string) error { return nil }
	updateChecksumHook = func(string, string, string) error { return nil }

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit != 1 {
		t.Fatalf("malformed archive exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_INTEGRITY" || errObj["retryable"] != false {
		t.Fatalf("malformed archive error = %#v", errObj)
	}
	assertUpdateFailureDetails(t, stdout, stageReplace, false, "not_run")
}

func TestUpdateStandalonePartialSkillSyncFailure(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	origVerify := updateVerifySignature
	origChecksum := updateChecksumHook
	origExtract := updateExtractHook
	origApply := updateApply
	origSync := updateSkillSync
	origExec := updateExecutable
	t.Cleanup(func() {
		updateDownloadHook = origDownload
		updateVerifySignature = origVerify
		updateChecksumHook = origChecksum
		updateExtractHook = origExtract
		updateApply = origApply
		updateSkillSync = origSync
		updateExecutable = origExec
	})

	updateDownloadHook = func(context.Context, string, string) error { return nil }
	updateVerifySignature = func(context.Context, string, string, string) error { return nil }
	updateChecksumHook = func(string, string, string) error { return nil }
	updateExtractHook = func(string, string, string) (string, error) { return "new-bin", nil }
	updateApply = func(_ context.Context, _, dst string) (updateApplyResult, error) {
		return updateApplyResult{Status: "installed", Path: dst}, nil
	}
	updateSkillSync = func(context.Context, string) error { return errors.New("npx not found") }
	executable := filepath.Join(t.TempDir(), updateBinaryName)
	if err := os.WriteFile(executable, []byte("old binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	updateExecutable = func() (string, error) { return executable, nil }

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit != 7 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_NETWORK" || errObj["retryable"] != true {
		t.Fatalf("error = %#v", errObj)
	}
	details := errObj["details"].(map[string]any)
	if details["stage"] != stageSkillSync || details["binary_replaced"] != true || details["skill_sync_command"] == "" {
		t.Fatalf("details = %#v", details)
	}
	assertUpdateFailureDetails(t, stdout, stageSkillSync, true, "failed")
	assertNoMetaNotices(t, stdout)
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 0 {
		t.Fatalf("stale notice cache remains after partial update: %#v", cached)
	}
}

func TestUpdateNPMManagedDispatch(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)
	useNPMManagedExecutable(t)
	mockUpdateInstalledVersion(t, "9.9.9", nil)

	origPM := updateRunPackageManager
	origSync := updateSkillSync
	t.Cleanup(func() {
		updateRunPackageManager = origPM
		updateSkillSync = origSync
	})
	var gotMethod, gotVersion string
	updateRunPackageManager = func(_ context.Context, method, targetVersion string) error {
		gotMethod, gotVersion = method, targetVersion
		return nil
	}
	updateSkillSync = func(context.Context, string) error { return nil }

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("npm update exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if gotMethod != "npm" || gotVersion != "9.9.9" {
		t.Fatalf("package manager call = %q %q", gotMethod, gotVersion)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["install_method"] != "npm" || data["signature_status"] != "not_checked" || data["binary_replaced"] != true {
		t.Fatalf("npm data = %#v", data)
	}
	assertNoMetaNotices(t, stdout)
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 0 {
		t.Fatalf("stale notice cache remains after npm update: %#v", cached)
	}
}

func TestUpdateNPMManagedPartialSkillSyncFailure(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)
	useNPMManagedExecutable(t)
	mockUpdateInstalledVersion(t, "9.9.9", nil)

	originalPackageManager := updateRunPackageManager
	originalSkillSync := updateSkillSync
	t.Cleanup(func() {
		updateRunPackageManager = originalPackageManager
		updateSkillSync = originalSkillSync
	})
	updateRunPackageManager = func(context.Context, string, string) error { return nil }
	updateSkillSync = func(context.Context, string) error { return errors.New("npx not found") }

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 7 || stderr != "" {
		t.Fatalf("npm partial update exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errorObject["code"] != "E_NETWORK" || errorObject["retryable"] != true {
		t.Fatalf("npm partial update error = %#v", errorObject)
	}
	details := assertUpdateFailureDetails(t, stdout, stageSkillSync, true, "failed")
	for field, want := range map[string]any{
		"current_version":    "9.9.9",
		"previous_version":   version,
		"target_version":     "9.9.9",
		"update_available":   false,
		"install_method":     "npm",
		"signature_status":   "not_checked",
		"signature_verified": false,
		"checksum_verified":  false,
	} {
		if details[field] != want {
			t.Errorf("details[%q]=%#v want %#v in %#v", field, details[field], want, details)
		}
	}
	if details["skill_sync_command"] == "" {
		t.Errorf("npm partial update missing skill_sync_command: %#v", details)
	}
	assertNoMetaNotices(t, stdout)
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 0 {
		t.Fatalf("stale notice cache remains after npm partial update: %#v", cached)
	}
}

func TestUpdateNPMCancellationReportsInterrupted(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)
	useNPMManagedExecutable(t)
	mockUpdateInstalledVersion(t, version, nil)

	original := updateRunPackageManager
	updateRunPackageManager = func(context.Context, string, string) error { return context.Canceled }
	t.Cleanup(func() { updateRunPackageManager = original })

	exit, stdout, _ := runCommand(t, "update", "--compact")
	if exit != 130 {
		t.Fatalf("exit=%d stdout=%s", exit, stdout)
	}
	errObj := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObj["code"] != "E_INTERRUPTED" || errObj["retryable"] != true {
		t.Fatalf("error = %#v", errObj)
	}
	assertUpdateFailureDetails(t, stdout, stageReplace, false, "not_run")
}

func TestUpdateNPMManagedNoOpSkipsNPMAndSyncsSkill(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	seedStaleUpdateNotice(t, stateDir)
	srv := updateMockReleaseServer(t, version, true)
	defer srv.Close()
	withUpdateServer(t, srv)
	useNPMManagedExecutable(t)

	origPM := updateRunPackageManager
	origSync := updateSkillSync
	t.Cleanup(func() {
		updateRunPackageManager = origPM
		updateSkillSync = origSync
	})
	updateRunPackageManager = func(context.Context, string, string) error {
		t.Fatal("npm no-op must not execute npm")
		return nil
	}
	skillSyncCalls := 0
	updateSkillSync = func(context.Context, string) error {
		skillSyncCalls++
		return nil
	}

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("npm no-op exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status"] != "up_to_date" || data["install_method"] != "npm" || data["update_available"] != false {
		t.Fatalf("npm no-op data = %#v", data)
	}
	if data["skill_sync_status"] != "synced" || skillSyncCalls != 1 {
		t.Fatalf("npm no-op must sync the Skill once: calls=%d data=%#v", skillSyncCalls, data)
	}
	assertNoMetaNotices(t, stdout)
	if cached := readUpdateNoticeCache(stateDir); len(cached) != 0 {
		t.Fatalf("stale notice cache remains after npm no-op: %#v", cached)
	}
}

func TestApplyUpdateBinarySwapsInPlace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, updateBinaryName)
	if err := os.WriteFile(dst, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "extracted")
	if err := os.WriteFile(src, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := applyUpdateBinary(t.Context(), src, dst)
	if err != nil {
		t.Fatalf("applyUpdateBinary: %v", err)
	}
	resultInfo, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "installed" || !os.SameFile(resultInfo, targetInfo) {
		t.Fatalf("result = %#v", res)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Fatalf("target = %q, want NEW", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "."+updateBinaryName+".new")); !os.IsNotExist(err) {
		t.Fatalf(".new staging file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "."+updateBinaryName+".old")); !os.IsNotExist(err) {
		t.Fatalf(".old backup file remains: %v", err)
	}
}

func TestDetectInstallMethodFromNPMWrapperAndPackageLayout(t *testing.T) {
	t.Setenv("CLIPROXYAPI_CLI_INSTALL_METHOD", "npm")
	if got := detectInstallMethod(filepath.Join(t.TempDir(), updateBinaryName)); got != "binary" {
		t.Fatalf("environment marker must not reclassify a standalone binary: %q", got)
	}
	if got := detectInstallMethod(useNPMManagedExecutable(t)); got != "npm" {
		t.Fatalf("platform package install method = %q, want npm", got)
	}

	lookalike := filepath.Join(t.TempDir(), "node_modules", "@fateforge", "cliproxyapi-cli-lookalike")
	if err := os.MkdirAll(filepath.Join(lookalike, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lookalike, "package.json"), []byte(`{"name":"@fateforge/cliproxyapi-cli-lookalike"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectInstallMethod(filepath.Join(lookalike, "bin", updateBinaryName)); got != "binary" {
		t.Fatalf("lookalike package must not be trusted as npm-managed: %q", got)
	}
}

func TestUpdateGitHubTokenIsScopedAndRedacted(t *testing.T) {
	const secret = "github-token-secret"
	t.Setenv("GITHUB_TOKEN", secret)

	githubRequest, err := newUpdateRequest(t.Context(), "https://api.github.com/repos/fatecannotbealtered/cliproxyapi-cli/releases/latest", "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if githubRequest.Header.Get("Authorization") != "Bearer "+secret {
		t.Fatal("GitHub API request did not receive the configured token")
	}
	externalRequest, err := newUpdateRequest(t.Context(), "https://example.com/release", "application/octet-stream")
	if err != nil {
		t.Fatal(err)
	}
	if externalRequest.Header.Get("Authorization") != "" {
		t.Fatal("GitHub token leaked to a non-GitHub host")
	}
	if got := truncateForError("upstream echoed "+secret, 200); strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
		t.Fatalf("secret was not redacted: %q", got)
	}
}

func TestUpdateNoticeSeverityUsesChangelogDerivedReleaseNotes(t *testing.T) {
	if !updateReleaseNotesHaveSecurity("## [1.0.2]\n\n### Security\n\n- Fix a verification issue.\n") {
		t.Fatal("security release note was not detected")
	}
	if updateReleaseNotesHaveSecurity("## [1.0.2]\n\n### Security\n\n### Fixed\n\n- Routine fix.\n") {
		t.Fatal("empty security heading must not elevate severity")
	}
	notices := updateNoticesFromPlan(updatePlan{
		CurrentVersion:  "1.0.1",
		TargetVersion:   "1.0.2",
		UpdateAvailable: true,
		SecurityUpdate:  true,
		InstallMethod:   "binary",
	}, "update_check")
	if len(notices) != 1 || notices[0].Severity != "warning" {
		t.Fatalf("notices = %#v", notices)
	}
}

func TestUpdateCheckUsesCompleteTargetChangelogDelta(t *testing.T) {
	current, ok := parseUpdateSemver(version)
	if !ok {
		t.Fatalf("runtime version %q is not semantic", version)
	}
	intermediate := fmt.Sprintf("%d.%d.0", current.Major, current.Minor+1)
	latest := fmt.Sprintf("%d.%d.0", current.Major, current.Minor+2)
	changelogRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/releases/latest"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"tag_name":"v%s","html_url":"https://example.com/release","body":"","assets":[]}`, latest)
		case strings.HasSuffix(request.URL.Path, "/CHANGELOG.md"):
			changelogRequests++
			_, _ = fmt.Fprintf(w, "# Changelog\n\n## [%s] - 2026-08-10\n\n### Added\n\n- Routine change.\n\n## [%s] - 2026-08-09\n\n### Security\n\n- Security fix.\n\n## [%s] - 2026-08-08\n", latest, intermediate, version)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	withUpdateServer(t, server)

	exit, stdout, stderr := runCommand(t, "update", "--check", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("update --check exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	if changelogRequests != 1 {
		t.Fatalf("target changelog requests=%d want 1", changelogRequests)
	}
	notices := decodeEnvelope(t, stdout)["data"].(map[string]any)["notices"].([]any)
	if len(notices) != 1 || notices[0].(map[string]any)["severity"] != "warning" {
		t.Fatalf("complete-delta notice=%#v, want warning", notices)
	}
}
