package cmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type additionalUpdateRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn additionalUpdateRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func additionalUpdateHigherVersion(t *testing.T) string {
	t.Helper()
	current, ok := parseUpdateSemver(version)
	if !ok {
		t.Fatalf("running version %q is not semantic", version)
	}
	return fmt.Sprintf("%d.0.0", current.Major+1)
}

func assertAdditionalUpdateFailure(
	t *testing.T,
	exit int,
	stdout []byte,
	stderr string,
	wantExit int,
	wantCode string,
	wantRetryable bool,
	wantStage string,
) map[string]any {
	t.Helper()
	if exit != wantExit || stderr != "" {
		t.Fatalf("exit=%d want=%d stderr=%q stdout=%s", exit, wantExit, stderr, stdout)
	}
	envelope := decodeEnvelope(t, stdout)
	if envelope["ok"] != false || envelope["schema_version"] != "1.0" {
		t.Fatalf("failure envelope header = %#v", envelope)
	}
	errorObject, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("failure envelope missing error object: %#v", envelope)
	}
	if errorObject["code"] != wantCode || errorObject["retryable"] != wantRetryable {
		t.Fatalf("error triple = exit %d error %#v", exit, errorObject)
	}
	details := assertUpdateFailureDetails(t, stdout, wantStage, false, "not_run")
	if details["current_version"] != version {
		t.Fatalf("current_version=%#v want %q in %#v", details["current_version"], version, details)
	}
	return details
}

func forbidAdditionalUpdateMutationHooks(t *testing.T) {
	t.Helper()
	originalDownload := updateDownloadHook
	originalVerify := updateVerifySignature
	originalChecksum := updateChecksumHook
	originalExtract := updateExtractHook
	originalApply := updateApply
	originalPackageManager := updateRunPackageManager
	originalSkillSync := updateSkillSync
	t.Cleanup(func() {
		updateDownloadHook = originalDownload
		updateVerifySignature = originalVerify
		updateChecksumHook = originalChecksum
		updateExtractHook = originalExtract
		updateApply = originalApply
		updateRunPackageManager = originalPackageManager
		updateSkillSync = originalSkillSync
	})
	updateDownloadHook = func(context.Context, string, string) error {
		t.Fatal("read-only/no-op update must not download")
		return nil
	}
	updateVerifySignature = func(context.Context, string, string, string) error {
		t.Fatal("read-only/no-op update must not verify a downloaded signature")
		return nil
	}
	updateChecksumHook = func(string, string, string) error {
		t.Fatal("read-only/no-op update must not verify a downloaded checksum")
		return nil
	}
	updateExtractHook = func(string, string, string) (string, error) {
		t.Fatal("read-only/no-op update must not extract")
		return "", nil
	}
	updateApply = func(context.Context, string, string) (updateApplyResult, error) {
		t.Fatal("read-only/no-op update must not replace the binary")
		return updateApplyResult{}, nil
	}
	updateRunPackageManager = func(context.Context, string, string) error {
		t.Fatal("read-only/no-op update must not run npm")
		return nil
	}
	updateSkillSync = func(context.Context, string) error {
		t.Fatal("read-only/no-op update must not sync the Skill")
		return nil
	}
}

func TestUpdateDiscoverFailureContract(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantCode  string
		wantExit  int
		retryable bool
		wantHint  string
		headers   map[string]string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: "E_AUTH", wantExit: 4, retryable: false, wantHint: "GitHub access"},
		{name: "forbidden", status: http.StatusForbidden, wantCode: "E_FORBIDDEN", wantExit: 4, retryable: false, wantHint: "GitHub access"},
		{name: "GitHub rate limited", status: http.StatusForbidden, wantCode: "E_RATE_LIMITED", wantExit: 7, retryable: true, headers: map[string]string{"X-RateLimit-Remaining": "0"}},
		{name: "not found", status: http.StatusNotFound, wantCode: "E_NOT_FOUND", wantExit: 3, retryable: false},
		{name: "conflict", status: http.StatusConflict, wantCode: "E_CONFLICT", wantExit: 6, retryable: false},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: "E_RATE_LIMITED", wantExit: 7, retryable: true},
		{name: "server error", status: http.StatusServiceUnavailable, wantCode: "E_SERVER", wantExit: 7, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for name, value := range test.headers {
					w.Header().Set(name, value)
				}
				http.Error(w, "temporary upstream failure", test.status)
			}))
			defer server.Close()
			withUpdateServer(t, server)

			exit, stdout, stderr := runCommand(t, "update", "--check", "--compact")
			details := assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, test.retryable, stageDiscover)
			if test.retryable && details["next_step"] == "" {
				t.Fatalf("transient failure missing next_step: %#v", details)
			}
			if test.wantHint != "" && !strings.Contains(fmt.Sprint(details["next_step"]), test.wantHint) {
				t.Fatalf("next_step=%q want substring %q", details["next_step"], test.wantHint)
			}
		})
	}
}

func TestUpdateDiscoverTimeoutContract(t *testing.T) {
	originalClient := updateHTTPClient
	updateHTTPClient = &http.Client{Transport: additionalUpdateRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	t.Cleanup(func() { updateHTTPClient = originalClient })

	exit, stdout, stderr := runCommand(t, "update", "--check", "--compact")
	assertAdditionalUpdateFailure(t, exit, stdout, stderr, 8, "E_TIMEOUT", true, stageDiscover)
}

func TestUpdateCheckChangelogContextFailureContract(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantExit  int
		retryable bool
	}{
		{name: "cancelled", err: context.Canceled, wantCode: "E_INTERRUPTED", wantExit: 130, retryable: true},
		{name: "timed out", err: context.DeadlineExceeded, wantCode: "E_TIMEOUT", wantExit: 8, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalClient := updateHTTPClient
			updateHTTPClient = &http.Client{Transport: additionalUpdateRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if strings.HasSuffix(request.URL.Path, "/releases/latest") {
					body := fmt.Sprintf(`{"tag_name":"v%s","html_url":"https://example.com/release","body":"","assets":[]}`, target)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    request,
					}, nil
				}
				return nil, test.err
			})}
			t.Cleanup(func() { updateHTTPClient = originalClient })

			exit, stdout, stderr := runCommand(t, "update", "--check", "--compact")
			assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, test.retryable, stageDiscover)
		})
	}
}

func TestUpdateMissingReleasePartsUseExactErrorContract(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	assetName, err := updateArchiveName(target)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		assets    []string
		wantCode  string
		wantExit  int
		wantStage string
	}{
		{
			name:      "platform archive",
			assets:    []string{"checksums.txt", "checksums.txt.sigstore.json"},
			wantCode:  "E_NOT_FOUND",
			wantExit:  3,
			wantStage: stageDiscover,
		},
		{
			name:      "checksums",
			assets:    []string{assetName, "checksums.txt.sigstore.json"},
			wantCode:  "E_INTEGRITY",
			wantExit:  1,
			wantStage: stageVerifyChecksum,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServerWithAssets(t, target, test.assets)
			defer server.Close()
			withUpdateServer(t, server)

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, false, test.wantStage)
		})
	}
}

func TestUpdateNPMFailureContract(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantExit  int
		retryable bool
	}{
		{name: "ordinary failure", err: errors.New("npm exited with status 1"), wantCode: "E_IO", wantExit: 1},
		{name: "registry network failure", err: errors.New("npm ERR code ECONNRESET"), wantCode: "E_NETWORK", wantExit: 7, retryable: true},
		{name: "timeout", err: context.DeadlineExceeded, wantCode: "E_TIMEOUT", wantExit: 8, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)
			useNPMManagedExecutable(t)
			mockUpdateInstalledVersion(t, version, nil)

			originalPackageManager := updateRunPackageManager
			originalSkillSync := updateSkillSync
			updateRunPackageManager = func(context.Context, string, string) error { return test.err }
			updateSkillSync = func(context.Context, string) error {
				t.Fatal("failed npm update must not sync the Skill")
				return nil
			}
			t.Cleanup(func() {
				updateRunPackageManager = originalPackageManager
				updateSkillSync = originalSkillSync
			})

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			details := assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, test.retryable, stageReplace)
			if details["install_method"] != "npm" || details["command"] != updateInstallCommand("npm", target) {
				t.Fatalf("npm recovery details = %#v", details)
			}
		})
	}
}

func TestUpdateNPMPostStateVerification(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	tests := []struct {
		name               string
		managerErr         error
		observedVersion    string
		observeErr         error
		wantCode           string
		wantExit           int
		wantState          string
		wantCurrentVersion string
		wantReplaced       any
	}{
		{name: "successful npm command left previous version", observedVersion: version, wantCode: "E_CONFLICT", wantExit: 6, wantState: "previous", wantCurrentVersion: version, wantReplaced: false},
		{name: "successful npm command has unknown state", observeErr: errors.New("installed executable missing"), wantCode: "E_CONFLICT", wantExit: 6, wantState: "unknown", wantCurrentVersion: "unknown", wantReplaced: nil},
		{name: "failed npm command nevertheless installed target", managerErr: errors.New("npm exited with status 1"), observedVersion: target, wantCode: "E_IO", wantExit: 1, wantState: "target", wantCurrentVersion: target, wantReplaced: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)
			useNPMManagedExecutable(t)
			mockUpdateInstalledVersion(t, test.observedVersion, test.observeErr)

			originalPackageManager := updateRunPackageManager
			originalSkillSync := updateSkillSync
			updateRunPackageManager = func(context.Context, string, string) error { return test.managerErr }
			updateSkillSync = func(context.Context, string) error {
				t.Fatal("unverified npm state must not sync the Skill")
				return nil
			}
			t.Cleanup(func() {
				updateRunPackageManager = originalPackageManager
				updateSkillSync = originalSkillSync
			})

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			if exit != test.wantExit || stderr != "" {
				t.Fatalf("exit=%d want=%d stderr=%q stdout=%s", exit, test.wantExit, stderr, stdout)
			}
			errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
			if errorObject["code"] != test.wantCode {
				t.Fatalf("error=%#v want code %s", errorObject, test.wantCode)
			}
			details := errorObject["details"].(map[string]any)
			if details["install_state"] != test.wantState || details["current_version"] != test.wantCurrentVersion || details["binary_replaced"] != test.wantReplaced {
				t.Fatalf("post-state=%#v want state=%s current=%s replaced=%#v", details, test.wantState, test.wantCurrentVersion, test.wantReplaced)
			}
			if test.wantState == "target" && details["skill_sync_command"] == "" {
				t.Fatalf("target partial state missing Skill recovery command: %#v", details)
			}
		})
	}
}

func TestUpdateLocalIOFailureClassification(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	tests := []struct {
		name      string
		configure func(t *testing.T)
		wantStage string
		wantCode  string
		wantExit  int
	}{
		{
			name: "download permission",
			configure: func(t *testing.T) {
				updateDownloadHook = func(context.Context, string, string) error {
					return &updateLocalIOError{err: os.ErrPermission}
				}
			},
			wantStage: stageDownload,
			wantCode:  "E_FORBIDDEN",
			wantExit:  4,
		},
		{
			name: "checksum read",
			configure: func(t *testing.T) {
				updateChecksumHook = func(string, string, string) error {
					return &updateLocalIOError{err: errors.New("disk read failed")}
				}
			},
			wantStage: stageVerifyChecksum,
			wantCode:  "E_IO",
			wantExit:  1,
		},
		{
			name: "extract write",
			configure: func(t *testing.T) {
				updateExtractHook = func(string, string, string) (string, error) {
					return "", &updateLocalIOError{err: errors.New("disk write failed")}
				}
			},
			wantStage: stageReplace,
			wantCode:  "E_IO",
			wantExit:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)

			originalDownload := updateDownloadHook
			originalVerify := updateVerifySignature
			originalChecksum := updateChecksumHook
			originalExtract := updateExtractHook
			originalApply := updateApply
			originalSkillSync := updateSkillSync
			originalExecutable := updateExecutable
			t.Cleanup(func() {
				updateDownloadHook = originalDownload
				updateVerifySignature = originalVerify
				updateChecksumHook = originalChecksum
				updateExtractHook = originalExtract
				updateApply = originalApply
				updateSkillSync = originalSkillSync
				updateExecutable = originalExecutable
			})
			updateDownloadHook = func(context.Context, string, string) error { return nil }
			updateVerifySignature = func(context.Context, string, string, string) error { return nil }
			updateChecksumHook = func(string, string, string) error { return nil }
			updateExtractHook = func(string, string, string) (string, error) { return "new-binary", nil }
			updateApply = func(context.Context, string, string) (updateApplyResult, error) {
				t.Fatal("local pre-swap failure must not replace the binary")
				return updateApplyResult{}, nil
			}
			updateSkillSync = func(context.Context, string) error {
				t.Fatal("local pre-swap failure must not sync the Skill")
				return nil
			}
			executable := filepath.Join(t.TempDir(), updateBinaryName)
			if err := os.WriteFile(executable, []byte("old binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			updateExecutable = func() (string, error) { return executable, nil }
			test.configure(t)

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			details := assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, false, test.wantStage)
			if test.wantCode == "E_FORBIDDEN" && !strings.Contains(fmt.Sprint(details["next_step"]), "local permissions") {
				t.Fatalf("local permission failure has wrong recovery hint: %#v", details)
			}
		})
	}
}

func TestUpdateChecksumFailuresAtCommandBoundary(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	assetName, err := updateArchiveName(target)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name            string
		checksum        string
		wantMessagePart string
	}{
		{
			name:            "mismatch",
			checksum:        strings.Repeat("0", 64) + "  " + assetName + "\n",
			wantMessagePart: "checksum mismatch",
		},
		{
			name:            "invalid digest",
			checksum:        "not-a-sha256  " + assetName + "\n",
			wantMessagePart: "not a SHA-256 digest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)

			originalDownload := updateDownloadHook
			originalVerify := updateVerifySignature
			originalExtract := updateExtractHook
			originalApply := updateApply
			originalExecutable := updateExecutable
			updateDownloadHook = func(_ context.Context, _ string, destination string) error {
				switch filepath.Base(destination) {
				case assetName:
					return os.WriteFile(destination, []byte("archive bytes"), 0o600)
				case "checksums.txt":
					return os.WriteFile(destination, []byte(test.checksum), 0o600)
				case "checksums.txt.sigstore.json":
					return os.WriteFile(destination, []byte("{}"), 0o600)
				default:
					return fmt.Errorf("unexpected download destination %s", destination)
				}
			}
			updateVerifySignature = func(context.Context, string, string, string) error { return nil }
			updateExtractHook = func(string, string, string) (string, error) {
				t.Fatal("invalid checksum must stop before extraction")
				return "", nil
			}
			updateApply = func(context.Context, string, string) (updateApplyResult, error) {
				t.Fatal("invalid checksum must stop before replacement")
				return updateApplyResult{}, nil
			}
			executable := filepath.Join(t.TempDir(), updateBinaryName)
			if err := os.WriteFile(executable, []byte("old binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			updateExecutable = func() (string, error) { return executable, nil }
			t.Cleanup(func() {
				updateDownloadHook = originalDownload
				updateVerifySignature = originalVerify
				updateExtractHook = originalExtract
				updateApply = originalApply
				updateExecutable = originalExecutable
			})

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			assertAdditionalUpdateFailure(t, exit, stdout, stderr, 1, "E_INTEGRITY", false, stageVerifyChecksum)
			errorObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
			if !strings.Contains(errorObject["message"].(string), test.wantMessagePart) {
				t.Fatalf("message=%q want substring %q", errorObject["message"], test.wantMessagePart)
			}
		})
	}
}

func TestUpdateStandaloneCompleteSuccessContract(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	assetName, err := updateArchiveName(target)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	writeTestUpdateNotice(t, stateDir, target, "info")
	server := updateMockReleaseServer(t, target, true)
	defer server.Close()
	withUpdateServer(t, server)

	originalDownload := updateDownloadHook
	originalVerify := updateVerifySignature
	originalChecksum := updateChecksumHook
	originalExtract := updateExtractHook
	originalApply := updateApply
	originalSkillSync := updateSkillSync
	originalExecutable := updateExecutable
	t.Cleanup(func() {
		updateDownloadHook = originalDownload
		updateVerifySignature = originalVerify
		updateChecksumHook = originalChecksum
		updateExtractHook = originalExtract
		updateApply = originalApply
		updateSkillSync = originalSkillSync
		updateExecutable = originalExecutable
	})

	archive := []byte("verified standalone archive")
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), assetName)
	executable := filepath.Join(t.TempDir(), updateBinaryName)
	if err := os.WriteFile(executable, []byte("old binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	var events []string
	updateExecutable = func() (string, error) { return executable, nil }
	updateDownloadHook = func(_ context.Context, _ string, destination string) error {
		name := filepath.Base(destination)
		events = append(events, "download:"+name)
		switch name {
		case assetName:
			return os.WriteFile(destination, archive, 0o600)
		case "checksums.txt":
			return os.WriteFile(destination, []byte(checksum), 0o600)
		case "checksums.txt.sigstore.json":
			return os.WriteFile(destination, []byte("{}"), 0o600)
		default:
			return fmt.Errorf("unexpected download destination %s", destination)
		}
	}
	updateVerifySignature = func(_ context.Context, checksumPath, bundlePath, identity string) error {
		events = append(events, "verify_signature")
		if filepath.Base(checksumPath) != "checksums.txt" || filepath.Base(bundlePath) != "checksums.txt.sigstore.json" {
			return fmt.Errorf("unexpected signature inputs %s %s", checksumPath, bundlePath)
		}
		if want := updateSignerIdentityRegexp(target); identity != want {
			return fmt.Errorf("signer identity=%q want %q", identity, want)
		}
		return nil
	}
	updateChecksumHook = func(archivePath, checksumPath, name string) error {
		events = append(events, "verify_checksum")
		return verifyUpdateChecksum(archivePath, checksumPath, name)
	}
	updateExtractHook = func(_ string, _ string, tempDir string) (string, error) {
		events = append(events, "extract")
		path := filepath.Join(tempDir, "new-binary")
		return path, os.WriteFile(path, []byte("new binary"), 0o700)
	}
	updateApply = func(_ context.Context, source, destination string) (updateApplyResult, error) {
		events = append(events, "replace")
		if filepath.Base(source) != "new-binary" || destination != executable {
			return updateApplyResult{}, fmt.Errorf("unexpected replacement %s -> %s", source, destination)
		}
		return updateApplyResult{Status: "installed", Path: destination}, nil
	}
	updateSkillSync = func(_ context.Context, repo string) error {
		events = append(events, "skill_sync")
		if repo != updateSkillRepo {
			return fmt.Errorf("skill repo=%q want %q", repo, updateSkillRepo)
		}
		return nil
	}

	exit, stdout, stderr := runCommand(t, "update", "--compact")
	if exit != 0 || stderr != "" {
		t.Fatalf("standalone update exit=%d stderr=%q stdout=%s", exit, stderr, stdout)
	}
	wantEvents := strings.Join([]string{
		"download:" + assetName,
		"download:checksums.txt",
		"download:checksums.txt.sigstore.json",
		"verify_signature",
		"verify_checksum",
		"extract",
		"replace",
		"skill_sync",
	}, ",")
	if got := strings.Join(events, ","); got != wantEvents {
		t.Fatalf("update stages=%q want %q", got, wantEvents)
	}
	data := decodeEnvelope(t, stdout)["data"].(map[string]any)
	if data["status"] != "updated" || data["previous_version"] != version || data["current_version"] != target {
		t.Fatalf("standalone version result = %#v", data)
	}
	for field, want := range map[string]any{
		"target_version":     target,
		"update_available":   false,
		"signature_status":   "verified",
		"signature_verified": true,
		"checksum_verified":  true,
		"binary_replaced":    true,
		"skill_sync_status":  "synced",
		"path":               executable,
	} {
		if data[field] != want {
			t.Errorf("data[%q]=%#v want %#v in %#v", field, data[field], want, data)
		}
	}
	if hint, _ := data["hint"].(string); !strings.Contains(hint, "changelog --since "+version) {
		t.Errorf("post-update hint=%q", hint)
	}
	assertNoMetaNotices(t, stdout)
	if notices := readUpdateNoticeCache(stateDir); len(notices) != 0 {
		t.Fatalf("successful update left cached notice: %#v", notices)
	}
}

func TestUpdateReadOnlyModesDoNotMutateNPMInstall(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	for _, args := range [][]string{
		{"update", "--check", "--compact"},
		{"update", "--dry-run", "--compact"},
	} {
		t.Run(strings.Join(args[1:], "_"), func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)
			useNPMManagedExecutable(t)
			forbidAdditionalUpdateMutationHooks(t)

			exit, stdout, stderr := runCommand(t, args...)
			if exit != 0 || stderr != "" {
				t.Fatalf("%v exit=%d stderr=%q stdout=%s", args, exit, stderr, stdout)
			}
			data := decodeEnvelope(t, stdout)["data"].(map[string]any)
			if data["install_method"] != "npm" {
				t.Fatalf("%v install_method=%#v", args, data["install_method"])
			}
		})
	}
}

func TestUpdateReplaceFailureContract(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	tests := []struct {
		name      string
		err       error
		wantCode  string
		wantExit  int
		retryable bool
	}{
		{name: "permission", err: fmt.Errorf("replace denied: %w", os.ErrPermission), wantCode: "E_FORBIDDEN", wantExit: 4},
		{name: "io", err: errors.New("disk full"), wantCode: "E_IO", wantExit: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)

			originalDownload := updateDownloadHook
			originalVerify := updateVerifySignature
			originalChecksum := updateChecksumHook
			originalExtract := updateExtractHook
			originalApply := updateApply
			originalSkillSync := updateSkillSync
			originalExecutable := updateExecutable
			updateDownloadHook = func(context.Context, string, string) error { return nil }
			updateVerifySignature = func(context.Context, string, string, string) error { return nil }
			updateChecksumHook = func(string, string, string) error { return nil }
			updateExtractHook = func(string, string, string) (string, error) { return "new-binary", nil }
			updateApply = func(context.Context, string, string) (updateApplyResult, error) { return updateApplyResult{}, test.err }
			updateSkillSync = func(context.Context, string) error {
				t.Fatal("failed replacement must not sync the Skill")
				return nil
			}
			executable := filepath.Join(t.TempDir(), updateBinaryName)
			if err := os.WriteFile(executable, []byte("old binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			updateExecutable = func() (string, error) { return executable, nil }
			t.Cleanup(func() {
				updateDownloadHook = originalDownload
				updateVerifySignature = originalVerify
				updateChecksumHook = originalChecksum
				updateExtractHook = originalExtract
				updateApply = originalApply
				updateSkillSync = originalSkillSync
				updateExecutable = originalExecutable
			})

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			details := assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, test.retryable, stageReplace)
			if details["next_step"] == "" {
				t.Fatalf("replace failure missing recovery next_step: %#v", details)
			}
		})
	}
}

func TestUpdatePreSwapCancellationContract(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	tests := []struct {
		name      string
		wantStage string
		configure func(t *testing.T)
	}{
		{
			name:      "checksum",
			wantStage: stageVerifyChecksum,
			configure: func(t *testing.T) {
				updateChecksumHook = func(string, string, string) error { return context.Canceled }
				updateExtractHook = func(string, string, string) (string, error) {
					t.Fatal("cancelled checksum must not extract")
					return "", nil
				}
			},
		},
		{
			name:      "extract",
			wantStage: stageReplace,
			configure: func(t *testing.T) {
				updateChecksumHook = func(string, string, string) error { return nil }
				updateExtractHook = func(string, string, string) (string, error) { return "", context.Canceled }
			},
		},
		{
			name:      "replacement precommit",
			wantStage: stageReplace,
			configure: func(t *testing.T) {
				updateChecksumHook = func(string, string, string) error { return nil }
				updateExtractHook = func(string, string, string) (string, error) { return "new-binary", nil }
				updateApply = func(context.Context, string, string) (updateApplyResult, error) {
					return updateApplyResult{}, context.Canceled
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := updateMockReleaseServer(t, target, true)
			defer server.Close()
			withUpdateServer(t, server)

			originalDownload := updateDownloadHook
			originalVerify := updateVerifySignature
			originalChecksum := updateChecksumHook
			originalExtract := updateExtractHook
			originalApply := updateApply
			originalSkillSync := updateSkillSync
			originalExecutable := updateExecutable
			t.Cleanup(func() {
				updateDownloadHook = originalDownload
				updateVerifySignature = originalVerify
				updateChecksumHook = originalChecksum
				updateExtractHook = originalExtract
				updateApply = originalApply
				updateSkillSync = originalSkillSync
				updateExecutable = originalExecutable
			})
			updateDownloadHook = func(context.Context, string, string) error { return nil }
			updateVerifySignature = func(context.Context, string, string, string) error { return nil }
			updateApply = func(context.Context, string, string) (updateApplyResult, error) {
				t.Fatal("cancelled pre-swap stage must not replace the binary")
				return updateApplyResult{}, nil
			}
			updateSkillSync = func(context.Context, string) error {
				t.Fatal("cancelled pre-swap stage must not sync the Skill")
				return nil
			}
			executable := filepath.Join(t.TempDir(), updateBinaryName)
			if err := os.WriteFile(executable, []byte("old binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			updateExecutable = func() (string, error) { return executable, nil }
			test.configure(t)

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			assertAdditionalUpdateFailure(t, exit, stdout, stderr, 130, "E_INTERRUPTED", true, test.wantStage)
			installed, err := os.ReadFile(executable)
			if err != nil || string(installed) != "old binary" {
				t.Fatalf("installed binary changed after cancellation: %q err=%v", installed, err)
			}
		})
	}
}

func TestUpdateFailureEnvelopeCarriesCachedNotice(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CLIPROXYAPI_CLI_STATE_DIR", stateDir)
	writeTestUpdateNotice(t, stateDir, additionalUpdateHigherVersion(t), "warning")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary upstream failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	withUpdateServer(t, server)

	exit, stdout, stderr := runCommand(t, "update", "--check", "--compact")
	assertAdditionalUpdateFailure(t, exit, stdout, stderr, 7, "E_SERVER", true, stageDiscover)
	meta := decodeEnvelope(t, stdout)["meta"].(map[string]any)
	notices, ok := meta["notices"].([]any)
	if !ok || len(notices) != 1 {
		t.Fatalf("failure meta.notices = %#v", meta["notices"])
	}
	notice := notices[0].(map[string]any)
	if notice["type"] != "update_available" || notice["severity"] != "warning" || notice["source"] != "cache" {
		t.Fatalf("cached failure notice = %#v", notice)
	}
}
