# 开源检查清单

[English](OPEN_SOURCE_CHECKLIST.md) | [中文](OPEN_SOURCE_CHECKLIST_zh.md)

审查记录：**2026-08-08**，`v1.0.0` 发布提交为 `03fe02f`，通过 [PR #4](https://github.com/fatecannotbealtered/cliproxyapi-cli/pull/4) 合并，真实环境证据绑定 `f3c5c4a`，规范固定为 `ai-native-cli-spec` `v1.5.0`。`[x]` 表示已验证或明确不适用。

本地证据包括 Go test/vet/lint、Linux race、六目标交叉构建、版本/规范守卫、npm audit/pack、actionlint、命令级契约测试、Gitleaks `v8.30.1` 对 Git 历史和工作树的扫描，以及[经脱敏的授权生产真实 Codex E2E](e2e/2026-08-08-1.0.0-f3c5c4a-production.md)。GitHub Actions 证据来自真实 PR、`main` 与 tag 运行；Release 产物和 npm 发布均已根据公开产物实测验证。

## 密钥

- [x] 已用 `gitleaks dir --redact` 扫描工作树；未发现凭据、token、API key 或密码。
- [x] 已用 `gitleaks git --redact` 扫描完整 Git 历史；未发现泄露。
- [x] 不含内部主机名、内网 IP、内部 URL 或公司内部标识符。
- [x] 测试夹具和录制响应只使用合成或脱敏数据。
- [x] `.env`、`*.local`、凭据状态和构建产物均被忽略且未跟踪；本地被忽略的 `cliproxyapi-cli.exe` 不在 Git 中。
- [x] 保存的凭据使用操作系统 keyring；profile 不含 secret，也没有明文回退。

## 文档

- [x] `README.md` 遵循 REPO-SPEC §2 骨架。
- [x] `README.md` 与 `README_zh.md` 的章节、命令和发布状态表述同步。
- [x] `CHANGELOG.md` 使用 Keep a Changelog，且顶部为 `## [Unreleased]`。
- [x] `LICENSE` 为 MIT，已填写 `2026` / `Sean Guo`。
- [x] 规范安装命令 `@fateforge/cliproxyapi-cli@1.0.0` 能从 npm 解析；Windows x64 全新安装已实际运行打包二进制，并报告 `1.0.0`、`stable`、`live_smoke_status=verified`。

## 治理

- [x] `SECURITY.md` 提供可用的私有 advisory/邮箱渠道和支持版本策略。
- [x] `CONTRIBUTING.md` 覆盖环境、分支/提交、测试和 PR 流程。
- [x] `CODE_OF_CONDUCT.md` 包含 Contributor Covenant。
- [x] `NOTICE.md` 载明 CLIProxyAPI 非隶属声明，`docs/COMPATIBILITY.md` 记录已验证后端范围。

## 构建 / CI

- [x] [PR #4](https://github.com/fatecannotbealtered/cliproxyapi-cli/pull/4) 候选分支、最终 `main` 提交 `03fe02f` 与 `v1.0.0` 发布运行的 GitHub Actions CI 均为绿色。
- [x] CI 将格式、vet、lint、测试、race、npm audit、版本同步和规范漂移作为阻断检查。
- [x] 功能契约覆盖所有公开命令，以及文档中的 help/version、成功、校验、配置/鉴权、上游失败、超时、空结果、分页、fan-out、envelope 和 `_untrusted` 行为。
- [x] `reference.release_readiness.level` 为 `stable`：FCC、mock contract 与 `live_smoke_status=verified` 均有已记录证据支持。
- [x] `doctor` 报告匹配的 `release_readiness` pass，且无需修复建议。
- [x] `.golangci.yml` 已提交，CI 强制 `gofmt` 与 lint。
- [x] 没有跟踪构建产物、缓存、IDE 文件或 coverage 输出。
- [x] 已为候选提交 `f3c5c4a` 记录合格的真实 Codex E2E：CLIProxyAPI `v7.2.114` / `41fc5e1`、授权生产范围、脱敏证据见上方链接。

## 分发

- [x] `release.yml` 会拒绝与 `package.json` 版本不一致的 tag，本地版本守卫对 `1.0.0` 通过。
- [x] 注解 tag `v1.0.0` 已存在，并解析到发布提交 `03fe02f`。
- [x] 二进制由 CI 生成并被 gitignore，不会提交。
- [x] GoReleaser 已配置生成 `checksums.txt` 和 keyless Sigstore bundle；项目不宣传 standalone installer 或 self-update，因此进程内 updater 校验为 **N/A**。
- [x] [GitHub Release](https://github.com/fatecannotbealtered/cliproxyapi-cli/releases/tag/v1.0.0) 包含 5 个平台压缩包、`checksums.txt` 和 Sigstore bundle；下载后的每个压缩包均匹配 checksum，`cosign verify-blob` 对发布工作流身份返回 `Verified OK`。
- [x] release workflow 已配置通过 `npm publish --provenance` 发布 wrapper 和全部平台包。
- [x] npm 上 wrapper 与 6 个平台包均为 `latest=1.0.0`，全部绑定 `gitHead=03fe02f`，并具有 integrity、registry signature 与 SLSA provenance。
- [x] `package.json` 是版本真相源；运行时版本和测试从其派生，运行时/release changelog 内容从 `CHANGELOG.md` 派生。

## AI 原生

- [x] 根目录 `AGENTS.md` 指向 `.agent/AGENT.md`。
- [x] `.agent/{AGENT,CLI-SPEC,SKILL-SPEC,SEC-SPEC}.md` 与 canonical contract 已固定版本，并通过规范漂移守卫。
- [x] `skills/cliproxyapi-cli/SKILL.md` frontmatter 与 CLI `1.0.0`、MIT、用户调用和 `min_version` 一致。
- [x] Skill 包含触发边界、第一步、实时 reference 指引、写操作配方、STOP checkpoint、错误树、权限边界、`_untrusted` 和评估场景。
- [x] `test-prompts.json` 合法，并覆盖上手、写安全、权限、`_untrusted` 与升级行为。
- [x] **N/A —— 设计上不提供 self-update。** 升级由外部完成；Skill 要求单独同步整个 Skill，再运行 `changelog`、`reference`、`context` 和 `doctor`。
- [x] `reference`、`context`、`doctor` 均以真实命令运行并输出 canonical JSON envelope。
- [x] `reference` 与 `doctor` 暴露相同的发布就绪状态。
- [x] `SECURITY.md`、`.agent/SEC-SPEC.md` 和实时 `reference` 都声明风险等级 `T2`。
