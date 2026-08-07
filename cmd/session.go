package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/config"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/confirm"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/credential"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/spf13/cobra"
)

const (
	loginOperation  = "cliproxyapi-cli login"
	logoutOperation = "cliproxyapi-cli logout"
)

func (a *application) loginCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Verify and save one Management API session",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !a.dryRun && strings.TrimSpace(a.confirm) == "" {
				return output.NewError("E_CONFIRMATION_REQUIRED", "run with --dry-run, then retry with --confirm <confirm_token>", nil)
			}
			if a.store == nil {
				return output.NewError("E_CONFIG", "OS credential store is unavailable", nil)
			}

			cfg, err := config.Load(config.LoadOptions{
				BaseURL:          a.baseURL,
				StateDir:         a.stateDir,
				Timeout:          a.timeout,
				ReadKeyFromStdin: a.managementStdin,
				Stdin:            a.in,
			})
			if err != nil {
				return output.WrapError("E_CONFIG", err.Error(), err, nil)
			}
			if strings.TrimSpace(cfg.ManagementKey) == "" {
				return output.NewError("E_CONFIG", "login requires a management key from stdin or the environment", map[string]any{
					"accepted_env":  config.EnvManagementKey,
					"accepted_flag": "--management-key-stdin",
				})
			}

			existing, exists, err := config.LoadProfile(cfg.StateDir)
			if err != nil {
				return output.WrapError("E_CONFIG", "saved login profile is invalid", err, nil)
			}
			if exists && existing.BaseURL != cfg.BaseURL {
				return output.NewError("E_CONFLICT", "another Management API URL is already configured; run logout first", map[string]any{
					"configured_base_url": existing.BaseURL,
					"requested_base_url":  cfg.BaseURL,
				})
			}

			client, err := api.New(cfg.BaseURL, cfg.ManagementKey, newHTTPClient(cfg.Timeout))
			if err != nil {
				return err
			}
			if _, err := client.ListAuthFiles(command.Context()); err != nil {
				return err
			}

			details := map[string]any{
				"action":             "save_management_credential",
				"base_url":           cfg.BaseURL,
				"credential_backend": config.CredentialBackendKeyring,
			}
			confirmContext := map[string]any{
				"state_directory":        cfg.StateDir,
				"credential_fingerprint": cfg.CredentialFingerprint(),
			}
			gate := confirm.New(filepath.Join(cfg.StateDir, "confirm"))
			if a.dryRun {
				token, expiresAt, err := gate.Issue(loginOperation, details, confirmContext)
				if err != nil {
					return output.WrapError("E_IO", "failed to issue confirmation token", err, nil)
				}
				return a.success(map[string]any{
					"preview":       details,
					"confirm_token": token,
					"expires_at":    expiresAt.UTC().Format(time.RFC3339),
				})
			}

			if err := gate.Consume(strings.TrimSpace(a.confirm), loginOperation, details, confirmContext); err != nil {
				return mapConfirmError(err)
			}
			profile := config.Profile{
				Version:           config.ProfileVersion,
				BaseURL:           cfg.BaseURL,
				CredentialBackend: config.CredentialBackendKeyring,
			}
			if err := saveLogin(a.store, cfg.StateDir, profile, cfg.ManagementKey); err != nil {
				return err
			}
			return a.success(map[string]any{
				"configured":         true,
				"verified":           true,
				"base_url":           cfg.BaseURL,
				"credential_backend": config.CredentialBackendKeyring,
			})
		},
	}
}

func (a *application) logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved Management API session",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !a.dryRun && strings.TrimSpace(a.confirm) == "" {
				return output.NewError("E_CONFIRMATION_REQUIRED", "run with --dry-run, then retry with --confirm <confirm_token>", nil)
			}
			stateDir, err := config.ResolveStateDir(config.LoadOptions{StateDir: a.stateDir})
			if err != nil {
				return output.WrapError("E_CONFIG", err.Error(), err, nil)
			}
			profile, exists, err := config.LoadProfile(stateDir)
			if err != nil {
				return output.WrapError("E_CONFIG", "saved login profile is invalid", err, nil)
			}
			details := map[string]any{
				"action":     "remove_management_credential",
				"configured": exists,
			}
			if exists {
				details["base_url"] = profile.BaseURL
				details["credential_backend"] = profile.CredentialBackend
			}
			gate := confirm.New(filepath.Join(stateDir, "confirm"))
			confirmContext := map[string]any{"state_directory": stateDir}
			if a.dryRun {
				token, expiresAt, err := gate.Issue(logoutOperation, details, confirmContext)
				if err != nil {
					return output.WrapError("E_IO", "failed to issue confirmation token", err, nil)
				}
				return a.success(map[string]any{
					"preview":       details,
					"confirm_token": token,
					"expires_at":    expiresAt.UTC().Format(time.RFC3339),
				})
			}
			if err := gate.Consume(strings.TrimSpace(a.confirm), logoutOperation, details, confirmContext); err != nil {
				return mapConfirmError(err)
			}
			if exists {
				if a.store == nil {
					return output.NewError("E_CONFIG", "OS credential store is unavailable", nil)
				}
				if err := a.store.Delete(credential.Account(profile.BaseURL)); err != nil && !errors.Is(err, credential.ErrNotFound) {
					return output.WrapError("E_CONFIG", "failed to remove credential from the OS credential store", err, nil)
				}
			}
			removed, err := config.DeleteProfile(stateDir)
			if err != nil {
				return output.WrapError("E_IO", "failed to remove saved login profile", err, nil)
			}
			result := map[string]any{"removed": removed}
			if exists {
				result["base_url"] = profile.BaseURL
				result["credential_backend"] = profile.CredentialBackend
			}
			return a.success(result)
		},
	}
}

func saveLogin(store credential.Store, stateDir string, profile config.Profile, secret string) error {
	account := credential.Account(profile.BaseURL)
	previous, err := store.Get(account)
	previousExists := err == nil
	if err != nil && !errors.Is(err, credential.ErrNotFound) {
		return output.WrapError("E_CONFIG", "failed to read the OS credential store", err, nil)
	}
	if err := store.Set(account, secret); err != nil {
		return output.WrapError("E_CONFIG", "failed to save the management key in the OS credential store", err, nil)
	}
	if err := config.SaveProfile(stateDir, profile); err != nil {
		if previousExists {
			_ = store.Set(account, previous)
		} else {
			_ = store.Delete(account)
		}
		return output.WrapError("E_IO", "failed to save the login profile", err, nil)
	}
	return nil
}
