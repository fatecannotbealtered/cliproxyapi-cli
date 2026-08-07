package quota

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAssessCodexHealthy(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	body := []byte(`{
		"rate_limit": {
			"allowed": true,
			"limit_reached": false,
			"primary_window": {
				"used_percent": 42.5,
				"limit_window_seconds": 18000,
				"reset_after_seconds": 60
			}
		}
	}`)

	got := AssessCodex(200, body, now)

	if got.State != StateHealthy {
		t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateHealthy, got.Reason)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("len(Windows) = %d, want 1", len(got.Windows))
	}
	w := got.Windows[0]
	if w.Name != "primary" || w.UsedPercent == nil || *w.UsedPercent != 42.5 {
		t.Fatalf("unexpected primary window: %#v", w)
	}
	wantReset := now.Add(time.Minute)
	if w.ResetAt == nil || !w.ResetAt.Equal(wantReset) {
		t.Fatalf("ResetAt = %v, want %v", w.ResetAt, wantReset)
	}
	if got.ResetAt != nil {
		t.Fatalf("assessment ResetAt = %v, want nil for a healthy assessment", got.ResetAt)
	}
}

func TestAssessCodexExplicitExhaustionSignals(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		status    int
		body      string
		wantReset time.Time
	}{
		{
			name:      "allowed false",
			status:    200,
			body:      `{"rate_limit":{"allowed":false,"primary_window":{"used_percent":10,"reset_after_seconds":120},"secondary_window":{"used_percent":20,"reset_after_seconds":60}}}`,
			wantReset: now.Add(time.Minute),
		},
		{
			name:      "limit reached",
			status:    200,
			body:      `{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":10,"reset_after_seconds":90}}}`,
			wantReset: now.Add(90 * time.Second),
		},
		{
			name:   "allowed false with nullable window",
			status: 200,
			body:   `{"rate_limit":{"allowed":false,"primary_window":null}}`,
		},
		{
			name:      "primary at one hundred",
			status:    200,
			body:      `{"rate_limit":{"primary_window":{"used_percent":100,"reset_after_seconds":120},"secondary_window":{"used_percent":20,"reset_after_seconds":30}}}`,
			wantReset: now.Add(2 * time.Minute),
		},
		{
			name:      "earliest exhausted window reset",
			status:    200,
			body:      `{"rate_limit":{"primary_window":{"used_percent":101,"reset_after_seconds":120},"secondary_window":{"used_percent":100,"reset_after_seconds":30}}}`,
			wantReset: now.Add(30 * time.Second),
		},
		{
			name:   "structured error code despite 429",
			status: 429,
			body:   `{"error":{"code":"usage_limit_reached","message":"upstream text"}}`,
		},
		{
			name:   "structured error type",
			status: 403,
			body:   `{"error":{"type":"quota_exhausted"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessCodex(tt.status, []byte(tt.body), now)
			if got.State != StateConfirmedExhausted {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateConfirmedExhausted, got.Reason)
			}
			if tt.wantReset.IsZero() {
				if got.ResetAt != nil {
					t.Fatalf("ResetAt = %v, want nil", got.ResetAt)
				}
			} else if got.ResetAt == nil || !got.ResetAt.Equal(tt.wantReset) {
				t.Fatalf("ResetAt = %v, want %v", got.ResetAt, tt.wantReset)
			}
		})
	}
}

func TestAssessCodexRateLimitReachedTypes(t *testing.T) {
	for _, reachedType := range []string{
		"rate_limit_reached",
		"workspace_owner_credits_depleted",
		"workspace_member_credits_depleted",
		"workspace_owner_usage_limit_reached",
		"workspace_member_usage_limit_reached",
	} {
		t.Run(reachedType, func(t *testing.T) {
			body := []byte(`{"rate_limit_reached_type":{"type":"` + reachedType + `"}}`)
			got := AssessCodex(200, body, time.Time{})

			if got.State != StateConfirmedExhausted {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateConfirmedExhausted, got.Reason)
			}
			evidence, ok := got.Evidence["rate_limit_reached_type"].(map[string]any)
			if !ok || evidence["type"] != reachedType {
				t.Fatalf("rate_limit_reached_type evidence = %#v, want type %q", got.Evidence["rate_limit_reached_type"], reachedType)
			}
		})
	}
}

func TestAssessCodexSpendControlExhaustionAndReset(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		limit     string
		wantReset time.Time
	}{
		{
			name:      "absolute reset",
			limit:     `{"reset_at":` + jsonNumberUnix(now.Add(5*time.Minute)) + `}`,
			wantReset: now.Add(5 * time.Minute),
		},
		{
			name:      "relative reset",
			limit:     `{"reset_after_seconds":90}`,
			wantReset: now.Add(90 * time.Second),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"spend_control":{"reached":true,"individual_limit":` + tt.limit + `}}`)
			got := AssessCodex(200, body, now)

			if got.State != StateConfirmedExhausted {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateConfirmedExhausted, got.Reason)
			}
			if got.ResetAt == nil || !got.ResetAt.Equal(tt.wantReset) {
				t.Fatalf("ResetAt = %v, want %v", got.ResetAt, tt.wantReset)
			}
			if _, ok := got.Evidence["spend_control"]; !ok {
				t.Fatalf("Evidence = %#v, want spend_control", got.Evidence)
			}
		})
	}
}

func TestAssessCodexCreditsWithoutCreditsIsNotExhaustion(t *testing.T) {
	got := AssessCodex(200, []byte(`{"credits":{"has_credits":false}}`), time.Time{})

	if got.State != StateUnknown {
		t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateUnknown, got.Reason)
	}
	if _, ok := got.Evidence["credits"]; !ok {
		t.Fatalf("Evidence = %#v, want credits", got.Evidence)
	}
}

func TestAssessCodexAdditionalRateLimitExhaustionIsUnknown(t *testing.T) {
	tests := []struct {
		name string
		main string
	}{
		{
			name: "does not override healthy account limit",
			main: `"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},`,
		},
		{
			name: "does not authorize account exhaustion",
			main: `"rate_limit":{"allowed":false},`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{` + tt.main + `"additional_rate_limits":[{` +
				`"limit_name":"gpt-example","metered_feature":"model",` +
				`"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}` +
				`}]}`)
			got := AssessCodex(200, body, time.Time{})

			if got.State != StateUnknown {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateUnknown, got.Reason)
			}
			if !strings.Contains(got.Reason, "additional_rate_limits") {
				t.Fatalf("Reason = %q, want additional_rate_limits context", got.Reason)
			}
			evidence, ok := got.Evidence["additional_rate_limits"].([]map[string]any)
			if !ok || len(evidence) != 1 || evidence[0]["limit_name"] != "gpt-example" {
				t.Fatalf("additional_rate_limits evidence = %#v, want exhausted gpt-example limit", got.Evidence["additional_rate_limits"])
			}
			limit, ok := evidence[0]["rate_limit"].(map[string]any)
			if !ok || limit["allowed"] != false || limit["limit_reached"] != true {
				t.Fatalf("additional rate_limit evidence = %#v, want explicit exhaustion fields", evidence[0]["rate_limit"])
			}
		})
	}
}

func TestAssessCodexMalformedKnownFieldsOverrideExhaustion(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "malformed main window",
			body: `{"rate_limit":{"allowed":false,"primary_window":{"used_percent":"100"}}}`,
		},
		{
			name: "malformed unused reset field",
			body: `{"rate_limit":{"allowed":false,"primary_window":{"reset_at":1785931500,"reset_after_seconds":"90"}}}`,
		},
		{
			name: "malformed reached type",
			body: `{"rate_limit":{"allowed":false},"rate_limit_reached_type":{"type":123}}`,
		},
		{
			name: "malformed spend control",
			body: `{"rate_limit_reached_type":{"type":"rate_limit_reached"},"spend_control":{"reached":"true"}}`,
		},
		{
			name: "malformed additional limit",
			body: `{"rate_limit":{"allowed":false},"additional_rate_limits":{}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessCodex(200, []byte(tt.body), time.Time{})
			if got.State != StateUnknown {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateUnknown, got.Reason)
			}
			if !strings.Contains(got.Reason, "malformed") {
				t.Fatalf("Reason = %q, want malformed context", got.Reason)
			}
		})
	}
}

func TestAssessCodexNearLimitIsHealthy(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":99.9}}}`)
	got := AssessCodex(200, body, time.Time{})
	if got.State != StateHealthy {
		t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateHealthy, got.Reason)
	}
}

func TestAssessCodexWindowMetadataDoesNotProveHealthy(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "window size only", body: `{"rate_limit":{"primary_window":{"limit_window_seconds":18000}}}`},
		{name: "reset only", body: `{"rate_limit":{"primary_window":{"reset_after_seconds":60}}}`},
		{name: "window size and reset", body: `{"rate_limit":{"primary_window":{"limit_window_seconds":18000,"reset_after_seconds":60}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessCodex(200, []byte(tt.body), time.Time{})
			if got.State != StateUnknown {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateUnknown, got.Reason)
			}
		})
	}
}

func TestAssessCodexUnknownInputs(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "ordinary 429", status: 429, body: `{"error":{"message":"rate limit reached"}}`},
		{name: "plain text phrase", status: 429, body: `quota exhausted`},
		{name: "unknown 2xx schema", status: 200, body: `{"plan_type":"plus"}`},
		{name: "missing fields", status: 200, body: `{"rate_limit":{"primary_window":{}}}`},
		{name: "malformed json", status: 200, body: `{"rate_limit":`},
		{name: "empty body", status: 204, body: ``},
		{name: "near match error code", status: 429, body: `{"error":{"code":"USAGE_LIMIT_REACHED"}}`},
		{name: "unstructured error string", status: 429, body: `{"error":"usage_limit_reached"}`},
		{name: "malformed known field", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":"100"}}}`},
		{name: "conflicting aliases", status: 200, body: `{"rate_limit":{"primary_window":{"used_percent":100,"usedPercent":10}}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessCodex(tt.status, []byte(tt.body), time.Time{})
			if got.State != StateUnknown {
				t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateUnknown, got.Reason)
			}
		})
	}
}

func TestAssessCodexCamelCaseAndResetFormats(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	rfcReset := now.Add(5 * time.Minute)
	body := []byte(`{
		"rateLimit": {
			"limitReached": false,
			"primaryWindow": {
				"usedPercent": 100,
				"limitWindowSeconds": 18000,
				"resetAt": "` + rfcReset.Format(time.RFC3339) + `"
			},
			"secondaryWindow": {
				"usedPercent": 100,
				"resetAt": ` + jsonNumberUnix(now.Add(2*time.Minute)) + `
			}
		}
	}`)

	got := AssessCodex(200, body, now)

	if got.State != StateConfirmedExhausted {
		t.Fatalf("State = %q, want %q (reason: %s)", got.State, StateConfirmedExhausted, got.Reason)
	}
	wantReset := now.Add(2 * time.Minute)
	if got.ResetAt == nil || !got.ResetAt.Equal(wantReset) {
		t.Fatalf("ResetAt = %v, want %v", got.ResetAt, wantReset)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("len(Windows) = %d, want 2", len(got.Windows))
	}
}

func TestAssessCodexEvidenceDoesNotLeakSensitiveFields(t *testing.T) {
	body := []byte(`{
		"authorization":"Bearer top-secret",
		"cookie":"session=top-secret",
		"token":"top-secret",
		"rate_limit": {
			"allowed": false,
			"primary_window": {
				"used_percent": 100,
				"access_token":"top-secret"
			}
		},
		"error":{"code":"usage_limit_reached","message":"top-secret"}
	}`)

	got := AssessCodex(200, body, time.Time{})
	encoded, err := json.Marshal(got.Evidence)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"top-secret", "authorization", "cookie", "token", "message"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Evidence contains forbidden value or field %q: %s", forbidden, encoded)
		}
	}
	if len(got.Untrusted) == 0 {
		t.Fatal("Untrusted is empty; upstream textual error evidence must be marked")
	}
	found := false
	for _, path := range got.Untrusted {
		if path == "evidence.error.code" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Untrusted = %#v, want evidence.error.code", got.Untrusted)
	}
}

func jsonNumberUnix(value time.Time) string {
	b, _ := json.Marshal(value.Unix())
	return string(b)
}
