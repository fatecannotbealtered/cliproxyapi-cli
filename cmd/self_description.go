package cmd

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/config"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/contract"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type releaseReadiness struct {
	Level                      string   `json:"level"`
	FCCRequired                bool     `json:"fcc_required"`
	FCCStatus                  string   `json:"fcc_status"`
	MockUpstreamRequired       bool     `json:"mock_upstream_required"`
	MockUpstreamStatus         string   `json:"mock_upstream_status"`
	LiveSmokeRequiredForStable bool     `json:"live_smoke_required_for_stable"`
	LiveSmokeStatus            string   `json:"live_smoke_status"`
	Reason                     string   `json:"reason"`
	RequiredEvidence           []string `json:"required_evidence"`
}

func currentReleaseReadiness() releaseReadiness {
	return releaseReadiness{
		Level:                      "stable",
		FCCRequired:                true,
		FCCStatus:                  "verified",
		MockUpstreamRequired:       true,
		MockUpstreamStatus:         "verified",
		LiveSmokeRequiredForStable: true,
		LiveSmokeStatus:            "verified",
		Reason:                     "Command-level coverage, mock-upstream contracts, the full 1.0.0 production E2E, and the targeted 1.0.1 quota regression smoke are verified.",
		RequiredEvidence: []string{
			"functional_contract_coverage_100",
			"mock_upstream_contract_tests",
			"recorded_live_smoke_for_stable",
		},
	}
}

func (a *application) contextCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Report resolved runtime context without exposing secrets",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := a.runtimeConfig(false)
			if err != nil {
				return err
			}
			return a.success(map[string]any{
				"tool":    "cliproxyapi-cli",
				"version": version,
				"runtime": map[string]any{"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH},
				"target":  map[string]any{"base_url": cfg.BaseURL},
				"state":   map[string]any{"directory": cfg.StateDir},
				"credentials": map[string]any{
					"configured": cfg.ManagementKey != "",
					"source":     cfg.CredentialSource,
				},
				"notices": []any{},
			})
		},
	}
}

type doctorCheck struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Fix     any    `json:"fix"`
	Message string `json:"message,omitempty"`
}

func (a *application) doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, Management API connectivity, and release readiness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.runtimeConfig(false)
			if err != nil {
				return err
			}
			readiness := currentReleaseReadiness()
			readinessCheck := doctorCheck{Check: "release_readiness", Status: "pass", Fix: nil}
			if readiness.Level != "stable" {
				readinessCheck = doctorCheck{
					Check:  "release_readiness",
					Status: "warn",
					Fix:    "record a qualifying real-Codex E2E run before declaring stable",
				}
			}
			checks := []doctorCheck{
				{Check: "configuration", Status: "pass", Fix: nil},
				{Check: "credentials", Status: "pass", Fix: nil},
				{Check: "management_api", Status: "pass", Fix: nil},
				readinessCheck,
			}
			if !managementPathLooksCanonical(cfg.BaseURL) {
				checks[0] = doctorCheck{
					Check:   "configuration",
					Status:  "warn",
					Fix:     "set the base URL to the full /v0/management path unless the server intentionally uses a compatible prefix",
					Message: "base URL does not end in /v0/management",
				}
			}
			if cfg.ManagementKey == "" {
				checks[1] = doctorCheck{Check: "credentials", Status: "fail", Fix: formatCredentialFix(), Message: "management key is not configured"}
				checks[2] = doctorCheck{Check: "management_api", Status: "fail", Fix: formatCredentialFix(), Message: "connectivity was not checked without a management key"}
			} else {
				checkTimeout := cfg.Timeout
				if checkTimeout > 5*time.Second {
					checkTimeout = 5 * time.Second
				}
				client, clientErr := api.New(cfg.BaseURL, cfg.ManagementKey, newHTTPClient(checkTimeout))
				if clientErr == nil {
					_, clientErr = client.ListAuthFiles(cmd.Context())
				}
				if clientErr != nil {
					cliErr := asCLIError(clientErr)
					checks[2] = doctorCheck{
						Check:   "management_api",
						Status:  "fail",
						Fix:     "verify the base URL, service availability, and management key",
						Message: "Management API check failed with " + cliErr.Code,
					}
				}
			}
			return a.success(map[string]any{
				"checks":            checks,
				"base_url":          cfg.BaseURL,
				"release_readiness": readiness,
			})
		},
	}
}

type commandParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Multiple bool   `json:"multiple"`
}

type commandReference struct {
	Path              string         `json:"path"`
	Type              string         `json:"type"`
	Description       string         `json:"description"`
	PermissionTier    string         `json:"permission_tier"`
	BlastRadius       string         `json:"blast_radius"`
	WriteGate         string         `json:"write_gate"`
	StateVerification string         `json:"state_verification"`
	RetrySemantics    string         `json:"retry_semantics"`
	Params            []commandParam `json:"params"`
	OutputSchema      string         `json:"output_schema"`
	Examples          []string       `json:"examples"`
}

type dataSchema struct {
	Shape           string   `json:"shape"`
	Fields          []string `json:"fields"`
	UntrustedFields []string `json:"untrusted_fields"`
}

type commandMetadata struct {
	Schema            string
	Examples          []string
	Write             bool
	PermissionTier    string
	BlastRadius       string
	WriteGate         string
	StateVerification string
	RetrySemantics    string
}

func commandMetadataCatalog() map[string]commandMetadata {
	return map[string]commandMetadata{
		"cliproxyapi-cli reference":            {Schema: "reference", Examples: []string{"cliproxyapi-cli reference --compact"}, BlastRadius: "reads local command metadata", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli context":              {Schema: "context", Examples: []string{"cliproxyapi-cli context --compact"}, BlastRadius: "reads local environment metadata without exposing credentials", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli doctor":               {Schema: "doctor", Examples: []string{"cliproxyapi-cli doctor --compact"}, BlastRadius: "checks local configuration and Management API connectivity", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli changelog":            {Schema: "changelog", Examples: []string{"cliproxyapi-cli changelog --since 1.0.0 --compact"}, BlastRadius: "reads the embedded changelog", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli login":                {Schema: "login_session", Examples: []string{"cliproxyapi-cli login --base-url https://example.com/v0/management --management-key-stdin --dry-run --compact", "cliproxyapi-cli login --base-url https://example.com/v0/management --management-key-stdin --confirm <confirm_token> --compact"}, Write: true, BlastRadius: "verifies a Management key and saves it in the current user's OS credential store", WriteGate: "dry_run_confirm", StateVerification: "preflight_and_local_commit", RetrySemantics: "new_dry_run_required"},
		"cliproxyapi-cli logout":               {Schema: "logout_session", Examples: []string{"cliproxyapi-cli logout --dry-run --compact", "cliproxyapi-cli logout --confirm <confirm_token> --compact"}, Write: true, BlastRadius: "removes the saved Management key and local login profile", WriteGate: "dry_run_confirm", StateVerification: "local_delete", RetrySemantics: "new_dry_run_required"},
		"cliproxyapi-cli auth-file list":       {Schema: "auth_file_list", Examples: []string{"cliproxyapi-cli auth-file list --provider codex --compact"}, BlastRadius: "reads auth metadata visible to the Management key", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli auth-file set-status": {Schema: "auth_file_status", Examples: []string{"cliproxyapi-cli auth-file set-status --name account.json --disabled=true --dangerous --dry-run --compact", "cliproxyapi-cli auth-file set-status --name account.json --disabled=true --dangerous --confirm <confirm_token> --compact"}, Write: true, PermissionTier: "dangerous", BlastRadius: "disables or enables one CLIProxyAPI auth record", WriteGate: "dangerous_dry_run_confirm", StateVerification: "reread_after_write", RetrySemantics: "new_dry_run_required"},
		"cliproxyapi-cli quota inspect":        {Schema: "quota_inspection", Examples: []string{"cliproxyapi-cli quota inspect --provider codex --compact"}, BlastRadius: "makes allowlisted read-only provider usage probes through CLIProxyAPI and reports used and remaining percentages", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli guard run-once":       {Schema: "guard_result", Examples: []string{"cliproxyapi-cli guard run-once --compact"}, BlastRadius: "reads auth metadata and provider quota without changing account state", WriteGate: "none", StateVerification: "response_only", RetrySemantics: "safe_retry"},
		"cliproxyapi-cli raw request":          {Schema: "raw_response", Examples: []string{"cliproxyapi-cli raw request --method GET --path /debug --dangerous --dry-run --compact", "cliproxyapi-cli raw request --method GET --path /debug --dangerous --confirm <confirm_token> --compact", "cliproxyapi-cli raw request --method PATCH --path /debug --body '{\"value\":true}' --dangerous --dry-run --compact", "cliproxyapi-cli raw request --method PATCH --path /debug --body '{\"value\":true}' --dangerous --confirm <confirm_token> --compact"}, Write: true, PermissionTier: "dangerous", BlastRadius: "may invoke any operation exposed by one relative Management API endpoint; response bodies are omitted", WriteGate: "dangerous_dry_run_confirm", StateVerification: "manual_followup", RetrySemantics: "new_dry_run_required"},
	}
}

func referenceSchemas() map[string]dataSchema {
	return map[string]dataSchema{
		"reference":        {Shape: "object", Fields: []string{"tool", "version", "risk_tier", "release_readiness", "commands", "schemas", "exit_codes", "error_codes"}, UntrustedFields: []string{}},
		"context":          {Shape: "object", Fields: []string{"tool", "version", "runtime", "target", "state", "credentials", "notices"}, UntrustedFields: []string{}},
		"doctor":           {Shape: "object", Fields: []string{"checks", "base_url", "release_readiness"}, UntrustedFields: []string{}},
		"changelog":        {Shape: "object", Fields: []string{"current_version", "since", "entries", "count"}, UntrustedFields: []string{}},
		"login_session":    {Shape: "object", Fields: []string{"configured", "verified", "base_url", "credential_backend", "preview", "confirm_token", "expires_at"}, UntrustedFields: []string{}},
		"logout_session":   {Shape: "object", Fields: []string{"removed", "base_url", "credential_backend", "preview", "confirm_token", "expires_at"}, UntrustedFields: []string{}},
		"auth_file_list":   {Shape: "object", Fields: []string{"items", "count", "offset", "next_offset", "has_more", "_untrusted"}, UntrustedFields: authFileListUntrustedFields()},
		"auth_file_status": {Shape: "object", Fields: []string{"name", "auth_index", "previous_disabled", "disabled", "verified", "updated_at", "preview", "confirm_token", "expires_at", "_untrusted"}, UntrustedFields: authFileStatusUntrustedFields()},
		"quota_inspection": {Shape: "object", Fields: []string{"items", "count", "summary", "offset", "next_offset", "has_more", "_untrusted"}, UntrustedFields: quotaInspectionUntrustedFields()},
		"guard_result":     {Shape: "object", Fields: []string{"summary", "decisions", "partial_failure", "fatal", "fatal_error", "_untrusted"}, UntrustedFields: guardResultUntrustedFields()},
		"raw_response":     {Shape: "object", Fields: []string{"status_code", "response_body_omitted", "preview", "confirm_token", "expires_at", "notices"}, UntrustedFields: []string{}},
	}
}

func (a *application) referenceCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "reference",
		Short: "Print the live machine-readable command contract",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return a.success(buildReference(root))
		},
	}
}

func buildReference(root *cobra.Command) map[string]any {
	catalog := commandMetadataCatalog()
	commands := make([]commandReference, 0, len(catalog))
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		children := command.Commands()
		for _, child := range children {
			if child.Hidden || !child.IsAvailableCommand() {
				continue
			}
			walk(child)
		}
		if !command.Runnable() {
			return
		}
		metadata := catalog[command.CommandPath()]
		commands = append(commands, commandReference{
			Path:              command.CommandPath(),
			Type:              map[bool]string{true: "write", false: "read"}[metadata.Write],
			Description:       command.Short,
			PermissionTier:    commandPermissionTier(metadata),
			BlastRadius:       metadata.BlastRadius,
			WriteGate:         metadata.WriteGate,
			StateVerification: metadata.StateVerification,
			RetrySemantics:    metadata.RetrySemantics,
			Params:            collectCommandParams(command),
			OutputSchema:      metadata.Schema,
			Examples:          metadata.Examples,
		})
	}
	walk(root)
	sort.Slice(commands, func(i, j int) bool { return commands[i].Path < commands[j].Path })
	errorCodes := map[string]any{}
	for code, spec := range canonicalErrorCodes() {
		errorCodes[code] = spec
	}
	return map[string]any{
		"tool":              "cliproxyapi-cli",
		"version":           version,
		"risk_tier":         "T2",
		"release_readiness": currentReleaseReadiness(),
		"commands":          commands,
		"schemas":           referenceSchemas(),
		"exit_codes":        canonicalExitCodes(),
		"error_codes":       errorCodes,
	}
}

func commandPermissionTier(metadata commandMetadata) string {
	if metadata.PermissionTier != "" {
		return metadata.PermissionTier
	}
	return map[bool]string{true: "write", false: "read"}[metadata.Write]
}

func collectCommandParams(command *cobra.Command) []commandParam {
	params := make([]commandParam, 0)
	seen := map[string]bool{}
	collect := func(flag *pflag.Flag) {
		if flag.Name == "help" {
			return
		}
		if seen[flag.Name] {
			return
		}
		seen[flag.Name] = true
		params = append(params, commandParam{
			Name:     flag.Name,
			Type:     flag.Value.Type(),
			Required: len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0,
			Multiple: flag.Value.Type() == "stringSlice",
		})
	}
	command.InheritedFlags().VisitAll(collect)
	command.NonInheritedFlags().VisitAll(collect)
	sort.Slice(params, func(i, j int) bool { return params[i].Name < params[j].Name })
	return params
}

func canonicalExitCodes() map[string]string {
	return map[string]string{
		"0":   "success",
		"1":   "generic / integrity / io / unknown (non-retryable, read the envelope)",
		"2":   "usage / validation (fix args, do not retry)",
		"3":   "not found (do not retry)",
		"4":   "auth / forbidden / config (surface credentials/permission)",
		"5":   "confirmation required (run dry-run for a token)",
		"6":   "conflict / invalid token (re-read state, retry)",
		"7":   "retryable transient (network / rate-limit / server)",
		"8":   "timeout (back off, retry)",
		"9":   "human action required (optional; relay to user, run resume)",
		"130": "interrupted by signal (retryable; nothing half-applied)",
	}
}

func canonicalErrorCodes() map[string]map[string]any {
	codes := make(map[string]map[string]any, len(contract.Codes))
	for code, spec := range contract.Codes {
		codes[code] = map[string]any{"exit": spec.Exit, "retryable": spec.Retryable}
	}
	return codes
}

func managementPathLooksCanonical(baseURL string) bool {
	return strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/v0/management")
}

func formatCredentialFix() string {
	return fmt.Sprintf("run cliproxyapi-cli login, set %s, or use --management-key-stdin", config.EnvManagementKey)
}
