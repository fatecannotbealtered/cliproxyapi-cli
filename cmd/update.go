package cmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	semver "github.com/blang/semver"
	"github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"
	"github.com/spf13/cobra"
)

const (
	updateDefaultRepo = "fatecannotbealtered/cliproxyapi-cli"
	updateBinaryName  = "cliproxyapi-cli"
	updateAPIBaseURL  = "https://api.github.com"
	updateRawBaseURL  = "https://raw.githubusercontent.com"
	updateNPMPackage  = "@fateforge/cliproxyapi-cli"
	updateSkillRepo   = updateDefaultRepo

	stageDiscover        = "discover"
	stageDownload        = "download"
	stageVerifySignature = "verify_signature"
	stageVerifyChecksum  = "verify_checksum"
	stageReplace         = "replace"
	stageSkillSync       = "skill_sync"
)

type updateReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type updateRelease struct {
	TagName string               `json:"tag_name"`
	HTMLURL string               `json:"html_url"`
	Body    string               `json:"body"`
	Assets  []updateReleaseAsset `json:"assets"`
}

type updatePlan struct {
	CurrentVersion     string
	TargetVersion      string
	ReleaseURL         string
	AssetName          string
	AssetURL           string
	ChecksumURL        string
	SignatureBundleURL string
	UpdateAvailable    bool
	SecurityUpdate     bool
	InstallMethod      string
	SkillSyncCommand   string
}

type updateApplyResult struct {
	Status                   string
	Path                     string
	OriginalRestored         bool
	InstalledExecutableState string
	BackupPath               string
	BackupState              string
	StagedPath               string
	StagedState              string
}

type updateHTTPError struct {
	StatusCode int
	Header     http.Header
	err        error
}

func (e *updateHTTPError) Error() string { return e.err.Error() }
func (e *updateHTTPError) Unwrap() error { return e.err }

type updateLocalIOError struct{ err error }

func (e *updateLocalIOError) Error() string { return e.err.Error() }
func (e *updateLocalIOError) Unwrap() error { return e.err }

type updatePackageManagerError struct {
	err    error
	output string
}

func (e *updatePackageManagerError) Error() string { return "npm install failed" }
func (e *updatePackageManagerError) Unwrap() error { return e.err }

var (
	updateHTTPClient = &http.Client{Timeout: 2 * time.Minute}
	updateGitHubAPI  = updateAPIBaseURL
	updateGitHubRaw  = updateRawBaseURL
	updatePlatform   = func() (string, string) { return runtime.GOOS, runtime.GOARCH }
	updateExecutable = os.Executable
	updateApply      = applyUpdateBinary
	updateSkillSync  = runUpdateSkillSync

	updateRunPackageManager = runPackageManagerInstall
	updateInstalledVersion  = verifyUpdateInstalledVersion
	updateDownloadHook      = downloadUpdateFile
	updateChecksumHook      = verifyUpdateChecksum
	updateExtractHook       = extractUpdateArchive
	updateRename            = os.Rename
)

func (a *application) updateCommand() *cobra.Command {
	var checkOnly bool
	command := &cobra.Command{
		Use:   "update",
		Short: "Update cliproxyapi-cli to the latest release",
		Long: `Update cliproxyapi-cli to the latest release.

A bare update discovers the GitHub release, verifies the signed checksums for
standalone binaries, replaces the current binary, and syncs the bundled Agent
Skill. npm-managed installs are updated through npm instead of in-place binary
replacement. Use --check or --dry-run for read-only probes; neither emits a
confirmation token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runUpdate(cmd.Context(), updateOptions{
				CheckOnly: checkOnly,
			})
		},
	}
	command.Flags().BoolVar(&checkOnly, "check", false, "Check for an available update without installing")
	command.Flags().BoolVar(&a.dryRun, "dry-run", false, "Preview the update without changing the installation or issuing a confirmation token")
	command.Flags().StringVar(&a.confirm, "confirm", "", "Unsupported for update")
	_ = command.Flags().MarkHidden("confirm")
	return command
}

type updateOptions struct {
	CheckOnly bool
}

func (a *application) runUpdate(ctx context.Context, opts updateOptions) error {
	if strings.TrimSpace(a.confirm) != "" {
		return updateFailure("update does not use --confirm; use --check or --dry-run for read-only probes", "E_USAGE",
			updateFailDetails(stageDiscover, version, false, "not_run"))
	}
	if err := validateUpdateFields(a.fields); err != nil {
		return updateFailure("invalid --fields selection: "+err.Error(), "E_VALIDATION",
			updateFailDetails(stageDiscover, version, false, "not_run"))
	}
	exePath, _ := updateExecutable()
	installMethod := detectInstallMethod(exePath)
	if installMethod == "binary" && !opts.CheckOnly && !a.dryRun {
		if err := cleanupStaleUpdateFiles(exePath); err != nil {
			return updateFailure("cleaning stale update files: "+err.Error(), "E_IO",
				updateFailDetails(stageReplace, version, false, "not_run"))
		}
	}

	release, err := fetchUpdateRelease(ctx)
	if err != nil {
		if interrupted := updateInterrupted(ctx, err); interrupted != nil {
			return interrupted(stageDiscover, version, false, "not_run", "cancelled before any change; still on "+version)
		}
		return updateFailure("checking release: "+err.Error(), classifyUpdateNetworkError(err),
			updateFailDetails(stageDiscover, version, false, "not_run"))
	}
	plan, err := buildUpdatePlan(release, version)
	if err != nil {
		return updateFailure(err.Error(), "E_VALIDATION", updateFailDetails(stageDiscover, version, false, "not_run"))
	}
	plan.InstallMethod = installMethod
	plan.SkillSyncCommand = updateSkillSyncCommand()
	if opts.CheckOnly && plan.UpdateAvailable {
		if changelog, changelogErr := fetchUpdateChangelog(ctx, plan.TargetVersion); changelogErr != nil {
			if interrupted := updateInterrupted(ctx, changelogErr); interrupted != nil {
				return interrupted(stageDiscover, plan.CurrentVersion, false, "not_run",
					"cancelled while checking the release changelog; no change, still on "+plan.CurrentVersion)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(changelogErr, context.DeadlineExceeded) {
				return updateFailure("update check timed out while checking the release changelog; no change, still on "+plan.CurrentVersion,
					"E_TIMEOUT", updateFailDetails(stageDiscover, plan.CurrentVersion, false, "not_run"))
			}
			plan.SecurityUpdate = true
		} else {
			plan.SecurityUpdate = plan.SecurityUpdate || updateChangelogDeltaHasSecurity(changelog, plan.CurrentVersion, plan.TargetVersion)
		}
	}
	result := updateResultMap(plan, updateStatus(plan))
	if opts.CheckOnly {
		notices := updateNoticesFromPlan(plan, "update_check")
		if len(notices) > 0 {
			result["notices"] = notices
		}
		if stateDir := a.updateNoticeStateDir(); stateDir != "" {
			_ = writeUpdateNoticeCache(stateDir, notices)
		}
		return a.updateSuccess(result)
	}

	installNeeded := plan.UpdateAvailable
	if a.dryRun {
		result["status"] = "dry_run"
		changes := []map[string]any{}
		if installNeeded && installMethod == "npm" {
			changes = append(changes, map[string]any{
				"action":          "run package manager update",
				"current_version": plan.CurrentVersion,
				"target_version":  plan.TargetVersion,
				"command":         updateInstallCommand(installMethod, plan.TargetVersion),
			})
		} else if installNeeded {
			changes = append(changes, map[string]any{
				"action":          "replace binary",
				"current_version": plan.CurrentVersion,
				"target_version":  plan.TargetVersion,
				"asset":           plan.AssetName,
			})
			result["verification"] = []string{stageVerifySignature, stageVerifyChecksum}
		}
		changes = append(changes, map[string]any{"action": "sync skill directory", "command": plan.SkillSyncCommand})
		result["preview"] = map[string]any{"action": "update cliproxyapi-cli", "changes": changes}
		return a.updateSuccess(result)
	}
	if !installNeeded {
		result["target_version"] = plan.CurrentVersion
		result["previous_version"] = plan.CurrentVersion
		result["update_available"] = false
		if err := updateSkillSync(ctx, updateSkillRepo); err != nil {
			a.clearUpdateNotices()
			details := updateFailDetails(stageSkillSync, plan.CurrentVersion, false, "failed")
			details["skill_sync_command"] = plan.SkillSyncCommand
			details["previous_version"] = plan.CurrentVersion
			details["target_version"] = plan.CurrentVersion
			details["update_available"] = false
			code := "E_NETWORK"
			if updateInterrupted(ctx, err) != nil {
				code = "E_INTERRUPTED"
			} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				code = "E_TIMEOUT"
			}
			return updateFailure("syncing skill directory: "+err.Error(), code, details)
		}
		result["skill_sync_status"] = "synced"
		a.clearUpdateNotices()
		return a.updateSuccess(result)
	}
	if installMethod == "npm" {
		return a.runPackageManagerUpdate(ctx, plan, result, installMethod, exePath)
	}
	return a.runStandaloneUpdate(ctx, plan, exePath)
}

func (a *application) runStandaloneUpdate(ctx context.Context, plan updatePlan, exePath string) error {
	if exePath == "" {
		return updateFailure("could not determine current executable path", "E_IO",
			updateFailDetails(stageReplace, plan.CurrentVersion, false, "not_run"))
	}
	if plan.AssetURL == "" {
		return updateFailure("release does not include the platform archive; stop and report the incomplete release", "E_NOT_FOUND",
			updateFailDetails(stageDiscover, plan.CurrentVersion, false, "not_run"))
	}
	if plan.ChecksumURL == "" {
		return updateFailure("release does not include checksums.txt; integrity failure, do not retry", "E_INTEGRITY",
			updateFailDetails(stageVerifyChecksum, plan.CurrentVersion, false, "not_run"))
	}
	tmpDir, err := os.MkdirTemp("", "cliproxyapi-cli-update-*")
	if err != nil {
		return updateFailure("creating temp dir: "+err.Error(), "E_IO",
			updateFailDetails(stageReplace, plan.CurrentVersion, false, "not_run"))
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, plan.AssetName)
	if err := updateDownloadHook(ctx, plan.AssetURL, archivePath); err != nil {
		if interrupted := updateInterrupted(ctx, err); interrupted != nil {
			return interrupted(stageDownload, plan.CurrentVersion, false, "not_run", "cancelled during download; no change, still on "+plan.CurrentVersion)
		}
		details := updateFailDetails(stageDownload, plan.CurrentVersion, false, "not_run")
		markUpdateFailureScope(details, err)
		return updateFailure("downloading archive: "+err.Error(), classifyUpdateTransferError(err), details)
	}
	checksumPath := filepath.Join(tmpDir, "checksums.txt")
	if err := updateDownloadHook(ctx, plan.ChecksumURL, checksumPath); err != nil {
		if interrupted := updateInterrupted(ctx, err); interrupted != nil {
			return interrupted(stageDownload, plan.CurrentVersion, false, "not_run", "cancelled during download; no change, still on "+plan.CurrentVersion)
		}
		details := updateFailDetails(stageDownload, plan.CurrentVersion, false, "not_run")
		markUpdateFailureScope(details, err)
		return updateFailure("downloading checksums: "+err.Error(), classifyUpdateTransferError(err), details)
	}
	signatureStatus, code, err := verifyUpdateChecksumSignature(ctx, checksumPath, plan.SignatureBundleURL, tmpDir, plan.TargetVersion)
	if err != nil {
		failureStage := stageVerifySignature
		cancelMessage := "cancelled during signature verification; no change, still on " + plan.CurrentVersion
		if signatureStatus == "download_failed" {
			failureStage = stageDownload
			cancelMessage = "cancelled while downloading the signature bundle; no change, still on " + plan.CurrentVersion
		}
		if interrupted := updateInterrupted(ctx, err); interrupted != nil {
			return interrupted(failureStage, plan.CurrentVersion, false, "not_run", cancelMessage)
		}
		details := updateFailDetails(failureStage, plan.CurrentVersion, false, "not_run")
		details["signature_status"] = signatureStatus
		markUpdateFailureScope(details, err)
		return updateFailure("verifying release signature: "+err.Error(), code, details)
	}
	if err := updateChecksumHook(archivePath, checksumPath, plan.AssetName); err != nil {
		if interrupted := updateInterrupted(ctx, err); interrupted != nil {
			return interrupted(stageVerifyChecksum, plan.CurrentVersion, false, "not_run", "cancelled during checksum verification; no change, still on "+plan.CurrentVersion)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return updateFailure("checksum verification timed out; no change, still on "+plan.CurrentVersion, "E_TIMEOUT",
				updateFailDetails(stageVerifyChecksum, plan.CurrentVersion, false, "not_run"))
		}
		return updateFailure("verifying archive: "+err.Error(), classifyUpdateVerificationError(err),
			updateFailDetails(stageVerifyChecksum, plan.CurrentVersion, false, "not_run"))
	}
	if err := updateStageContextFailure(ctx, stageVerifyChecksum, plan.CurrentVersion, false, "not_run", "during checksum verification"); err != nil {
		return err
	}
	binPath, err := updateExtractHook(archivePath, plan.AssetName, tmpDir)
	if err != nil {
		if interrupted := updateInterrupted(ctx, err); interrupted != nil {
			return interrupted(stageReplace, plan.CurrentVersion, false, "not_run", "cancelled during archive extraction; no change, still on "+plan.CurrentVersion)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return updateFailure("archive extraction timed out; no change, still on "+plan.CurrentVersion, "E_TIMEOUT",
				updateFailDetails(stageReplace, plan.CurrentVersion, false, "not_run"))
		}
		code := classifyUpdateVerificationError(err)
		message := "extracting verified archive: " + err.Error()
		if code == "E_INTEGRITY" {
			message += "; integrity failure, do not retry"
		}
		return updateFailure(message, code, updateFailDetails(stageReplace, plan.CurrentVersion, false, "not_run"))
	}
	if err := updateStageContextFailure(ctx, stageReplace, plan.CurrentVersion, false, "not_run", "during archive extraction"); err != nil {
		return err
	}
	applied, err := updateApply(ctx, binPath, exePath)
	if err != nil {
		details := updateFailDetails(stageReplace, plan.CurrentVersion, false, "not_run")
		if applied.Status != "" {
			details["replacement_status"] = applied.Status
			details["target_path"] = applied.Path
			details["original_restored"] = applied.OriginalRestored
			details["installed_executable_state"] = applied.InstalledExecutableState
			details["backup_path"] = applied.BackupPath
			details["backup_state"] = applied.BackupState
			details["staged_path"] = applied.StagedPath
			details["staged_state"] = applied.StagedState
			details["next_step"] = fmt.Sprintf("restore %s from %s before starting the CLI again; then verify the installed version before re-running update", applied.Path, applied.BackupPath)
		}
		if interrupted := updateInterrupted(ctx, err); interrupted != nil && applied.Status == "" {
			return interrupted(stageReplace, plan.CurrentVersion, false, "not_run", "cancelled before the atomic replacement; no change, still on "+plan.CurrentVersion)
		}
		if updateInterrupted(ctx, err) != nil {
			return updateFailure("update cancelled during atomic replacement: "+err.Error(), "E_INTERRUPTED", details)
		}
		return updateFailure("installing update: "+err.Error(), updateReplaceFailureClass(err), details)
	}
	if err := updateSkillSync(ctx, updateSkillRepo); err != nil {
		a.clearUpdateNotices()
		details := updateFailDetails(stageSkillSync, plan.TargetVersion, true, "failed")
		details["skill_sync_command"] = plan.SkillSyncCommand
		details["previous_version"] = plan.CurrentVersion
		details["target_version"] = plan.TargetVersion
		details["update_available"] = false
		details["signature_status"] = signatureStatus
		details["signature_verified"] = signatureStatus == "verified"
		details["checksum_verified"] = true
		details["status"] = applied.Status
		details["hint"] = fmt.Sprintf("binary now at %s; run %q to sync the Skill", plan.TargetVersion, plan.SkillSyncCommand)
		code := "E_NETWORK"
		if updateInterrupted(ctx, err) != nil {
			code = "E_INTERRUPTED"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			code = "E_TIMEOUT"
		}
		return updateFailure("syncing skill directory: "+err.Error(), code, details)
	}
	result := updateResultMap(plan, "updated")
	result["path"] = applied.Path
	result["previous_version"] = plan.CurrentVersion
	result["current_version"] = plan.TargetVersion
	result["update_available"] = false
	result["binary_replaced"] = true
	result["checksum_verified"] = true
	result["signature_status"] = signatureStatus
	result["signature_verified"] = signatureStatus == "verified"
	result["skill_sync_status"] = "synced"
	result["hint"] = fmt.Sprintf("run \"cliproxyapi-cli changelog --since %s\" to see what changed", plan.CurrentVersion)
	a.clearUpdateNotices()
	return a.updateSuccess(result)
}

func (a *application) runPackageManagerUpdate(ctx context.Context, plan updatePlan, result map[string]any, method, exePath string) error {
	command := updateInstallCommand(method, plan.TargetVersion)
	result["command"] = command
	managerErr := updateRunPackageManager(ctx, method, plan.TargetVersion)
	observedVersion, observeErr := updateInstalledVersion(exePath)
	installState, currentVersion, binaryReplaced := observedPackageState(plan, observedVersion, observeErr)
	if managerErr != nil {
		details := updateFailDetails(stageReplace, currentVersion, binaryReplaced, "not_run")
		details["install_method"] = method
		details["command"] = command
		details["install_state"] = installState
		if observeErr != nil {
			details["verification_error"] = observeErr.Error()
		}
		if observedVersion != "" {
			details["verified_current_version"] = observedVersion
		}
		if observedVersion == plan.TargetVersion {
			details["previous_version"] = plan.CurrentVersion
			details["target_version"] = plan.TargetVersion
			details["update_available"] = false
			details["skill_sync_command"] = plan.SkillSyncCommand
			details["next_step"] = fmt.Sprintf("package is at %s; re-run update to sync the Skill, or run %q", plan.TargetVersion, plan.SkillSyncCommand)
			a.clearUpdateNotices()
		}
		return updateFailure("package-manager update failed; inspect install_state and run the reported command if needed", classifyPackageManagerError(managerErr), details)
	}
	if observeErr != nil || observedVersion != plan.TargetVersion {
		details := updateFailDetails(stageReplace, currentVersion, binaryReplaced, "not_run")
		details["install_method"] = method
		details["command"] = command
		details["install_state"] = installState
		if observeErr != nil {
			details["verification_error"] = observeErr.Error()
		}
		if observedVersion != "" {
			details["verified_current_version"] = observedVersion
		}
		details["next_step"] = "the npm command exited successfully but the installed CLI did not verify at the target version; inspect the npm prefix and optional platform package, then re-run update"
		return updateFailure("package-manager update did not reach the verified target version", "E_CONFLICT", details)
	}
	result["status"] = "updated"
	result["previous_version"] = plan.CurrentVersion
	result["current_version"] = plan.TargetVersion
	result["update_available"] = false
	result["signature_status"] = "not_checked"
	result["signature_verified"] = false
	result["checksum_verified"] = false
	result["binary_replaced"] = true
	if err := updateSkillSync(ctx, updateSkillRepo); err != nil {
		a.clearUpdateNotices()
		details := updateFailDetails(stageSkillSync, plan.TargetVersion, true, "failed")
		details["skill_sync_command"] = plan.SkillSyncCommand
		details["previous_version"] = plan.CurrentVersion
		details["target_version"] = plan.TargetVersion
		details["update_available"] = false
		details["install_method"] = method
		details["signature_status"] = "not_checked"
		details["signature_verified"] = false
		details["checksum_verified"] = false
		details["hint"] = fmt.Sprintf("binary now at %s; run %q to sync the Skill", plan.TargetVersion, plan.SkillSyncCommand)
		code := "E_NETWORK"
		if updateInterrupted(ctx, err) != nil {
			code = "E_INTERRUPTED"
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			code = "E_TIMEOUT"
		}
		return updateFailure("syncing skill directory: "+err.Error(), code, details)
	}
	result["skill_sync_status"] = "synced"
	result["hint"] = fmt.Sprintf("run \"cliproxyapi-cli changelog --since %s\" to see what changed", plan.CurrentVersion)
	a.clearUpdateNotices()
	return a.updateSuccess(result)
}

func validateUpdateFields(fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := map[string]bool{}
	for _, field := range referenceSchemas()["update"].Fields {
		allowed[field] = true
	}
	for _, rawField := range fields {
		field := strings.TrimSpace(rawField)
		if field != "" && !allowed[field] {
			return fmt.Errorf("unknown output field %q", field)
		}
	}
	return nil
}

func (a *application) updateSuccess(result map[string]any) error {
	for _, rawField := range a.fields {
		field := strings.TrimSpace(rawField)
		if field != "" {
			if _, ok := result[field]; !ok {
				result[field] = nil
			}
		}
	}
	return a.success(result)
}

func updateFailDetails(stage, currentVersion string, binaryReplaced any, skillSyncStatus string) map[string]any {
	return map[string]any{
		"stage":             stage,
		"current_version":   currentVersion,
		"binary_replaced":   binaryReplaced,
		"skill_sync_status": skillSyncStatus,
	}
}

func ensureUpdateFailureDetails(err *output.CLIError) *output.CLIError {
	if err == nil {
		return err
	}
	if err.Details == nil {
		err.Details = map[string]any{}
	}
	defaults := updateFailDetails(stageDiscover, version, false, "not_run")
	for key, value := range defaults {
		if _, ok := err.Details[key]; !ok {
			err.Details[key] = value
		}
	}
	return err
}

func updateFailure(message, code string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	if _, ok := details["next_step"]; !ok {
		switch code {
		case "E_NETWORK", "E_SERVER", "E_RATE_LIMITED", "E_TIMEOUT":
			details["next_step"] = "transient failure; re-run cliproxyapi-cli update (it is idempotent)"
		case "E_AUTH":
			details["next_step"] = "check GitHub access or credentials, then re-run cliproxyapi-cli update"
		case "E_INTEGRITY":
			details["next_step"] = "do not retry; stop and report a possible incomplete, corrupt, or forged release"
		case "E_INTERRUPTED":
			details["next_step"] = "inspect current_version and binary_replaced, then re-run the idempotent update or the reported Skill sync command"
		case "E_FORBIDDEN":
			stage, _ := details["stage"].(string)
			if scope, _ := details["failure_scope"].(string); scope == "local" {
				details["next_step"] = "fix local permissions, disk space, or file locks, then re-run cliproxyapi-cli update"
			} else if stage == stageDiscover || stage == stageDownload {
				details["next_step"] = "check GitHub access or credentials, then re-run cliproxyapi-cli update"
			} else {
				details["next_step"] = "fix local permissions, disk space, or file locks, then re-run cliproxyapi-cli update"
			}
		case "E_IO", "E_CONFIG":
			details["next_step"] = "fix local permissions, disk space, or file locks, then re-run cliproxyapi-cli update"
		}
	}
	return output.NewError(code, message, details)
}

func classifyUpdateNetworkError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "E_TIMEOUT"
	}
	var httpErr *updateHTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode > 0 {
		switch {
		case httpErr.StatusCode == http.StatusUnauthorized:
			return "E_AUTH"
		case httpErr.StatusCode == http.StatusForbidden &&
			(httpErr.Header.Get("X-RateLimit-Remaining") == "0" || httpErr.Header.Get("Retry-After") != ""):
			return "E_RATE_LIMITED"
		case httpErr.StatusCode == http.StatusForbidden:
			return "E_FORBIDDEN"
		case httpErr.StatusCode == http.StatusNotFound:
			return "E_NOT_FOUND"
		case httpErr.StatusCode == http.StatusRequestTimeout:
			return "E_TIMEOUT"
		case httpErr.StatusCode == http.StatusConflict:
			return "E_CONFLICT"
		case httpErr.StatusCode == http.StatusTooManyRequests:
			return "E_RATE_LIMITED"
		case httpErr.StatusCode >= 500:
			return "E_SERVER"
		}
	}
	return "E_NETWORK"
}

func classifyUpdateTransferError(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "E_FORBIDDEN"
	}
	var localErr *updateLocalIOError
	if errors.As(err, &localErr) {
		return "E_IO"
	}
	return classifyUpdateNetworkError(err)
}

func classifyUpdateVerificationError(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "E_FORBIDDEN"
	}
	var localErr *updateLocalIOError
	if errors.As(err, &localErr) {
		return "E_IO"
	}
	return "E_INTEGRITY"
}

func classifyPackageManagerError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "E_INTERRUPTED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "E_TIMEOUT"
	}
	text := strings.ToUpper(err.Error())
	var managerErr *updatePackageManagerError
	if errors.As(err, &managerErr) {
		text += " " + strings.ToUpper(managerErr.output)
	}
	for _, marker := range []string{"EAI_AGAIN", "ECONNRESET", "ECONNREFUSED", "ENETUNREACH", "ERR_SOCKET_TIMEOUT", "ETIMEDOUT"} {
		if strings.Contains(text, marker) {
			return "E_NETWORK"
		}
	}
	if strings.Contains(text, "EACCES") || strings.Contains(text, "EPERM") || errors.Is(err, os.ErrPermission) {
		return "E_FORBIDDEN"
	}
	return "E_IO"
}

func markUpdateFailureScope(details map[string]any, err error) {
	var localErr *updateLocalIOError
	var httpErr *updateHTTPError
	if errors.As(err, &localErr) || (errors.Is(err, os.ErrPermission) && !errors.As(err, &httpErr)) {
		details["failure_scope"] = "local"
	}
}

func observedPackageState(plan updatePlan, observedVersion string, observeErr error) (string, string, any) {
	if observeErr != nil || normalizeUpdateVersion(observedVersion) == "" {
		return "unknown", "unknown", nil
	}
	observedVersion = normalizeUpdateVersion(observedVersion)
	switch observedVersion {
	case normalizeUpdateVersion(plan.TargetVersion):
		return "target", observedVersion, true
	case normalizeUpdateVersion(plan.CurrentVersion):
		return "previous", observedVersion, false
	default:
		return "unexpected", observedVersion, true
	}
}

func updateReplaceFailureClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "E_INTERRUPTED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "E_TIMEOUT"
	}
	if errors.Is(err, os.ErrPermission) {
		return "E_FORBIDDEN"
	}
	return "E_IO"
}

func updateInterrupted(ctx context.Context, err error) func(stage, currentVersion string, binaryReplaced bool, skillSyncStatus, message string) error {
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return func(stage, currentVersion string, binaryReplaced bool, skillSyncStatus, message string) error {
		return updateFailure("update cancelled: "+message, "E_INTERRUPTED",
			updateFailDetails(stage, currentVersion, binaryReplaced, skillSyncStatus))
	}
}

func updateStageContextFailure(ctx context.Context, stage, currentVersion string, binaryReplaced bool, skillSyncStatus, action string) error {
	if ctx.Err() == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return updateFailure("update timed out "+action+"; no change, still on "+currentVersion, "E_TIMEOUT",
			updateFailDetails(stage, currentVersion, binaryReplaced, skillSyncStatus))
	}
	return updateFailure("update cancelled "+action+"; no change, still on "+currentVersion, "E_INTERRUPTED",
		updateFailDetails(stage, currentVersion, binaryReplaced, skillSyncStatus))
}

func fetchUpdateRelease(ctx context.Context) (*updateRelease, error) {
	data, err := updateHTTPGet(ctx, updateReleaseURL(updateDefaultRepo))
	if err != nil {
		return nil, err
	}
	var rel updateRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("parsing release JSON: %w", err)
	}
	return &rel, nil
}

func updateReleaseURL(repo string) string {
	base := strings.TrimRight(updateGitHubAPI, "/")
	return base + "/repos/" + repo + "/releases/latest"
}

func fetchUpdateChangelog(ctx context.Context, targetVersion string) (string, error) {
	base := strings.TrimRight(updateGitHubRaw, "/")
	rawURL := base + "/" + updateDefaultRepo + "/v" + normalizeUpdateVersion(targetVersion) + "/CHANGELOG.md"
	data, err := updateHTTPGet(ctx, rawURL)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func updateChangelogDeltaHasSecurity(markdown, current, latest string) bool {
	for _, entry := range parseChangelog(markdown) {
		if compareSemver(entry.Version, current) <= 0 || compareSemver(entry.Version, latest) > 0 {
			continue
		}
		if len(entry.Changes["security"]) > 0 {
			return true
		}
	}
	return false
}

func buildUpdatePlan(rel *updateRelease, currentVersion string) (updatePlan, error) {
	if rel == nil {
		return updatePlan{}, errors.New("empty release response")
	}
	target := normalizeUpdateVersion(rel.TagName)
	if target == "" {
		return updatePlan{}, errors.New("release is missing tag_name")
	}
	if _, ok := parseUpdateSemver(target); !ok {
		return updatePlan{}, fmt.Errorf("release tag %q is not a valid semantic version", rel.TagName)
	}
	assetName, err := updateArchiveName(target)
	if err != nil {
		return updatePlan{}, err
	}
	assetURL := findUpdateAssetURL(rel.Assets, assetName)
	checksumURL := findUpdateAssetURL(rel.Assets, "checksums.txt")
	current := normalizeUpdateVersion(currentVersion)
	cmp := compareUpdateVersions(current, target)
	return updatePlan{
		CurrentVersion:     currentVersion,
		TargetVersion:      target,
		ReleaseURL:         rel.HTMLURL,
		AssetName:          assetName,
		AssetURL:           assetURL,
		ChecksumURL:        checksumURL,
		SignatureBundleURL: findUpdateAssetURL(rel.Assets, "checksums.txt.sigstore.json"),
		UpdateAvailable:    cmp < 0,
		SecurityUpdate:     updateReleaseNotesHaveSecurity(rel.Body),
	}, nil
}

func updateReleaseNotesHaveSecurity(body string) bool {
	inSecurity := false
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "### ") {
			inSecurity = strings.EqualFold(line, "### Security")
			continue
		}
		if inSecurity && strings.HasPrefix(line, "- ") {
			return true
		}
	}
	return false
}

func updateArchiveName(ver string) (string, error) {
	goos, goarch := updatePlatform()
	platform, ok := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "windows"}[goos]
	if !ok {
		return "", fmt.Errorf("unsupported update platform: %s-%s", goos, goarch)
	}
	arch, ok := map[string]string{"amd64": "amd64", "arm64": "arm64"}[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported update platform: %s-%s", goos, goarch)
	}
	if goos == "windows" {
		return fmt.Sprintf("%s-%s-%s-%s.zip", updateBinaryName, normalizeUpdateVersion(ver), platform, arch), nil
	}
	return fmt.Sprintf("%s-%s-%s-%s.tar.gz", updateBinaryName, normalizeUpdateVersion(ver), platform, arch), nil
}

func findUpdateAssetURL(assets []updateReleaseAsset, name string) string {
	for _, asset := range assets {
		if asset.Name == name {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

func updateHTTPGet(ctx context.Context, url string) ([]byte, error) {
	req, err := newUpdateRequest(ctx, url, "application/json")
	if err != nil {
		return nil, err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("reading response: %w", readErr)
	}
	if resp.StatusCode >= 400 {
		return nil, &updateHTTPError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), err: fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, truncateForError(string(data), 200))}
	}
	return data, nil
}

func downloadUpdateFile(ctx context.Context, url, dest string) error {
	req, err := newUpdateRequest(ctx, url, "application/octet-stream")
	if err != nil {
		return err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return &updateHTTPError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), err: fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, truncateForError(string(data), 200))}
	}
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return &updateLocalIOError{err: err}
	}
	buffer := make([]byte, 64*1024)
	for {
		read, readErr := resp.Body.Read(buffer)
		if read > 0 {
			if _, writeErr := f.Write(buffer[:read]); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(tmp)
				return &updateLocalIOError{err: writeErr}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return readErr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return &updateLocalIOError{err: err}
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return &updateLocalIOError{err: err}
	}
	return nil
}

func newUpdateRequest(ctx context.Context, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", updateBinaryName)
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" && updateRequestMayUseGitHubToken(url) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

func updateRequestMayUseGitHubToken(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.github.com" || host == "github.com"
}

func verifyUpdateChecksum(archivePath, checksumPath, assetName string) error {
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		return &updateLocalIOError{err: fmt.Errorf("reading checksums: %w", err)}
	}
	expected := ""
	for _, line := range strings.Split(string(checksumData), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && filepath.Base(fields[len(fields)-1]) == assetName {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum for %s not found", assetName)
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("checksum for %s is not a SHA-256 digest", assetName)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("checksum for %s is not hexadecimal", assetName)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return &updateLocalIOError{err: fmt.Errorf("reading archive: %w", err)}
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return &updateLocalIOError{err: fmt.Errorf("hashing archive: %w", err)}
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return fmt.Errorf("checksum mismatch for %s", assetName)
	}
	return nil
}

func verifyUpdateChecksumSignature(ctx context.Context, checksumPath, bundleURL, tmpDir, targetVersion string) (string, string, error) {
	if strings.TrimSpace(bundleURL) == "" {
		return "missing", "E_INTEGRITY", errors.New("release does not include checksums.txt.sigstore.json; refusing to install an unsigned release")
	}
	bundlePath := filepath.Join(tmpDir, "checksums.txt.sigstore.json")
	if err := updateDownloadHook(ctx, bundleURL, bundlePath); err != nil {
		code := classifyUpdateTransferError(err)
		status := "download_failed"
		if code == "E_NOT_FOUND" {
			code = "E_INTEGRITY"
			status = "missing"
		}
		return status, code, fmt.Errorf("downloading checksum signature bundle: %w", err)
	}
	if err := updateVerifySignature(ctx, checksumPath, bundlePath, updateSignerIdentityRegexp(targetVersion)); err != nil {
		if errors.Is(err, errTrustRootUnavailable) {
			return "trust_root_unavailable", "E_INTEGRITY", err
		}
		return "failed", classifyUpdateVerificationError(err), err
	}
	return "verified", "E_UNKNOWN", nil
}

func extractUpdateArchive(archivePath, assetName, tmpDir string) (string, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractUpdateZip(archivePath, tmpDir)
	}
	if strings.HasSuffix(assetName, ".tar.gz") {
		return extractUpdateTarGz(archivePath, tmpDir)
	}
	return "", fmt.Errorf("unsupported archive type: %s", assetName)
}

func extractUpdateZip(archivePath, tmpDir string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return "", &updateLocalIOError{err: err}
		}
		return "", err
	}
	defer func() { _ = zr.Close() }()
	want := updateArchiveBinaryName()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer func() { _ = rc.Close() }()
		return writeExtractedUpdateBinary(tmpDir, want, rc)
	}
	return "", fmt.Errorf("%s not found in archive", want)
}

func extractUpdateTarGz(archivePath, tmpDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", &updateLocalIOError{err: err}
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	want := updateArchiveBinaryName()
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == want {
			return writeExtractedUpdateBinary(tmpDir, want, tr)
		}
	}
	return "", fmt.Errorf("%s not found in archive", want)
}

func updateArchiveBinaryName() string {
	goos, _ := updatePlatform()
	if goos == "windows" {
		return updateBinaryName + ".exe"
	}
	return updateBinaryName
}

func writeExtractedUpdateBinary(tmpDir, name string, r io.Reader) (string, error) {
	outDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", &updateLocalIOError{err: err}
	}
	outPath := filepath.Join(outDir, name)
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return "", &updateLocalIOError{err: err}
	}
	buffer := make([]byte, 64*1024)
	for {
		read, readErr := r.Read(buffer)
		if read > 0 {
			if _, writeErr := f.Write(buffer[:read]); writeErr != nil {
				_ = f.Close()
				return "", &updateLocalIOError{err: writeErr}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = f.Close()
			return "", readErr
		}
	}
	if err := f.Close(); err != nil {
		return "", &updateLocalIOError{err: err}
	}
	return outPath, nil
}

func applyUpdateBinary(ctx context.Context, src, dst string) (updateApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return updateApplyResult{}, err
	}
	target := dst
	if resolved, err := filepath.EvalSymlinks(dst); err == nil {
		target = resolved
	}
	mode := os.FileMode(0o755)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
		if mode&0o111 == 0 {
			mode |= 0o755
		}
	}
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	newPath := filepath.Join(dir, "."+base+".new")
	backupPath := filepath.Join(dir, "."+base+".old")

	_ = os.Remove(newPath)
	if err := copyFile(ctx, src, newPath, mode); err != nil {
		_ = os.Remove(newPath)
		return updateApplyResult{}, err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(newPath)
		return updateApplyResult{}, err
	}
	if runtime.GOOS != "windows" {
		if err := updateRename(newPath, target); err != nil {
			_ = os.Remove(newPath)
			return updateApplyResult{}, fmt.Errorf("replacing %s: %w", target, err)
		}
		return updateApplyResult{Status: "installed", Path: target}, nil
	}
	return applyWindowsUpdateBinary(ctx, newPath, target, backupPath)
}

func applyWindowsUpdateBinary(ctx context.Context, newPath, target, backupPath string) (updateApplyResult, error) {
	if err := ctx.Err(); err != nil {
		_ = os.Remove(newPath)
		return updateApplyResult{}, err
	}
	if updatePathState(target) != "present" {
		return updateApplyResult{}, fmt.Errorf("installed executable %s is missing; refusing to remove recovery files", target)
	}
	_ = os.Remove(backupPath)
	if err := updateRename(target, backupPath); err != nil {
		_ = os.Remove(newPath)
		return updateApplyResult{}, fmt.Errorf("preparing to replace %s: %w", target, err)
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := updateRename(backupPath, target); rollbackErr != nil {
			return updateApplyResult{
				Status:                   "rollback_failed",
				Path:                     target,
				OriginalRestored:         false,
				InstalledExecutableState: updatePathState(target),
				BackupPath:               backupPath,
				BackupState:              updatePathState(backupPath),
				StagedPath:               newPath,
				StagedState:              updatePathState(newPath),
			}, fmt.Errorf("update cancelled before replacement; restoring original from %s: %w", backupPath, rollbackErr)
		}
		_ = os.Remove(newPath)
		return updateApplyResult{}, err
	}
	if err := updateRename(newPath, target); err != nil {
		replaceErr := err
		if rollbackErr := updateRename(backupPath, target); rollbackErr != nil {
			result := updateApplyResult{
				Status:                   "rollback_failed",
				Path:                     target,
				OriginalRestored:         false,
				InstalledExecutableState: updatePathState(target),
				BackupPath:               backupPath,
				BackupState:              updatePathState(backupPath),
				StagedPath:               newPath,
				StagedState:              updatePathState(newPath),
			}
			return result, fmt.Errorf("replacing %s: %v; restoring original from %s: %w; restore the installed executable before starting the CLI again", target, replaceErr, backupPath, rollbackErr)
		}
		_ = os.Remove(newPath)
		return updateApplyResult{}, fmt.Errorf("replacing %s: %w; original restored", target, replaceErr)
	}
	_ = os.Remove(backupPath)
	return updateApplyResult{Status: "installed", Path: target}, nil
}

func updatePathState(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "present"
	} else if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	return "unknown"
}

func cleanupStaleUpdateFiles(exePath string) error {
	if strings.TrimSpace(exePath) == "" {
		return nil
	}
	target := exePath
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		target = resolved
	}
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	backupPath := filepath.Join(dir, "."+base+".old")
	if updatePathState(target) != "present" {
		if updatePathState(backupPath) == "present" {
			return fmt.Errorf("installed executable is missing while recovery backup remains at %s; restore it before updating", backupPath)
		}
		return fmt.Errorf("installed executable is missing at %s", target)
	}
	for _, path := range []string{filepath.Join(dir, "."+base+".new"), backupPath} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to remove non-file update artifact %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(ctx context.Context, src, dst string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := copyUpdateFileContents(ctx, out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func copyUpdateFileContents(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			if _, err := dst.Write(buffer[:read]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func updateResultMap(plan updatePlan, status string) map[string]any {
	command := "cliproxyapi-cli update --compact"
	if plan.InstallMethod == "npm" {
		command = updateInstallCommand(plan.InstallMethod, plan.TargetVersion)
	}
	return map[string]any{
		"status":               status,
		"asset":                plan.AssetName,
		"current_version":      plan.CurrentVersion,
		"target_version":       plan.TargetVersion,
		"update_available":     plan.UpdateAvailable,
		"release_url":          plan.ReleaseURL,
		"install_method":       plan.InstallMethod,
		"command":              command,
		"signature_available":  plan.SignatureBundleURL != "",
		"checksum_available":   plan.ChecksumURL != "",
		"skill_sync_supported": true,
		"binary_replaced":      false,
		"signature_verified":   false,
		"checksum_verified":    false,
		"signature_status":     "not_checked",
		"skill_sync_command":   plan.SkillSyncCommand,
		"skill_sync_status":    "not_run",
	}
}

func updateSkillSyncCommand() string {
	return "npx skills add " + updateSkillRepo + " -y -g"
}

func runUpdateSkillSync(ctx context.Context, repo string) error {
	command := exec.CommandContext(ctx, "npx", "skills", "add", repo, "-y", "-g")
	return command.Run()
}

func runPackageManagerInstall(ctx context.Context, method, targetVersion string) error {
	if method != "npm" {
		return fmt.Errorf("unsupported package manager: %s", method)
	}
	cmd := exec.CommandContext(ctx, "npm", "install", "-g", updateNPMPackage+"@"+normalizeUpdateVersion(targetVersion), "--include=optional")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return &updatePackageManagerError{err: err, output: truncateForError(string(combined), 1000)}
	}
	return nil
}

func verifyUpdateInstalledVersion(exePath string) (string, error) {
	if strings.TrimSpace(exePath) == "" {
		return "", errors.New("installed executable path is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, exePath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("running installed CLI --version: %w", err)
	}
	line := strings.TrimSpace(string(output))
	const prefix = updateBinaryName + " version "
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("installed CLI returned an unrecognized version line")
	}
	observed := normalizeUpdateVersion(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
	if _, ok := parseUpdateSemver(observed); !ok {
		return "", fmt.Errorf("installed CLI returned an invalid semantic version")
	}
	return observed, nil
}

func detectInstallMethod(exe string) string {
	if exe != "" && pathHasSegment(exe, "node_modules") && npmPackageRoot(exe) != "" {
		return "npm"
	}
	return "binary"
}

func pathHasSegment(path, segment string) bool {
	for _, part := range strings.FieldsFunc(filepath.Clean(path), func(r rune) bool {
		return r == os.PathSeparator || r == '/' || r == '\\'
	}) {
		if part == segment {
			return true
		}
	}
	return false
}

func npmPackageRoot(exe string) string {
	expectedPackage := npmPlatformPackageName()
	if expectedPackage == "" {
		return ""
	}
	for dir := filepath.Dir(exe); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &pkg) == nil && pkg.Name == expectedPackage {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

func npmPlatformPackageName() string {
	npmOS, ok := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "win32"}[runtime.GOOS]
	if !ok {
		return ""
	}
	npmArch, ok := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if !ok {
		return ""
	}
	return updateNPMPackage + "-" + npmOS + "-" + npmArch
}

func updateInstallCommand(method, targetVersion string) string {
	if method == "npm" {
		return "npm install -g " + updateNPMPackage + "@" + normalizeUpdateVersion(targetVersion) + " --include=optional"
	}
	return ""
}

func updateStatus(plan updatePlan) string {
	if plan.UpdateAvailable {
		return "available"
	}
	if compareUpdateVersions(plan.CurrentVersion, plan.TargetVersion) > 0 {
		return "ahead"
	}
	return "up_to_date"
}

func normalizeUpdateVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "refs/tags/")
	return strings.TrimPrefix(v, "v")
}

func compareUpdateVersions(current, target string) int {
	if current == target {
		return 0
	}
	c, cOK := parseUpdateSemver(current)
	t, tOK := parseUpdateSemver(target)
	if !cOK && tOK {
		return -1
	}
	if cOK && !tOK {
		return 1
	}
	if !cOK && !tOK {
		return strings.Compare(current, target)
	}
	return c.Compare(t)
}

func parseUpdateSemver(v string) (semver.Version, bool) {
	parsed, err := semver.Parse(normalizeUpdateVersion(v))
	return parsed, err == nil
}

func truncateForError(s string, n int) string {
	s = strings.TrimSpace(s)
	for _, name := range []string{"GITHUB_TOKEN", "NPM_TOKEN", "NODE_AUTH_TOKEN"} {
		if secret := strings.TrimSpace(os.Getenv(name)); secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
