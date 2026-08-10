package guard

import (
	"context"
	"time"
)

const (
	StateConfirmedExhausted = "confirmed_exhausted"
	StateUnknown            = "unknown"

	DecisionNone    = "none"
	DecisionDisable = "disable"

	OutcomeSkipped   = "skipped"
	OutcomeSuggested = "suggested"
	OutcomeFailed    = "failed"
	// The guard only observes, so nothing can emit these two. They stay declared
	// (and counted in Summary) as reserved values rather than disappearing from
	// the published result shape.
	OutcomeApplied = "applied"
	OutcomeStale   = "stale"

	ReasonConfirmedExhausted = "confirmed_exhausted"
	ReasonAssessmentUnknown  = "assessment_unknown"
	ReasonNotExhausted       = "not_confirmed_exhausted"
	ReasonAlreadyDisabled    = "already_disabled"
	ReasonProbeFailed        = "probe_failed"
	ReasonResetMissing       = "reset_at_missing"
	ReasonUnstableAuthIndex  = "unstable_auth_index"
	ReasonRuntimeOnly        = "runtime_only"
	ReasonMissingAccountID   = "missing_chatgpt_account_id"
	ReasonDisabledUnknown    = "disabled_status_unknown"

	FatalDependencyMissing = "dependency_missing"
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

// Summary counts decisions by outcome. `Applied` and `Stale` are reserved and
// always zero — see the outcome constants above.
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
