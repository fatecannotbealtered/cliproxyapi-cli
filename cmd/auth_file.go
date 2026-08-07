package cmd

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/confirm"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/spf13/cobra"
)

const authFileStatusOperation = "cliproxyapi-cli auth-file set-status"

func (a *application) authFileCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "auth-file",
		Short: "Inspect and manage CLIProxyAPI auth records",
	}
	command.AddCommand(a.authFileListCommand())
	command.AddCommand(a.authFileSetStatusCommand())
	return command
}

func (a *application) authFileListCommand() *cobra.Command {
	var provider string
	var disabled bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List auth records with stable local filtering",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if a.dryRun || strings.TrimSpace(a.confirm) != "" {
				return output.NewError("E_USAGE", "auth-file list is read-only and does not accept confirmation flags", nil)
			}
			cfg, err := a.runtimeConfig(true)
			if err != nil {
				return err
			}
			client, err := api.New(cfg.BaseURL, cfg.ManagementKey, newHTTPClient(cfg.Timeout))
			if err != nil {
				return err
			}
			files, err := client.ListAuthFiles(command.Context())
			if err != nil {
				return err
			}

			provider = strings.TrimSpace(provider)
			filterDisabled := command.Flags().Changed("disabled")
			filtered := make([]api.AuthFile, 0, len(files))
			for _, file := range files {
				if provider != "" && !strings.EqualFold(strings.TrimSpace(file.Provider), provider) {
					continue
				}
				if filterDisabled {
					current, present := authFileBool(file, "disabled")
					if !present || current != disabled {
						continue
					}
				}
				filtered = append(filtered, file)
			}
			sortAuthFiles(filtered)
			items := make([]map[string]any, 0, len(filtered))
			for _, file := range filtered {
				items = append(items, authFileListItem(file))
			}
			return a.success(map[string]any{
				"items":       items,
				"count":       len(items),
				"next_cursor": nil,
				"has_more":    false,
				"_untrusted":  []string{"items.name", "items.account", "items.email", "items.status_message"},
			})
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "Filter by provider (case-insensitive)")
	command.Flags().BoolVar(&disabled, "disabled", false, "Filter by disabled state; accepts true or false")
	return command
}

func (a *application) authFileSetStatusCommand() *cobra.Command {
	var name string
	var authIndex string
	var disabled bool
	command := &cobra.Command{
		Use:   "set-status",
		Short: "Enable or disable one exact auth record",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			name = strings.TrimSpace(name)
			authIndex = strings.TrimSpace(authIndex)
			if name == "" {
				return output.NewError("E_USAGE", "--name is required", nil)
			}
			if !command.Flags().Changed("disabled") {
				return output.NewError("E_USAGE", "--disabled must be explicitly set to true or false", nil)
			}
			if !a.dryRun && strings.TrimSpace(a.confirm) == "" {
				return output.NewError("E_CONFIRMATION_REQUIRED", "run with --dry-run, then retry with --confirm <confirm_token>", nil)
			}

			cfg, err := a.runtimeConfig(true)
			if err != nil {
				return err
			}
			client, err := api.New(cfg.BaseURL, cfg.ManagementKey, newHTTPClient(cfg.Timeout))
			if err != nil {
				return err
			}
			files, err := client.ListAuthFiles(command.Context())
			if err != nil {
				return err
			}
			target, err := exactAuthFile(files, name, authIndex)
			if err != nil {
				return err
			}
			currentDisabled, disabledPresent := authFileBool(target, "disabled")
			if !disabledPresent {
				return output.NewError("E_CONFLICT", "auth record does not expose a current disabled state", map[string]any{
					"name":       target.Name,
					"auth_index": target.AuthIndex,
				})
			}
			target.Disabled = currentDisabled

			details := authFileStatusScope(target, disabled)
			confirmContext := map[string]any{
				"base_url":               cfg.BaseURL,
				"credential_fingerprint": cfg.CredentialFingerprint(),
			}
			gate := confirm.New(filepath.Join(cfg.StateDir, "confirm"))
			if a.dryRun {
				token, expiresAt, err := gate.Issue(authFileStatusOperation, details, confirmContext)
				if err != nil {
					return output.WrapError("E_IO", "failed to issue confirmation token", err, nil)
				}
				return a.success(map[string]any{
					"preview":       details,
					"confirm_token": token,
					"expires_at":    expiresAt.UTC().Format(time.RFC3339),
					"_untrusted":    []string{"preview.name"},
				})
			}

			if err := gate.Consume(strings.TrimSpace(a.confirm), authFileStatusOperation, details, confirmContext); err != nil {
				return mapConfirmError(err)
			}
			if err := client.SetAuthFileDisabled(command.Context(), target.Name, target.AuthIndex, disabled); err != nil {
				return err
			}
			verifiedFiles, err := client.ListAuthFiles(command.Context())
			if err != nil {
				return err
			}
			verified, err := exactAuthFile(verifiedFiles, target.Name, target.AuthIndex)
			verifiedDisabled, disabledPresent := authFileBool(verified, "disabled")
			if err != nil || !disabledPresent || verifiedDisabled != disabled {
				return output.WrapError("E_CONFLICT", "auth status write could not be verified", err, map[string]any{
					"name":       target.Name,
					"auth_index": target.AuthIndex,
				})
			}
			verified.Disabled = verifiedDisabled
			return a.success(map[string]any{
				"name":              verified.Name,
				"auth_index":        verified.AuthIndex,
				"previous_disabled": target.Disabled,
				"disabled":          verified.Disabled,
				"verified":          true,
				"updated_at":        verified.UpdatedAt,
				"_untrusted":        []string{"name"},
			})
		},
	}
	command.Flags().StringVar(&name, "name", "", "Exact auth file name")
	command.Flags().StringVar(&authIndex, "auth-index", "", "Stable auth_index used to disambiguate the name")
	command.Flags().BoolVar(&disabled, "disabled", false, "Target disabled state (required, true or false)")
	_ = command.MarkFlagRequired("name")
	_ = command.MarkFlagRequired("disabled")
	return command
}

func exactAuthFile(files []api.AuthFile, name, authIndex string) (api.AuthFile, error) {
	matches := make([]api.AuthFile, 0, 1)
	for _, file := range files {
		if file.Name != name {
			continue
		}
		if authIndex != "" && file.AuthIndex != authIndex {
			continue
		}
		matches = append(matches, file)
	}
	if len(matches) == 0 {
		return api.AuthFile{}, output.NewError("E_NOT_FOUND", "auth record was not found", map[string]any{
			"name":       name,
			"auth_index": authIndex,
		})
	}
	if len(matches) > 1 {
		return api.AuthFile{}, output.NewError("E_CONFLICT", "multiple auth records share this name; provide --auth-index", map[string]any{
			"name":  name,
			"count": len(matches),
		})
	}
	return matches[0], nil
}

func authFileStatusScope(file api.AuthFile, targetDisabled bool) map[string]any {
	currentDisabled, disabledPresent := authFileBool(file, "disabled")
	return map[string]any{
		"action":                   "set_auth_file_status",
		"name":                     file.Name,
		"auth_index":               file.AuthIndex,
		"current_disabled":         currentDisabled,
		"current_disabled_present": disabledPresent,
		"target_disabled":          targetDisabled,
		"version": map[string]any{
			"id":                 file.ID,
			"id_present":         authFileFieldPresent(file, "id"),
			"updated_at":         file.UpdatedAt,
			"updated_at_present": authFileFieldPresent(file, "updated_at"),
		},
	}
}

func authFileListItem(file api.AuthFile) map[string]any {
	item := make(map[string]any)
	for field, value := range map[string]string{
		"id":             file.ID,
		"auth_index":     file.AuthIndex,
		"name":           file.Name,
		"provider":       file.Provider,
		"type":           file.Type,
		"account":        file.Account,
		"email":          file.Email,
		"status":         file.Status,
		"status_message": file.StatusMessage,
		"updated_at":     file.UpdatedAt,
	} {
		if authFileFieldPresent(file, field) {
			item[field] = value
		}
	}
	for field := range map[string]struct{}{
		"disabled":     {},
		"unavailable":  {},
		"runtime_only": {},
	} {
		if value, present := authFileBool(file, field); present {
			item[field] = value
		}
	}
	return item
}

func authFileFieldPresent(file api.AuthFile, field string) bool {
	_, present := file.Raw[field]
	return present
}

func authFileBool(file api.AuthFile, field string) (bool, bool) {
	raw, present := file.Raw[field]
	if !present {
		return false, false
	}
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return false, false
	}
	return *value, true
}

func sortAuthFiles(files []api.AuthFile) {
	sort.SliceStable(files, func(i, j int) bool {
		left := authFileSortKey(files[i])
		right := authFileSortKey(files[j])
		for index := range left {
			if left[index] != right[index] {
				return left[index] < right[index]
			}
		}
		return false
	})
}

func authFileSortKey(file api.AuthFile) [4]string {
	return [4]string{
		strings.ToLower(strings.TrimSpace(file.Name)),
		strings.ToLower(strings.TrimSpace(file.Provider)),
		strings.TrimSpace(file.AuthIndex),
		strings.TrimSpace(file.ID),
	}
}
