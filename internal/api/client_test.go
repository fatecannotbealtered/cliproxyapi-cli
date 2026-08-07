package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewValidatesConfiguration(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		baseURL string
		key     string
	}{
		{name: "empty URL", key: "secret"},
		{name: "relative URL", baseURL: "/v0/management", key: "secret"},
		{name: "unsupported scheme", baseURL: "ftp://localhost/v0/management", key: "secret"},
		{name: "URL credentials", baseURL: "http://user:pass@localhost/v0/management", key: "secret"},
		{name: "URL query", baseURL: "http://localhost/v0/management?x=1", key: "secret"},
		{name: "empty management key", baseURL: "http://localhost/v0/management"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(tc.baseURL, tc.key, nil); err == nil {
				t.Fatal("New() error = nil, want configuration error")
			}
		})
	}
}

func TestListAuthFilesPreservesOrderAndFallbackFields(t *testing.T) {
	t.Parallel()

	const key = "management-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v0/management/auth-files" {
			t.Errorf("path = %q", r.URL.Path)
		}
		assertManagementHeaders(t, r, key)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"files":[`+
			`{"id":"id-2","auth_index":"idx-2","name":"z.json","provider":"codex","type":"oauth","account":"team","email":"z@example.test","status":"ready","status_message":"ok","disabled":false,"unavailable":false,"runtime_only":true,"updated_at":"2026-08-05T12:00:00Z","extra":{"kept":true}},`+
			`{"name":"a.json","type":"codex","email":"a@example.test"}`+
			`]}`)
	}))
	defer server.Close()

	client, err := New(server.URL+"/v0/management/", key, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	files, err := client.ListAuthFiles(context.Background())
	if err != nil {
		t.Fatalf("ListAuthFiles() error = %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	if files[0].Name != "z.json" || files[1].Name != "a.json" {
		t.Fatalf("order = [%q, %q], want upstream order", files[0].Name, files[1].Name)
	}
	got := files[0]
	if got.ID != "id-2" || got.AuthIndex != "idx-2" || got.Provider != "codex" || got.Type != "oauth" ||
		got.Account != "team" || got.Email != "z@example.test" || got.Status != "ready" ||
		got.StatusMessage != "ok" || got.Disabled || got.Unavailable || !got.RuntimeOnly ||
		got.UpdatedAt != "2026-08-05T12:00:00Z" || !got.DisabledPresent || !got.UnavailablePresent || !got.RuntimeOnlyPresent {
		t.Fatalf("full auth file parsed incorrectly: %#v", got)
	}
	if _, ok := got.Raw["extra"]; !ok {
		t.Fatal("Raw does not preserve unknown upstream field")
	}
	fallback := files[1]
	if fallback.ID != "" || fallback.AuthIndex != "" || fallback.Provider != "" || fallback.Status != "" ||
		fallback.Disabled || fallback.Unavailable || fallback.RuntimeOnly || fallback.UpdatedAt != "" ||
		fallback.DisabledPresent || fallback.UnavailablePresent || fallback.RuntimeOnlyPresent {
		t.Fatalf("fallback entry fabricated missing fields: %#v", fallback)
	}
	if _, ok := fallback.Raw["id"]; ok {
		t.Fatal("fallback Raw unexpectedly contains missing id")
	}
}

func TestListAuthFilesRejectsInvalidFilesContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
	}{
		{name: "missing files", response: `{}`},
		{name: "null files", response: `{"files":null}`},
		{name: "non-array files", response: `{"files":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()

			client, err := New(server.URL+"/v0/management", "secret", nil)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, err = client.ListAuthFiles(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "E_SERVER" || apiErr.StatusCode != http.StatusOK {
				t.Fatalf("error = %T %v, want HTTP 200 E_SERVER protocol error", err, err)
			}
		})
	}
}

func TestSetAuthFileDisabledSendsBothIdentifiersAndDoesNotRetry(t *testing.T) {
	t.Parallel()

	const key = "management-secret"
	var requests atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/v0/management/auth-files/status" {
			t.Errorf("path = %q", r.URL.Path)
		}
		assertManagementHeaders(t, r, key)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["name"] != "account.json" || payload["auth_index"] != "idx-1" || payload["disabled"] != true {
			t.Errorf("payload = %#v", payload)
		}
		return nil, &net.DNSError{Err: "connection refused", Name: "localhost"}
	})
	client, err := New("http://localhost/v0/management", key, &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.SetAuthFileDisabled(context.Background(), "account.json", "idx-1", true); err == nil {
		t.Fatal("SetAuthFileDisabled() error = nil, want network error")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want exactly one write attempt", got)
	}
}

func TestSetAuthFileDisabledSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v0/management/auth-files/status" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Name      string `json:"name"`
			AuthIndex string `json:"auth_index"`
			Disabled  bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Name != "account.json" || payload.AuthIndex != "idx-1" || payload.Disabled {
			t.Errorf("payload = %#v", payload)
		}
		_, _ = io.WriteString(w, `{"status":"ok","disabled":false}`)
	}))
	defer server.Close()

	client, err := New(server.URL+"/v0/management", "secret", nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.SetAuthFileDisabled(context.Background(), "account.json", "idx-1", false); err != nil {
		t.Fatalf("SetAuthFileDisabled() error = %v", err)
	}
}

func TestAPICallPreservesStringAndNestedJSONBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		wantBody string
		wantCode int
	}{
		{
			name:     "JSON encoded string",
			response: `{"status_code":429,"header":{"X-Upstream":["limited"]},"body":"{\"remaining\":0}"}`,
			wantBody: `{"remaining":0}`,
			wantCode: http.StatusTooManyRequests,
		},
		{
			name:     "nested JSON object",
			response: `{"status_code":200,"header":{},"body":{"remaining":0}}`,
			wantBody: `{"remaining":0}`,
			wantCode: http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v0/management/api-call" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				assertManagementHeaders(t, r, "secret")
				var payload APICallRequest
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				if payload.AuthIndex != "idx-1" || payload.Method != http.MethodGet || payload.URL != "https://chatgpt.com/backend-api/wham/usage" {
					t.Errorf("payload = %#v", payload)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()

			client, err := New(server.URL+"/v0/management", "secret", nil)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			got, err := client.APICall(context.Background(), APICallRequest{
				AuthIndex: "idx-1",
				Method:    http.MethodGet,
				URL:       "https://chatgpt.com/backend-api/wham/usage",
				Header:    map[string]string{"Authorization": "Bearer $TOKEN$"},
			})
			if err != nil {
				t.Fatalf("APICall() error = %v", err)
			}
			if got.StatusCode != tc.wantCode || string(got.Body) != tc.wantBody {
				t.Fatalf("APICall() = %#v, want status %d body %q", got, tc.wantCode, tc.wantBody)
			}
			if tc.wantCode == http.StatusTooManyRequests && got.Header.Get("X-Upstream") != "limited" {
				t.Fatalf("header = %#v", got.Header)
			}
		})
	}
}

func TestAPICallValidatesStatusCodeContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  string
		wantCode  int
		wantError bool
	}{
		{name: "minimum", response: `{"status_code":100,"body":"ok"}`, wantCode: 100},
		{name: "maximum", response: `{"status_code":599,"body":"ok"}`, wantCode: 599},
		{name: "missing", response: `{"body":"ok"}`, wantError: true},
		{name: "null", response: `{"status_code":null,"body":"ok"}`, wantError: true},
		{name: "string", response: `{"status_code":"200","body":"ok"}`, wantError: true},
		{name: "fractional", response: `{"status_code":200.5,"body":"ok"}`, wantError: true},
		{name: "below minimum", response: `{"status_code":99,"body":"ok"}`, wantError: true},
		{name: "above maximum", response: `{"status_code":600,"body":"ok"}`, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()

			client, err := New(server.URL+"/v0/management", "secret", nil)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			response, err := client.APICall(context.Background(), APICallRequest{Method: http.MethodGet, URL: "https://example.test"})
			if tc.wantError {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Code != "E_SERVER" || apiErr.StatusCode != http.StatusOK {
					t.Fatalf("error = %T %v, want HTTP 200 E_SERVER protocol error", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("APICall() error = %v", err)
			}
			if response.StatusCode != tc.wantCode || string(response.Body) != "ok" {
				t.Fatalf("APICall() = %#v, want status %d body %q", response, tc.wantCode, "ok")
			}
		})
	}
}

func TestManagementHTTPStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		code   string
	}{
		{status: http.StatusUnauthorized, code: "E_AUTH"},
		{status: http.StatusForbidden, code: "E_FORBIDDEN"},
		{status: http.StatusNotFound, code: "E_NOT_FOUND"},
		{status: http.StatusRequestTimeout, code: "E_TIMEOUT"},
		{status: http.StatusConflict, code: "E_CONFLICT"},
		{status: http.StatusTooManyRequests, code: "E_RATE_LIMITED"},
		{status: http.StatusInternalServerError, code: "E_SERVER"},
		{status: http.StatusServiceUnavailable, code: "E_SERVER"},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"upstream_code","message":"denied"}`)
			}))
			defer server.Close()

			client, err := New(server.URL+"/v0/management", "secret", nil)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, err = client.RawRequest(context.Background(), http.MethodGet, "/auth-files", nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %T %v, want *APIError", err, err)
			}
			if apiErr.StatusCode != tc.status || apiErr.Code != tc.code || apiErr.Message != "denied" {
				t.Fatalf("APIError = %#v", apiErr)
			}
		})
	}
}

func TestErrorsNeverExposeManagementCredential(t *testing.T) {
	t.Parallel()

	const key = "super-secret-management-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"Authorization: Bearer `+key+`"}`)
	}))
	defer server.Close()

	client, err := New(server.URL+"/v0/management", key, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.RawRequest(context.Background(), http.MethodGet, "/auth-files", nil)
	if err == nil {
		t.Fatal("RawRequest() error = nil")
	}
	if got := err.Error(); strings.Contains(got, key) || strings.Contains(strings.ToLower(got), "authorization") {
		t.Fatalf("error leaks credentials: %q", got)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || strings.Contains(apiErr.Message, key) || strings.Contains(strings.ToLower(apiErr.Message), "authorization") {
		t.Fatalf("structured error message leaks credentials: %#v", apiErr)
	}
}

func TestNetworkAndTimeoutErrorsRemainClassifiable(t *testing.T) {
	t.Parallel()

	t.Run("network", func(t *testing.T) {
		t.Parallel()
		want := &net.DNSError{Err: "no such host", Name: "invalid.test", IsTemporary: true}
		client, err := New("http://localhost/v0/management", "secret", &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, want }),
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, err = client.RawRequest(context.Background(), http.MethodGet, "/auth-files", nil)
		var got *net.DNSError
		if !errors.As(err, &got) || got != want {
			t.Fatalf("error = %T %v, want wrapped DNS error", err, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		client, err := New("http://localhost/v0/management", "secret", &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}),
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		_, err = client.RawRequest(ctx, http.MethodGet, "/auth-files", nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %T %v, want context deadline", err, err)
		}
	})
}

func TestRawRequestRejectsPathsOutsideManagementBase(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, err := New(server.URL+"/v0/management", "secret", nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	paths := []string{
		"http://evil.test/steal",
		"https://evil.test/steal",
		"//evil.test/steal",
		"../config",
		"auth-files/../config",
		"%2e%2e/config",
		"auth-files/%2E%2E/config",
		`auth-files\..\config`,
	}
	for _, relativePath := range paths {
		t.Run(relativePath, func(t *testing.T) {
			_, err := client.RawRequest(context.Background(), http.MethodGet, relativePath, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "E_VALIDATION" {
				t.Fatalf("error = %T %v, want E_VALIDATION", err, err)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("malicious paths made %d requests", got)
	}
}

func TestRedirectIsNotFollowedAndCredentialStaysOnOrigin(t *testing.T) {
	t.Parallel()

	const key = "management-secret"
	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		destinationRequests.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect destination received Authorization %q", got)
		}
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertManagementHeaders(t, r, key)
		http.Redirect(w, r, destination.URL+"/steal", http.StatusFound)
	}))
	defer origin.Close()

	callerRedirects := atomic.Int32{}
	supplied := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		callerRedirects.Add(1)
		return nil
	}}
	client, err := New(origin.URL+"/v0/management", key, supplied)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.RawRequest(context.Background(), http.MethodGet, "/auth-files", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusFound {
		t.Fatalf("error = %T %v, want redirect APIError", err, err)
	}
	if destinationRequests.Load() != 0 || callerRedirects.Load() != 0 {
		t.Fatalf("redirect followed: destination=%d caller_check=%d", destinationRequests.Load(), callerRedirects.Load())
	}
	if supplied.CheckRedirect == nil {
		t.Fatal("New mutated supplied http.Client")
	}
}

func TestResponseBodyLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.CopyN(w, zeroReader{}, maxResponseBodyBytes+1)
	}))
	defer server.Close()
	client, err := New(server.URL+"/v0/management", "secret", nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.RawRequest(context.Background(), http.MethodGet, "/auth-files", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "E_SERVER" {
		t.Fatalf("error = %T %v, want E_SERVER", err, err)
	}
}

func TestRawPreservesSuccessfulManagementStatusHeadersAndBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/config" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("X-Management-Result", "created")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	client, err := New(server.URL+"/v0/management", "secret", nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response, err := client.Raw(context.Background(), http.MethodPost, "/config", []byte(`{}`))
	if err != nil {
		t.Fatalf("Raw() error = %v", err)
	}
	if response.StatusCode != http.StatusCreated || response.Header.Get("X-Management-Result") != "created" || string(response.Body) != `{"status":"ok"}` {
		t.Fatalf("Raw() = %#v", response)
	}

	body, err := client.RawRequest(context.Background(), http.MethodPost, "/config", []byte(`{}`))
	if err != nil || string(body) != `{"status":"ok"}` {
		t.Fatalf("RawRequest() body=%q error=%v", body, err)
	}
}

func assertManagementHeaders(t *testing.T, r *http.Request, key string) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+key {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
