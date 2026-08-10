package cmd

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/guard"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/quota"
	"github.com/spf13/cobra"
)

func (a *application) guardCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "guard",
		Short: "Evaluate Codex quota guard decisions without changing account state",
	}
	command.AddCommand(a.guardRunOnceCommand())
	return command
}

func (a *application) guardRunOnceCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "run-once",
		Short: "Run one observation-only quota guard pass",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if a.dryRun || strings.TrimSpace(a.confirm) != "" {
				return output.NewError("E_USAGE", "guard run-once is observation-only and does not accept confirmation flags", nil)
			}
			cfg, err := a.runtimeConfig(true)
			if err != nil {
				return err
			}
			client, err := api.New(cfg.BaseURL, cfg.ManagementKey, newHTTPClient(cfg.Timeout))
			if err != nil {
				return err
			}
			backend := &managementGuardBackend{client: client}
			runner := guard.NewRunner(backend)
			result := runner.RunOnce(command.Context())
			data := guardResultData(result)
			if result.IsFatal() {
				if result.FatalError == guard.FatalListFailed {
					if backend.lastError != nil {
						return output.WrapError(asCLIError(backend.lastError).Code, "guard could not list auth records", backend.lastError, data)
					}
				}
				return output.NewError("E_UNKNOWN", "guard run could not start or finish safely", data)
			}
			if result.PartialFailure {
				if backend.lastError != nil {
					return output.WrapError(asCLIError(backend.lastError).Code, "guard run completed with failed decisions", backend.lastError, data)
				}
				return output.NewError("E_UNKNOWN", "guard run completed with failed decisions", data)
			}
			return a.success(data)
		},
	}
	return command
}

func guardResultData(result guard.Result) map[string]any {
	return map[string]any{
		"summary":         result.Summary,
		"decisions":       result.Decisions,
		"partial_failure": result.PartialFailure,
		"fatal":           result.Fatal,
		"fatal_error":     result.FatalError,
		"_untrusted":      guardResultUntrustedFields(),
	}
}

type managementGuardBackend struct {
	client    *api.Client
	lastError error
}

func (b *managementGuardBackend) List(ctx context.Context) ([]guard.Account, error) {
	files, err := b.client.ListAuthFiles(ctx)
	if err != nil {
		b.lastError = err
		return nil, err
	}
	accounts := make([]guard.Account, 0, len(files))
	for _, file := range files {
		accountID, _ := codexAccountID(file)
		disabled, disabledKnown := authFileBool(file, "disabled")
		accounts = append(accounts, guard.Account{
			ID:               file.ID,
			ChatGPTAccountID: accountID,
			AuthIndex:        file.AuthIndex,
			Name:             file.Name,
			Provider:         file.Provider,
			Disabled:         disabled,
			DisabledKnown:    disabledKnown,
			Unavailable:      file.Unavailable,
			RuntimeOnly:      file.RuntimeOnly,
			UpdatedAt:        file.UpdatedAt,
		})
	}
	return accounts, nil
}

func (b *managementGuardBackend) ProbeCodex(ctx context.Context, account guard.Account) (guard.Assessment, error) {
	response, err := b.client.APICall(ctx, api.APICallRequest{
		AuthIndex: account.AuthIndex,
		Method:    http.MethodGet,
		URL:       codexUsageURL,
		Header: map[string]string{
			"Authorization":      "Bearer $TOKEN$",
			"Content-Type":       "application/json",
			"User-Agent":         codexUsageUserAgent,
			"Chatgpt-Account-Id": account.ChatGPTAccountID,
		},
	})
	if err != nil {
		b.lastError = err
		return guard.Assessment{}, err
	}
	assessment := quota.AssessCodex(response.StatusCode, response.Body, time.Now().UTC())
	return guard.Assessment{
		State:       string(assessment.State),
		Reason:      assessment.Reason,
		ResetAt:     assessment.ResetAt,
		UsedPercent: assessment.UsedPercent,
	}, nil
}
