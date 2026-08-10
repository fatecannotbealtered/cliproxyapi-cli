package guard

import (
	"context"
	"errors"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)

func TestRunOnceNeverWritesAndNeedsNoState(t *testing.T) {
	account := testAccount("observation")
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: "healthy"}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || len(result.Decisions) != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Summary.Applied != 0 || result.Summary.Stale != 0 {
		t.Fatalf("summary=%#v, applied/stale must stay reserved", result.Summary)
	}
}

func TestRunOnceConfirmedExhaustionOnlySuggests(t *testing.T) {
	account := testAccount("one")
	resetAt := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || result.Summary.Suggested != 1 || result.Summary.Applied != 0 {
		t.Fatalf("result = %#v, want one suggestion and no application", result)
	}
	if !hasDecision(result, accountIdentity(account), DecisionDisable, OutcomeSuggested) {
		t.Fatalf("decisions = %#v, want a disable suggestion", result.Decisions)
	}
}

func TestRunOnceDoesNotSuggestWithoutResetPlan(t *testing.T) {
	account := testAccount("no-reset")
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || result.Summary.Suggested != 0 {
		t.Fatalf("result = %#v, want a conservative skip", result)
	}
	if !hasReason(result, accountIdentity(account), ReasonResetMissing) {
		t.Fatalf("decisions = %#v, want missing reset reason", result.Decisions)
	}
}

func TestRunOnceUnknownAndProbeErrorNeverSuggest(t *testing.T) {
	unknown := testAccount("unknown")
	failed := testAccount("failed")
	backend := newFakeBackend(unknown, failed)
	backend.assessments[unknown.ID] = Assessment{State: StateUnknown, Reason: "ordinary rate limit"}
	backend.probeErrors[failed.ID] = errors.New("probe failed")
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if result.OK || !result.PartialFailure || result.Fatal || result.Summary.Failed != 1 {
		t.Fatalf("result = %#v, want a stable item-level failure signal", result)
	}
	if !hasDecision(result, accountIdentity(unknown), DecisionNone, OutcomeSkipped) {
		t.Fatalf("decisions = %#v, want unknown skipped", result.Decisions)
	}
	if !hasDecision(result, accountIdentity(failed), DecisionNone, OutcomeFailed) {
		t.Fatalf("decisions = %#v, want probe failure", result.Decisions)
	}
}

func TestRunOnceUnknownDisabledStatusIsNotProbed(t *testing.T) {
	account := testAccount("unknown-status")
	account.DisabledKnown = false
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: ptrTime(testNow.Add(time.Hour))}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || len(backend.probes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, want no probe", result, backend.probes)
	}
	if !hasReason(result, accountIdentity(account), ReasonDisabledUnknown) {
		t.Fatalf("decisions = %#v, want unknown disabled status", result.Decisions)
	}
}

func TestRunOnceSkipsAlreadyDisabledAccount(t *testing.T) {
	account := testAccount("already-disabled")
	account.Disabled = true
	backend := newFakeBackend(account)
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || len(backend.probes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, want no probe", result, backend.probes)
	}
	if !hasReason(result, accountIdentity(account), ReasonAlreadyDisabled) {
		t.Fatalf("decisions = %#v, want already-disabled skip", result.Decisions)
	}
}

func TestRunOnceRequiresStableAuthIndex(t *testing.T) {
	account := testAccount("unstable")
	account.AuthIndex = ""
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || len(backend.probes) != 0 {
		t.Fatalf("result = %#v, probes = %#v", result, backend.probes)
	}
	if !hasReason(result, account.ID, ReasonUnstableAuthIndex) {
		t.Fatalf("decisions = %#v, want unstable auth index", result.Decisions)
	}
}

func TestRunOnceRequiresChatGPTAccountIDForProbe(t *testing.T) {
	account := testAccount("missing-account-id")
	account.ChatGPTAccountID = ""
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: ptrTime(testNow.Add(time.Hour))}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || len(backend.probes) != 0 {
		t.Fatalf("result = %#v, probes = %#v", result, backend.probes)
	}
	if !hasReason(result, accountIdentity(account), ReasonMissingAccountID) {
		t.Fatalf("decisions = %#v, want missing ChatGPT account id", result.Decisions)
	}
}

func TestRunOnceIgnoresNonCodexAndSkipsRuntimeOnly(t *testing.T) {
	nonCodex := testAccount("other")
	nonCodex.Provider = "anthropic"
	runtimeOnly := testAccount("runtime")
	runtimeOnly.RuntimeOnly = true
	backend := newFakeBackend(nonCodex, runtimeOnly)
	backend.assessments[nonCodex.ID] = Assessment{State: StateConfirmedExhausted}
	backend.assessments[runtimeOnly.ID] = Assessment{State: StateConfirmedExhausted}
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if !result.OK || len(backend.probes) != 0 {
		t.Fatalf("result = %#v, probes = %#v", result, backend.probes)
	}
	if result.Summary.Total != 1 || !hasReason(result, accountIdentity(runtimeOnly), ReasonRuntimeOnly) {
		t.Fatalf("decisions = %#v, want only runtime-only Codex decision", result.Decisions)
	}
}

func TestRunOnceListFailureIsFatal(t *testing.T) {
	backend := newFakeBackend(testAccount("any"))
	backend.listErrors[1] = errors.New("list failed")
	runner := NewRunner(backend)

	result := runner.RunOnce(context.Background())

	if result.OK || !result.Fatal || result.PartialFailure || result.FatalError != FatalListFailed {
		t.Fatalf("result = %#v, want fatal list failure", result)
	}
}

func TestRunOnceWithoutBackendIsFatal(t *testing.T) {
	result := NewRunner(nil).RunOnce(context.Background())

	if result.OK || !result.Fatal || result.FatalError != FatalDependencyMissing {
		t.Fatalf("result = %#v, want fatal dependency failure", result)
	}
}

func testAccount(id string) Account {
	return Account{
		ID:               id,
		ChatGPTAccountID: "chatgpt-" + id,
		AuthIndex:        "index-" + id,
		Name:             id + ".json",
		Provider:         "codex",
		DisabledKnown:    true,
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func hasDecision(result Result, identity, decision, outcome string) bool {
	for _, item := range result.Decisions {
		if item.Identity == identity && item.Decision == decision && item.Outcome == outcome {
			return true
		}
	}
	return false
}

func hasReason(result Result, identity, reason string) bool {
	for _, item := range result.Decisions {
		if item.Identity == identity && item.Reason == reason {
			return true
		}
	}
	return false
}

type fakeBackend struct {
	accounts    []Account
	assessments map[string]Assessment
	probeErrors map[string]error
	listErrors  map[int]error
	listCalls   int
	probes      []string
}

func newFakeBackend(accounts ...Account) *fakeBackend {
	return &fakeBackend{
		accounts:    append([]Account(nil), accounts...),
		assessments: make(map[string]Assessment),
		probeErrors: make(map[string]error),
		listErrors:  make(map[int]error),
	}
}

func (f *fakeBackend) List(context.Context) ([]Account, error) {
	f.listCalls++
	if err := f.listErrors[f.listCalls]; err != nil {
		return nil, err
	}
	return append([]Account(nil), f.accounts...), nil
}

func (f *fakeBackend) ProbeCodex(_ context.Context, account Account) (Assessment, error) {
	f.probes = append(f.probes, account.ID)
	if err := f.probeErrors[account.ID]; err != nil {
		return Assessment{}, err
	}
	return f.assessments[account.ID], nil
}
