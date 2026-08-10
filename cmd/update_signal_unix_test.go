//go:build unix

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	updateSignalHelperEnv     = "CLIPROXYAPI_CLI_UPDATE_SIGNAL_HELPER"
	updateSignalExecutableEnv = "CLIPROXYAPI_CLI_UPDATE_SIGNAL_EXECUTABLE"
	updateSignalAPIEnv        = "CLIPROXYAPI_CLI_UPDATE_SIGNAL_API"
	updateSignalStageEnv      = "CLIPROXYAPI_CLI_UPDATE_SIGNAL_STAGE"
)

type updateSignalReady struct {
	tempDir string
	err     error
}

func TestUpdateSignalHelperProcess(_ *testing.T) {
	if os.Getenv(updateSignalHelperEnv) != "1" {
		return
	}

	ready := os.NewFile(3, "update-signal-ready")
	if ready == nil {
		os.Exit(2)
	}
	updateGitHubAPI = os.Getenv(updateSignalAPIEnv)
	updateExecutable = func() (string, error) {
		return os.Getenv(updateSignalExecutableEnv), nil
	}
	stage := os.Getenv(updateSignalStageEnv)
	updateTempDir := ""
	signalReady := func() error {
		if _, err := io.WriteString(ready, updateTempDir); err != nil {
			return err
		}
		return ready.Close()
	}
	updateDownloadHook = func(ctx context.Context, _ string, destination string) error {
		if err := os.WriteFile(destination, []byte("downloaded update data"), 0o600); err != nil {
			return err
		}
		updateTempDir = filepath.Dir(destination)
		if stage == stageDownload {
			if err := signalReady(); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	if stage == stageSkillSync {
		updateVerifySignature = func(context.Context, string, string, string) error { return nil }
		updateChecksumHook = func(string, string, string) error { return nil }
		updateExtractHook = func(_, _ string, tempDir string) (string, error) {
			path := filepath.Join(tempDir, "new-binary")
			return path, os.WriteFile(path, []byte("new binary"), 0o700)
		}
		updateSkillSync = func(ctx context.Context, _ string) error {
			if err := signalReady(); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}
	} else if stage != stageDownload {
		os.Exit(2)
	}

	os.Args = []string{os.Args[0], "update", "--compact"}
	Execute()
}

func TestUpdateProcessSignalsProduceInterruptedFailure(t *testing.T) {
	const targetVersion = "9.9.9"
	server := updateMockReleaseServer(t, targetVersion, true)
	defer server.Close()

	tests := []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGINT", signal: os.Interrupt},
		{name: "SIGTERM", signal: syscall.SIGTERM},
	}
	stages := []struct {
		name               string
		stage              string
		wantCurrentVersion string
		wantBinaryReplaced bool
		wantSkillStatus    string
		wantBinary         string
	}{
		{name: "download", stage: stageDownload, wantCurrentVersion: version, wantSkillStatus: "not_run", wantBinary: "original binary"},
		{name: "skill_sync", stage: stageSkillSync, wantCurrentVersion: targetVersion, wantBinaryReplaced: true, wantSkillStatus: "failed", wantBinary: "new binary"},
	}

	for _, stageTest := range stages {
		for _, signalTest := range tests {
			t.Run(stageTest.name+"/"+signalTest.name, func(t *testing.T) {
				dir := t.TempDir()
				executable := filepath.Join(dir, updateBinaryName)
				if err := os.WriteFile(executable, []byte("original binary"), 0o755); err != nil {
					t.Fatal(err)
				}

				readyRead, readyWrite, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				command := exec.Command(os.Args[0], "-test.run=^TestUpdateSignalHelperProcess$")
				command.Env = append(os.Environ(),
					updateSignalHelperEnv+"=1",
					updateSignalExecutableEnv+"="+executable,
					updateSignalAPIEnv+"="+server.URL,
					updateSignalStageEnv+"="+stageTest.stage,
				)
				command.ExtraFiles = []*os.File{readyWrite}
				var stdout, stderr bytes.Buffer
				command.Stdout = &stdout
				command.Stderr = &stderr
				if err := command.Start(); err != nil {
					_ = readyRead.Close()
					_ = readyWrite.Close()
					t.Fatal(err)
				}
				_ = readyWrite.Close()
				t.Cleanup(func() {
					_ = command.Process.Kill()
					_ = readyRead.Close()
				})

				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				readyResult := make(chan updateSignalReady, 1)
				go func() {
					data, err := io.ReadAll(readyRead)
					readyResult <- updateSignalReady{tempDir: string(data), err: err}
				}()

				var waitErr error
				var updateTempDir string
				select {
				case result := <-readyResult:
					if result.err != nil || result.tempDir == "" {
						_ = command.Process.Kill()
						waitErr = <-done
						t.Fatalf("helper readiness failed: %v (wait=%v stderr=%q stdout=%q)", result.err, waitErr, stderr.String(), stdout.String())
					}
					updateTempDir = result.tempDir
				case waitErr = <-done:
					t.Fatalf("helper exited before signal (wait=%v stderr=%q stdout=%q)", waitErr, stderr.String(), stdout.String())
				case <-time.After(10 * time.Second):
					_ = command.Process.Kill()
					waitErr = <-done
					t.Fatalf("helper did not reach the blocking download stage (wait=%v stderr=%q stdout=%q)", waitErr, stderr.String(), stdout.String())
				}

				if err := command.Process.Signal(signalTest.signal); err != nil {
					_ = command.Process.Kill()
					<-done
					t.Fatalf("sending %s: %v", signalTest.name, err)
				}
				select {
				case waitErr = <-done:
				case <-time.After(10 * time.Second):
					_ = command.Process.Kill()
					waitErr = <-done
					t.Fatalf("helper did not exit after %s (wait=%v)", signalTest.name, waitErr)
				}

				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 130 {
					t.Fatalf("exit after %s = %v, want 130 (stderr=%q stdout=%q)", signalTest.name, waitErr, stderr.String(), stdout.String())
				}
				if stderr.Len() != 0 {
					t.Fatalf("stderr after %s = %q", signalTest.name, stderr.String())
				}

				envelope := decodeEnvelope(t, stdout.Bytes())
				if envelope["ok"] != false {
					t.Fatalf("envelope after %s = %#v", signalTest.name, envelope)
				}
				errorObject := envelope["error"].(map[string]any)
				if errorObject["code"] != "E_INTERRUPTED" || errorObject["retryable"] != true {
					t.Fatalf("error after %s = %#v", signalTest.name, errorObject)
				}
				details := assertUpdateFailureDetails(t, stdout.Bytes(), stageTest.stage, stageTest.wantBinaryReplaced, stageTest.wantSkillStatus)
				if details["current_version"] != stageTest.wantCurrentVersion {
					t.Fatalf("current_version after %s = %v, want %s", signalTest.name, details["current_version"], stageTest.wantCurrentVersion)
				}

				if _, err := os.Stat(updateTempDir); !os.IsNotExist(err) {
					t.Fatalf("update temp dir remains after %s at %q: %v", signalTest.name, updateTempDir, err)
				}
				binary, err := os.ReadFile(executable)
				if err != nil {
					t.Fatal(err)
				}
				if string(binary) != stageTest.wantBinary {
					t.Fatalf("binary after %s = %q, want %q", signalTest.name, binary, stageTest.wantBinary)
				}
				for _, suffix := range []string{".new", ".old"} {
					path := filepath.Join(dir, "."+filepath.Base(executable)+suffix)
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("swap file remains after %s at %q: %v", signalTest.name, path, err)
					}
				}
			})
		}
	}
}
