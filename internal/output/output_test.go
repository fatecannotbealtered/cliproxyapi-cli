package output

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestPrinterSuccessEnvelope(t *testing.T) {
	var out bytes.Buffer
	printer := NewPrinter(&out, Options{Format: FormatJSON, Compact: true, StartedAt: time.Now()})
	if err := printer.Success(map[string]any{"value": "ok"}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if got["ok"] != true || got["schema_version"] != "1.0" {
		t.Fatalf("envelope = %#v", got)
	}
	if _, ok := got["meta"].(map[string]any)["duration_ms"]; !ok {
		t.Fatalf("meta.duration_ms missing: %#v", got)
	}
	if bytes.Contains(out.Bytes(), []byte("\n  ")) {
		t.Fatalf("compact output is indented: %q", out.String())
	}
}

func TestPrinterErrorEnvelopeUsesCanonicalMapping(t *testing.T) {
	var out bytes.Buffer
	printer := NewPrinter(&out, Options{Format: FormatJSON, Compact: true})
	cliErr := NewError("E_RATE_LIMITED", "try later", map[string]any{"status_code": 429})
	if err := printer.Failure(cliErr); err != nil {
		t.Fatalf("Failure() error = %v", err)
	}

	var got struct {
		OK    bool `json:"ok"`
		Error struct {
			Code      string         `json:"code"`
			Details   map[string]any `json:"details"`
			Retryable bool           `json:"retryable"`
		} `json:"error"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if got.OK || got.Error.Code != "E_RATE_LIMITED" || !got.Error.Retryable {
		t.Fatalf("error envelope = %#v", got)
	}
	if got.Error.Details == nil || got.Meta == nil {
		t.Fatalf("required objects missing: %#v", got)
	}
	if cliErr.ExitCode() != 7 {
		t.Fatalf("ExitCode() = %d, want 7", cliErr.ExitCode())
	}
}

func TestPrinterProjectsTopLevelFields(t *testing.T) {
	var out bytes.Buffer
	printer := NewPrinter(&out, Options{Format: FormatJSON, Compact: true, Fields: []string{"count"}})
	if err := printer.Success(map[string]any{"count": 2, "items": []string{"a", "b"}}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var got struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data["count"] != float64(2) {
		t.Fatalf("projected data = %#v", got.Data)
	}
	if _, exists := got.Data["items"]; exists {
		t.Fatalf("items should be omitted: %#v", got.Data)
	}
}

func TestPrinterProjectionPreservesRelevantUntrustedPaths(t *testing.T) {
	var out bytes.Buffer
	printer := NewPrinter(&out, Options{Format: FormatJSON, Compact: true, Fields: []string{"items", "summary"}})
	if err := printer.Success(map[string]any{
		"items":       []map[string]any{{"name": "external"}},
		"summary":     map[string]any{"total": 1},
		"next_offset": 1,
		"_untrusted":  []string{"items.name", "next_offset"},
	}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var got struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	fields, ok := got.Data["_untrusted"].([]any)
	if !ok || len(fields) != 1 || fields[0] != "items.name" {
		t.Fatalf("projected _untrusted = %#v, want [items.name]", got.Data["_untrusted"])
	}
	if _, exists := got.Data["next_offset"]; exists {
		t.Fatalf("next_offset should be omitted: %#v", got.Data)
	}
}

func TestPrinterProjectionOmitsUntrustedMarkerForTrustedFields(t *testing.T) {
	var out bytes.Buffer
	printer := NewPrinter(&out, Options{Format: FormatJSON, Compact: true, Fields: []string{"count"}})
	if err := printer.Success(map[string]any{
		"count":      1,
		"items":      []map[string]any{{"name": "external"}},
		"_untrusted": []string{"items.name"},
	}); err != nil {
		t.Fatalf("Success() error = %v", err)
	}
	var got struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, exists := got.Data["_untrusted"]; exists {
		t.Fatalf("_untrusted should be omitted for trusted-only projection: %#v", got.Data)
	}
}

func TestPrinterRejectsRawSuccess(t *testing.T) {
	printer := NewPrinter(&bytes.Buffer{}, Options{Format: FormatRaw})
	if err := printer.Success(map[string]any{"value": "not raw"}); err == nil {
		t.Fatal("Success() error = nil, want raw-mode error")
	}
}
