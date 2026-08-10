# Open-Source Checklist

[English](OPEN_SOURCE_CHECKLIST.md) | [中文](OPEN_SOURCE_CHECKLIST_zh.md)

Review record: **2026-08-08**, release `v1.0.0` at `03fe02f`, merged through [PR #4](https://github.com/fatecannotbealtered/cliproxyapi-cli/pull/4), with live evidence bound to `f3c5c4a` and the specification pinned to `ai-native-cli-spec` `v1.5.0`. `[x]` means verified or explicitly not applicable.

Local evidence includes Go tests/vet/lint, Linux race tests, six-target cross-builds, version/spec guards, npm audit/pack, actionlint, command-level contract tests, Gitleaks `v8.30.1` scans of both the Git history and working tree, and the [sanitized authorized production real-Codex E2E](e2e/2026-08-08-1.0.0-f3c5c4a-production.md). GitHub Actions evidence comes from the actual PR, `main`, and tag runs; release artifacts and npm publication were verified from their published outputs.

This is a frozen evidence record for the `v1.0.0` first-public-release artifact. Checked statements below describe that artifact only; they do not qualify later Unreleased behavior.

## `1.0.2` Self-Update Delta

- [x] Bump the current published `1.0.1` release by the explicitly selected patch increment to `1.0.2`; do not republish it as `1.0.1`.
- [x] Verify command-level FCC for update success, validation, network/timeout/interruption, integrity failure, package-manager dispatch, atomic replacement, notice caching, and partial Skill sync.
- [ ] Record isolated standalone and npm-managed update E2E evidence against a clean candidate commit, including whole-Skill synchronization and post-update version checks.
- [ ] Re-run the full release, native six-architecture, npm provenance, security, version/spec, and stable-readiness gates for that candidate.

## Secrets

- [x] The working tree was scanned with `gitleaks dir --redact`; no credentials, tokens, API keys, or passwords were found.
- [x] The full Git history was scanned with `gitleaks git --redact`; no leaks were found.
- [x] No internal hostnames, private IPs, internal URLs, or employer-internal identifiers are present.
- [x] Test fixtures and recorded responses use only synthetic or redacted data.
- [x] `.env`, `*.local`, credential state, and build artifacts are ignored and untracked; the local ignored `cliproxyapi-cli.exe` is not in Git.
- [x] Saved credentials use the OS keyring; the profile contains no secret and there is no plaintext fallback.

## Docs

- [x] `README.md` follows the REPO-SPEC §2 skeleton.
- [x] `README.md` and `README_zh.md` have matching sections, commands, and release-state wording.
- [x] `CHANGELOG.md` uses Keep a Changelog and has `## [Unreleased]` first.
- [x] `LICENSE` is MIT with `2026` / `Sean Guo` filled in.
- [x] The canonical `@fateforge/cliproxyapi-cli@1.0.0` install resolves from npm; a clean Windows x64 install ran the packaged binary and reported `1.0.0`, `stable`, and `live_smoke_status=verified`.

## Governance

- [x] `SECURITY.md` has a working private-advisory/email channel and supported-version policy.
- [x] `CONTRIBUTING.md` covers setup, branches/commits, tests, and PR flow.
- [x] `CODE_OF_CONDUCT.md` contains the Contributor Covenant.
- [x] `NOTICE.md` carries the CLIProxyAPI non-affiliation notice and `docs/COMPATIBILITY.md` records the verified backend scope.

## Build / CI

- [x] GitHub Actions CI is green on the candidate branch in [PR #4](https://github.com/fatecannotbealtered/cliproxyapi-cli/pull/4), final `main` commit `03fe02f`, and the `v1.0.0` release run.
- [x] CI makes formatting, vet, lint, tests, race tests, npm audit, version sync, and spec drift blocking checks.
- [x] Functional Contract Coverage covers every public command and the documented help/version, success, validation, config/auth, upstream failure, timeout, empty, pagination, fan-out, envelope, and `_untrusted` behavior.
- [x] `reference.release_readiness.level` is `stable`: FCC, mock contracts, and `live_smoke_status=verified` are backed by recorded evidence.
- [x] `doctor` reports the matching `release_readiness` pass with no remediation.
- [x] `.golangci.yml` is committed and CI enforces `gofmt` plus lint.
- [x] No build artifacts, caches, IDE files, or coverage outputs are tracked.
- [x] A qualifying real-Codex E2E is recorded for candidate `f3c5c4a`: CLIProxyAPI `v7.2.114` / `41fc5e1`, authorized production scope, sanitized evidence linked above.

## Distribution

- [x] `release.yml` rejects a tag whose version differs from `package.json`, and the local version guard passes for `1.0.0`.
- [x] The annotated `v1.0.0` tag exists and resolves to release commit `03fe02f`.
- [x] Binaries are CI outputs and are gitignored rather than committed.
- [x] For the reviewed `v1.0.0` artifact, no self-update path was advertised, so in-process updater verification was **N/A**. This historical result does not cover the `1.0.2` updater candidate.
- [x] The [GitHub Release](https://github.com/fatecannotbealtered/cliproxyapi-cli/releases/tag/v1.0.0) contains five platform archives, `checksums.txt`, and a Sigstore bundle; every downloaded archive matched its checksum and `cosign verify-blob` returned `Verified OK` for the release workflow identity.
- [x] The release workflow is configured to publish the wrapper and all platform packages with `npm publish --provenance`.
- [x] npm shows the wrapper and all six platform packages at `latest=1.0.0`, all bound to `gitHead=03fe02f`, with integrity, registry signatures, and SLSA provenance.
- [x] `package.json` is the version source of truth; runtime version and tests derive from it, while runtime/release changelog content derives from `CHANGELOG.md`.

## AI-native

- [x] Root `AGENTS.md` points to `.agent/AGENT.md`.
- [x] `.agent/{AGENT,CLI-SPEC,SKILL-SPEC,SEC-SPEC}.md` and the canonical contract are pinned and pass the spec drift guard.
- [x] `skills/cliproxyapi-cli/SKILL.md` frontmatter matches CLI version `1.0.0`, MIT, user invocation, and `min_version`.
- [x] The Skill contains trigger boundaries, first step, live-reference guidance, write recipe, STOP checkpoints, error tree, permission boundary, `_untrusted`, and eval scenarios.
- [x] `test-prompts.json` is valid and covers onboarding, write safety, permissions, `_untrusted`, and upgrade behavior.
- [x] **N/A for the reviewed `v1.0.0` artifact only.** That release used an external upgrade plus separate Skill resync; the `1.0.2` single-command updater is gated by the unchecked delta above.
- [x] `reference`, `context`, and `doctor` run as real commands and emit canonical JSON envelopes.
- [x] `reference` and `doctor` expose the same release-readiness state.
- [x] `SECURITY.md`, `.agent/SEC-SPEC.md`, and live `reference` all declare risk tier `T2`.
