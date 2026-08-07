package confirm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueValidateAndConsumeOnce(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	gate := New(dir)
	gate.Now = func() time.Time { return now }
	detail := map[string]any{"name": "codex.json", "disabled": true, "updated_at": "2026-08-05T11:00:00Z"}
	context := map[string]any{"base_url": "http://127.0.0.1:8317/v0/management", "credential_sha256": "abcd"}

	token, expiresAt, err := gate.Issue("auth-file set-status", detail, context)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if !strings.HasPrefix(token, "ct_") || !expiresAt.Equal(now.Add(TTL)) {
		t.Fatalf("token=%q expires=%s", token, expiresAt)
	}
	if err := gate.Validate(token, "auth-file set-status", detail, context); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := gate.Consume(token, "auth-file set-status", detail, context); err != nil {
		t.Fatalf("first Consume() error = %v", err)
	}
	if err := gate.Consume(token, "auth-file set-status", detail, context); err == nil {
		t.Fatal("second Consume() error = nil, want replay rejection")
	}
}

func TestValidateRejectsChangedScopeAndExpiry(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	gate := New(dir)
	gate.Now = func() time.Time { return now }
	token, _, err := gate.Issue("raw request", map[string]any{"method": "PATCH", "path": "/debug"}, map[string]any{"base_url": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Validate(token, "raw request", map[string]any{"method": "DELETE", "path": "/debug"}, map[string]any{"base_url": "a"}); err == nil {
		t.Fatal("changed operation detail was accepted")
	}
	if err := gate.Validate(token, "raw request", map[string]any{"method": "PATCH", "path": "/debug"}, map[string]any{"base_url": "b"}); err == nil {
		t.Fatal("changed context was accepted")
	}
	gate.Now = func() time.Time { return now.Add(TTL + time.Second) }
	if err := gate.Validate(token, "raw request", map[string]any{"method": "PATCH", "path": "/debug"}, map[string]any{"base_url": "a"}); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestIssueFailsClosedWhenSecretCannotBeStored(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := New(blocked)
	if _, _, err := gate.Issue("write", nil, nil); err == nil {
		t.Fatal("Issue() error = nil, want fail-closed storage error")
	}
}
