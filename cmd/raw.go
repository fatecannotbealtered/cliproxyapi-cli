package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/confirm"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/spf13/cobra"
)

const maxRawRequestBodyBytes int64 = 4 << 20

func (a *application) rawCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "raw",
		Short: "Call an explicitly selected Management API endpoint",
	}
	command.AddCommand(a.rawRequestCommand())
	return command
}

func (a *application) rawRequestCommand() *cobra.Command {
	var method string
	var path string
	var bodyText string
	var bodyStdin bool
	var dangerous bool

	command := &cobra.Command{
		Use:   "request",
		Short: "Call one relative Management API path without exposing its response body",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			method = strings.ToUpper(strings.TrimSpace(method))
			path = strings.TrimSpace(path)
			if method == "" {
				return output.NewError("E_VALIDATION", "--method is required", nil)
			}
			if path == "" {
				return output.NewError("E_VALIDATION", "--path is required", nil)
			}
			if bodyStdin && bodyText != "" {
				return output.NewError("E_USAGE", "--body and --body-stdin are mutually exclusive", nil)
			}
			if bodyStdin && a.managementStdin {
				return output.NewError("E_USAGE", "--body-stdin and --management-key-stdin cannot share stdin", nil)
			}
			if !dangerous {
				return output.NewError("E_FORBIDDEN", "raw request requires explicit --dangerous authorization", nil)
			}

			body, err := a.rawRequestBody(bodyText, bodyStdin)
			if err != nil {
				return err
			}
			cfg, err := a.runtimeConfig(true)
			if err != nil {
				return err
			}
			client, err := api.New(cfg.BaseURL, cfg.ManagementKey, newHTTPClient(cfg.Timeout))
			if err != nil {
				return err
			}

			details := rawConfirmDetails(method, path, body, dangerous)
			contextScope := map[string]any{
				"base_url":               cfg.BaseURL,
				"credential_fingerprint": cfg.CredentialFingerprint(),
			}
			gate := confirm.New(filepath.Join(cfg.StateDir, "confirm"))
			if a.dryRun {
				token, expiresAt, issueErr := gate.Issue(cmd.CommandPath(), details, contextScope)
				if issueErr != nil {
					return output.WrapError("E_IO", "failed to create confirmation token", issueErr, nil)
				}
				result := map[string]any{
					"preview":       rawPreviewDetails(method, path, body),
					"confirm_token": token,
					"expires_at":    expiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
				}
				if notices := rawRequestNotices(method, path); len(notices) > 0 {
					result["notices"] = notices
				}
				return a.success(result)
			}
			if strings.TrimSpace(a.confirm) == "" {
				return output.NewError("E_CONFIRMATION_REQUIRED", "run with --dry-run, then pass the returned token with --confirm", nil)
			}
			if consumeErr := gate.Consume(a.confirm, cmd.CommandPath(), details, contextScope); consumeErr != nil {
				return mapConfirmError(consumeErr)
			}
			return a.executeRawRequest(cmd, client, method, path, body)
		},
	}
	command.Flags().StringVar(&method, "method", http.MethodGet, "HTTP method")
	command.Flags().StringVar(&path, "path", "", "Relative Management API path, including an optional query")
	command.Flags().StringVar(&bodyText, "body", "", "Request body text; prefer --body-stdin for sensitive values")
	command.Flags().BoolVar(&bodyStdin, "body-stdin", false, "Read the request body from stdin")
	command.Flags().BoolVar(&dangerous, "dangerous", false, "Acknowledge that an arbitrary Management API request can change or expose sensitive state")
	_ = command.MarkFlagRequired("path")
	_ = command.MarkFlagRequired("dangerous")
	return command
}

func (a *application) rawRequestBody(bodyText string, bodyStdin bool) ([]byte, error) {
	if !bodyStdin {
		if bodyText == "" {
			return nil, nil
		}
		return []byte(bodyText), nil
	}
	raw, err := io.ReadAll(io.LimitReader(a.in, maxRawRequestBodyBytes+1))
	if err != nil {
		return nil, output.WrapError("E_IO", "failed to read request body from stdin", err, nil)
	}
	if int64(len(raw)) > maxRawRequestBodyBytes {
		return nil, output.NewError("E_VALIDATION", "request body exceeds 4 MiB", nil)
	}
	return raw, nil
}

func rawConfirmDetails(method, path string, body []byte, dangerous bool) map[string]any {
	digest := sha256.Sum256(body)
	return map[string]any{
		"method":      method,
		"path":        path,
		"body_bytes":  len(body),
		"body_sha256": hex.EncodeToString(digest[:]),
		"dangerous":   dangerous,
	}
}

func rawPreviewDetails(method, path string, body []byte) map[string]any {
	previewPath := path
	query := ""
	if index := strings.IndexByte(path, '?'); index >= 0 {
		previewPath = path[:index]
		query = path[index+1:]
	}
	details := rawConfirmDetails(method, previewPath, body, true)
	if query != "" {
		digest := sha256.Sum256([]byte(query))
		details["query_present"] = true
		details["query_sha256"] = hex.EncodeToString(digest[:])
	}
	return details
}

func mapConfirmError(err error) error {
	var pathError *os.PathError
	if errors.As(err, &pathError) || errors.Is(err, os.ErrPermission) {
		return output.WrapError("E_IO", "confirmation state is unavailable", err, nil)
	}
	return output.WrapError("E_CONFLICT", "confirmation token is invalid, expired, already used, or does not match current state", err, nil)
}

func (a *application) executeRawRequest(cmd *cobra.Command, client *api.Client, method, path string, body []byte) error {
	response, err := client.Raw(cmd.Context(), method, path, body)
	if err != nil {
		return err
	}
	result := map[string]any{
		"status_code":           response.StatusCode,
		"response_body_omitted": len(response.Body) > 0,
	}
	if notices := rawRequestNotices(method, path); len(notices) > 0 {
		result["notices"] = notices
	}
	return a.success(result)
}

func rawRequestNotices(method, path string) []map[string]string {
	if method == http.MethodGet && rawPathIsUsageQueue(path) {
		return []map[string]string{{
			"severity": "warning",
			"message":  "GET /usage-queue removes the returned records from the queue",
		}}
	}
	return nil
}

func rawPathIsUsageQueue(path string) bool {
	path = strings.TrimSpace(path)
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	return strings.EqualFold(strings.Trim(path, "/"), "usage-queue")
}
