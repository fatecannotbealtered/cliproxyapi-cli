package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAtomicJSONAndRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")
	store := NewStore(path)
	records := []Record{
		{Identity: "b", Provider: "codex", AuthIndex: "2", Fingerprint: "fb", DisabledByTool: true},
		{Identity: "a", Provider: "codex", AuthIndex: "1", Fingerprint: "fa", DisabledByTool: true},
	}

	if err := store.save(records); err != nil {
		t.Fatal(err)
	}
	got, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Identity != "a" || got[1].Identity != "b" {
		t.Fatalf("records = %#v, want deterministic identity order", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version int      `json:"version"`
		Records []Record `json:"records"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("state is not valid JSON: %v", err)
	}
	if document.Version != StoreVersion || len(document.Records) != 2 {
		t.Fatalf("document = %#v", document)
	}
	if err := store.save([]Record{{
		Identity:       "c",
		Provider:       "codex",
		AuthIndex:      "3",
		Fingerprint:    "fc",
		DisabledByTool: true,
	}}); err != nil {
		t.Fatalf("replace existing state: %v", err)
	}
	if got, err := store.Records(); err != nil || len(got) != 1 || got[0].Identity != "c" {
		t.Fatalf("records after replacement = %#v, err = %v", got, err)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".state.json.tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %#v, err = %v", matches, err)
	}
}

func TestStoreRejectsMalformedOrUnsupportedState(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "unsupported version", body: `{"version":99,"records":[]}`},
		{name: "duplicate identity", body: `{"version":1,"records":[{"identity":"x"},{"identity":"x"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewStore(path).Records(); err == nil {
				t.Fatal("Records() error = nil, want invalid state error")
			}
		})
	}
}
