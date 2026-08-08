# Live E2E Evidence

## Current Status

A local Management API smoke run was performed on a pre-`1.0.0` development snapshot on 2026-08-06, but it is not a complete release E2E: the workspace had no Git metadata and no disposable real Codex account was used. `1.0.0` is the first tagged release. Unit, mock-upstream, and synthetic-auth tests are not substitutes for the remaining provider evidence. The project must not claim `stable` until a successful, traceable disposable real-Codex run is recorded here.

## Safe Test Scope

Use a disposable CLIProxyAPI instance and disposable Codex accounts. Never test write paths against production accounts.

A valid run should record:

1. CLI version and commit SHA;
2. OS and architecture;
3. CLIProxyAPI version or commit SHA;
4. redacted base URL and credential source (`keyring`, `env`, or `stdin`, never the key);
5. `reference`, `context`, and `doctor` results;
6. auth-file listing and a Codex quota inspection;
7. observation-only `guard run-once` decisions;
8. a dangerous + dry-run/confirm disable of one disposable account, followed by re-read verification;
9. a separate dangerous + dry-run/confirm enable of that account, followed by re-read verification; and
10. stdout/stderr redaction and `_untrusted` behavior.

For manual `auth-file set-status` or any `raw request`, obtain the exact command shape from `reference`, include `--dangerous` in both calls, run `--dry-run`, inspect the preview, and confirm with the returned token. Guard runs are observation-only. Raw requests never expose response bodies. Do not record the Management key, provider token, cookies, authorization headers, raw auth files, or unredacted account identifiers.

## Evidence Record

Add one row per completed run and link to a sanitized artifact committed under `docs/e2e/` or retained by the release process.

| Date | CLI version/SHA | CLIProxyAPI version/SHA | Platform | Scope | Result | Evidence |
|------|-----------------|-------------------------|----------|-------|--------|----------|
| 2026-08-06 | Pre-`1.0.0` development snapshot; SHA unavailable | `v7.2.120` / `ea37d13` | Windows amd64 + Docker Desktop | Loopback Management API, synthetic Codex auth, auth list/status round-trip, `/api-call`, unknown-result safety, Windows Credential Manager `login` → credential-free `doctor` → `logout`, and secret-redaction checks | Partial; does not qualify for stable | This record; sanitized command artifact unavailable |

## Failure Rule

If the upstream version cannot be identified, the provider response is ambiguous, a write cannot be verified by re-reading state, or any secret appears in an artifact, the run does not count as release evidence. Record the incompatibility in [COMPATIBILITY.md](COMPATIBILITY.md) before changing release readiness.
