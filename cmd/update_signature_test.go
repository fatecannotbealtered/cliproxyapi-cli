package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/contract"
)

func TestVerifyUpdateChecksumSignatureFailClosed(t *testing.T) {
	tmp := t.TempDir()

	status, code, err := verifyUpdateChecksumSignature(context.Background(), filepath.Join(tmp, "checksums.txt"), "", tmp, "1.5.0")
	if err == nil || !strings.Contains(err.Error(), "unsigned release") {
		t.Fatalf("missing bundle error = %v", err)
	}
	if status != "missing" || code != "E_INTEGRITY" {
		t.Fatalf("missing bundle status=%q code=%q", status, code)
	}

	origDownload := updateDownloadHook
	origVerify := updateVerifySignature
	t.Cleanup(func() {
		updateDownloadHook = origDownload
		updateVerifySignature = origVerify
	})
	updateDownloadHook = func(context.Context, string, string) error { return nil }
	updateVerifySignature = func(context.Context, string, string, string) error { return nil }
	status, _, err = verifyUpdateChecksumSignature(context.Background(), filepath.Join(tmp, "checksums.txt"), "https://example.com/bundle.json", tmp, "1.5.0")
	if err != nil || status != "verified" {
		t.Fatalf("verified status=%q err=%v", status, err)
	}

	updateVerifySignature = func(context.Context, string, string, string) error {
		return errors.New("certificate identity mismatch")
	}
	status, code, err = verifyUpdateChecksumSignature(context.Background(), filepath.Join(tmp, "checksums.txt"), "https://example.com/bundle.json", tmp, "1.5.0")
	if err == nil || status != "failed" || code != "E_INTEGRITY" {
		t.Fatalf("failed status=%q code=%q err=%v", status, code, err)
	}
}

func TestVerifySigstoreBundleClassifiesBundleReadFailure(t *testing.T) {
	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "checksums.txt")
	bundlePath := filepath.Join(tmp, "checksums.txt.sigstore.json")
	if err := os.WriteFile(artifactPath, []byte("checksum"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifySigstoreBundle(context.Background(), artifactPath, bundlePath, ".*")
	var localErr *updateLocalIOError
	if !errors.As(err, &localErr) {
		t.Fatalf("error=%T %v, want updateLocalIOError", err, err)
	}
}

func TestUpdateSignerIdentityPinsReleaseWorkflow(t *testing.T) {
	exact := regexp.MustCompile(updateSignerIdentityRegexp("1.5.0"))
	if !exact.MatchString("https://github.com/fatecannotbealtered/cliproxyapi-cli/.github/workflows/release.yml@refs/tags/v1.5.0") {
		t.Fatal("target release workflow identity should match")
	}
	if exact.MatchString("https://github.com/fatecannotbealtered/cliproxyapi-cli/.github/workflows/release.yml@refs/tags/v1.5.1") {
		t.Fatal("different release tag must not match an exact target identity")
	}
}

func TestVerifyUpdateChecksumSignatureNetworkClasses(t *testing.T) {
	tmp := t.TempDir()
	origDownload := updateDownloadHook
	origVerify := updateVerifySignature
	t.Cleanup(func() {
		updateDownloadHook = origDownload
		updateVerifySignature = origVerify
	})

	updateDownloadHook = func(context.Context, string, string) error {
		return &updateHTTPError{StatusCode: http.StatusServiceUnavailable, err: errors.New("503")}
	}
	status, code, err := verifyUpdateChecksumSignature(context.Background(), filepath.Join(tmp, "checksums.txt"), "https://example.com/bundle.json", tmp, "1.5.0")
	if err == nil || status != "download_failed" || code != "E_SERVER" {
		t.Fatalf("bundle download status=%q code=%q err=%v", status, code, err)
	}

	updateDownloadHook = func(context.Context, string, string) error { return nil }
	updateVerifySignature = func(context.Context, string, string, string) error {
		return errTrustRootUnavailable
	}
	status, code, err = verifyUpdateChecksumSignature(context.Background(), filepath.Join(tmp, "checksums.txt"), "https://example.com/bundle.json", tmp, "1.5.0")
	if err == nil || status != "trust_root_unavailable" || code != "E_INTEGRITY" {
		t.Fatalf("trust root status=%q code=%q err=%v", status, code, err)
	}
	if contract.Retryable(code) {
		t.Fatalf("trust root failure code %q must not be retryable", code)
	}
}

func TestUpdateSignatureLocalReadFailureContract(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
		wantExit int
	}{
		{name: "permission", err: &updateLocalIOError{err: os.ErrPermission}, wantCode: "E_FORBIDDEN", wantExit: 4},
		{name: "local IO", err: &updateLocalIOError{err: errors.New("disk read failed")}, wantCode: "E_IO", wantExit: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := updateMockReleaseServer(t, "9.9.9", true)
			defer srv.Close()
			withUpdateServer(t, srv)

			originalDownload := updateDownloadHook
			originalVerify := updateVerifySignature
			updateDownloadHook = func(context.Context, string, string) error { return nil }
			updateVerifySignature = func(context.Context, string, string, string) error { return test.err }
			t.Cleanup(func() {
				updateDownloadHook = originalDownload
				updateVerifySignature = originalVerify
			})

			exit, stdout, stderr := runCommand(t, "update", "--compact")
			details := assertAdditionalUpdateFailure(t, exit, stdout, stderr, test.wantExit, test.wantCode, false, stageVerifySignature)
			if details["failure_scope"] != "local" {
				t.Fatalf("failure_scope=%#v want local in %#v", details["failure_scope"], details)
			}
		})
	}
}

func TestUpdateTrustedRootFailureReportsIntegrityStatus(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	origVerify := updateVerifySignature
	t.Cleanup(func() {
		updateDownloadHook = origDownload
		updateVerifySignature = origVerify
	})
	updateDownloadHook = func(context.Context, string, string) error { return nil }
	updateVerifySignature = func(context.Context, string, string, string) error {
		return errTrustRootUnavailable
	}

	exitCode, stdout, _ := runCommand(t, "update", "--compact")
	if exitCode != 1 {
		t.Fatalf("exit=%d stdout=%s", exitCode, stdout)
	}
	errObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObject["code"] != "E_INTEGRITY" || errObject["retryable"] != false {
		t.Fatalf("trusted root error = %#v", errObject)
	}
	details := assertUpdateFailureDetails(t, stdout, stageVerifySignature, false, "not_run")
	if details["signature_status"] != "trust_root_unavailable" {
		t.Fatalf("signature status = %#v", details)
	}
}

func TestUpdateSignatureBundleDownloadFailureReportsDownloadStage(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	t.Cleanup(func() { updateDownloadHook = origDownload })
	updateDownloadHook = func(_ context.Context, rawURL, _ string) error {
		if strings.HasSuffix(rawURL, "checksums.txt.sigstore.json") {
			return &updateHTTPError{StatusCode: http.StatusServiceUnavailable, err: errors.New("503")}
		}
		return nil
	}

	exitCode, stdout, _ := runCommand(t, "update", "--compact")
	if exitCode != 7 {
		t.Fatalf("exit=%d stdout=%s", exitCode, stdout)
	}
	errObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObject["code"] != "E_SERVER" || errObject["retryable"] != true {
		t.Fatalf("signature bundle download error = %#v", errObject)
	}
	details := assertUpdateFailureDetails(t, stdout, stageDownload, false, "not_run")
	if details["signature_status"] != "download_failed" {
		t.Fatalf("signature status = %#v", details)
	}
}

func TestUpdateSignatureLocalReadFailureReportsIO(t *testing.T) {
	target := additionalUpdateHigherVersion(t)
	srv := updateMockReleaseServer(t, target, true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	t.Cleanup(func() { updateDownloadHook = origDownload })
	updateDownloadHook = func(context.Context, string, string) error {
		// Report a successful transfer without creating the local files. The
		// verifier must classify the resulting local read failure as I/O, not as
		// a forged or invalid release.
		return nil
	}

	exitCode, stdout, stderr := runCommand(t, "update", "--compact")
	if exitCode != 1 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", exitCode, stderr, stdout)
	}
	errObject := decodeEnvelope(t, stdout)["error"].(map[string]any)
	if errObject["code"] != "E_IO" || errObject["retryable"] != false {
		t.Fatalf("local signature read error = %#v", errObject)
	}
	details := assertUpdateFailureDetails(t, stdout, stageVerifySignature, false, "not_run")
	if details["signature_status"] != "failed" {
		t.Fatalf("signature status = %#v", details)
	}
}
