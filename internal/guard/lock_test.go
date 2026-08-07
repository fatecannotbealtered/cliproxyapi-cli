package guard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockIsExclusiveAndRecordsStartTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.lock")
	lock := NewFileLock(path)
	lock.Now = func() time.Time { return testNow }

	lease, err := lock.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		StartedAt time.Time `json:"started_at"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	if !info.StartedAt.Equal(testNow) {
		t.Fatalf("started_at = %v, want %v", info.StartedAt, testNow)
	}

	if _, err := NewFileLock(path).Acquire(context.Background()); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second Acquire error = %v, want ErrLockHeld", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	next, err := NewFileLock(path).Acquire(context.Background())
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFileLockClearAllowsExplicitStaleLockCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.lock")
	if err := os.WriteFile(path, []byte(`{"started_at":"2026-08-05T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := NewFileLock(path)
	if err := lock.Clear(); err != nil {
		t.Fatal(err)
	}
	lease, err := lock.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestActiveLeaseCannotBeClearedOrReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.lock")
	lock := NewFileLock(path)
	lease, err := lock.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Clear(); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Clear error = %v, want ErrLockHeld", err)
	}
	if _, err := NewFileLock(path).Acquire(context.Background()); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("replacement Acquire error = %v, want ErrLockHeld", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	replacement, err := NewFileLock(path).Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := replacement.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFileLockIsExclusiveAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.lock")
	lease, err := NewFileLock(path).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	command := exec.Command(os.Args[0], "-test.run=^TestFileLockHelperProcess$")
	command.Env = append(os.Environ(), "CLIPROXYAPI_GUARD_LOCK_HELPER="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper process: %v\n%s", err, output)
	}
}

func TestFileLockHelperProcess(t *testing.T) {
	path := os.Getenv("CLIPROXYAPI_GUARD_LOCK_HELPER")
	if path == "" {
		return
	}
	if _, err := NewFileLock(path).Acquire(context.Background()); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Acquire error = %v, want ErrLockHeld", err)
	}
}
