package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/config"
	"github.com/spf13/cobra"
)

const (
	noticeCacheFileName = "update-notices.json"
	noticeCacheVersion  = 1
	noticeCacheTTL      = 24 * time.Hour
)

type updateNotice struct {
	Type               string   `json:"type"`
	Severity           string   `json:"severity"`
	Message            string   `json:"message"`
	CurrentVersion     string   `json:"current_version"`
	LatestVersion      string   `json:"latest_version"`
	UpdateAvailable    bool     `json:"update_available"`
	InstallMethod      string   `json:"install_method"`
	RecommendedCommand string   `json:"recommended_command"`
	ReleaseURL         string   `json:"release_url,omitempty"`
	CheckedAt          string   `json:"checked_at"`
	Source             string   `json:"source"`
	NextSteps          []string `json:"next_steps"`
}

type noticeCacheDocument struct {
	Version   int            `json:"version"`
	CheckedAt string         `json:"checked_at"`
	Notices   []updateNotice `json:"notices"`
}

func (a *application) cachedNotices() []any {
	if a.suppressUpdateNotices {
		return nil
	}
	stateDir := a.updateNoticeStateDir()
	if stateDir == "" {
		return nil
	}
	return loadNoticeCache(stateDir)
}

func (a *application) clearUpdateNotices() {
	a.suppressUpdateNotices = true
	if stateDir := a.updateNoticeStateDir(); stateDir != "" {
		_ = writeUpdateNoticeCache(stateDir, nil)
	}
}

func (a *application) updateNoticeStateDir() string {
	if updateNoticeAmbientDisabled(a.stateDir) {
		return ""
	}
	stateDir, err := config.ResolveStateDir(config.LoadOptions{StateDir: a.stateDir})
	if err != nil {
		return ""
	}
	return stateDir
}

func updateNoticeAmbientDisabled(explicitStateDir string) bool {
	if !updateNoticeTestProcess() {
		return false
	}
	return strings.TrimSpace(explicitStateDir) == "" && strings.TrimSpace(os.Getenv(config.EnvStateDir)) == ""
}

var updateNoticeTestProcess = func() bool {
	exe := strings.ToLower(os.Args[0])
	return strings.HasSuffix(exe, ".test") || strings.HasSuffix(exe, ".test.exe")
}

func updateNoticesFromPlan(plan updatePlan, source string) []updateNotice {
	if !plan.UpdateAvailable {
		return nil
	}
	current := normalizeUpdateVersion(plan.CurrentVersion)
	latest := normalizeUpdateVersion(plan.TargetVersion)
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	return []updateNotice{{
		Type:               "update_available",
		Severity:           updateNoticeSeverity(current, latest, plan.SecurityUpdate),
		Message:            fmt.Sprintf("cliproxyapi-cli %s is available (current %s)", latest, current),
		CurrentVersion:     current,
		LatestVersion:      latest,
		UpdateAvailable:    true,
		InstallMethod:      plan.InstallMethod,
		RecommendedCommand: "cliproxyapi-cli update --compact",
		ReleaseURL:         plan.ReleaseURL,
		CheckedAt:          checkedAt,
		Source:             source,
		NextSteps: []string{
			"run cliproxyapi-cli update --compact",
			"verify current_version, signature/checksum status, and skill_sync_status",
			"run cliproxyapi-cli changelog --since " + current + " --compact",
			"refresh cliproxyapi-cli reference --compact before using new behavior",
		},
	}}
}

func updateNoticeSeverity(current, latest string, releaseNotesHaveSecurity bool) string {
	if releaseNotesHaveSecurity {
		return "warning"
	}
	currentVersion, currentOK := parseSemver(current)
	latestVersion, latestOK := parseSemver(latest)
	if currentOK && latestOK && latestVersion[0] > currentVersion[0] {
		return "warning"
	}
	return "info"
}

func loadNoticeCache(stateDir string) []any {
	notices := readUpdateNoticeCache(stateDir)
	if len(notices) == 0 {
		return nil
	}
	result := make([]any, len(notices))
	for index := range notices {
		result[index] = notices[index]
	}
	return result
}

func readUpdateNoticeCache(stateDir string) []updateNotice {
	path := noticeCachePath(stateDir)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cache noticeCacheDocument
	if err := json.Unmarshal(raw, &cache); err != nil || cache.Version != noticeCacheVersion {
		return nil
	}
	checkedAt, err := time.Parse(time.RFC3339, cache.CheckedAt)
	if err != nil || time.Since(checkedAt) > noticeCacheTTL || checkedAt.After(time.Now().Add(5*time.Minute)) {
		return nil
	}
	result := make([]updateNotice, 0, len(cache.Notices))
	for _, notice := range cache.Notices {
		if notice.Type != "update_available" || !notice.UpdateAvailable || normalizeUpdateVersion(notice.LatestVersion) == "" {
			continue
		}
		if compareUpdateVersions(notice.LatestVersion, version) <= 0 {
			continue
		}
		if notice.Severity != "info" && notice.Severity != "warning" {
			continue
		}
		notice.CurrentVersion = normalizeUpdateVersion(version)
		notice.LatestVersion = normalizeUpdateVersion(notice.LatestVersion)
		notice.Message = fmt.Sprintf("cliproxyapi-cli %s is available (current %s)", notice.LatestVersion, notice.CurrentVersion)
		notice.RecommendedCommand = "cliproxyapi-cli update --compact"
		notice.CheckedAt = checkedAt.UTC().Format(time.RFC3339)
		notice.Source = "cache"
		notice.NextSteps = []string{
			"run cliproxyapi-cli update --compact",
			"verify current_version, signature/checksum status, and skill_sync_status",
			"run cliproxyapi-cli changelog --since " + notice.CurrentVersion + " --compact",
			"refresh cliproxyapi-cli reference --compact before using new behavior",
		}
		if !strings.HasPrefix(notice.ReleaseURL, "https://github.com/"+updateDefaultRepo+"/releases/") {
			notice.ReleaseURL = ""
		}
		result = append(result, notice)
	}
	return result
}

func writeUpdateNoticeCache(stateDir string, notices []updateNotice) error {
	path := noticeCachePath(stateDir)
	if path == "" {
		return errors.New("state directory is empty")
	}
	if len(notices) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	for index := range notices {
		notices[index].CheckedAt = checkedAt
	}
	document := noticeCacheDocument{Version: noticeCacheVersion, CheckedAt: checkedAt, Notices: notices}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func noticeCachePath(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, noticeCacheFileName)
}

func installUpdateNoticeHelp(root *cobra.Command, app *application) {
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(command *cobra.Command, args []string) {
		defaultHelp(command, args)
		printUpdateNoticeHint(command.OutOrStdout(), app.cachedNotices())
	})
}

func printUpdateNoticeHint(writer io.Writer, notices []any) {
	if len(notices) == 0 {
		return
	}
	notice, ok := notices[0].(updateNotice)
	if !ok {
		return
	}
	_, _ = fmt.Fprintf(writer, "\nUpdate available: cliproxyapi-cli %s -> %s. Run: %s\n", notice.CurrentVersion, notice.LatestVersion, notice.RecommendedCommand)
}
