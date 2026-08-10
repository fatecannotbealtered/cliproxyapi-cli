package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	done   bool
}

func (r *cancelAfterFirstRead) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, errors.New("reader should not be called after cancellation")
	}
	r.done = true
	r.cancel()
	return copy(buffer, "partial"), nil
}

func TestApplyUpdateBinaryReportsRollbackFailureState(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, updateBinaryName+".exe")
	if err := os.WriteFile(dst, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "."+filepath.Base(dst)+".new")
	if err := os.WriteFile(staged, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "."+filepath.Base(dst)+".old")

	originalRename := updateRename
	t.Cleanup(func() { updateRename = originalRename })
	replaceErr := errors.New("replace denied")
	rollbackErr := errors.New("rollback denied")
	renameCalls := 0
	updateRename = func(oldPath, newPath string) error {
		renameCalls++
		switch renameCalls {
		case 1:
			return os.Rename(oldPath, newPath)
		case 2:
			return replaceErr
		case 3:
			return rollbackErr
		default:
			t.Fatalf("unexpected rename %q -> %q", oldPath, newPath)
			return nil
		}
	}

	result, err := applyWindowsUpdateBinary(t.Context(), staged, dst, backup)
	if err == nil || !strings.Contains(err.Error(), replaceErr.Error()) || !strings.Contains(err.Error(), rollbackErr.Error()) {
		t.Fatalf("rollback failure error = %v", err)
	}
	if result.Status != "rollback_failed" || result.Path != dst || result.OriginalRestored {
		t.Fatalf("rollback failure result = %#v", result)
	}
	if result.InstalledExecutableState != "missing" || result.BackupState != "present" || result.StagedState != "present" {
		t.Fatalf("rollback failure filesystem state = %#v", result)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup not preserved: %v", err)
	}
	if _, err := os.Stat(result.StagedPath); err != nil {
		t.Fatalf("verified staged binary not preserved: %v", err)
	}
}

func TestApplyUpdateBinaryPrecommitFailureCleansStagedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, updateBinaryName)
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceDirectory := filepath.Join(dir, "source-directory")
	if err := os.Mkdir(sourceDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := applyUpdateBinary(t.Context(), sourceDirectory, target); err == nil {
		t.Fatal("applyUpdateBinary() error = nil, want precommit copy failure")
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != "OLD" {
		t.Fatalf("installed binary=%q err=%v, want OLD", installed, err)
	}
	for _, suffix := range []string{".new", ".old"} {
		path := filepath.Join(dir, "."+filepath.Base(target)+suffix)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("precommit failure left %s: %v", path, err)
		}
	}
}

func TestCleanupStaleUpdateFilesPreservesRecoveryBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, updateBinaryName+".exe")
	backup := filepath.Join(dir, "."+filepath.Base(target)+".old")
	if err := os.WriteFile(backup, []byte("RECOVERABLE"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := cleanupStaleUpdateFiles(target)
	if err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("cleanup error=%v, want restore guidance", err)
	}
	content, readErr := os.ReadFile(backup)
	if readErr != nil || string(content) != "RECOVERABLE" {
		t.Fatalf("recovery backup was changed: %q err=%v", content, readErr)
	}
}

func TestApplyUpdateBinaryCanceledBeforeCopyLeavesNoStagingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, updateBinaryName)
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := applyUpdateBinary(ctx, source, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("applyUpdateBinary() error=%v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "."+filepath.Base(target)+".new")); !os.IsNotExist(err) {
		t.Fatalf("cancelled copy left staging file: %v", err)
	}
}

func TestCopyUpdateFileContentsStopsDuringCopyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var destination bytes.Buffer
	err := copyUpdateFileContents(ctx, &destination, &cancelAfterFirstRead{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copyUpdateFileContents() error=%v, want context.Canceled", err)
	}
	if destination.String() != "partial" {
		t.Fatalf("copied content=%q, want only the pre-cancel chunk", destination.String())
	}
}

func TestUpdateReportsRollbackFailureState(t *testing.T) {
	srv := updateMockReleaseServer(t, "9.9.9", true)
	defer srv.Close()
	withUpdateServer(t, srv)

	origDownload := updateDownloadHook
	origVerify := updateVerifySignature
	origChecksum := updateChecksumHook
	origExtract := updateExtractHook
	origApply := updateApply
	origExec := updateExecutable
	t.Cleanup(func() {
		updateDownloadHook = origDownload
		updateVerifySignature = origVerify
		updateChecksumHook = origChecksum
		updateExtractHook = origExtract
		updateApply = origApply
		updateExecutable = origExec
	})

	target := filepath.Join(t.TempDir(), updateBinaryName+".exe")
	if err := os.WriteFile(target, []byte("old binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".old")
	staged := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".new")
	updateDownloadHook = func(context.Context, string, string) error { return nil }
	updateVerifySignature = func(context.Context, string, string, string) error { return nil }
	updateChecksumHook = func(string, string, string) error { return nil }
	updateExtractHook = func(string, string, string) (string, error) { return "new-bin", nil }
	updateExecutable = func() (string, error) { return target, nil }
	updateApply = func(context.Context, string, string) (updateApplyResult, error) {
		return updateApplyResult{
			Status:                   "rollback_failed",
			Path:                     target,
			OriginalRestored:         false,
			InstalledExecutableState: "missing",
			BackupPath:               backup,
			BackupState:              "present",
			StagedPath:               staged,
			StagedState:              "present",
		}, errors.New("replacement failed; rollback failed")
	}

	exitCode, stdout, _ := runCommand(t, "update", "--compact")
	if exitCode != 1 {
		t.Fatalf("exit=%d stdout=%s", exitCode, stdout)
	}
	details := assertUpdateFailureDetails(t, stdout, stageReplace, false, "not_run")
	if details["current_version"] != version || details["replacement_status"] != "rollback_failed" {
		t.Fatalf("version/replacement status = %#v", details)
	}
	if details["original_restored"] != false || details["installed_executable_state"] != "missing" {
		t.Fatalf("rollback terminal state = %#v", details)
	}
	if details["target_path"] != target || details["backup_path"] != backup || details["backup_state"] != "present" ||
		details["staged_path"] != staged || details["staged_state"] != "present" {
		t.Fatalf("rollback paths = %#v", details)
	}
	if nextStep, _ := details["next_step"].(string); !strings.Contains(nextStep, "restore") || !strings.Contains(nextStep, backup) {
		t.Fatalf("rollback recovery next_step = %#v", details["next_step"])
	}
}
