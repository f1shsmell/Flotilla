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
| claude-agent-acp | `claude-agent-acp` | **全会话绿**,advertise resume;模式/模型切换走协议(零 token) | 2026-11-20 · Windows 10 19045 · `claude-agent-acp@0.100.3` + Claude Code 已登录 |
| codex-acp | `npx -y @agentclientprotocol/codex-acp@2.8.0` | **全会话绿**,advertise resume;模式/模型切换走协议(零 token);环境要求见下文注意 | 2026-11-20 · Windows 10 19045 · `@openai/codex@0.169.0` + 隔离 `CODEX_HOME` |

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
claude-agent-acp: stop=end_turn resume=true messages=3 thoughts=0 tools=0 elapsed=3.532s reply="flotilla-ok"
```

```
$ CODEX_HOME=<隔离目录> flt probe --command "npx -y @agentclientprotocol/codex-acp@2.8.0" --dir <空目录>
npx: stop=end_turn resume=true messages=5 thoughts=0 tools=0 elapsed=2m0.096s reply="Warning: Falling back from WebSockets to HTTPS transport. request timed out\n\nflotilla-ok"
```

codex 那一行的标签是 `npx`,因为 probe 用命令的第一个词做标签;回合本身由适配器
背后的 codex core 完成。

opencode 这一条说明的是:我们的 ACP 客户端与它握手、建会话都成立,失败发生在
agent 自己的 provider 调用里(该端点不接受本机的 PAT)。换成可用凭据后应当重跑,
重跑通过才把结论改成「全会话绿」。

claude-agent-acp 在 2026-11-17 的回合曾被它自己的 `Authentication required` 拒绝,
但会话创建**完整跑完**了 `validate-cwd → settings → prepare-query → sdk-initialize →
models → modes → agents → register` 八个阶段。2026-11-20 本机 Claude Code 登录后重跑,
回合绿,上表与证据块已按重跑结果更新。

## 模式/模型切换(零 token 取证)

`session/set_mode` 与 `session/set_config_option` 不发 prompt、不花 token。2026-11-20
用临时探针(`acp.Open` → `SetMode`/`SetConfigValue` → 读回 surface)对两家各取证一次,
会话全程未重启。探针是未入库的临时文件,取证后已删除;把这条路径收进 `flt probe`
是后续工作项,在那之前本节结论按行为描述复现。

claude-agent-acp(default → plan,sonnet → haiku;探针输出节选):

```
mode before: default
mode after: plan via config_option_update(mode)
model before: sonnet
model after: haiku via forced options broadcast (set_config_option does not notify)
SWITCH-OK: mode and model changed over the protocol, session never restarted
```

codex-acp(agent → workspace-write,gpt-6-luna → gpt-5.6-terra;探针输出节选):

```
mode before: agent
mode after: workspace-write via config_option_update(mode)
model before: gpt-6-luna
model after: gpt-5.6-terra via config_option_update
SWITCH-OK: mode and model changed over the protocol, session never restarted
```

取证同时暴露了两家适配器的通知行为差异,都是本次取证运行中核实的事实:

- **两次切换取证里,两家都没有发 `current_mode_update`**:模式变更经
  `config_option_update` 里 id 为 `mode` 的 select 通知。我们的 `Modes()` 投影只读
  `current_mode_update`,对这两家会停在开场值;options 投影实时跟随。GUI 的模式列
  因此要以 options 为准,不编造档位。
- **claude-agent-acp 的 `set_config_option` 在本次取证中未观察到携带新值的
  通知**:请求成功,新值要到下一次 options 广播(例如一次 `set_mode` 之后)
  才读回。上面 claude 的 model 一行就是靠切换模式强制广播后读回的。
- **codex-acp 两种设置都立即广播全量 options**,请求成功与 surface 更新一一对应。

## GUI 真回合(全程不碰终端)

smoke 驱动真实窗口离屏走 GUI → daemon → session-host → agent 全链路。期望回复
不得是 prompt 的子串(harness 守卫会直接拒绝):日志会先渲染用户自己的 prompt
回声,子串期望值会在回合完成前假通过。

- claude-agent-acp(2026-11-20,会话 offscreen-c):prompt「What is 17 plus 25?
  Reply with only the number.」,回复 `42` 渲染进日志,smoke exit=0。
- codex-acp(2026-11-20,会话 codex-gui7,回合约 2 分钟):同一 prompt,回复
  `42` 渲染进日志,smoke exit=0。窗口 body 节选:

  ```
  codex-gui7
  acp · idle · end_turn
  What is 17 plus 25? Reply with only the number.
  Warning: Falling back from WebSockets to HTTPS transport. request timed out
  42
  ```

- claude-agent-acp(2026-11-20,会话 smoke-real):agent 在工作目录写出
  `hello.txt`(内容 `flotilla-real-ok`,文件仍在盘上),回合完成、回复落
  daemon transcript;该次运行的窗口渲染当时未单独验证,渲染证据以上面两条
  重跑为准。

诚实注脚:同日早先的 codex-gui3/gui4 记录(一次「超时」、一次「通过」)都是
harness 问题,不是产品链路:窗口当时显示在桌面上,被其他窗口遮罩时 rAF 暂停、
日志冻结(gui3 的空日志);期望值是 prompt 子串,回声假通过(gui4)。已由
PR #35(smoke 窗口关节流 + 回声守卫)与 PR #36(离屏定位)修复,本节证据是
修复后的重跑。

## codex-acp 环境注意

- **版本必须钉死 `@agentclientprotocol/codex-acp@2.8.0`**。旧包
  `@zed-industries/codex-acp`(0.16.0)已被 npm registry 标记弃用,原文:
  「This package has been replaced by @agentclientprotocol/codex-acp.
  Please migrate to continue receiving updates.」(2026-11-20 `npm view` 查证)。
- `@openai/codex@0.169.0` CLI 没有原生 `acp` 子命令,必须走适配器。
- 本机 `~/.codex/config.toml` 若被第三方工具桥接改写,codex core 可能拒绝加载。
  取证一律用隔离 `CODEX_HOME`(auth.json 副本 + 只含 `model` 一行的 config.toml)
  绕开本机配置,取证后副本即删。
- 适配器的 WebSocket 连接会超时回退 HTTPS,回复带
  `Warning: Falling back from WebSockets to HTTPS transport` 前缀;本环境实测
  回合约 2 分钟(见上方证据块与 GUI 一节)。握手探针收尾阶段另有非致命的
  `loadCommands` 报错(app-server 已退出时发布 available_commands 失败),不影响
  握手与回合。

## 未取证

| agent | 为什么没写结论 |
|---|---|
| Gemini CLI | 本机未安装;`.gemini` 只有 hooks 配置,没有 `oauth_creds.json`,装上也要先登录 |
| Pi | 要走 `pi-acp` 适配器包(`pi --acp` 40s 无应答);本机未跑过真回合 |

## 一条诚实的注脚

Reasonix Studio 是维护者自己的产品。它跑通证明的是「我们的 ACP 客户端能驱动一个
真实 agent」,不能证明「第三方 agent 生态已被验证」。claude-agent-acp 与 codex-acp
在 2026-11-20 于本机补上了真回合,「第三方 ACP agent 全会话绿」自此有了证据;但样本
只有两家,codex 还依赖特定适配器版本与隔离环境。首发话术可以写「已验证
claude-agent-acp 与 codex-acp 全会话」,不能外推成「所有第三方 agent 都行」。

## 复现

```bash
# 零 token:只读 agent 自报的 modes 与 configOptions,不需要可用登录
# --json 是根级旗标,必须放在子命令之前
go run ./cmd/flt probe --handshake --command "<你的 agent> --acp"
go run ./cmd/flt --json probe --handshake --command "<你的 agent> --acp"

# 真回合:花你自己的 token
go run ./cmd/flt probe reasonix
go run ./cmd/flt probe --command "opencode acp"
go run ./cmd/flt probe --command "claude-agent-acp"

# codex-acp:钉死版本 + 隔离 CODEX_HOME(auth.json 副本 + 最小 config.toml)
CODEX_HOME=<隔离目录> go run ./cmd/flt probe --command "npx -y @agentclientprotocol/codex-acp@2.8.0" --dir <空目录>

# GUI 真回合:smoke 离屏驱动真实窗口;期望回复不得是 prompt 的子串
# codex 另需隔离 CODEX_HOME,并用 FLT_GUI_SMOKE_DEADLINE_MS 放宽死线(回合约 2 分钟)
FLT_GUI_SMOKE=1 FLT_DAEMON_BIN=<flt-daemon 路径> \
  FLT_GUI_SMOKE_START='{"id":"gui-ev","mode":"acp","command":"claude-agent-acp","policy":"auto","dir":"<空目录>"}' \
  FLT_GUI_SMOKE_OPEN=gui-ev \
  FLT_GUI_SMOKE_PROMPT="What is 17 plus 25? Reply with only the number." \
  FLT_GUI_SMOKE_EXPECT=42 pnpm --dir gui start
```

`flt probe` 花的是你自己的 token;缺二进制或缺登录时它在开回合之前就失败。
`--handshake` 不花 token,缺登录也能读到档位——它报告里的 `updates=0` 就是
「没有发出任何回合」的证据。
