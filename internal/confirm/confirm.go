// Package confirm implements HMAC-bound, expiring, single-use write tokens.
package confirm

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/contract"
)

const TTL = 15 * time.Minute

type Gate struct {
	dir string
	Now func() time.Time

	mu     sync.Mutex
	secret []byte
}

func New(dir string) *Gate {
	return &Gate{dir: dir, Now: time.Now}
}

func (g *Gate) Issue(operation string, details, context map[string]any) (string, time.Time, error) {
	now := g.now().UTC()
	expiresAt := now.Add(TTL)
	digest, err := g.digest(operation, details, context, expiresAt)
	if err != nil {
		return "", time.Time{}, err
	}
	return fmt.Sprintf("ct_%d_%s", expiresAt.Unix(), digest[:32]), expiresAt, nil
}

func (g *Gate) Validate(token, operation string, details, context map[string]any) error {
	expiresAt, suppliedDigest, err := parseToken(token)
	if err != nil {
		return err
	}
	if !g.now().UTC().Before(expiresAt) {
		return errors.New("confirm token expired")
	}
	expected, err := g.digest(operation, details, context, expiresAt)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(suppliedDigest), []byte(expected[:32])) {
		return errors.New("confirm token does not match this operation")
	}
	return nil
}

// Consume claims the token before the caller performs the external write.
func (g *Gate) Consume(token, operation string, details, context map[string]any) error {
	if err := g.Validate(token, operation, details, context); err != nil {
		return err
	}
	expiresAt, _, _ := parseToken(token)
	return g.claim(token, expiresAt)
}

func (g *Gate) now() time.Time {
	if g.Now == nil {
		return time.Now()
	}
	return g.Now()
}

func (g *Gate) digest(operation string, details, context map[string]any, expiresAt time.Time) (string, error) {
	secret, err := g.loadSecret()
	if err != nil {
		return "", err
	}
	if details == nil {
		details = map[string]any{}
	}
	if context == nil {
		context = map[string]any{}
	}
	payload := map[string]any{
		"schema_version": contract.SchemaVersion,
		"operation":      operation,
		"details":        details,
		"context":        context,
		"expires_at":     expiresAt.UTC().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode confirm scope: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (g *Gate) loadSecret() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.secret) >= 32 {
		return g.secret, nil
	}
	if strings.TrimSpace(g.dir) == "" {
		return nil, errors.New("confirm state directory is empty")
	}
	if err := os.MkdirAll(g.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create confirm state directory: %w", err)
	}
	path := filepath.Join(g.dir, "confirm.secret")
	if existing, err := os.ReadFile(path); err == nil {
		if len(existing) < 32 {
			return nil, errors.New("confirm secret is invalid")
		}
		g.secret = append([]byte(nil), existing...)
		return g.secret, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read confirm secret: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate confirm secret: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil || len(existing) < 32 {
			return nil, errors.New("confirm secret race produced an unreadable secret")
		}
		g.secret = append([]byte(nil), existing...)
		return g.secret, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create confirm secret: %w", err)
	}
	if _, err := file.Write(secret); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write confirm secret: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync confirm secret: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close confirm secret: %w", err)
	}
	g.secret = secret
	return g.secret, nil
}

func (g *Gate) claim(token string, expiresAt time.Time) error {
	dir := filepath.Join(g.dir, "confirm-consumed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create consumed-token directory: %w", err)
	}
	g.prune(dir)
	fingerprint := sha256.Sum256([]byte(token))
	path := filepath.Join(dir, hex.EncodeToString(fingerprint[:])[:24])
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return errors.New("confirm token already used; run dry-run again")
	}
	if err != nil {
		return fmt.Errorf("claim confirm token: %w", err)
	}
	if _, err := file.WriteString(strconv.FormatInt(expiresAt.Unix(), 10)); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write consumed-token marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close consumed-token marker: %w", err)
	}
	return nil
}

func (g *Gate) prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := g.now().Unix()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		expires, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err == nil && expires <= now {
			_ = os.Remove(path)
		}
	}
}

func parseToken(token string) (time.Time, string, error) {
	parts := strings.Split(token, "_")
	if len(parts) != 3 || parts[0] != "ct" || len(parts[2]) != 32 {
		return time.Time{}, "", errors.New("invalid confirm token")
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expires <= 0 {
		return time.Time{}, "", errors.New("invalid confirm token")
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return time.Time{}, "", errors.New("invalid confirm token")
	}
	return time.Unix(expires, 0).UTC(), parts[2], nil
}
