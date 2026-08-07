// Package config resolves runtime configuration and zero-secret saved profiles.
package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/credential"
)

const (
	EnvBaseURL       = "CLIPROXYAPI_CLI_BASE_URL"
	EnvManagementKey = "CLIPROXYAPI_CLI_MANAGEMENT_KEY"
	EnvStateDir      = "CLIPROXYAPI_CLI_STATE_DIR"
	EnvTimeout       = "CLIPROXYAPI_CLI_TIMEOUT_SECONDS"

	DefaultBaseURL = "http://127.0.0.1:8317/v0/management"
	DefaultTimeout = 20 * time.Second
)

type Config struct {
	BaseURL          string
	ManagementKey    string
	CredentialSource string
	StateDir         string
	Timeout          time.Duration
}

type LoadOptions struct {
	BaseURL          string
	StateDir         string
	Timeout          time.Duration
	ReadKeyFromStdin bool
	Stdin            io.Reader
	Getenv           func(string) string
	CredentialStore  credential.Store
}

func Load(options LoadOptions) (Config, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	stateDir, err := resolveStateDir(options, getenv)
	if err != nil {
		return Config{}, err
	}
	profile, profileExists, err := LoadProfile(stateDir)
	if err != nil {
		return Config{}, err
	}
	savedBaseURL := ""
	if profileExists {
		savedBaseURL = profile.BaseURL
	}
	baseURL := firstNonEmpty(options.BaseURL, getenv(EnvBaseURL), savedBaseURL, DefaultBaseURL)
	baseURL, err = normalizeBaseURL(baseURL)
	if err != nil {
		return Config{}, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		if raw := strings.TrimSpace(getenv(EnvTimeout)); raw != "" {
			seconds, err := strconv.Atoi(raw)
			if err != nil || seconds <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive integer", EnvTimeout)
			}
			timeout = time.Duration(seconds) * time.Second
		} else {
			timeout = DefaultTimeout
		}
	}
	managementKey := strings.TrimSpace(getenv(EnvManagementKey))
	credentialSource := ""
	if managementKey != "" {
		credentialSource = "env"
	}
	if options.ReadKeyFromStdin {
		key, err := readSingleLineSecret(options.Stdin)
		if err != nil {
			return Config{}, err
		}
		managementKey = key
		credentialSource = "stdin"
	}
	if managementKey == "" && options.CredentialStore != nil && profileExists && profile.CredentialBackend == CredentialBackendKeyring && baseURL == profile.BaseURL {
		key, err := options.CredentialStore.Get(credential.Account(baseURL))
		if err != nil && !errors.Is(err, credential.ErrNotFound) {
			return Config{}, fmt.Errorf("read management key from OS credential store: %w", err)
		}
		if err == nil {
			managementKey = strings.TrimSpace(key)
			if managementKey != "" {
				credentialSource = CredentialBackendKeyring
			}
		}
	}
	return Config{
		BaseURL:          baseURL,
		ManagementKey:    managementKey,
		CredentialSource: credentialSource,
		StateDir:         filepath.Clean(stateDir),
		Timeout:          timeout,
	}, nil
}

// ResolveStateDir resolves the local profile, guard, and confirmation directory.
func ResolveStateDir(options LoadOptions) (string, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return resolveStateDir(options, getenv)
}

func resolveStateDir(options LoadOptions, getenv func(string) string) (string, error) {
	stateDir := strings.TrimSpace(options.StateDir)
	if stateDir == "" {
		stateDir = strings.TrimSpace(getenv(EnvStateDir))
	}
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		stateDir = filepath.Join(home, ".cliproxyapi-cli")
	}
	return filepath.Clean(stateDir), nil
}

func (c Config) CredentialFingerprint() string {
	if strings.TrimSpace(c.ManagementKey) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.ManagementKey))
	return hex.EncodeToString(sum[:])[:16]
}

func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("base URL must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("base URL must use http or https")
	}
	if parsed.User != nil {
		return "", errors.New("base URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("base URL must not contain a query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func readSingleLineSecret(reader io.Reader) (string, error) {
	if reader == nil {
		return "", errors.New("management key stdin is unavailable")
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, 16*1024))
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read management key from stdin: %w", err)
		}
		return "", errors.New("management key stdin is empty")
	}
	key := strings.TrimSpace(scanner.Text())
	if key == "" {
		return "", errors.New("management key stdin is empty")
	}
	if scanner.Scan() && strings.TrimSpace(scanner.Text()) != "" {
		return "", errors.New("management key stdin must contain exactly one line")
	}
	return key, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
