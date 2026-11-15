# 支持矩阵（只写已取证的）

`docs/positioning.md` 说营销话术以支持矩阵为限。这份文件就是那个矩阵:每一条
都必须有一次真实回合的证据,没跑过的不写。

取证工具是 `flt probe`(见 `internal/acp/probe`),它做的是一次完整回合:
initialize → newSession → prompt → 流式 update → stop reason,并读出 agent 是否
advertise `session/resume`。**它不覆盖**权限请求(probe 用自动批准策略,不会触发)、
cancel、fs 能力探测——这些要另外取证,不能拿这张表冒充。

## 已取证

| agent | 命令 | 结论 | 取证时间 / 环境 |
|---|---|---|---|
| Reasonix Studio | `reasonix acp` | **全会话绿**,advertise resume | 2026-11-16 · Windows 11 · reasonix 本机安装 |
| opencode | `opencode acp` | ACP 握手与会话建立通过,回合被 provider 鉴权拒绝 | 2026-11-16 · Windows 11 · opencode 本机安装 |

证据原文(可直接复现):

```
$ flt probe --command "reasonix acp" --dir <空目录>
reasonix: stop=end_turn resume=true messages=2 thoughts=0 tools=0 elapsed=3.824s reply="flotilla-ok"
```

```
$ flt probe --command "opencode acp" --dir <空目录>
flt: probe opencode: probe opencode: prompt the agent: {"code":-32603,"message":"Internal error: Provider request failed with HTTP 400: checking third-party user token: bad request: Personal Access Tokens are not supported for this endpoint", ...}
```

opencode 这一条说明的是:我们的 ACP 客户端与它握手、建会话都成立,失败发生在
agent 自己的 provider 调用里(该端点不接受本机的 PAT)。换成可用凭据后应当重跑,
重跑通过才把结论改成「全会话绿」。

## 未取证

| agent | 为什么没写结论 |
|---|---|
| claude-agent-acp | 本机未安装,`flt probe claude` 在 PATH 检查处失败 |
| Gemini CLI | 本机未安装,同上 |
| Codex / Pi | 它们的 CLI 不暴露 ACP 子命令(`--help` 里没有),要走适配器层,不在 ACP 路径上 |

## 一条诚实的注脚

Reasonix Studio 是维护者自己的产品。它跑通证明的是「我们的 ACP 客户端能驱动一个
真实 agent」,不能证明「第三方 agent 生态已被验证」。第三方证据要等 claude-agent-acp
或 Gemini CLI 在本机装上后补。

## 复现

```bash
go run ./cmd/flt probe reasonix
go run ./cmd/flt probe --command "opencode acp"
go run ./cmd/flt probe --command "<你的 agent> --acp"
```

`flt probe` 花的是你自己的 token;缺二进制或缺登录时它在开回合之前就失败。
