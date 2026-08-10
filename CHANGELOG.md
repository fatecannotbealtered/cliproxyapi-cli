# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.3] - 2026-08-10

### Changed

- Promote machine-readable release readiness from `beta`/`missing` to `stable`/`verified` after recording the candidate-bound disposable-instance and self-update E2E for `1b2393c`.

- Render `--format text` success output as flat `path: value` lines instead of the unwrapped JSON payload, so human output can no longer be mistaken for the machine envelope.

### Fixed

- Name the rejected field in the `--fields` validation error instead of reporting a generic selection failure.
- Reject an unusable `raw request` path while previewing. A dry-run for a traversal, percent-encoded traversal, or absolute-URL path previously returned a valid confirm token and only failed at confirm time, telling the agent an impossible operation was authorized; validation now runs through the same resolver the execute path uses.

### Removed

- Remove the unreachable guard apply-mode machinery (ownership state store and cross-process run lock). The guard is observation-only by construction now, not merely by configuration; `summary.applied` and `summary.stale` remain in the result shape as reserved, always-zero counters.

## [1.0.2] - 2026-08-10

### Added

- Add the single-command `update` lifecycle with npm-managed and standalone-binary dispatch, whole-Skill synchronization, and structured update checks.
- Add in-process Sigstore verification of signed release checksums before standalone archive checksum verification and atomic replacement.

### Changed

- Surface cached update notices through self-description and machine envelopes without adding network calls to ordinary commands.
- Verify npm-managed updates against the installed CLI, refresh the whole Skill even when the binary is already current, and derive notice severity from the complete target CHANGELOG delta.
- Build a native Windows ARM64 release artifact instead of publishing an amd64 binary under an ARM64 package label.
- Report the `1.0.2` self-update candidate as `beta` until candidate-bound standalone and npm-managed update E2E evidence is recorded.

### Fixed

- Classify local signature-input read failures as local I/O or permission failures instead of reporting a forged-release integrity failure.
- Report the normalized final `updated` status after successful standalone replacement, matching package-manager updates.

### Security

- Fail closed on missing or invalid Sigstore bundles, missing checksums, checksum mismatches, and unexpected standalone archives.
- Preserve the old binary and recovery artifacts across pre-swap interruption, local I/O failure, and Windows rollback failure, while reporting the observed post-update state.

## [1.0.1] - 2026-08-09

### Added

- Report `remaining_percent` alongside upstream `used_percent` for each recognized Codex quota window and the overall assessment.

### Changed

- Match the Codex quota and guard probe headers used by the Management Web UI.

### Fixed

- Remove the ambiguous `0%` quota presentation by labeling consumed and remaining percentages separately.

## [1.0.0] - 2026-08-08

### Added

- Initial Go/Cobra CLI with a native binary and npm launcher.
- JSON-first `reference`, `context`, `doctor`, and `changelog` self-description commands.
- CLIProxyAPI auth-file listing, manual status changes, allowlisted Codex quota inspection, observation-only guard decisions, and relative raw Management API requests.
- Observation-only `guard run-once` with conservative exhaustion classification; writes remain separate atomic commands.
- Agent Skill, mock/contract tests, pinned `ai-native-cli-spec` v1.5.0 assets, and CI checks.
- Add confirmed `login` and `logout` commands. Login verifies the Management key and stores it in the current user's OS keyring; the local profile contains only the version, normalized base URL, and credential-backend marker.
- Let ordinary commands reuse the saved Management URL and key, while retaining stdin and environment credentials as higher-priority temporary overrides.

### Changed

- Promote machine-readable release readiness from `beta`/`missing` to `stable`/`verified` after recording the authorized production real-Codex E2E for candidate `f3c5c4a`.

### Fixed

- Return a non-zero process exit when `guard run-once` completes with failed decisions, while preserving the full partial result in the error envelope.
- Reject malformed Management API responses that omit the `/auth-files` `files` array or a valid `/api-call` `status_code`.
- Recognize current account-level Codex exhaustion signals without treating feature-scoped additional limits as permission to change an entire account.
- Keep npm platform-package lock entries synchronized with the release version.
- Verify the Git tag and package version before GoReleaser creates release artifacts.
- Derive the Go runtime version from the embedded `package.json` source of truth and test real command envelopes against `contract.json`.
- Publish the exact canonical exit table, inherited command parameters, stable list pagination, and per-account quota failures through the live contract.

### Security

- Accept Management keys only from one-line stdin, `CLIPROXYAPI_CLI_MANAGEMENT_KEY`, or the OS keyring session established by `login`; never accept them in argv or a plaintext credential file.
- Require an expiring, operation-bound dry-run/confirm token for login and logout; account-status and raw writes additionally require the explicit `--dangerous` tier.
- Prevent HTTP 429, network failures, free-form text, local usage statistics, and unknown schemas from producing a confirmed-exhausted classification or authorizing an account write.
- Treat missing upstream status fields as unknown, comprehensively mark exposed provider/auth strings as untrusted, and omit arbitrary raw response bodies from stdout.
- Preserve only the relevant `_untrusted` paths when `--fields` projects external content.
- Keep the guard observation-only so external orchestration must use the separately gated, state-verified per-account write primitive.
- Bind saved credentials to their normalized Management base URL, require dry-run/confirm for login/logout changes, and fail when the OS keyring is unavailable instead of falling back to argv or a plaintext secret file.
- Require Go 1.26.5 or newer for builds so distributed binaries include current standard-library security fixes.

[Unreleased]: https://github.com/fatecannotbealtered/cliproxyapi-cli/compare/v1.0.3...HEAD
[1.0.3]: https://github.com/fatecannotbealtered/cliproxyapi-cli/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/fatecannotbealtered/cliproxyapi-cli/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/fatecannotbealtered/cliproxyapi-cli/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/fatecannotbealtered/cliproxyapi-cli/releases/tag/v1.0.0
