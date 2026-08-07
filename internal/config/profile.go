package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const (
	ProfileVersion           = 1
	CredentialBackendKeyring = "keyring"
	profileFileName          = "config.json"
)

// Profile contains only non-sensitive metadata. Management credentials are
// stored separately in the operating-system keyring.
type Profile struct {
	Version           int    `json:"version"`
	BaseURL           string `json:"base_url"`
	CredentialBackend string `json:"credential_backend"`
}

func ProfilePath(dir string) string {
	return filepath.Join(dir, profileFileName)
}

func LoadProfile(dir string) (Profile, bool, error) {
	if dir == "" {
		return Profile{}, false, errors.New("profile directory is empty")
	}
	raw, err := os.ReadFile(ProfilePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, true, fmt.Errorf("read profile: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, true, fmt.Errorf("decode profile: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Profile{}, true, err
	}
	profile, err = validateProfile(profile)
	if err != nil {
		return Profile{}, true, err
	}
	return profile, true, nil
}

func SaveProfile(dir string, profile Profile) error {
	if dir == "" {
		return errors.New("profile directory is empty")
	}
	profile, err := validateProfile(profile)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profile: %w", err)
	}
	raw = append(raw, '\n')

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+profileFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create profile temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect profile temporary file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write profile temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync profile temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close profile temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, ProfilePath(dir)); err != nil {
		return fmt.Errorf("replace profile: %w", err)
	}
	removeTemp = false
	if err := syncProfileDirectory(dir); err != nil {
		return fmt.Errorf("sync profile directory: %w", err)
	}
	return nil
}

func DeleteProfile(dir string) (bool, error) {
	if dir == "" {
		return false, errors.New("profile directory is empty")
	}
	err := os.Remove(ProfilePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete profile: %w", err)
	}
	if err := syncProfileDirectory(dir); err != nil {
		return true, fmt.Errorf("sync profile directory: %w", err)
	}
	return true, nil
}

func validateProfile(profile Profile) (Profile, error) {
	if profile.Version != ProfileVersion {
		return Profile{}, fmt.Errorf("unsupported profile version %d", profile.Version)
	}
	baseURL, err := normalizeBaseURL(profile.BaseURL)
	if err != nil {
		return Profile{}, fmt.Errorf("invalid profile base URL: %w", err)
	}
	if profile.CredentialBackend != CredentialBackendKeyring {
		return Profile{}, errors.New("unsupported credential backend")
	}
	profile.BaseURL = baseURL
	return profile, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode profile: %w", err)
	}
	return errors.New("decode profile: unexpected trailing JSON")
}

func syncProfileDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
