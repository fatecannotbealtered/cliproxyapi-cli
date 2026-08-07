# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-08-07

### Added

- Initial Go/Cobra CLI with a native binary and npm launcher.
- JSON-first `reference`, `context`, `doctor`, and `changelog` self-description commands.
- CLIProxyAPI auth-file listing, manual status changes, allowlisted Codex quota inspection, local guard ownership state, and relative raw Management API requests.
- Observation-first `guard run-once` with explicit `--apply`, conservative exhaustion classification, single-instance locking, write verification, and ownership-safe recovery.
- Agent Skill, mock/contract tests, pinned `ai-native-cli-spec` v1.5.0 assets, and CI checks.
- Add confirmed `login` and `logout` commands. Login verifies the Management key and stores it in the current user's OS keyring; the local profile contains only the version, normalized base URL, and credential-backend marker.
- Let ordinary commands reuse the saved Management URL and key, while retaining stdin and environment credentials as higher-priority temporary overrides.

### Fixed

- Return a non-zero process exit when `guard run-once` completes with failed decisions, while preserving the full partial result in the error envelope.
- Reject malformed Management API responses that omit the `/auth-files` `files` array or a valid `/api-call` `status_code`.
- Recognize current account-level Codex exhaustion signals without treating feature-scoped additional limits as permission to disable or restore an entire account.
- Keep npm platform-package lock entries synchronized with the release version.
- Verify the Git tag and package version before GoReleaser creates release artifacts.

### Security

- Accept Management keys only from one-line stdin, `CLIPROXYAPI_CLI_MANAGEMENT_KEY`, or the OS keyring session established by `login`; never accept them in argv or a plaintext credential file.
- Require an expiring, operation-bound dry-run/confirm token for login, logout, and manual status changes; every raw request also requires the explicit `--dangerous` gate.
- Prevent HTTP 429, network failures, free-form text, local usage statistics, and unknown schemas from triggering automatic account disablement.
- Treat missing upstream status fields as unknown, mark provider/auth external data as untrusted where exposed, and omit arbitrary raw response bodies from stdout.
- Keep failed, interrupted, or ambiguous disables unowned so they can never cause an automatic restore.
- Bind saved credentials to their normalized Management base URL, require dry-run/confirm for login/logout changes, and fail when the OS keyring is unavailable instead of falling back to argv or a plaintext secret file.
- Require Go 1.26.5 or newer for builds so distributed binaries include current standard-library security fixes.

[Unreleased]: https://github.com/fatecannotbealtered/cliproxyapi-cli/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/fatecannotbealtered/cliproxyapi-cli/releases/tag/v1.0.0
