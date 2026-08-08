# Live E2E Evidence

## Current Status

A traceable, explicitly authorized production real-Codex run was completed for the clean `1.0.0` candidate at `f3c5c4a` on 2026-08-08. The run exercised the real Management API, all configured Codex quota probes, observation-only guard decisions, the dangerous dry-run/confirm gate, and post-write re-read verification without changing the requested production state. Together with the existing command/FCC and mock-upstream coverage, this evidence qualifies `1.0.0` for `stable`.

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

For manual `auth-file set-status` or any `raw request`, obtain the exact command shape from `reference`, include `--dangerous` in both calls, run `--dry-run`, inspect the preview, and confirm with the returned token. Guard runs are observation-only. Raw requests never expose response bodies. Do not record the Management key, provider token, cookies, authorization headers, raw auth files, confirmation tokens, production hostnames, or unredacted account identifiers.

## Evidence Record

Add one row per completed run and link to a sanitized artifact committed under `docs/e2e/` or retained by the release process.

| Date | CLI version/SHA | CLIProxyAPI version/SHA | Platform | Scope | Result | Evidence |
|------|-----------------|-------------------------|----------|-------|--------|----------|
| 2026-08-06 | Pre-`1.0.0` development snapshot; SHA unavailable | `v7.2.120` / `ea37d13` | Windows amd64 + Docker Desktop | Loopback Management API, synthetic Codex auth, auth list/status round-trip, `/api-call`, unknown-result safety, Windows Credential Manager `login` → credential-free `doctor` → `logout`, and secret-redaction checks | Partial; does not qualify for stable | This record; sanitized command artifact unavailable |
| 2026-08-08 | `1.0.0` / `f3c5c4a` / binary `461C82AC...17A85` | `v7.2.114` / `41fc5e1` | Windows amd64 | Explicitly authorized production Management API; real quota probes; observation-only guard; dangerous idempotent status write and re-read; retained disabled state | Pass; qualifies for stable | [Sanitized evidence](e2e/2026-08-08-1.0.0-f3c5c4a-production.md), SHA-256 `26534E29...6AC35106` |

## Failure Rule

If the upstream version cannot be identified, the provider response is ambiguous, a write cannot be verified by re-reading state, or any secret appears in an artifact, the run does not count as release evidence. Record the incompatibility in [COMPATIBILITY.md](COMPATIBILITY.md) before changing release readiness.
