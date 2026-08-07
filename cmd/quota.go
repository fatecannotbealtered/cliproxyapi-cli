package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/api"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/quota"
	"github.com/spf13/cobra"
)

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

type quotaInspectionItem struct {
	Name        string         `json:"name"`
	AuthIndex   string         `json:"auth_index"`
	Provider    string         `json:"provider"`
	StatusCode  int            `json:"status_code,omitempty"`
	State       quota.State    `json:"state"`
	Reason      string         `json:"reason"`
	UsedPercent *float64       `json:"used_percent,omitempty"`
	ResetAt     *time.Time     `json:"reset_at,omitempty"`
	Windows     []quota.Window `json:"windows"`
	Evidence    map[string]any `json:"evidence"`
}

func (a *application) quotaCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "quota",
		Short: "Inspect allowlisted provider quota signals",
	}
	command.AddCommand(a.quotaInspectCommand())
	return command
}

func (a *application) quotaInspectCommand() *cobra.Command {
	var provider string
	command := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect Codex quota through the fixed ChatGPT usage endpoint",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if a.dryRun || strings.TrimSpace(a.confirm) != "" {
				return output.NewError("E_USAGE", "quota inspect is read-only and does not accept confirmation flags", nil)
			}
			provider = strings.ToLower(strings.TrimSpace(provider))
			if provider != "codex" {
				return output.NewError("E_VALIDATION", "quota inspect currently supports only provider codex", map[string]any{
					"provider": provider,
				})
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
			codexFiles := make([]api.AuthFile, 0, len(files))
			for _, file := range files {
				if strings.EqualFold(strings.TrimSpace(file.Provider), "codex") {
					codexFiles = append(codexFiles, file)
				}
			}
			sortAuthFiles(codexFiles)

			items := make([]quotaInspectionItem, 0, len(codexFiles))
			for _, file := range codexFiles {
				if strings.TrimSpace(file.AuthIndex) == "" {
					items = append(items, unknownQuotaItem(file, "auth_index is missing; quota was not probed"))
					continue
				}
				accountID, ok := codexAccountID(file)
				if !ok {
					items = append(items, unknownQuotaItem(file, "id_token.chatgpt_account_id is missing or invalid; quota was not probed"))
					continue
				}
				response, err := client.APICall(command.Context(), api.APICallRequest{
					AuthIndex: file.AuthIndex,
					Method:    http.MethodGet,
					URL:       codexUsageURL,
					Header: map[string]string{
						"Authorization":      "Bearer $TOKEN$",
						"Content-Type":       "application/json",
						"User-Agent":         "cliproxyapi-cli/" + version,
						"Chatgpt-Account-Id": accountID,
					},
				})
				if err != nil {
					return err
				}
				assessment := quota.AssessCodex(response.StatusCode, response.Body, time.Now().UTC())
				items = append(items, quotaInspectionItem{
					Name:        file.Name,
					AuthIndex:   file.AuthIndex,
					Provider:    "codex",
					StatusCode:  response.StatusCode,
					State:       assessment.State,
					Reason:      assessment.Reason,
					UsedPercent: assessment.UsedPercent,
					ResetAt:     assessment.ResetAt,
					Windows:     assessment.Windows,
					Evidence:    assessment.Evidence,
				})
			}
			return a.success(map[string]any{
				"items":      items,
				"count":      len(items),
				"_untrusted": []string{"items.name", "items.evidence"},
			})
		},
	}
	command.Flags().StringVar(&provider, "provider", "codex", "Quota provider (only codex is supported)")
	return command
}

func codexAccountID(file api.AuthFile) (string, bool) {
	raw, ok := file.Raw["id_token"]
	if !ok || len(raw) == 0 {
		return "", false
	}
	var claims struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", false
	}
	accountID := strings.TrimSpace(claims.ChatGPTAccountID)
	if accountID == "" || strings.ContainsAny(accountID, "\r\n") {
		return "", false
	}
	return accountID, true
}

func unknownQuotaItem(file api.AuthFile, reason string) quotaInspectionItem {
	return quotaInspectionItem{
		Name:      file.Name,
		AuthIndex: file.AuthIndex,
		Provider:  "codex",
		State:     quota.StateUnknown,
		Reason:    reason,
		Windows:   []quota.Window{},
		Evidence:  map[string]any{},
	}
}
