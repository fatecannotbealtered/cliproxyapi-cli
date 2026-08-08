package guard

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)

func TestRunOnceObservationDoesNotRequireWriteState(t *testing.T) {
	account := testAccount("observation")
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateHealthy}
	runner := NewRunner(backend, nil, nil)

	result := runner.RunOnce(context.Background(), false)

	if !result.OK || len(result.Decisions) != 1 || len(backend.writes) != 0 {
		t.Fatalf("result=%#v writes=%#v", result, backend.writes)
	}
}

func TestRunOnceConfirmedExhaustionSuggestsOrDisables(t *testing.T) {
	account := testAccount("one")
	resetAt := testNow.Add(time.Hour)

	t.Run("dry run suggests without writing", func(t *testing.T) {
		backend := newFakeBackend(account)
		backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
		runner, store := testRunner(t, backend)

		result := runner.RunOnce(context.Background(), false)

		if !result.OK || result.Summary.Suggested != 1 {
			t.Fatalf("result = %#v, want one successful suggestion", result)
		}
		if len(backend.writes) != 0 {
			t.Fatalf("writes = %#v, want none", backend.writes)
		}
		if records, err := store.Records(); err != nil || len(records) != 0 {
			t.Fatalf("records = %#v, err = %v, want empty", records, err)
		}
	})

	t.Run("apply disables then records ownership", func(t *testing.T) {
		backend := newFakeBackend(account)
		backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
		runner, store := testRunner(t, backend)

		result := runner.RunOnce(context.Background(), true)

		if !result.OK || result.Summary.Applied != 1 {
			t.Fatalf("result = %#v, want one applied decision", result)
		}
		if len(backend.writes) != 1 || !backend.writes[0].disabled {
			t.Fatalf("writes = %#v, want one disable", backend.writes)
		}
		records, err := store.Records()
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != 1 || !records[0].DisabledByTool {
			t.Fatalf("records = %#v, want owned disable", records)
		}
		if records[0].ResetAt == nil || !records[0].ResetAt.Equal(resetAt) {
			t.Fatalf("reset_at = %v, want %v", records[0].ResetAt, resetAt)
		}
		if records[0].Fingerprint != fingerprintAccount(account) {
			t.Fatalf("fingerprint = %q, want current account fingerprint", records[0].Fingerprint)
		}
	})
}

func TestRunOnceDoesNotDisableWithoutResetPlan(t *testing.T) {
	account := testAccount("no-reset")
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted}
	runner, store := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, writes = %#v, want conservative skip", result, backend.writes)
	}
	if !hasReason(result, accountIdentity(account), ReasonResetMissing) {
		t.Fatalf("decisions = %#v, want missing reset reason", result.Decisions)
	}
	if records, err := store.Records(); err != nil || len(records) != 0 {
		t.Fatalf("records = %#v, err = %v, want no pending ownership", records, err)
	}
}

func TestRunOnceUnknownAndProbeErrorNeverDisable(t *testing.T) {
	unknown := testAccount("unknown")
	failed := testAccount("failed")
	backend := newFakeBackend(unknown, failed)
	backend.assessments[unknown.ID] = Assessment{State: StateUnknown, Reason: "ordinary rate limit"}
	backend.probeErrors[failed.ID] = errors.New("probe failed")
	runner, _ := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if len(backend.writes) != 0 {
		t.Fatalf("writes = %#v, want none", backend.writes)
	}
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

func TestRunOnceUnknownDisabledStatusNeverChangesUpstreamOrOwnership(t *testing.T) {
	t.Run("unowned account is not probed or disabled", func(t *testing.T) {
		account := testAccount("unknown-disabled")
		account.DisabledKnown = false
		backend := newFakeBackend(account)
		backend.assessments[account.ID] = Assessment{
			State:   StateConfirmedExhausted,
			ResetAt: ptrTime(testNow.Add(time.Hour)),
		}
		runner, store := testRunner(t, backend)

		result := runner.RunOnce(context.Background(), true)

		if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
			t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
		}
		if !hasReason(result, accountIdentity(account), ReasonDisabledUnknown) {
			t.Fatalf("decisions = %#v, want unknown disabled status", result.Decisions)
		}
		if records, err := store.Records(); err != nil || len(records) != 0 {
			t.Fatalf("records = %#v, err = %v, want empty", records, err)
		}
	})

	t.Run("owned account keeps ownership", func(t *testing.T) {
		account := testAccount("owned-unknown-disabled")
		backend := newFakeBackend(account)
		runner, store := testRunner(t, backend)
		seedOwnership(t, store, account, testNow.Add(-time.Minute))
		account.DisabledKnown = false
		backend.accounts[0] = account

		result := runner.RunOnce(context.Background(), true)

		if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
			t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
		}
		if !hasReason(result, accountIdentity(account), ReasonDisabledUnknown) {
			t.Fatalf("decisions = %#v, want unknown disabled status", result.Decisions)
		}
		if records, err := store.Records(); err != nil || len(records) != 1 || !records[0].DisabledByTool {
			t.Fatalf("records = %#v, err = %v, want ownership preserved", records, err)
		}
	})
}

func TestRunOnceWaitsForResetBeforeProbe(t *testing.T) {
	account := testAccount("waiting")
	account.Disabled = true
	resetAt := testNow.Add(time.Minute)
	backend := newFakeBackend(account)
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, account, resetAt)

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
	}
	if !hasDecision(result, accountIdentity(account), DecisionNone, OutcomeSkipped) {
		t.Fatalf("decisions = %#v, want reset wait", result.Decisions)
	}
}

func TestRunOnceRestoresOnlyOwnedDisabledAccount(t *testing.T) {
	owned := testAccount("owned")
	owned.Disabled = true
	unowned := testAccount("unowned")
	unowned.Disabled = true
	backend := newFakeBackend(owned, unowned)
	backend.assessments[owned.ID] = Assessment{State: StateHealthy}
	backend.assessments[unowned.ID] = Assessment{State: StateHealthy}
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, owned, testNow.Add(-time.Minute))

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.probes) != 1 || backend.probes[0] != owned.ID {
		t.Fatalf("result = %#v, probes = %#v, want owned only", result, backend.probes)
	}
	if len(backend.writes) != 1 || backend.writes[0].id != owned.ID || backend.writes[0].disabled {
		t.Fatalf("writes = %#v, want one enable for owned account", backend.writes)
	}
	if records, err := store.Records(); err != nil || len(records) != 0 {
		t.Fatalf("records = %#v, err = %v, want ownership released", records, err)
	}
}

func TestRunOnceFingerprintChangeDoesNotRestore(t *testing.T) {
	account := testAccount("changed")
	account.Disabled = true
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateHealthy}
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, account, testNow.Add(-time.Minute))
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	records[0].Fingerprint = "different-stable-identity"
	if err := store.save(records); err != nil {
		t.Fatal(err)
	}

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
	}
	records, err = store.Records()
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v, err = %v, want ownership preserved", records, err)
	}
	if !hasReason(result, accountIdentity(account), ReasonFingerprintChanged) {
		t.Fatalf("decisions = %#v, want fingerprint change", result.Decisions)
	}
}

func TestRunOnceStableAuthIndexSurvivesIDAndNameChanges(t *testing.T) {
	original := testAccount("stable")
	original.Disabled = true
	current := original
	current.ID = "new-runtime-id"
	current.Name = "renamed-file.json"
	backend := newFakeBackend(current)
	backend.assessments[current.ID] = Assessment{State: StateHealthy}
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, original, testNow.Add(-time.Minute))

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.writes) != 1 || backend.writes[0].disabled {
		t.Fatalf("result = %#v, writes = %#v, want restore by auth_index", result, backend.writes)
	}
	if records, err := store.Records(); err != nil || len(records) != 0 {
		t.Fatalf("records = %#v, err = %v, want ownership released", records, err)
	}
}

func TestRunOnceVerificationFailureNeverClaimsPendingOwnership(t *testing.T) {
	account := testAccount("pending-verify")
	resetAt := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
	backend.listErrors[2] = errors.New("verification unavailable")
	runner, store := testRunner(t, backend)

	first := runner.RunOnce(context.Background(), true)

	if first.OK || !first.PartialFailure || len(backend.writes) != 1 || !backend.accounts[0].Disabled {
		t.Fatalf("first result = %#v, writes = %#v, account = %#v", first, backend.writes, backend.accounts[0])
	}
	records, err := store.Records()
	if err != nil || len(records) != 1 || records[0].DisabledByTool || records[0].LastState != StatePendingDisable {
		t.Fatalf("pending records = %#v, err = %v", records, err)
	}

	second := runner.RunOnce(context.Background(), true)

	if !second.OK || second.Summary.Skipped != 1 || len(backend.writes) != 1 || !hasReason(second, accountIdentity(account), ReasonPendingAmbiguous) {
		t.Fatalf("second result = %#v, writes = %#v, want ambiguous pending state preserved", second, backend.writes)
	}
	records, err = store.Records()
	if err != nil || len(records) != 1 || records[0].DisabledByTool || records[0].LastState != StatePendingDisable {
		t.Fatalf("pending records = %#v, err = %v, want no ownership", records, err)
	}
}

func TestRunOnceWriteAheadCleansCrashBeforeExternalWrite(t *testing.T) {
	account := testAccount("pending-before-write")
	resetAt := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	runner, store := testRunner(t, backend)
	seedPending(t, store, account, resetAt)

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || result.Summary.Applied != 1 || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
	}
	if records, err := store.Records(); err != nil || len(records) != 0 {
		t.Fatalf("records = %#v, err = %v, want abandoned pending removed", records, err)
	}
}

func TestRunOnceAmbiguousWriteErrorNeverClaimsOwnership(t *testing.T) {
	account := testAccount("ambiguous-write")
	resetAt := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
	backend.setErrorsAfterWrite[account.ID] = errors.New("timeout after upstream commit")
	runner, store := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if result.OK || !result.PartialFailure || result.Summary.Failed != 1 || !backend.accounts[0].Disabled {
		t.Fatalf("result = %#v, account = %#v, want ambiguous failed write", result, backend.accounts[0])
	}
	if records, err := store.Records(); err != nil || len(records) != 1 || records[0].DisabledByTool || records[0].LastState != StatePendingDisable {
		t.Fatalf("records = %#v, err = %v, want unowned pending state", records, err)
	}

	second := runner.RunOnce(context.Background(), true)
	if !second.OK || second.Summary.Skipped != 1 || len(backend.writes) != 1 || !hasReason(second, accountIdentity(account), ReasonPendingAmbiguous) {
		t.Fatalf("second result = %#v, writes = %#v, want ownership left unproven", second, backend.writes)
	}
}

func TestRunOnceFailedWriteThenExternalDisableNeverClaimsOwnership(t *testing.T) {
	account := testAccount("external-disable-after-error")
	resetAt := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
	backend.setErrors[account.ID] = errors.New("write rejected")
	runner, store := testRunner(t, backend)

	first := runner.RunOnce(context.Background(), true)
	if first.OK || !first.PartialFailure || backend.accounts[0].Disabled {
		t.Fatalf("first result = %#v, account = %#v", first, backend.accounts[0])
	}

	// Another actor disables the account after this guard's write failed.
	backend.accounts[0].Disabled = true
	second := runner.RunOnce(context.Background(), true)
	if !second.OK || second.Summary.Skipped != 1 || !hasReason(second, accountIdentity(account), ReasonPendingAmbiguous) {
		t.Fatalf("second result = %#v, want external disable left unowned", second)
	}
	if records, err := store.Records(); err != nil || len(records) != 1 || records[0].DisabledByTool {
		t.Fatalf("records = %#v, err = %v, want no ownership", records, err)
	}
}

func TestRunOnceWriteAheadSurvivesFinalOwnershipSaveFailure(t *testing.T) {
	account := testAccount("pending-save")
	resetAt := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &resetAt}
	runner, store := testRunner(t, backend)
	saves := 0
	store.beforeReplace = func() error {
		saves++
		if saves == 2 {
			return errors.New("injected final save failure")
		}
		return nil
	}

	first := runner.RunOnce(context.Background(), true)

	if first.OK || !backend.accounts[0].Disabled {
		t.Fatalf("first result = %#v, account = %#v", first, backend.accounts[0])
	}
	records, err := store.Records()
	if err != nil || len(records) != 1 || records[0].DisabledByTool || records[0].LastState != StatePendingDisable {
		t.Fatalf("pending records = %#v, err = %v", records, err)
	}

	store.beforeReplace = nil
	second := runner.RunOnce(context.Background(), true)
	if !second.OK || second.Summary.Skipped != 1 || !hasReason(second, accountIdentity(account), ReasonPendingAmbiguous) {
		t.Fatalf("second result = %#v, want pending ownership left unproven", second)
	}
	if records, err = store.Records(); err != nil || len(records) != 1 || records[0].DisabledByTool {
		t.Fatalf("records = %#v, err = %v, want pending record preserved", records, err)
	}
}

func TestRunOnceRefreshesResetForOwnedExhaustedAccount(t *testing.T) {
	account := testAccount("refresh-reset")
	account.Disabled = true
	newReset := testNow.Add(time.Hour)
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: &newReset}
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, account, testNow.Add(-time.Minute))

	first := runner.RunOnce(context.Background(), true)

	if !first.OK || len(backend.probes) != 1 || len(backend.writes) != 0 {
		t.Fatalf("first result = %#v, probes = %#v, writes = %#v", first, backend.probes, backend.writes)
	}
	records, err := store.Records()
	if err != nil || len(records) != 1 || records[0].ResetAt == nil || !records[0].ResetAt.Equal(newReset) {
		t.Fatalf("records = %#v, err = %v, want refreshed reset", records, err)
	}

	second := runner.RunOnce(context.Background(), true)
	if !second.OK || len(backend.probes) != 1 {
		t.Fatalf("second result = %#v, probes = %#v, want no repeat probe before new reset", second, backend.probes)
	}
}

func TestRunOnceVerificationFailureDoesNotChangeOwnership(t *testing.T) {
	t.Run("failed disable verification does not register", func(t *testing.T) {
		account := testAccount("disable-verify")
		backend := newFakeBackend(account)
		backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: ptrTime(testNow.Add(time.Hour))}
		backend.ignoreWrites[account.ID] = true
		runner, store := testRunner(t, backend)

		result := runner.RunOnce(context.Background(), true)

		if result.OK || !result.PartialFailure || result.Summary.Failed != 1 {
			t.Fatalf("result = %#v, want verification failure", result)
		}
		if records, err := store.Records(); err != nil || len(records) != 1 || records[0].DisabledByTool || records[0].LastState != StatePendingDisable {
			t.Fatalf("records = %#v, err = %v, want pending but no ownership", records, err)
		}
	})

	t.Run("failed enable verification keeps ownership", func(t *testing.T) {
		account := testAccount("enable-verify")
		account.Disabled = true
		backend := newFakeBackend(account)
		backend.assessments[account.ID] = Assessment{State: StateHealthy}
		backend.ignoreWrites[account.ID] = true
		runner, store := testRunner(t, backend)
		seedOwnership(t, store, account, testNow.Add(-time.Minute))

		result := runner.RunOnce(context.Background(), true)

		if result.OK || !result.PartialFailure || result.Summary.Failed != 1 {
			t.Fatalf("result = %#v, want verification failure", result)
		}
		if records, err := store.Records(); err != nil || len(records) != 1 {
			t.Fatalf("records = %#v, err = %v, want ownership preserved", records, err)
		}
	})
}

func TestRunOnceManualEnableReleasesOwnership(t *testing.T) {
	account := testAccount("manual")
	backend := newFakeBackend(account)
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, account, testNow.Add(-time.Minute))

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || result.Summary.Applied != 1 || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
	}
	if records, err := store.Records(); err != nil || len(records) != 0 {
		t.Fatalf("records = %#v, err = %v, want ownership released", records, err)
	}
	if !hasDecision(result, accountIdentity(account), DecisionReleaseOwnership, OutcomeApplied) {
		t.Fatalf("decisions = %#v, want ownership release", result.Decisions)
	}
}

func TestRunOnceReportsPartialFailureAfterOtherSuccess(t *testing.T) {
	good := testAccount("good")
	bad := testAccount("bad")
	backend := newFakeBackend(good, bad)
	backend.assessments[good.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: ptrTime(testNow.Add(time.Hour))}
	backend.assessments[bad.ID] = Assessment{State: StateConfirmedExhausted, ResetAt: ptrTime(testNow.Add(time.Hour))}
	backend.setErrors[bad.ID] = errors.New("write failed")
	runner, store := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if result.OK || !result.PartialFailure || result.Fatal || result.Summary.Applied != 1 || result.Summary.Failed != 1 {
		t.Fatalf("result = %#v, want one applied plus one failed", result)
	}
	records, err := store.Records()
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %#v, err = %v, want ownership plus unowned pending", records, err)
	}
	byIdentity := recordsByIdentity(records)
	if !byIdentity[accountIdentity(good)].DisabledByTool || byIdentity[accountIdentity(bad)].DisabledByTool || byIdentity[accountIdentity(bad)].LastState != StatePendingDisable {
		t.Fatalf("records = %#v, want good owned and bad pending", records)
	}
}

func TestRunOnceMarksMissingOwnershipStaleWithoutAction(t *testing.T) {
	missing := testAccount("missing")
	missing.Disabled = true
	backend := newFakeBackend()
	runner, store := testRunner(t, backend)
	seedOwnership(t, store, missing, testNow.Add(-time.Minute))

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || result.Summary.Stale != 1 || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
	}
	if records, err := store.Records(); err != nil || len(records) != 1 {
		t.Fatalf("records = %#v, err = %v, want stale record preserved", records, err)
	}
	if !hasDecision(result, accountIdentity(missing), DecisionStale, OutcomeStale) {
		t.Fatalf("decisions = %#v, want stale decision", result.Decisions)
	}
}

func TestRunOnceMarksMissingPendingRecordStale(t *testing.T) {
	missing := testAccount("missing-pending")
	backend := newFakeBackend()
	runner, store := testRunner(t, backend)
	seedPending(t, store, missing, testNow.Add(time.Hour))

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || result.Summary.Stale != 1 || !hasDecision(result, accountIdentity(missing), DecisionStale, OutcomeStale) {
		t.Fatalf("result = %#v, want stale pending decision", result)
	}
	if records, err := store.Records(); err != nil || len(records) != 1 || records[0].LastState != StatePendingDisable {
		t.Fatalf("records = %#v, err = %v, want pending preserved", records, err)
	}
}

func TestRunOnceLockConflictIsFatalAndDoesNotCallBackend(t *testing.T) {
	account := testAccount("locked")
	backend := newFakeBackend(account)
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "state.json"))
	lockPath := filepath.Join(dir, "guard.lock")
	first := NewFileLock(lockPath)
	lease, err := first.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	runner := NewRunner(backend, store, NewFileLock(lockPath))
	runner.Now = func() time.Time { return testNow }

	result := runner.RunOnce(context.Background(), true)

	if result.OK || !result.Fatal || !result.Locked || result.PartialFailure {
		t.Fatalf("result = %#v, want fatal lock conflict", result)
	}
	if backend.listCalls != 0 || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("backend called while locked: list=%d probes=%#v writes=%#v", backend.listCalls, backend.probes, backend.writes)
	}
}

func TestRunOnceRequiresStableAuthIndexForWrites(t *testing.T) {
	account := testAccount("unstable")
	account.AuthIndex = ""
	backend := newFakeBackend(account)
	backend.assessments[account.ID] = Assessment{State: StateConfirmedExhausted}
	runner, _ := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
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
	runner, _ := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
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
	runner, _ := testRunner(t, backend)

	result := runner.RunOnce(context.Background(), true)

	if !result.OK || len(backend.probes) != 0 || len(backend.writes) != 0 {
		t.Fatalf("result = %#v, probes = %#v, writes = %#v", result, backend.probes, backend.writes)
	}
	if result.Summary.Total != 1 || !hasReason(result, accountIdentity(runtimeOnly), ReasonRuntimeOnly) {
		t.Fatalf("decisions = %#v, want only runtime-only Codex decision", result.Decisions)
	}
}

func testAccount(id string) Account {
	return Account{
		ID:               id,
		ChatGPTAccountID: "chatgpt-" + id,
		AuthIndex:        "auth-" + id,
		Name:             "account-" + id,
		Provider:         "codex",
		DisabledKnown:    true,
	}
}

func testRunner(t *testing.T, backend *fakeBackend) (*Runner, *Store) {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "state.json"))
	runner := NewRunner(backend, store, NewFileLock(filepath.Join(dir, "guard.lock")))
	runner.Now = func() time.Time { return testNow }
	return runner, store
}

func seedOwnership(t *testing.T, store *Store, account Account, resetAt time.Time) {
	t.Helper()
	if err := store.save([]Record{{
		Identity:       accountIdentity(account),
		Name:           account.Name,
		AuthIndex:      account.AuthIndex,
		Provider:       account.Provider,
		Fingerprint:    fingerprintAccount(account),
		DisabledByTool: true,
		DisabledAt:     ptrTime(testNow.Add(-time.Hour)),
		ResetAt:        ptrTime(resetAt),
		LastProbe:      ptrTime(testNow.Add(-time.Hour)),
		LastState:      StateConfirmedExhausted,
	}}); err != nil {
		t.Fatal(err)
	}
}

func seedPending(t *testing.T, store *Store, account Account, resetAt time.Time) {
	t.Helper()
	if err := store.save([]Record{{
		Identity:       accountIdentity(account),
		Name:           account.Name,
		AuthIndex:      account.AuthIndex,
		Provider:       account.Provider,
		Fingerprint:    fingerprintAccount(account),
		DisabledByTool: false,
		ResetAt:        ptrTime(resetAt),
		LastProbe:      ptrTime(testNow),
		LastState:      StatePendingDisable,
	}}); err != nil {
		t.Fatal(err)
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
	accounts            []Account
	assessments         map[string]Assessment
	probeErrors         map[string]error
	setErrors           map[string]error
	setErrorsAfterWrite map[string]error
	ignoreWrites        map[string]bool
	listErrors          map[int]error
	listCalls           int
	probes              []string
	writes              []fakeWrite
}

type fakeWrite struct {
	id       string
	disabled bool
}

func newFakeBackend(accounts ...Account) *fakeBackend {
	return &fakeBackend{
		accounts:            append([]Account(nil), accounts...),
		assessments:         make(map[string]Assessment),
		probeErrors:         make(map[string]error),
		setErrors:           make(map[string]error),
		setErrorsAfterWrite: make(map[string]error),
		ignoreWrites:        make(map[string]bool),
		listErrors:          make(map[int]error),
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

func (f *fakeBackend) SetDisabled(_ context.Context, account Account, disabled bool) error {
	f.writes = append(f.writes, fakeWrite{id: account.ID, disabled: disabled})
	if err := f.setErrors[account.ID]; err != nil {
		return err
	}
	if f.ignoreWrites[account.ID] {
		return nil
	}
	for i := range f.accounts {
		if f.accounts[i].ID == account.ID {
			f.accounts[i].Disabled = disabled
		}
	}
	return f.setErrorsAfterWrite[account.ID]
}
