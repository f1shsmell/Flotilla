# 支持矩阵（只写已取证的）

`docs/positioning.md` 说营销话术以支持矩阵为限。这份文件就是那个矩阵:每一条
都必须有一次真实回合的证据,没跑过的不写。

取证工具是 `flt probe`(见 `internal/acp/probe`),它有两条路径:

- **`flt probe`** 做的是一次完整回合:initialize → newSession → prompt → 流式 update →
  stop reason,并读出 agent 是否 advertise `session/resume`。它花真 token,而且**不覆盖**
  权限请求(probe 用自动批准策略,不会触发)、cancel、fs 能力探测——这些要另外取证,
  不能拿这张表冒充。
- **`flt probe --handshake`** 在 session 建立后立刻关闭,不发 prompt:**零 token、零
  provider 调用**。它读回 agent 自报的 `modes` 与 `configOptions`(含 id、名称、当前值与
  可选项),所以一台**没有可用登录**的机器也能取证。表里「握手通过」那一档,今后一律
  用这条命令复现;原先写在仓库外临时探针里的结果,以此为可持久化的入口。

两条路径分属两档证据,不可互相顶替:握手通过只证明协议、鉴权方式与会话建立;回合内容
仍要 `flt probe` 打真回合才算取证。

## 已取证

| agent | 命令 | 结论 | 取证时间 / 环境 |
|---|---|---|---|
| Reasonix Studio | `reasonix acp` | **全会话绿**,advertise resume | 2026-11-16 · Windows 11 · reasonix 本机安装 |
| opencode | `opencode acp` | ACP 握手与会话建立通过,回合被 provider 鉴权拒绝 | 2026-11-16 · Windows 11 · opencode 本机安装 |
| claude-agent-acp | `claude-agent-acp` | ACP 握手与会话建立通过,回合被 agent 自身的登录检查拒绝 | 2026-11-17 · Windows 10 19045 · `claude-agent-acp@0.100.3` |

证据原文(可直接复现):

```
$ flt probe --command "reasonix acp" --dir <空目录>
reasonix: stop=end_turn resume=true messages=2 thoughts=0 tools=0 elapsed=3.824s reply="flotilla-ok"
```

```
$ flt probe --command "opencode acp" --dir <空目录>
flt: probe opencode: probe opencode: prompt the agent: {"code":-32603,"message":"Internal error: Provider request failed with HTTP 400: checking third-party user token: bad request: Personal Access Tokens are not supported for this endpoint", ...}
```

```
$ flt probe --command "claude-agent-acp" --dir <空目录>
[session/create] sessionId=... phase=sdk-initialize durationMs=1061 totalMs=1144
[session/create] sessionId=... phase=register durationMs=2 totalMs=1159
flt: probe claude-agent-acp: probe claude-agent-acp: prompt the agent: {"code":-32000,"message":"Authentication required"}
```

opencode 这一条说明的是:我们的 ACP 客户端与它握手、建会话都成立,失败发生在
agent 自己的 provider 调用里(该端点不接受本机的 PAT)。换成可用凭据后应当重跑,
重跑通过才把结论改成「全会话绿」。

claude-agent-acp 同理,而且这一条比 opencode 更进一步:它的会话创建**完整跑完**
了 `validate-cwd → settings → prepare-query → sdk-initialize → models → modes → agents →
register` 八个阶段,失败发生在 prompt 阶段它自己的 `Authentication required`。这说明我们
的客户端能驱动一个**非 Reasonix Studio 的 ACP 实现**走完会话建立;回合内容仍未被证明。
本机没有 `ANTHROPIC_API_KEY`,也没有 Claude 的 OAuth 凭据,所以补不上这一刀。

## 未取证

| agent | 为什么没写结论 |
|---|---|
| Gemini CLI | 本机未安装;`.gemini` 只有 hooks 配置,没有 `oauth_creds.json`,装上也要先登录 |
| Codex / Pi | 它们的 CLI 不暴露 ACP 子命令(`--help` 里没有),要走适配器层,不在 ACP 路径上 |

## 一条诚实的注脚

Reasonix Studio 是维护者自己的产品。它跑通证明的是「我们的 ACP 客户端能驱动一个
真实 agent」,不能证明「第三方 agent 生态已被验证」。claude-agent-acp 把「非 Reasonix Studio
的 ACP 实现能握手、建会话」这一半补上了,但**回合内容**仍待有可用凭据的机器重跑。
在那之前,首发话术只能写「已验证客户端能驱动第三方 ACP agent 建立会话」,不能写
「第三方 agent 全会话绿」。

## 复现

```bash
# 零 token:只读 agent 自报的 modes 与 configOptions,不需要可用登录
go run ./cmd/flt probe --handshake --command "<你的 agent> --acp"
go run ./cmd/flt probe --handshake --json --command "<你的 agent> --acp"

# 真回合:花你自己的 token
go run ./cmd/flt probe reasonix
go run ./cmd/flt probe --command "opencode acp"
go run ./cmd/flt probe --command "<你的 agent> --acp"
```

`flt probe` 花的是你自己的 token;缺二进制或缺登录时它在开回合之前就失败。
`--handshake` 不花 token,缺登录也能读到档位——它报告里的 `updates=0` 就是
「没有发出任何回合」的证据。
