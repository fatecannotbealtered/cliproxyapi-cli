package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

const StoreVersion = 1

type Store struct {
	path          string
	beforeReplace func() error
}

type storeDocument struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

// Records returns the current ownership records without exposing mutable store
// internals. A missing state file is an empty store.
func (s *Store) Records() ([]Record, error) {
	document, err := s.load()
	if err != nil {
		return nil, err
	}
	return cloneRecords(document.Records), nil
}

func (s *Store) load() (storeDocument, error) {
	if s == nil || s.path == "" {
		return storeDocument{}, errors.New("guard state path is empty")
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return storeDocument{Version: StoreVersion, Records: []Record{}}, nil
	}
	if err != nil {
		return storeDocument{}, fmt.Errorf("read guard state: %w", err)
	}
	var document storeDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return storeDocument{}, fmt.Errorf("decode guard state: %w", err)
	}
	if document.Version != StoreVersion {
		return storeDocument{}, fmt.Errorf("unsupported guard state version %d", document.Version)
	}
	if err := validateRecords(document.Records); err != nil {
		return storeDocument{}, err
	}
	sortRecords(document.Records)
	if document.Records == nil {
		document.Records = []Record{}
	}
	return document, nil
}

func (s *Store) save(records []Record) error {
	if s == nil || s.path == "" {
		return errors.New("guard state path is empty")
	}
	records = cloneRecords(records)
	if err := validateRecords(records); err != nil {
		return err
	}
	sortRecords(records)
	if records == nil {
		records = []Record{}
	}
	document := storeDocument{Version: StoreVersion, Records: records}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode guard state: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create guard state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create guard state temporary file: %w", err)
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
		return fmt.Errorf("protect guard state temporary file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write guard state temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync guard state temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close guard state temporary file: %w", err)
	}
	if s.beforeReplace != nil {
		if err := s.beforeReplace(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace guard state: %w", err)
	}
	removeTemp = false
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync guard state directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
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

func validateRecords(records []Record) error {
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Identity == "" {
			return errors.New("guard ownership record has empty identity")
		}
		if _, exists := seen[record.Identity]; exists {
			return fmt.Errorf("duplicate guard ownership identity %q", record.Identity)
		}
		seen[record.Identity] = struct{}{}
	}
	return nil
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		return records[i].Identity < records[j].Identity
	})
}

func cloneRecords(records []Record) []Record {
	cloned := make([]Record, len(records))
	for i, record := range records {
		cloned[i] = record
		cloned[i].DisabledAt = cloneTime(record.DisabledAt)
		cloned[i].ResetAt = cloneTime(record.ResetAt)
		cloned[i].LastProbe = cloneTime(record.LastProbe)
	}
	return cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
