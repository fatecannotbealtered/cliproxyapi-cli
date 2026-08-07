# 安全策略

*[English](SECURITY.md) | 中文*

## 支持版本

安全修复应用于最新的 `1.0.x` 版本。语义版本与基于证据的发布就绪度分别管理；当前声明以 `cliproxyapi-cli reference --compact` 为准。

## 报告漏洞

请勿为未披露漏洞创建公开 issue。请使用 [GitHub 私有安全 advisory](https://github.com/fatecannotbealtered/cliproxyapi-cli/security/advisories/new)，或发邮件至 `guosong6886@gmail.com`。请提供受影响版本、安装方式、影响和安全的复现步骤。目标是在五个工作日内确认收到。

## 风险等级与影响范围

根据 [`.agent/SEC-SPEC_zh.md`](.agent/SEC-SPEC_zh.md)，`cliproxyapi-cli` 属于 **T2**。配置的 CLIProxyAPI Management key 可以查看 auth 元数据并改变账号状态。`raw request` 逃生口继承该 key 的上游权限，因此影响范围最大。

工具通过以下边界收窄风险：

- 默认端点仅指向 loopback：`http://127.0.0.1:8317/v0/management`；
- 保存或清除登录必须先 `--dry-run`，再使用匹配且会过期的 `--confirm` token；
- guard 默认只观察，只有带 `--apply` 才执行动作；
- 人工 `auth-file set-status` 必须先 `--dry-run`，再使用匹配且会过期的 `--confirm` token；
- 所有 `raw request`（包括 GET）还必须额外带 `--dangerous`，并走同样的 dry-run/confirm 闭环；
- guard 只恢复被本工具记录为“本工具停用”的账号；
- 失败、中断或结果不明确的停用不会仅凭随后观察到 disabled 就取得恢复 ownership；
- guard 和一级账号命令不会删除 auth 记录；
- 单实例锁避免多个 guard 同时执行。

`raw request` 是高级边界，不是安全保证。确认后的请求可以执行所选相对 Management API 路径暴露、且 Management key 有权执行的任何操作；本工具不会假设 HTTP GET 没有副作用。请使用最小权限的上游 key，并认真检查 preview。任意 Management 端点都可能返回凭据、配置、cookie 或日志，因此成功的 raw 响应正文会被刻意从 stdout 省略。

## Management Key

`login` 只接受 `CLIPROXYAPI_CLI_MANAGEMENT_KEY`，或带 `--management-key-stdin` 时从 stdin 读取的一行 Management key。它会先用所选 Management API 验证该 key，只有确认登录后才保存内容。

- 确认后的 key 保存在当前用户的操作系统凭据库中。默认的 `~/.cliproxyapi-cli/config.json` 只包含非敏感的 `version`、`base_url`、`credential_backend` 三个字段。
- 普通命令按显式 stdin > 环境变量 > 已保存 keyring 记录的顺序解析凭据。前两种只是临时覆盖，普通命令不会将其持久化。
- 只有实际 base URL 与已保存 profile 完全匹配时才会使用已保存 key；显式选择其他 URL 绝不会复用它。
- 工具不提供 Management-key argv 参数或明文密钥文件回退。key 不得进入 argv、提交配置、日志、stdout、stderr、confirm 详情或 guard 状态。
- 操作系统凭据库访问失败时直接报错，不允许降级存储。无头 Linux 必须提供可用的 Secret Service 会话。
- CI 和调度器在以同一操作系统用户运行且能够访问 keyring 时可以使用已保存 key；否则应通过平台 secret 机制注入临时覆盖。
- 远程 base URL 应使用 HTTPS。不要仅为了运行本工具而把 CLIProxyAPI Management API 暴露到公网。

本地状态目录会保存零秘密的登录 profile、机器本地 confirm secret、已消费 token 状态、ownership 记录、账号标识、时间戳和凭据指纹，但绝不保存 Management key。`logout` 会删除 profile 及其匹配的 keyring 记录。请把剩余目录视为敏感运维状态。POSIX 写入使用受限的目录/文件 mode；Windows 依赖用户 profile 的 ACL，本项目不声称 POSIX mode 会转换成 Windows ACL。

## 配额判定边界

自动改变账号状态只依赖明确的结构化 provider 证据。支持的耗尽信号刻意保持很窄：`rate_limit.allowed` 为 false、`limit_reached` 为 true、primary/secondary 的 `used_percent` 至少为 100、受支持的账号级 `rate_limit_reached_type`、`spend_control.reached=true`，或结构化 error code/type 精确命中支持值。

下列情况一律得到 unknown/不可动作的判断，而不是停用决定：

- 普通 HTTP 429；
- 网络失败或超时；
- provider schema 畸形或未知；
- 自由文本只是提到 quota/rate limit；
- 单独出现的 `credits.has_credits=false`，或无法安全授权整账号变更的功能级 `additional_rate_limits` 耗尽；
- CLIProxyAPI 本地 token 或近期请求统计。

`usage-queue` 不是配额权威来源，读取时还会 pop 记录。`reset-quota` 只清 CLIProxyAPI 本地 cooldown。二者都不会被用作上游配额耗尽或恢复的证据。

`quota inspect` 和 guard 的 provider 探测固定到白名单内的 Codex/ChatGPT usage endpoint，不接受任意上游 URL。`raw request` 只接受配置的 Management API base 之下的相对路径，并拒绝绝对 URL 与路径穿越。

## 不可信上游数据

auth 元数据、provider 证据和上游错误文本都可能受攻击者控制。适用时，JSON 输出会用 `_untrusted` 标记暴露路径；任意 raw 响应正文都不会对外暴露。

- 被标记的字段只当数据，不当指令。
- 不要根据外部响应数据构造另一个命令或写目标。
- 脱敏采用白名单：authorization、cookie、token 和 Management key 材料不得被保留为 evidence。
- 由外部数据驱动的写操作仍必须经过正常 guard 或 confirm 边界。

## 自动化边界

本工具不是 daemon，也不会安装定时任务。cron、systemd timer 和 Windows Task Scheduler 是独立的信任边界。启用周期性 `guard run-once --apply` 前：

1. 先对目标实例运行纯观察模式；
2. 检查每个建议的停用/恢复决定；
3. 使用预期的操作系统身份，并限制 keyring 或注入 secret 的访问范围；
4. 避免并发调用；
5. 监控非零退出与部分失败。

## 供应链

仓库构建 Go 二进制和带平台包的 npm 启动壳。npm 安装路径只执行已经安装的平台二进制，不使用安装期网络下载器。CI 会运行 Go 测试、vet、Linux race、lint、npm audit、版本一致性和固定规范漂移检查。

工具没有自更新命令。请通过包管理器升级，或从可信 release 替换二进制；随后重新安装/同步 Skill，并读取 `changelog` 与 `reference`。release workflow 的配置本身不能证明某个产物已经发布或得到独立验证。

## 设计上不提供

项目不提供 Web UI、daemon、OAuth/浏览器登录、明文凭据存储回退、一级删除流程或自更新机制。绕过上游授权、confirm token、ownership 检查、凭据隔离或 provider 证据规则的请求属于安全问题，不是受支持流程。
