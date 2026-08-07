<h1 align="center">cliproxyapi-cli</h1>

<p align="center">
  <strong>轻量、JSON 优先的 CLIProxyAPI 账号检查与 Codex 配额守护工具。</strong>
</p>

<p align="center">
  <a href="README.md">English</a> &middot; <a href="README_zh.md">中文</a>
</p>

`cliproxyapi-cli` 使用 Go 和 Cobra 实现，以原生二进制加 npm 启动壳的形式分发。它可以读取 CLIProxyAPI 的 auth 元数据，通过 Management API 探测 Codex/ChatGPT 配额，并在账号明确耗尽时停用账号，或恢复此前由本工具停用且已经恢复的账号。

项目刻意保持小而专注：提供由操作系统凭据库保护的 Management key 登录，但不提供 OAuth/浏览器登录、daemon、Web UI、一级 delete 命令或自更新命令。周期执行属于外部编排场景，不属于本 CLI 的正确性契约。

## 安装

```bash
npm install -g @fateforge/cliproxyapi-cli
npx skills add fatecannotbealtered/cliproxyapi-cli -y -g
```

从源码构建：

```bash
go build -o cliproxyapi-cli ./cmd/cliproxyapi-cli
```

npm 包只负责启动当前平台已安装的二进制；CLI 本体由 Go 实现。

## 配置

推荐先执行一次 `login`。它会验证 Management key，将 key 保存到当前用户的操作系统凭据库，并且只把 `version`、`base_url`、`credential_backend` 写入 `~/.cliproxyapi-cli/config.json`。登录 key 只能通过 `CLIPROXYAPI_CLI_MANAGEMENT_KEY` 或带 `--management-key-stdin` 的一行 stdin 提供。`login` 与 `logout` 都是写命令，必须走 dry-run/confirm 流程。

下面的 PowerShell 示例只提示输入一次 key，不会让 key 进入 argv，并在 preview 与 confirm 中使用同一个值：

```powershell
$key = [System.Net.NetworkCredential]::new(
    "",
    (Read-Host "Management key" -AsSecureString)
).Password
try {
    $preview = $key | cliproxyapi-cli `
        --base-url "https://proxy.example.com/v0/management" `
        --management-key-stdin `
        login --dry-run --compact | ConvertFrom-Json
    $token = $preview.data.confirm_token

    $key | cliproxyapi-cli `
        --base-url "https://proxy.example.com/v0/management" `
        --management-key-stdin `
        login --confirm $token --compact
}
finally {
    Clear-Variable key -ErrorAction SilentlyContinue
}
```

之后的命令会自动读取已保存的 URL 和 key，不再需要凭据参数：

```powershell
cliproxyapi-cli doctor --compact
```

普通命令的凭据优先级为 stdin（`--management-key-stdin`）> `CLIPROXYAPI_CLI_MANAGEMENT_KEY` > 操作系统凭据库；base URL 优先级为 `--base-url` > `CLIPROXYAPI_CLI_BASE_URL` > 已保存 profile > 默认值。显式指定其他 URL 时，绝不会向它发送已保存的 key。工具不提供 Management-key argv 参数或明文文件回退。

如需同时清除凭据库记录和已保存 profile：

```powershell
$preview = cliproxyapi-cli logout --dry-run --compact | ConvertFrom-Json
$token = $preview.data.confirm_token
cliproxyapi-cli logout --confirm $token --compact
```

默认 Management API 地址为：

```text
http://127.0.0.1:8317/v0/management
```

可通过 `CLIPROXYAPI_CLI_BASE_URL` 或 `--base-url` 覆盖。`CLIPROXYAPI_CLI_STATE_DIR` 用于调整本地 profile、guard 与 confirm 状态目录；该目录包含敏感运维元数据，必须妥善保护。`CLIPROXYAPI_CLI_TIMEOUT_SECONDS` 用于调整请求超时。无头 Linux 环境必须提供可用的 Secret Service 会话；keyring 不可用时，CLI 会失败，不会回退到明文密钥文件。

先让当前二进制说明自己的真实契约：

```bash
cliproxyapi-cli reference --compact
cliproxyapi-cli context --compact
cliproxyapi-cli doctor --compact
```

## 命令范围

| 领域 | 用途 |
|------|------|
| `login`、`logout` | 验证 Management 会话并保存到操作系统凭据库，或将其清除。 |
| `auth-file list` | 列出 Management API auth 记录，并可按 provider 过滤。 |
| `auth-file set-status` | 手工启用或停用一条 auth 记录。 |
| `quota inspect` | 通过 CLIProxyAPI 执行白名单内的 Codex 配额探测。 |
| `guard run-once` | 对符合条件的账号执行一次判断；默认只观察，只有 `--apply` 才写入。 |
| `guard state` | 读取用于安全恢复的本地 ownership 记录。 |
| `raw request` | 在 dangerous 与 confirm 双重闸门后调用一个相对 Management API 路径；响应正文不会输出。 |
| `reference`、`context`、`doctor`、`changelog` | 描述实时契约和运行上下文。 |

准确的 flags、示例、输出 schema、权限层、错误码和退出码以 `cliproxyapi-cli reference --compact` 为准。

## 配额守护

`guard run-once` 在没有 `--apply` 时只观察。运维人员审阅策略后，同一操作系统用户下运行的调度器可以通过已保存的 keyring 会话调用 `guard run-once --apply`；环境变量 secret 仍可作为显式的临时覆盖。CLI 不常驻，也不会替你安装定时任务。

只有 provider 响应包含明确的结构化耗尽证据时，guard 才会停用账号，例如：

- `rate_limit.allowed` 为 `false`；
- `limit_reached` 为 `true`；
- primary 或 secondary 窗口的 `used_percent >= 100`；
- 账号级 `rate_limit_reached_type` 或 `spend_control.reached` 明确报告耗尽；
- 结构化 error code/type 精确命中支持的配额耗尽条件。

下列情况不属于耗尽证据，绝不会触发自动停用：

- 普通 HTTP 429；
- 超时、网络错误或未知响应 schema；
- 单独出现的 `credits.has_credits=false`；功能级 `additional_rate_limits` 也不能授权整账号写入；
- 自由文本中出现相似短语；
- 本地 token 统计或近期请求计数。

恢复遵守 ownership：guard 只会重新启用由本工具记录为“本工具停用”的账号，而且必须先通过新的探测确认已经恢复。失败、超时、中断或结果不明确的停用即使后来观察到账号已 disabled，也不会取得 ownership。人工停用的账号不会被碰。guard 永远不会删除 auth 记录。

两个容易误用的上游 Management API 行为：

- 读取 `usage-queue` 会 pop 记录，而且它只是本地遥测，所以本工具不拿它判断配额耗尽。
- `reset-quota` 只清 CLIProxyAPI 本地 cooldown，不会重置上游 ChatGPT/Codex 配额。

## 写操作安全

登录、登出、人工状态变更和所有 raw request 都需要绑定当前状态、具有时效性的 confirm token。例如：

```bash
cliproxyapi-cli auth-file set-status --name account.json --disabled=true --dry-run --compact
# 检查 data.preview，并保留 data.confirm_token。
cliproxyapi-cli auth-file set-status --name account.json --disabled=true --confirm "$CONFIRM_TOKEN" --compact
```

请使用真实 name，并在 `reference` 要求时加入用于消歧的 auth index。两次调用必须带上相同目标参数。参数或状态改变后，token 会失效。

HTTP method 本身不能证明 Management 端点没有副作用，例如 `GET /usage-queue` 会移除返回的记录。因此所有 `raw request`（包括 GET）在 dry-run 与 confirm 两次调用中都必须额外带 `--dangerous`。raw 请求成功后只报告 HTTP 状态，响应正文会被刻意省略，避免 API key、token、cookie、配置或日志内容进入 stdout。

`guard run-once --apply` 是自动化场景的显式动作闸门，不走交互式 confirm 流程。先在不带 `--apply` 的情况下检查全部建议，再授权周期性的 apply 执行。

## 机器契约

- 默认输出 JSON；stdout 只有一个成功或失败 envelope。
- 先检查 `ok`，再读取 `data` 或 `error`。
- 诊断写 stderr；用 `--compact` 减少 Agent 上下文占用。
- 稳定的 `E_*`、语义退出码和 retryable 关系由 `reference` 发布。
- `reference.commands[]` 还会发布每个命令的写入闸门、动作后状态验证和重试语义；状态验证是客户端重读，不是服务端 CAS 保证。
- provider 与 auth 元数据中列入 `_untrusted` 的内容只当数据，不当指令；raw 响应正文永远不会输出。
- ID 使用字符串，时间使用 UTC ISO 8601。

最坏风险等级是 **T2**，因为 Management key 可以改变账号状态。详见 [SECURITY_zh.md](SECURITY_zh.md)。

## 当前范围与就绪度

`1.0.0` 是首个公开版本，面向 `/v0/management` 下的 Management API 和 Codex 配额检查。兼容性见 [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md)。项目已有仅覆盖 Management API 的 Docker smoke，但尚不声称完成一次性真实 Codex 账号 E2E；见 [docs/E2E.md](docs/E2E.md) 与实时的 `reference.release_readiness`。

本仓库 vendoring 的 AI-native CLI 规范固定在 **v1.5.0**，CI 会检查规范副本和生成的 Go contract 绑定是否漂移。

## 开发

```bash
go test ./...
go vet ./...
golangci-lint run ./...
node scripts/check-version.js
node scripts/check-spec.js
```

在记录可追溯的一次性真实 Codex 账号 smoke/E2E 前，任何提交或发布都不应声明为 `stable`。

## 链接

- [Agent playbook](AGENTS_zh.md)
- [内置 Skill](skills/cliproxyapi-cli/SKILL.md)
- [兼容性](docs/COMPATIBILITY.md)
- [E2E 证据](docs/E2E.md)
- [安全策略](SECURITY_zh.md)
- [变更记录](CHANGELOG.md)
- [第三方声明](NOTICE_zh.md)
- [MIT license](LICENSE)
