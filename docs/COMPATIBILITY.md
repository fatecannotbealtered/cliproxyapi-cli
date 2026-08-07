# Compatibility

This document states what `cliproxyapi-cli` targets and what has actually been verified. It is not a promise that an untested upstream release is compatible.

## Contract Pin

The repository vendors `ai-native-cli-spec` **v1.5.0**. `contract/contract.json` and the generated Go binding are checked for drift by `node scripts/check-spec.js`.

## Runtime and Distribution

| Layer | Target |
|-------|--------|
| CLI implementation | Go 1.26.5 or newer with Cobra |
| Distribution | Native binary plus npm launcher/platform packages |
| Management base | HTTP(S), default `http://127.0.0.1:8317/v0/management` |
| Credentials | One saved OS-keyring session, with one-line stdin or `CLIPROXYAPI_CLI_MANAGEMENT_KEY` as explicit overrides |
| Local profile | `~/.cliproxyapi-cli/config.json`; non-secret `version`, `base_url`, and `credential_backend` only |
| Scheduler | External cron, systemd timer, or Windows Task Scheduler; saved credentials require the same OS user and keyring access |

## Credential Session

`login` verifies a Management key before saving it to Windows Credential Manager, macOS Keychain, or Linux Secret Service. `login` and `logout` both require dry-run followed by confirmation. There is no Management-key argv flag or plaintext secret-file fallback. Headless Linux must provide an accessible Secret Service session.

Ordinary commands resolve credentials as stdin, environment, then saved keyring, in that order. They resolve the Management base as flag, environment, saved profile, then loopback default. A saved key is bound to its normalized saved base URL and is never reused when another URL is explicitly selected.

## CLIProxyAPI Surface

The modeled implementation uses this small Management API surface:

| Route | Purpose | Verification status |
|-------|---------|---------------------|
| `GET /auth-files` | Enumerate auth metadata | Mock/contract tests plus local Docker smoke against CLIProxyAPI `v7.2.120` (`ea37d13`) |
| `PATCH /auth-files/status` | Enable or disable one auth record | Mock/contract tests plus dry-run, confirmed write, re-read verification, and restore against `v7.2.120` |
| `POST /api-call` | Perform the allowlisted Codex quota probe | Mock/contract tests plus Management API transport smoke against `v7.2.120`; no real Codex quota decision recorded |

`raw request` can invoke another relative path below the configured Management base, but every call requires the dangerous and confirmation gates, response bodies are omitted, and the escape hatch is not a compatibility guarantee for any unmodeled endpoint.

Status writes are verified by the CLI by reading the selected record again after the PATCH. The documented Management API does not expose a conditional-update/CAS field, so this is not a server-side linearizability guarantee against an independent concurrent writer; callers that require that stronger property must serialize writers at the orchestration layer.

The only live Management API version checked so far is CLIProxyAPI `v7.2.120` at commit `ea37d13`. That release accepts `auth_index` as an additional `/auth-files/status` selector and exposes parsed `id_token` claims in `/auth-files`; the current public Management API page does not promise those two details as a versioned contract. The CLI therefore treats missing claims as unknown and performs no quota write. Until a traceable disposable real-Codex run is added to [E2E.md](E2E.md), release readiness must remain below `stable` and the running binary's `reference.release_readiness` is authoritative.

## Codex Quota Response

The parser accepts snake_case and camelCase forms of the known `rate_limit`, primary-window, and secondary-window fields. It also recognizes current account-level `rate_limit_reached_type` values and `spend_control.reached`. It recognizes Unix-second or RFC 3339 reset timestamps and relative reset seconds.

`additional_rate_limits` are model- or feature-scoped. An exhausted additional limit makes the overall assessment unknown: it cannot authorize disabling an entire account, and it cannot be mistaken for a healthy signal that restores an account. `credits.has_credits=false` alone is also not proof that the subscription's normal quota is exhausted.

Compatibility intentionally fails closed. Only explicit structured signals can report confirmed exhaustion. HTTP 429 alone, transport errors, free-form phrases, missing fields, and unknown schemas remain unknown and do not authorize a write.

The provider probe is fixed to the Codex client's current ChatGPT usage endpoint. That endpoint is an implementation dependency observed in the open-source Codex client, not a stable public Platform API contract. Arbitrary upstream URLs are not accepted by the quota or guard commands, and unknown response variants fail closed.

## Explicit Non-Goals

Version `1.0.0` does not provide:

- OAuth or browser-based login;
- a plaintext credential-store fallback;
- a daemon or built-in scheduler;
- a Web UI;
- a first-class auth-file delete command;
- a self-update command; or
- quota decisions based on `usage-queue`, local usage counters, or `reset-quota`.

`usage-queue` pops local telemetry records when read. `reset-quota` clears a CLIProxyAPI cooldown, not an upstream quota. Neither behavior is treated as provider allowance state.
