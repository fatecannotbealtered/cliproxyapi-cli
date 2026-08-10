package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxResponseBodyBytes int64 = 4 << 20

// Client calls one CLIProxyAPI Management API origin.
type Client struct {
	baseURL       *url.URL
	managementKey string
	httpClient    *http.Client
}

// AuthFile is the stable subset of GET /auth-files used by the CLI. Raw keeps
// upstream fields that are not yet modelled, including the distinction between
// an absent fallback field and its Go zero value.
type AuthFile struct {
	ID            string `json:"id"`
	AuthIndex     string `json:"auth_index"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Type          string `json:"type"`
	Account       string `json:"account"`
	Email         string `json:"email"`
	Status        string `json:"status"`
	StatusMessage string `json:"status_message"`
	Disabled      bool   `json:"disabled"`
	Unavailable   bool   `json:"unavailable"`
	RuntimeOnly   bool   `json:"runtime_only"`
	UpdatedAt     string `json:"updated_at"`
	// Presence flags distinguish the auth-dir fallback response from an
	// explicit runtime state. A JSON null is not a known boolean state.
	DisabledPresent    bool                       `json:"-"`
	UnavailablePresent bool                       `json:"-"`
	RuntimeOnlyPresent bool                       `json:"-"`
	Raw                map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON retains fields not represented by AuthFile without deriving
// values for fields omitted by the auth-dir fallback response.
func (f *AuthFile) UnmarshalJSON(data []byte) error {
	type plain AuthFile
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*f = AuthFile(decoded)
	f.Raw = raw
	f.DisabledPresent = nonNullField(raw, "disabled")
	f.UnavailablePresent = nonNullField(raw, "unavailable")
	f.RuntimeOnlyPresent = nonNullField(raw, "runtime_only")
	return nil
}

func nonNullField(raw map[string]json.RawMessage, field string) bool {
	value, present := raw[field]
	return present && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

// APICallRequest is the request body accepted by POST /api-call.
type APICallRequest struct {
	AuthIndex string            `json:"auth_index,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header,omitempty"`
	Data      string            `json:"data,omitempty"`
}

// APICallResponse preserves the upstream status independently of the
// Management API HTTP status. Body contains decoded string bytes or raw nested
// JSON bytes, depending on the upstream response shape.
type APICallResponse struct {
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
	Body       []byte      `json:"body"`
}

// RawResponse preserves the successful Management API HTTP response metadata.
type RawResponse struct {
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
	Body       []byte      `json:"body"`
}

// APIError classifies a non-successful Management API response.
type APIError struct {
	StatusCode int    `json:"status_code"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

// Error deliberately excludes the upstream message so a reflected credential
// or Authorization header can never enter logs through error formatting.
func (e *APIError) Error() string {
	if e == nil {
		return "management API request failed"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: management API request failed (HTTP %d)", e.Code, e.StatusCode)
	}
	return fmt.Sprintf("%s: management API request failed", e.Code)
}

// New creates a client for a full Management API base URL, normally ending in
// /v0/management. Redirects are disabled on a copy of the supplied client so
// the bearer credential cannot cross origins.
func New(baseURL, managementKey string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return nil, configError("management base URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, configError("management base URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, configError("management base URL must not contain credentials, query, or fragment")
	}
	if strings.TrimSpace(managementKey) == "" {
		return nil, configError("management key is required")
	}

	var copied http.Client
	if httpClient == nil {
		copied = *http.DefaultClient
	} else {
		copied = *httpClient
	}
	copied.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	baseCopy := *parsed
	baseCopy.Path = strings.TrimRight(baseCopy.Path, "/")
	baseCopy.RawPath = ""
	return &Client{
		baseURL:       &baseCopy,
		managementKey: managementKey,
		httpClient:    &copied,
	}, nil
}

// ListAuthFiles returns auth files in exactly the order supplied by the
// Management API.
func (c *Client) ListAuthFiles(ctx context.Context) ([]AuthFile, error) {
	body, err := c.RawRequest(ctx, http.MethodGet, "/auth-files", nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Files json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(body, &wire); err != nil || len(wire.Files) == 0 || bytes.Equal(bytes.TrimSpace(wire.Files), []byte("null")) {
		return nil, protocolError(http.StatusOK, "invalid auth-files response")
	}
	var files []AuthFile
	if err := json.Unmarshal(wire.Files, &files); err != nil {
		return nil, protocolError(http.StatusOK, "invalid auth-files response")
	}
	return files, nil
}

// SetAuthFileDisabled changes one auth record. It performs one PATCH attempt;
// retry policy belongs to the caller and must account for ambiguous writes.
func (c *Client) SetAuthFileDisabled(ctx context.Context, name, authIndex string, disabled bool) error {
	payload, err := json.Marshal(struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}{
		Name:      name,
		AuthIndex: authIndex,
		Disabled:  disabled,
	})
	if err != nil {
		return protocolError(0, "failed to encode auth status request")
	}
	_, err = c.RawRequest(ctx, http.MethodPatch, "/auth-files/status", payload)
	return err
}

// APICall asks CLIProxyAPI to make an authenticated upstream request. A
// non-2xx StatusCode belongs to that upstream response and is returned normally
// when the Management API itself answered successfully.
func (c *Client) APICall(ctx context.Context, request APICallRequest) (APICallResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return APICallResponse{}, protocolError(0, "failed to encode api-call request")
	}
	body, err := c.RawRequest(ctx, http.MethodPost, "/api-call", payload)
	if err != nil {
		return APICallResponse{}, err
	}

	var wire struct {
		StatusCode json.RawMessage `json:"status_code"`
		Header     http.Header     `json:"header"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return APICallResponse{}, protocolError(http.StatusOK, "invalid api-call response")
	}
	var statusCode int
	if len(wire.StatusCode) == 0 || bytes.Equal(bytes.TrimSpace(wire.StatusCode), []byte("null")) ||
		json.Unmarshal(wire.StatusCode, &statusCode) != nil || statusCode < 100 || statusCode > 599 {
		return APICallResponse{}, protocolError(http.StatusOK, "invalid api-call response")
	}

	response := APICallResponse{
		StatusCode: statusCode,
		Header:     canonicalHeader(wire.Header),
	}
	if len(wire.Body) == 0 {
		return response, nil
	}
	if wire.Body[0] == '"' {
		var text string
		if err := json.Unmarshal(wire.Body, &text); err != nil {
			return APICallResponse{}, protocolError(http.StatusOK, "invalid api-call body")
		}
		response.Body = []byte(text)
		return response, nil
	}
	response.Body = append([]byte(nil), wire.Body...)
	return response, nil
}

// ValidateRelativePath reports whether a raw request path resolves below the
// configured Management API base, using the same rules the execute path applies.
// Callers use it to reject a bad path while previewing, so a dry-run never hands
// back a confirm token for a request that cannot run.
func (c *Client) ValidateRelativePath(relativePath string) error {
	_, err := c.managementURL(relativePath)
	return err
}

// RawRequest calls a path below the configured Management API base. It rejects
// URLs and traversal before constructing the request target.
func (c *Client) RawRequest(ctx context.Context, method, relativePath string, body []byte) ([]byte, error) {
	response, err := c.Raw(ctx, method, relativePath, body)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// Raw calls a path below the configured Management API base and preserves the
// successful Management HTTP status, headers, and body.
func (c *Client) Raw(ctx context.Context, method, relativePath string, body []byte) (RawResponse, error) {
	target, err := c.managementURL(relativePath)
	if err != nil {
		return RawResponse{}, err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return RawResponse{}, validationError("management request method is required")
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return RawResponse{}, validationError("invalid management request")
	}
	request.Header.Set("Authorization", "Bearer "+c.managementKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return RawResponse{}, &requestError{cause: err}
	}
	defer func() { _ = response.Body.Close() }()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	if err != nil {
		return RawResponse{}, &requestError{cause: err}
	}
	if int64(len(responseBody)) > maxResponseBodyBytes {
		return RawResponse{}, protocolError(response.StatusCode, "management API response exceeds size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return RawResponse{}, c.httpError(response.StatusCode, responseBody)
	}
	return RawResponse{
		StatusCode: response.StatusCode,
		Header:     canonicalHeader(response.Header),
		Body:       responseBody,
	}, nil
}

func (c *Client) managementURL(relativePath string) (*url.URL, error) {
	if relativePath == "" || strings.Contains(relativePath, `\`) {
		return nil, validationError("invalid management relative path")
	}
	parsed, err := url.Parse(relativePath)
	if err != nil || parsed.IsAbs() || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, validationError("invalid management relative path")
	}
	if parsed.Path == "" || strings.HasPrefix(relativePath, "//") {
		return nil, validationError("invalid management relative path")
	}
	if unsafePath(parsed.EscapedPath()) {
		return nil, validationError("invalid management relative path")
	}

	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(parsed.Path, "/")
	target.RawPath = ""
	target.RawQuery = parsed.RawQuery
	return &target, nil
}

func unsafePath(value string) bool {
	for {
		decoded, err := url.PathUnescape(value)
		if err != nil || strings.Contains(decoded, `\`) {
			return true
		}
		for _, segment := range strings.Split(decoded, "/") {
			if segment == ".." {
				return true
			}
		}
		if decoded == value {
			return false
		}
		value = decoded
	}
}

func (c *Client) httpError(statusCode int, body []byte) error {
	message := responseMessage(body)
	message = c.redact(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &APIError{
		StatusCode: statusCode,
		Code:       statusErrorCode(statusCode),
		Message:    message,
	}
}

func responseMessage(body []byte) string {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) == nil {
		for _, field := range []string{"message", "error"} {
			if raw := payload[field]; len(raw) > 0 {
				var message string
				if json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message) != "" {
					return message
				}
			}
		}
	}
	return ""
}

func (c *Client) redact(message string) string {
	message = strings.TrimSpace(message)
	if c.managementKey != "" {
		message = strings.ReplaceAll(message, c.managementKey, "[redacted]")
	}
	if strings.Contains(strings.ToLower(message), "authorization") {
		return "management API request rejected"
	}
	return message
}

func statusErrorCode(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized:
		return "E_AUTH"
	case http.StatusForbidden:
		return "E_FORBIDDEN"
	case http.StatusNotFound:
		return "E_NOT_FOUND"
	case http.StatusRequestTimeout:
		return "E_TIMEOUT"
	case http.StatusConflict:
		return "E_CONFLICT"
	case http.StatusTooManyRequests:
		return "E_RATE_LIMITED"
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
		return "E_VALIDATION"
	default:
		if statusCode >= http.StatusInternalServerError || statusCode >= http.StatusMultipleChoices && statusCode < http.StatusBadRequest {
			return "E_SERVER"
		}
		return "E_UNKNOWN"
	}
}

func canonicalHeader(header http.Header) http.Header {
	if header == nil {
		return nil
	}
	canonical := make(http.Header, len(header))
	for key, values := range header {
		canonical[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	return canonical
}

func validationError(message string) error {
	return &APIError{Code: "E_VALIDATION", Message: message}
}

func configError(message string) error {
	return &APIError{Code: "E_CONFIG", Message: message}
}

func protocolError(statusCode int, message string) error {
	return &APIError{StatusCode: statusCode, Code: "E_SERVER", Message: message}
}

// requestError hides transport error text while retaining its cause for
// errors.Is/errors.As classification (context deadlines, net.Error, and so on).
type requestError struct {
	cause error
}

func (e *requestError) Error() string {
	if e != nil && errors.Is(e.cause, context.DeadlineExceeded) {
		return "management API request timed out"
	}
	return "management API network request failed"
}

func (e *requestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
