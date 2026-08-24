# Contributing to cliproxyapi-cli

*English | [中文](CONTRIBUTING_zh.md)*

Thanks for improving **cliproxyapi-cli** — inspecting CLIProxyAPI auth accounts, Codex quota, and guard decisions. This document covers building, testing, and submitting changes.

> This is a side project shared for AI-tooling experimentation; maintainers do not provide commercial support or production guarantees — see the README disclaimer.

## The build playbook lives in the specs

This repo is an **AI-native CLI tool**, designed for AI agents first. Before implementing or changing any feature, read the playbook:

- **[AGENTS.md](AGENTS.md)** — entry point; routes you to the local specs and the shared repo skeleton standard.
- **[`.agent/AGENT.md`](.agent/AGENT.md)** — the project playbook.
- **[`.agent/CLI-SPEC.md`](.agent/CLI-SPEC.md)** — the CLI output / error / write-loop contract.
- **[`.agent/SKILL-SPEC.md`](.agent/SKILL-SPEC.md)** — the AI Skill bundle spec.
- **[`.agent/SEC-SPEC.md`](.agent/SEC-SPEC.md)** — the security baseline (risk tiers, untrusted content, credentials, supply chain).

These specs are authoritative and take priority over default habits. Code that violates the CLI contract (stdout is the contract, uniform envelope, error triple, write loop) will not be merged.

## Development setup

```bash
# Clone
git clone https://github.com/fatecannotbealtered/cliproxyapi-cli.git
cd cliproxyapi-cli

go mod download
make build                      # or: go build -o bin/cliproxyapi-cli ./cmd/cliproxyapi-cli
go test -race ./...
./bin/cliproxyapi-cli --help
