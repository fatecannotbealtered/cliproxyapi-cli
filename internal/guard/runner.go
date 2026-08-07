package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type Runner struct {
	Backend Backend
	Store   *Store
	Lock    Lock
	Now     func() time.Time
}

func NewRunner(backend Backend, store *Store, lock Lock) *Runner {
	return &Runner{Backend: backend, Store: store, Lock: lock}
}

func (r *Runner) RunOnce(ctx context.Context, apply bool) (result Result) {
	result.Decisions = []Decision{}
	if r == nil || r.Backend == nil || r.Store == nil || r.Lock == nil {
		return fatalResult(result, FatalDependencyMissing, false)
	}

	lease, err := r.Lock.Acquire(ctx)
	if err != nil {
		return fatalResult(result, lockFailureReason(err), errors.Is(err, ErrLockHeld))
	}
	defer func() {
		if err := lease.Release(); err != nil {
			result.OK = false
			result.PartialFailure = false
			result.Fatal = true
			result.FatalError = FatalLockReleaseFailed
		}
	}()

	records, err := r.Store.Records()
	if err != nil {
		return fatalResult(result, FatalStateLoadFailed, false)
	}
	owned := recordsByIdentity(records)
	accounts, err := r.Backend.List(ctx)
	if err != nil {
		return fatalResult(result, FatalListFailed, false)
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if !strings.EqualFold(strings.TrimSpace(account.Provider), "codex") {
			continue
		}
		identity := accountIdentity(account)
		if identity != "" {
			seen[identity] = struct{}{}
		}
		record, hasRecord := owned[identity]
		if !account.DisabledKnown {
			result.Decisions = append(result.Decisions, decisionFor(account, DecisionNone, OutcomeSkipped, ReasonDisabledUnknown))
			continue
		}
		if hasRecord && isPendingDisable(record) {
			result.Decisions = append(result.Decisions, r.handlePending(account, record, owned, apply))
			continue
		}

		// An enabled account previously owned by the guard was manually enabled.
		// Respect that override for this run instead of immediately probing and
		// potentially disabling it again.
		if hasRecord && record.DisabledByTool && !account.Disabled {
			decision := decisionFor(account, DecisionReleaseOwnership, OutcomeSuggested, ReasonManualEnable)
			if apply {
				delete(owned, identity)
				if err := r.Store.save(recordsFromMap(owned)); err != nil {
					owned[identity] = record
					decision.Outcome = OutcomeFailed
					decision.Reason = ReasonStateWriteFailed
				} else {
					decision.Outcome = OutcomeApplied
				}
			}
			result.Decisions = append(result.Decisions, decision)
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
			result.Decisions = append(result.Decisions, r.handleDisabled(ctx, account, record, hasRecord, owned, now, apply))
			continue
		}
		result.Decisions = append(result.Decisions, r.handleEnabled(ctx, account, owned, now, apply))
	}

	for _, record := range recordsFromMap(owned) {
		if (!record.DisabledByTool && !isPendingDisable(record)) || !strings.EqualFold(strings.TrimSpace(record.Provider), "codex") {
			continue
		}
		if _, exists := seen[record.Identity]; exists {
			continue
		}
		result.Decisions = append(result.Decisions, Decision{
			Identity:  record.Identity,
			Name:      record.Name,
			AuthIndex: record.AuthIndex,
			Provider:  record.Provider,
			Decision:  DecisionStale,
			Outcome:   OutcomeStale,
			Reason:    ReasonAccountMissing,
		})
	}
	finishResult(&result)
	return result
}

func (r *Runner) handleEnabled(ctx context.Context, account Account, owned map[string]Record, now time.Time, apply bool) Decision {
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
	if assessment.ResetAt == nil {
		return assessmentDecision(account, assessment, DecisionNone, OutcomeSkipped, ReasonResetMissing)
	}
	decision := assessmentDecision(account, assessment, DecisionDisable, OutcomeSuggested, ReasonConfirmedExhausted)
	if !apply {
		return decision
	}
	pending := Record{
		Identity:       accountIdentity(account),
		Name:           account.Name,
		AuthIndex:      account.AuthIndex,
		Provider:       account.Provider,
		Fingerprint:    fingerprintAccount(account),
		DisabledByTool: false,
		DisabledAt:     nil,
		ResetAt:        cloneTime(assessment.ResetAt),
		LastProbe:      cloneTime(&now),
		LastState:      StatePendingDisable,
	}
	identity := pending.Identity
	previous, existed := owned[identity]
	owned[identity] = pending
	if err := r.Store.save(recordsFromMap(owned)); err != nil {
		if existed {
			owned[identity] = previous
		} else {
			delete(owned, identity)
		}
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonStateWriteFailed
		return decision
	}
	if err := r.Backend.SetDisabled(ctx, account, true); err != nil {
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonWriteFailed
		return decision
	}
	verified, verifyErr := r.verifyDisabled(ctx, account, true)
	if verifyErr != nil {
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonVerificationFailed
		return decision
	}
	if !verified {
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonVerificationFailed
		return decision
	}
	pending.DisabledByTool = true
	pending.DisabledAt = cloneTime(&now)
	pending.LastState = StateConfirmedExhausted
	owned[identity] = pending
	if err := r.Store.save(recordsFromMap(owned)); err != nil {
		owned[identity] = Record{
			Identity:       pending.Identity,
			Name:           pending.Name,
			AuthIndex:      pending.AuthIndex,
			Provider:       pending.Provider,
			Fingerprint:    pending.Fingerprint,
			DisabledByTool: false,
			ResetAt:        cloneTime(pending.ResetAt),
			LastProbe:      cloneTime(pending.LastProbe),
			LastState:      StatePendingDisable,
		}
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonStateWriteFailed
		return decision
	}
	decision.Outcome = OutcomeApplied
	return decision
}

func (r *Runner) handleDisabled(ctx context.Context, account Account, record Record, hasRecord bool, owned map[string]Record, now time.Time, apply bool) Decision {
	if !hasRecord || !record.DisabledByTool {
		return decisionFor(account, DecisionNone, OutcomeSkipped, ReasonNotOwned)
	}
	if record.Fingerprint != fingerprintAccount(account) {
		return decisionFor(account, DecisionNone, OutcomeSkipped, ReasonFingerprintChanged)
	}
	if record.ResetAt == nil {
		return decisionFor(account, DecisionNone, OutcomeSkipped, ReasonResetMissing)
	}
	if now.Before(*record.ResetAt) {
		decision := decisionFor(account, DecisionNone, OutcomeSkipped, ReasonResetNotReached)
		decision.ResetAt = record.ResetAt.UTC().Format(time.RFC3339)
		return decision
	}
	assessment, err := r.Backend.ProbeCodex(ctx, account)
	if err != nil {
		return decisionFor(account, DecisionNone, OutcomeFailed, ReasonProbeFailed)
	}
	if assessment.State != StateHealthy {
		reason := ReasonNotHealthy
		if assessment.State == StateUnknown || assessment.State == "" {
			reason = ReasonAssessmentUnknown
		}
		decision := assessmentDecision(account, assessment, DecisionNone, OutcomeSkipped, reason)
		if apply {
			updated := record
			updated.LastProbe = cloneTime(&now)
			updated.LastState = assessment.State
			if assessment.ResetAt != nil {
				updated.ResetAt = cloneTime(assessment.ResetAt)
			}
			identity := accountIdentity(account)
			owned[identity] = updated
			if err := r.Store.save(recordsFromMap(owned)); err != nil {
				owned[identity] = record
				decision.Outcome = OutcomeFailed
				decision.Reason = ReasonStateWriteFailed
			}
		}
		return decision
	}
	decision := assessmentDecision(account, assessment, DecisionEnable, OutcomeSuggested, ReasonHealthy)
	if !apply {
		return decision
	}
	if err := r.Backend.SetDisabled(ctx, account, false); err != nil {
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonWriteFailed
		return decision
	}
	verified, verifyErr := r.verifyDisabled(ctx, account, false)
	if verifyErr != nil || !verified {
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonVerificationFailed
		return decision
	}
	identity := accountIdentity(account)
	delete(owned, identity)
	if err := r.Store.save(recordsFromMap(owned)); err != nil {
		owned[identity] = record
		decision.Outcome = OutcomeFailed
		decision.Reason = ReasonStateWriteFailed
		return decision
	}
	decision.Outcome = OutcomeApplied
	return decision
}

func (r *Runner) verifyDisabled(ctx context.Context, account Account, disabled bool) (bool, error) {
	accounts, err := r.Backend.List(ctx)
	if err != nil {
		return false, err
	}
	wantIdentity := accountIdentity(account)
	wantFingerprint := fingerprintAccount(account)
	for _, current := range accounts {
		if accountIdentity(current) == wantIdentity && fingerprintAccount(current) == wantFingerprint {
			return current.DisabledKnown && current.Disabled == disabled, nil
		}
	}
	return false, nil
}

func accountIdentity(account Account) string {
	if authIndex := strings.TrimSpace(account.AuthIndex); authIndex != "" {
		return strings.ToLower(strings.TrimSpace(account.Provider)) + ":" + authIndex
	}
	return ""
}

func fingerprintAccount(account Account) string {
	stable := struct {
		AccountID string `json:"account_id"`
		AuthIndex string `json:"auth_index"`
		Provider  string `json:"provider"`
	}{
		AccountID: strings.TrimSpace(account.ChatGPTAccountID),
		AuthIndex: strings.TrimSpace(account.AuthIndex),
		Provider:  strings.ToLower(strings.TrimSpace(account.Provider)),
	}
	raw, _ := json.Marshal(stable)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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

func isPendingDisable(record Record) bool {
	return !record.DisabledByTool && record.LastState == StatePendingDisable
}

func (r *Runner) handlePending(account Account, record Record, owned map[string]Record, apply bool) Decision {
	if !account.Disabled {
		decision := decisionFor(account, DecisionReleaseOwnership, OutcomeSuggested, ReasonPendingNotApplied)
		if apply {
			delete(owned, record.Identity)
			if err := r.Store.save(recordsFromMap(owned)); err != nil {
				owned[record.Identity] = record
				decision.Outcome = OutcomeFailed
				decision.Reason = ReasonStateWriteFailed
			} else {
				decision.Outcome = OutcomeApplied
			}
		}
		return decision
	}
	if record.Fingerprint != fingerprintAccount(account) {
		return decisionFor(account, DecisionNone, OutcomeSkipped, ReasonFingerprintChanged)
	}
	// A disabled state alone cannot prove that the interrupted or failed write
	// was committed by this guard; another actor may have disabled the account.
	// Keep the write-ahead record for audit, but never promote it to ownership.
	return decisionFor(account, DecisionNone, OutcomeSkipped, ReasonPendingAmbiguous)
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

func recordsByIdentity(records []Record) map[string]Record {
	result := make(map[string]Record, len(records))
	for _, record := range records {
		result[record.Identity] = record
	}
	return result
}

func recordsFromMap(records map[string]Record) []Record {
	result := make([]Record, 0, len(records))
	for _, record := range records {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Identity < result[j].Identity
	})
	return result
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

func fatalResult(result Result, reason string, locked bool) Result {
	result.OK = false
	result.Fatal = true
	result.Locked = locked
	result.FatalError = reason
	result.PartialFailure = false
	return result
}

func lockFailureReason(err error) string {
	if errors.Is(err, ErrLockHeld) {
		return FatalLockHeld
	}
	return FatalLockFailed
}
