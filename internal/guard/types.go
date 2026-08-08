package guard

import (
	"context"
	"time"
)

const (
	StateConfirmedExhausted = "confirmed_exhausted"
	StateHealthy            = "healthy"
	StatePendingDisable     = "pending_disable"
	StateUnknown            = "unknown"

	DecisionNone             = "none"
	DecisionDisable          = "disable"
	DecisionEnable           = "enable"
	DecisionReleaseOwnership = "release_ownership"
	DecisionStale            = "stale"

	OutcomeSkipped   = "skipped"
	OutcomeSuggested = "suggested"
	OutcomeApplied   = "applied"
	OutcomeFailed    = "failed"
	OutcomeStale     = "stale"

	ReasonConfirmedExhausted = "confirmed_exhausted"
	ReasonAssessmentUnknown  = "assessment_unknown"
	ReasonNotExhausted       = "not_confirmed_exhausted"
	ReasonAlreadyDisabled    = "already_disabled"
	ReasonProbeFailed        = "probe_failed"
	ReasonResetNotReached    = "reset_not_reached"
	ReasonResetMissing       = "reset_at_missing"
	ReasonNotOwned           = "not_owned_by_tool"
	ReasonFingerprintChanged = "fingerprint_changed"
	ReasonNotHealthy         = "not_healthy"
	ReasonHealthy            = "healthy"
	ReasonWriteFailed        = "write_failed"
	ReasonVerificationFailed = "write_verification_failed"
	ReasonStateWriteFailed   = "state_write_failed"
	ReasonManualEnable       = "manually_enabled"
	ReasonPendingNotApplied  = "pending_disable_not_applied"
	ReasonPendingAmbiguous   = "pending_disable_ownership_unproven"
	ReasonAccountMissing     = "account_missing"
	ReasonUnstableAuthIndex  = "unstable_auth_index"
	ReasonRuntimeOnly        = "runtime_only"
	ReasonMissingAccountID   = "missing_chatgpt_account_id"
	ReasonDisabledUnknown    = "disabled_status_unknown"

	FatalDependencyMissing = "dependency_missing"
	FatalLockHeld          = "lock_held"
	FatalLockFailed        = "lock_failed"
	FatalLockReleaseFailed = "lock_release_failed"
	FatalStateLoadFailed   = "state_load_failed"
	FatalListFailed        = "list_failed"
)

// Account is the guard's intentionally small view of an upstream account.
// Adapters in other packages translate their API models into this type.
type Account struct {
	ID               string `json:"id"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	AuthIndex        string `json:"auth_index"`
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	Disabled         bool   `json:"disabled"`
	DisabledKnown    bool   `json:"disabled_known"`
	Unavailable      bool   `json:"unavailable"`
	RuntimeOnly      bool   `json:"runtime_only"`
	UpdatedAt        string `json:"updated_at"`
}

type Assessment struct {
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	ResetAt     *time.Time `json:"reset_at"`
	UsedPercent *float64   `json:"used_percent"`
}

type Backend interface {
	List(context.Context) ([]Account, error)
	ProbeCodex(context.Context, Account) (Assessment, error)
	SetDisabled(context.Context, Account, bool) error
}

type Record struct {
	Identity       string     `json:"identity"`
	Name           string     `json:"name"`
	AuthIndex      string     `json:"auth_index"`
	Provider       string     `json:"provider"`
	Fingerprint    string     `json:"fingerprint"`
	DisabledByTool bool       `json:"disabled_by_tool"`
	DisabledAt     *time.Time `json:"disabled_at"`
	ResetAt        *time.Time `json:"reset_at"`
	LastProbe      *time.Time `json:"last_probe"`
	LastState      string     `json:"last_state"`
}

type Decision struct {
	Identity    string   `json:"identity"`
	Name        string   `json:"name,omitempty"`
	AuthIndex   string   `json:"auth_index,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	State       string   `json:"state,omitempty"`
	Decision    string   `json:"decision"`
	Outcome     string   `json:"outcome"`
	Reason      string   `json:"reason"`
	ResetAt     string   `json:"reset_at,omitempty"`
	UsedPercent *float64 `json:"used_percent,omitempty"`
}

type Summary struct {
	Total     int `json:"total"`
	Suggested int `json:"suggested"`
	Applied   int `json:"applied"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Stale     int `json:"stale"`
}

type Result struct {
	OK             bool       `json:"ok"`
	PartialFailure bool       `json:"partial_failure"`
	Fatal          bool       `json:"fatal"`
	Locked         bool       `json:"locked"`
	FatalError     string     `json:"fatal_error,omitempty"`
	Decisions      []Decision `json:"decisions"`
	Summary        Summary    `json:"summary"`
}

func (r Result) HasFailures() bool {
	return !r.OK
}

func (r Result) IsFatal() bool {
	return r.Fatal
}
