package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var ErrLockHeld = errors.New("guard lock is already held")

type Lease interface {
	Release() error
}

type Lock interface {
	Acquire(context.Context) (Lease, error)
}

type FileLock struct {
	Path string
	Now  func() time.Time
}

type lockInfo struct {
	Version   int       `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type fileLease struct {
	file     *os.File
	localKey string
	once     sync.Once
	err      error
}

var processLocks = struct {
	sync.Mutex
	held map[string]struct{}
}{held: make(map[string]struct{})}

func NewFileLock(path string) *FileLock {
	return &FileLock{Path: path}
}

func (l *FileLock) Acquire(ctx context.Context) (Lease, error) {
	if l == nil || l.Path == "" {
		return nil, errors.New("guard lock path is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	localKey, err := claimProcessLock(l.Path)
	if err != nil {
		return nil, err
	}
	releaseLocal := true
	defer func() {
		if releaseLocal {
			releaseProcessLock(localKey)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return nil, fmt.Errorf("create guard lock directory: %w", err)
	}
	file, err := os.OpenFile(l.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open guard lock: %w", err)
	}
	if err := tryLockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrLockHeld) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("acquire guard lock: %w", err)
	}
	cleanup := func() {
		_ = unlockFile(file)
		_ = file.Close()
	}
	if err := file.Truncate(0); err != nil {
		cleanup()
		return nil, fmt.Errorf("truncate guard lock: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		cleanup()
		return nil, fmt.Errorf("seek guard lock: %w", err)
	}
	now := time.Now().UTC()
	if l.Now != nil {
		now = l.Now().UTC()
	}
	if err := json.NewEncoder(file).Encode(lockInfo{Version: 1, PID: os.Getpid(), StartedAt: now}); err != nil {
		cleanup()
		return nil, fmt.Errorf("write guard lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return nil, fmt.Errorf("sync guard lock: %w", err)
	}
	releaseLocal = false
	return &fileLease{file: file, localKey: localKey}, nil
}

// Clear removes stale diagnostic metadata while holding the same OS lock used
// by a run. The lock file itself is intentionally persistent: deleting a lock
// pathname creates an ABA window where an old lease can affect a new one.
func (l *FileLock) Clear() error {
	lease, err := l.Acquire(context.Background())
	if err != nil {
		return err
	}
	fileLease := lease.(*fileLease)
	clearErr := fileLease.file.Truncate(0)
	if clearErr == nil {
		clearErr = fileLease.file.Sync()
	}
	releaseErr := lease.Release()
	return errors.Join(clearErr, releaseErr)
}

func (l *fileLease) Release() error {
	l.once.Do(func() {
		unlockErr := unlockFile(l.file)
		closeErr := l.file.Close()
		releaseProcessLock(l.localKey)
		l.err = errors.Join(unlockErr, closeErr)
	})
	return l.err
}

func claimProcessLock(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	key := filepath.Clean(absolute)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	processLocks.Lock()
	defer processLocks.Unlock()
	if _, exists := processLocks.held[key]; exists {
		return "", ErrLockHeld
	}
	processLocks.held[key] = struct{}{}
	return key, nil
}

func releaseProcessLock(key string) {
	processLocks.Lock()
	delete(processLocks.held, key)
	processLocks.Unlock()
}
