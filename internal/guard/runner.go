package guard

import (
	"context"
	"strings"
	"time"
)

type Runner struct {
	Backend Backend
}

func NewRunner(backend Backend) *Runner {
	return &Runner{Backend: backend}
}

// RunOnce evaluates every Codex account once and reports what it would suggest.
// It never changes account state: acting on a suggestion is a separate,
// explicitly authorized `auth-file set-status` call.
func (r *Runner) RunOnce(ctx context.Context) (result Result) {
	result.Decisions = []Decision{}
	if r == nil || r.Backend == nil {
		return fatalResult(result, FatalDependencyMissing)
	}

	accounts, err := r.Backend.List(ctx)
	if err != nil {
		return fatalResult(result, FatalListFailed)
	}

	for _, account := range accounts {
		if !strings.EqualFold(strings.TrimSpace(account.Provider), "codex") {
			continue
		}
		if !account.DisabledKnown {
			result.Decisions = append(result.Decisions, decisionFor(account, DecisionNone, OutcomeSkipped, ReasonDisabledUnknown))
			continue
		}
		if account.RuntimeOnly {
			result.Decisions = append(result.Decisions, decisionFor(account, DecisionNone, OutcomeSkipped, ReasonRuntimeOnly))
			continue
		}
		if strings.TrimSpace(account.AuthIndex) == "" {
			result.Decisions = append(result.Decisions, decisionFor(account, DecisionNone, OutcomeSkipped, ReasonUnstableAuthIndex))
			continue
		}
		if strings.TrimSpace(account.ChatGPTAccountID) == "" {
			result.Decisions = append(result.Decisions, decisionFor(account, DecisionNone, OutcomeSkipped, ReasonMissingAccountID))
			continue
		}
		if account.Disabled {
			result.Decisions = append(result.Decisions, decisionFor(account, DecisionNone, OutcomeSkipped, ReasonAlreadyDisabled))
			continue
		}
		result.Decisions = append(result.Decisions, r.handleEnabled(ctx, account))
	}

	finishResult(&result)
	return result
}

func (r *Runner) handleEnabled(ctx context.Context, account Account) Decision {
	assessment, err := r.Backend.ProbeCodex(ctx, account)
	if err != nil {
		return decisionFor(account, DecisionNone, OutcomeFailed, ReasonProbeFailed)
	}
	if assessment.State != StateConfirmedExhausted {
		reason := ReasonNotExhausted
		if assessment.State == StateUnknown || assessment.State == "" {
			reason = ReasonAssessmentUnknown
		}
		return assessmentDecision(account, assessment, DecisionNone, OutcomeSkipped, reason)
	}
	// Without a provider-declared reset time there is no recovery plan, so the
	// exhaustion is reported but never promoted to a suggestion.
	if assessment.ResetAt == nil {
		return assessmentDecision(account, assessment, DecisionNone, OutcomeSkipped, ReasonResetMissing)
	}
	return assessmentDecision(account, assessment, DecisionDisable, OutcomeSuggested, ReasonConfirmedExhausted)
}

func accountIdentity(account Account) string {
	if authIndex := strings.TrimSpace(account.AuthIndex); authIndex != "" {
		return strings.ToLower(strings.TrimSpace(account.Provider)) + ":" + authIndex
	}
	return ""
}

func decisionFor(account Account, action, outcome, reason string) Decision {
	identity := accountIdentity(account)
	if identity == "" {
		identity = strings.TrimSpace(account.ID)
	}
	return Decision{
		Identity:  identity,
		Name:      account.Name,
		AuthIndex: account.AuthIndex,
		Provider:  account.Provider,
		Decision:  action,
		Outcome:   outcome,
		Reason:    reason,
	}
}

func assessmentDecision(account Account, assessment Assessment, action, outcome, reason string) Decision {
	decision := decisionFor(account, action, outcome, reason)
	decision.State = assessment.State
	decision.UsedPercent = assessment.UsedPercent
	if assessment.ResetAt != nil {
		decision.ResetAt = assessment.ResetAt.UTC().Format(time.RFC3339)
	}
	return decision
}

func finishResult(result *Result) {
	result.Summary = Summary{Total: len(result.Decisions)}
	for _, decision := range result.Decisions {
		switch decision.Outcome {
		case OutcomeSuggested:
			result.Summary.Suggested++
		case OutcomeApplied:
			result.Summary.Applied++
		case OutcomeSkipped:
			result.Summary.Skipped++
		case OutcomeFailed:
			result.Summary.Failed++
		case OutcomeStale:
			result.Summary.Stale++
		}
	}
	result.OK = !result.Fatal && result.Summary.Failed == 0
	result.PartialFailure = !result.Fatal && result.Summary.Failed > 0
}

func fatalResult(result Result, reason string) Result {
	result.OK = false
	result.Fatal = true
	result.FatalError = reason
	result.PartialFailure = false
	return result
}
