package cmd

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/guard"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/quota"
	"github.com/spf13/cobra"
)

const (
	guardStateFile = "guard-state.json"
	guardLockFile  = "guard.lock"
)

func (a *application) guardCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "guard",
		Short: "Conservatively disable exhausted Codex accounts and restore owned accounts",
	}
	command.AddCommand(a.guardRunOnceCommand())
	command.AddCommand(a.guardStateCommand())
	return command
}

func (a *application) guardRunOnceCommand() *cobra.Command {
	var apply bool
	command := &cobra.Command{
		Use:   "run-once",
		Short: "Run one quota guard pass; observe unless --apply is explicit",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if a.dryRun || strings.TrimSpace(a.confirm) != "" {
				return output.NewError("E_USAGE", "guard run-once uses observation mode by default and --apply for automation; it does not accept confirmation flags", nil)
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
			runner := guard.NewRunner(
				backend,
				guard.NewStore(filepath.Join(cfg.StateDir, guardStateFile)),
				guard.NewFileLock(filepath.Join(cfg.StateDir, guardLockFile)),
			)
			result := runner.RunOnce(command.Context(), apply)
			data := guardResultData(apply, result)
			if result.IsFatal() {
				code := "E_IO"
				switch result.FatalError {
				case guard.FatalLockHeld:
					code = "E_CONFLICT"
				case guard.FatalListFailed:
					if backend.lastError != nil {
						return output.WrapError(asCLIError(backend.lastError).Code, "guard could not list auth records", backend.lastError, data)
					}
					code = "E_SERVER"
				case guard.FatalDependencyMissing:
					code = "E_UNKNOWN"
				}
				return output.NewError(code, "guard run could not start or finish safely", data)
			}
			if result.PartialFailure {
				if backend.lastError != nil {
					return output.WrapError(asCLIError(backend.lastError).Code, "guard run completed with failed decisions", backend.lastError, data)
				}
				code := "E_UNKNOWN"
				for _, decision := range result.Decisions {
					if decision.Outcome != guard.OutcomeFailed {
						continue
					}
					switch decision.Reason {
					case guard.ReasonStateWriteFailed:
						code = "E_IO"
					case guard.ReasonVerificationFailed:
						if code == "E_UNKNOWN" {
							code = "E_CONFLICT"
						}
					}
				}
				return output.NewError(code, "guard run completed with failed decisions", data)
			}
			return a.success(data)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "Apply suggested disable, enable, and ownership-state actions")
	return command
}

func (a *application) guardStateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "state",
		Short: "Read local guard ownership state",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if a.dryRun || strings.TrimSpace(a.confirm) != "" {
				return output.NewError("E_USAGE", "guard state is read-only and does not accept confirmation flags", nil)
			}
			cfg, err := a.runtimeConfig(false)
			if err != nil {
				return err
			}
			records, err := guard.NewStore(filepath.Join(cfg.StateDir, guardStateFile)).Records()
			if err != nil {
				return output.WrapError("E_IO", "failed to read guard ownership state", err, nil)
			}
			return a.success(map[string]any{
				"items":      records,
				"count":      len(records),
				"_untrusted": []string{"items.name"},
			})
		},
	}
}

func guardResultData(apply bool, result guard.Result) map[string]any {
	return map[string]any{
		"apply":           apply,
		"summary":         result.Summary,
		"decisions":       result.Decisions,
		"partial_failure": result.PartialFailure,
		"fatal":           result.Fatal,
		"locked":          result.Locked,
		"fatal_error":     result.FatalError,
		"_untrusted":      []string{"decisions.name"},
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
			"User-Agent":         "cliproxyapi-cli/" + version,
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

func (b *managementGuardBackend) SetDisabled(ctx context.Context, account guard.Account, disabled bool) error {
	err := b.client.SetAuthFileDisabled(ctx, account.Name, account.AuthIndex, disabled)
	if err != nil {
		b.lastError = err
	}
	return err
}
