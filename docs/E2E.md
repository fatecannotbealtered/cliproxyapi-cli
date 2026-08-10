# Live E2E Evidence

## Current Status

A traceable, explicitly authorized production real-Codex run was completed for the clean `1.0.0` candidate at `f3c5c4a` on 2026-08-08. The run exercised the real Management API, all configured Codex quota probes, observation-only guard decisions, the dangerous dry-run/confirm gate, and post-write re-read verification without changing the requested production state. Together with the existing command/FCC and mock-upstream coverage, this evidence qualifies `1.0.0` for `stable`.

The `1.0.1` quota-semantics candidate at `71bc3de` additionally passed a targeted, read-only production regression smoke on 2026-08-09. It verified all configured Codex probes and the new used/remaining percentage relationship without performing an account write. This supplements rather than replaces the qualifying `1.0.0` full E2E.

The uncommitted updater development tree, now designated as the `1.0.2` candidate, has passed isolated standalone and npm-managed update E2E against the real signed/public `1.0.1` release. The run is bound to the recorded updater binary digest and covers whole-Skill synchronization plus post-update `--version`, `changelog`, and `reference` checks. It remains `beta` because development evidence without a clean candidate commit SHA does not qualify as candidate-bound stable evidence; the same run must be repeated after the candidate is committed.

## Accepted Live Test Scope

Prefer a disposable CLIProxyAPI instance and disposable Codex accounts. An explicitly authorized production run may also qualify when it is bound to a clean candidate commit and binary digest, limits writes to the exact approved account state, verifies every write by re-reading, records the intended final state, and retains only sanitized aggregate evidence. Never infer production-write permission from release testing alone.

A valid run should record:

1. CLI version and commit SHA;
2. OS and architecture;
3. CLIProxyAPI version or commit SHA;
4. redacted base URL and credential source (`keyring`, `env`, or `stdin`, never the key);
5. `reference`, `context`, and `doctor` results;
6. auth-file listing and a Codex quota inspection;
7. observation-only `guard run-once` decisions;
8. a dangerous + dry-run/confirm status write, followed by re-read verification;
9. either restoration of a disposable account or an explicit record that the authorized production state must be retained; and
10. stdout/stderr redaction and `_untrusted` behavior.

For a self-update candidate, also record the starting and target versions, install method, signature/checksum result, `binary_replaced`, `skill_sync_status`, isolated npm prefix or standalone path, and the version observed on the next process invocation. A development run without a clean candidate SHA is useful diagnostics but does not qualify the candidate as `stable`.

For manual `auth-file set-status` or any `raw request`, obtain the exact command shape from `reference`, include `--dangerous` in both calls, run `--dry-run`, inspect the preview, and confirm with the returned token. Guard runs are observation-only. Raw requests never expose response bodies. Do not record the Management key, provider token, cookies, authorization headers, raw auth files, confirmation tokens, production hostnames, or unredacted account identifiers.

## Evidence Record

Add one row per completed run and link to a sanitized artifact committed under `docs/e2e/` or retained by the release process.

| Date | CLI version/SHA | CLIProxyAPI version/SHA | Platform | Scope | Result | Evidence |
|------|-----------------|-------------------------|----------|-------|--------|----------|
| 2026-08-06 | Pre-`1.0.0` development snapshot; SHA unavailable | `v7.2.120` / `ea37d13` | Windows amd64 + Docker Desktop | Loopback Management API, synthetic Codex auth, auth list/status round-trip, `/api-call`, unknown-result safety, Windows Credential Manager `login` → credential-free `doctor` → `logout`, and secret-redaction checks | Partial; does not qualify for stable | This record; sanitized command artifact unavailable |
| 2026-08-08 | `1.0.0` / `f3c5c4a` / binary `461C82AC...17A85` | `v7.2.114` / `41fc5e1` | Windows amd64 | Explicitly authorized production Management API; real quota probes; observation-only guard; dangerous idempotent status write and re-read; retained disabled state | Pass; qualifies for stable | [Sanitized evidence](e2e/2026-08-08-1.0.0-f3c5c4a-production.md), SHA-256 `26534E29...6AC35106` |
| 2026-08-09 | `1.0.1` / `71bc3de` / binary `99D08A8B...D7432F` | Same authorized deployment; version not re-collected | Windows amd64 | Targeted read-only regression for quota request parity and explicit used/remaining percentages | Pass; supplemental smoke, no write | [Sanitized evidence](e2e/2026-08-09-1.0.1-71bc3de-production.md), SHA-256 `FBBE761C...4955AA3` |

## Self-Update Evidence Record

| Date | Candidate | Install path | Target | Result | Qualification |
|------|-----------|--------------|--------|--------|---------------|
| 2026-08-10 | Uncommitted updater development tree based on `7cf88b0`, later designated as `1.0.2`; updater test binary version-injected as `1.0.0`, SHA-256 `F4931F81...D49F10E` | Isolated Windows amd64 standalone path and isolated npm prefix | Real signed GitHub Release and public npm packages for `1.0.1`; whole Skill sync to `1.0.1` | Pass after the first run exposed and regression-fixed a standalone final-status mismatch: standalone reported `updated`, verified Sigstore + checksum, replaced atomically, and launched as `1.0.1`; npm drove the manager, wrapper and platform metadata reached `1.0.1`, and both paths reported synced Skill with successful post-update changelog/reference reads | Development evidence only. No raw command artifact was retained; no Management endpoint, credential, or account data was used. Re-run against a clean candidate commit before declaring `1.0.2` stable. |

## Failure Rule

If the upstream version cannot be identified, the provider response is ambiguous, a write cannot be verified by re-reading state, or any secret appears in an artifact, the run does not count as release evidence. Record the incompatibility in [COMPATIBILITY.md](COMPATIBILITY.md) before changing release readiness.
