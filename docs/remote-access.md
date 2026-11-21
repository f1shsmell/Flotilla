# 远程连接方案（2026-11-22 设计稿）

> 状态：**设计稿，未实现**。本文只记录决策、依据与验收口径；代码按里程碑落地。
> 纪律：结论必须能指到源码行号或一次真实运行。没核实的不写成结论。

## 结论先行

1. **远程不是新协议，是 `ipc.dial` 的第二种传输。** `internal/ipc/conn.go` 的 `Dial`
   在拿到 `net.Conn` 之后才做握手、分帧与 `Call`；只要能让 `dial` 返回一个能读写的
   `net.Conn`，控制面、数据面、capability gate 一行都不用改。这是本方案唯一的地基。
2. **分三档，按代价排序，第一档零服务器。** SSH stdio 桥（herdr 形状）→ 自建 E2E
   relay（Alma 形状）→ 托管隧道（t3code/Cloudflare 形状）。先做第一档，手机端才需要
   第二档。
3. **token 永不上线。** 本机 pipe 的 token 处在"同用户即信任"的边界内
   （`SECURITY.md` 已声明）；一旦跨网络，明文的 token 就是一个可被中转方读走的
   长期凭据。远程档必须满足：要么传输本身可信（SSH），要么两端自己加密（E2E），
   且 token 只在两端之间出现。
4. **手机端只读、只批注、只审批。** 破坏性与外发操作留在桌面宿主，理由与用户自己
   在 Codex-Android 里踩过的 `host_routes.go` 一致，见下文第六节。
5. **不做账号体系。** 我们没有 accounts 服务，也不打算为了远程去建一个。配对用
   一次性 code + 本机指纹，账户层留给 magpie（PLAN M2 已定）。

## 一、现状地基（代码事实）

| 事实 | 位置 | 对方案的含义 |
|---|---|---|
| `Address{Channel, Name}` 只有本机命名空间 | `internal/ipc/transport.go:12` | 需要新增"远端地址"的表达，且不能破坏现有 pipe/UDS 语义 |
| `dial` 是每平台的私有函数 | `internal/ipc/transport_windows.go:52`、`transport_other.go:112` | 远程档在这里分叉，上层无感 |
| 握手带 `Token` / `Role` / `Client` / `Session` | `internal/ipc/handshake.go` | 远程连接复用同一套认证字段，不需要第二套 |
| 控制面 JSON-RPC，数据面 `kind(1)+stream(4)+len(4)` | `internal/ipc/frame.go` | relay 只需要当哑管道转字节；WebSocket 天然承载 binary |
| capability gate 已落地 | `internal/ipc/capability.go` | 远程端与本地端之间也能协商裁剪，旧手机连新 daemon 只少拿字段 |
| daemon 的客户端只有 `Connect` 一个入口 | `internal/daemon/client.go:20` | 远程只要换掉 `ipc.Dial` 的地址解析，`flt` 与 GUI 自动获得能力 |

**一句话**：`ipc.Conn` 是唯一的连接抽象，它的上游（daemon、CLI、GUI）不知道也不关心
底下是 pipe、UDS 还是 SSH 管道。方案必须保住这条。

## 二、四家参考实现（只写读过的）

### 2.1 herdr：SSH stdio 桥

- 入口：`herdr --remote <target>`，本地起一个私有 UDS 监听
  （`refs/herdr/src/remote/attach.rs:2996` 的 `SshStdioBridge`），每个本地连接
  转发到 `ssh <target> herdr <bridge 子命令>` 的 stdio。
- 远端：`refs/herdr/src/remote/host.rs` 的 `run_remote_client_bridge` 先确保远端
  server 在跑，再连本地 client socket，最后
  `forward_remote_bridge_stdio(stream, idle_timeout)`。
- 版本闸：远端 endpoint 协议代次不一致时**拒绝并提示重跑交互式安装**，不静默降级
  （`host.rs` 里 `endpoint_protocol_generation` 的比较）。
- 值得抄：**本地 socket 权限收紧到 0600、bind 私有监听、失败时按 identity 删 socket**
  （`attach.rs` 的 `bind_private_local_listener` / `restrict_socket_permissions`）。
- 不值得抄：把"远端需要更新"做成硬失败。我们用 capability gate，宁可少发字段。

### 2.2 Orca：出站 WSS relay + 拼接状态机

- 拓扑：桌面与手机**都向 relay 建立出站 WSS**，relay 配对并拼帧
  （`refs/orca/cloud/README.md`）。手机与桌面从不直连。
- 拼接是**显式状态机**，九个状态：
  `pre-auth-admitted → credential-lease-reserved → host-notified → attach-pending →
  host-attached → client-acknowledged → spliced → e2ee-confirmable → teardown`
  （`cloud/packages/relay-contract/src/splice-state-machine.ts`）。
  关键不变式：**两个转发 handler 都装好之前不许回 client 成功**
  （`mayAcknowledgeClient`），否则 client 会挂在一个假拼接上。
- 关闭码有语义，不是随便 1000：`4401` 外层凭据坏、`4404` host 离线、`4408` 对端掉、
  `4409` 走错 cell、`4429` 超限、`4503` 排水中（`close-codes.ts`）。
- 限额是常量表，不是散落的魔法数（`protocol-limits.ts`）：首帧 2s、单帧 8MiB、
  每 host 8 连接、空闲 10min、host attach 10s、resume 确认 30s、control ping 15s /
  静默 75s。
- host 身份用**挑战-应答**，不是自报：`HostHello → HostChallenge → HostChallengeAck`，
  证明用 `HMAC-SHA-256` 对一段字段化的 transcript，挑战里塞一个加密的随机 secret
  （`host-proof-transcript.ts`，注释写明"公开 transcript 不足以伪造 ack"）。
- 值得抄：状态机、关闭码、限额表、挑战应答。**不值得抄**：director/cell/lease 那一整套
  多区域编排——我们没有多区域，也不该有。

### 2.3 Paseo：Noise 加密的 relay 通道

- relay 不可信，两端自己加密：`Noise_NK_25519_ChaChaPoly_SHA256`
  （`refs/paseo/packages/relay/src/noise/index.ts`）。NK 意味着**发起方预先知道
  响应方的静态公钥**——这个公钥正是配对时分发的 daemon 身份，没有第二份身份。
- 迁移期双模：`legacy`（X25519 + NaCl box，无重放保护）与 `protected`（Noise，计数器
  防重放）。**两端都是新版就必选 protected**；对端省略 `protected` 才允许 legacy；
  带了一个非法的 `protected` 字段**永不回退**（`docs/relay-endpoint-security.md`）。
- 记录分帧：一个应用帧 = 一条记录 = 若干 Noise transport message，
  `chunk plaintext = kind(1) || flags(1) || totalLength(u32 BE) || data`，
  `wire = (u16 len || ciphertext)+`（`protected-records.ts`）。这样 relay 既不能合并、
  拆分、截断，也不能重排。
- 值得抄：**"声明了但非法 → 直接失败，不回退"** 这条判断。我们 capability gate 也要
  遵守：能力位给了但内容坏掉，是 bug，不是降级。

### 2.4 t3code：托管隧道与可达性判定

- 出站隧道用 cloudflared，不自己写服务器（`apps/server/src/cloud/ConnectReachability.ts`）。
- 可达性有**独立探测**：先用 Cloudflare 的 DoH 解析 `region1/2.v2.argotunnel.com`
  的 IP（用 IP 字面量避免本机 DNS 被劫持），再对 `7844` 做 connect 超时判定，
  结果缓存并 5 分钟复检（同文件常量）。
- 隧道重启有节流：cloudflared 会报告已断的 ready 连接，所以**530 要持续一段时间才重启，
  且每次重启后等更久**（同文件注释）。
- 值得抄：可达性是**独立事实**，不是"HTTP 请求失败了就算不可达"。
- 不值得抄：把 cloudflared 当依赖。我们的第一档不引入外部二进制。

### 2.5 用户自己的两套（一手材料，优先级最高）

**Alma / relay-kit（`relay-kit/PROTOCOL.md`，协议名 `relay-e2e/v1`）**

- 一个 256-bit `code` 是**全部凭证**：
  `routingToken = HKDF(code, salt="relay-e2e/v1/routing")`，
  `tokenHash = sha256hex(routingToken)`；桌面用 `tokenHash` 注册，手机用
  `routingToken` 寻址。relay 只见 hash 与路由键，**没有 code 连 relay 自己都握不了手**。
- 握手是 3 步 PAKE 风格：`hi{epk,n}` → `hi{epk,n,cr}` → `ok{ci}`。
  密钥派生把**双方公钥与 nonce 绑进 transcript**，`code` 进 HKDF 的 salt。
  任一 confirm 比对失败 → `{"t":"err","message":"auth-failed"}`。
- 帧加密：`IV = 0x0000_0000 || counter(uint64 BE)`，每方向独立从 0 严格递增；
  **重放防护是接收端行为**——拒绝 `counter ≤ 已见最大值` 的帧并作废会话。
  注释点明：诚实的对端不可能触发，只有复制帧的中间盒会。
- 抢占语义：同 `tokenHash` 的第二个 agent 注册成功，旧连接被 `4000` 踢掉，
  旧 agent 进 `replaced` **不自动重连**，等用户显式操作。
- 手机挂起：agent 不在线时 phone 等待，60s 仍无 agent → `4404`；
  挂起期早帧缓冲上限 256 帧 / 64 KiB。
- 明确不实现：APNs 推送、OAuth 账号绑定、team relay、非 E2E 兼容模式。

**Codex-Android（`SELF_HOST.md` / `RELAY_WATCHDOG.md` / `src/net/*`）**

- `SELF_HOST.md` 的结论值得直接引用：**自建 relay 技术上可行（协议公开、300 行骨架），
  但要连 accounts 服务一起自建，2–3 天 + 一台服务器，换回来的仍是 200ms 轮询**。
  所以最终选了 Cloudflare Tunnel 直接暴露 https 源站。
  → 对我们的含义：**不要为了远程先建服务器**。
- `RELAY_WATCHDOG.md` 是全场最有价值的一篇：半开 socket（电脑休眠、NAT 过期、射频漫游）
  **不产生 TCP 错误**，OkHttp 的 send 写进本地缓冲、永远等不到 close。
  检测靠 `pingInterval(20s)`，界约 40s；桥层必须把 `onFailure` 转成**合成的
  `close(1006)`**，否则"检测到了但没人听"。写入在丢链时**直接失败，不重试**
  ——内核可能已经执行了。30s 请求超时与链路看门狗是**两个不同的问题**。
- `deviceTrust.ts`：首次信任要人确认（显示分组指纹），之后每次连接**验证 pin**，
  key 变了**拒绝**而不是静默接受；解 pin 是重新安装后重新批准的唯一途径。
- `sameMachine.ts`：自动切换只在"两扇门指向同一台机器"时允许；
  **缺席是拒绝，不是猜测**——"unknown"与"different"要分开答。
- `connection.ts` 的注释：**reachability 与 stream health 是两个事实**，
  用一个数字表示两者会说谎。
- `transport.ts`：`EventChannel = "sse" | "mux" | "poll"`，把"这一帧走哪条车道"
  显式化，便于把 UI 从传输实现里解耦。

## 三、方案分档

### 档 A：SSH stdio 桥（零服务器，先做）

```
GUI ──本地 pipe──▶ flt-daemon ──?──▶ 远端 flt-daemon
                        │
                        └─ flt remote attach --ssh user@host
                             本地 loopback listener
                             ↕ ssh stdio（stdin/stdout 就是字节流）
                             远端 `flt serve --stdio`
```

- 实现点：`internal/ipc` 增加 `Remote` 传输，`dial` 在 `Address.Remote != ""` 时
  返回一个包住 `ssh` 子进程 stdio 的 `net.Conn`（`net.Pipe` 语义，带 idle 检测）。
- 远端 `flt serve --stdio` 只需把本地 daemon pipe 的两端对接 stdio，**协议零改动**。
- 认证：SSH 自己的密钥认证。token 仍然在握手时发送，但**它只在 SSH 隧道内出现**，
  不落到任何第三方。
- 验收：`TestRemoteAttachOverStdio` —— 进程内起 daemon，`ssh` 用
  `cmd /c type` 之类的替身管道（测试里替换 `ssh` 为 `TestHelperStdioBridge`），
  `flt status` 经桥拿到 fleet；杀 daemon 重连，会话不断。
- 代价：约 1–2 天。需要用户在远端装 `flt`（与 herdr 的取舍相同）。
- 不解决：手机蜂窝场景（SSH 在手机上不现实）。

### 档 B：自建 E2E relay（手机端才需要）

**直接复用 `relay-kit` 的 `relay-e2e/v1` 协议，不另造。**

- 理由一：协议已经有一份 300 行可运行的 Node 参考实现（`test/fixtures/relay-server.mjs`）
  与一份 Cloudflare Workers 实现，可以直接对着跑互操作测试。
- 理由二：它已经把最难的部分做对了——code 即全部凭证、接收端重放防护、
  抢占语义、挂起与超时码。
- 理由三：用户自己维护过 Alma 与 Codex-Android 两条链路，踩坑记录是现成的。
- 我们只需写两端：Go 的 relay agent（桌面侧，出站 WSS）与 Flotilla 手机的 E2E 客户端。
  桌面侧把 E2E 明文接到**本地 daemon pipe**，于是又回到"`ipc.Conn` 之上一切不变"。
- 分帧：E2E 隧道内跑的是 `ipc` 的帧格式（`kind+stream+len`），不是 HTTP。
  这一条与 Alma/Codex 不同（它们转 HTTP/SSE），因为 Flotilla 的 daemon 本来就是
  JSON-RPC over 流，不需要 HTTP 门面。
- 验收：`TestRelayKitInterop` —— Go 桌面 agent ↔ relay-kit 的 Node fixture，
  跑完握手、重放攻击被拒、抢占后旧连接收到 4000。

### 档 C：托管隧道（可选，不做默认）

- 用 Cloudflare Tunnel 把**本地 daemon 的 loopback HTTP 门面**暴露出去。
  但 Flotilla 现在没有 HTTP 门面，做这个等于为了隧道加一层 HTTP，性价比低。
- 结论：**不做**。记在这里是为了说明"看过、为什么不做"，不是待办。

## 四、认证与配对

三条硬规则：

1. **远程必须加密，明文 token 不许出本机。** 档 A 由 SSH 提供，档 B 由 E2E 提供。
2. **首次信任要人，之后每次验证。** 照 `deviceTrust.ts`：首次显示分组指纹并确认，
   之后每次连接比对 pin，不一致直接拒绝（`changed`），不是警告。
3. **声明了但坏了就是失败。** 照 Paseo：带了 `protected` 字段却验不过，不回落明文。

配对物料（档 B）：

- 一次性 256-bit code，二维码 + 可手输的分组形式（照 Alma 的
  `alma://connect?u=&k=&e2e=1` 形状，我们的 scheme 用 `flotilla://`）。
- code 同时派生路由键与 PAKE salt，**不写入磁盘**；手机只在安全存储里留 pin 的指纹。
- 二维码有效期 10 分钟、最多 5 次尝试（照 Orca 的 `inviteTtlMs` / `inviteMaxAttempts`）。

## 五、状态模型（诚实优先）

一个连接要分三层报，不能用布尔糊过去：

| 层 | 含义 | 说谎的后果 |
|---|---|---|
| `link` | 本机 GUI ↔ daemon（或 relay 段）是否活着 | 与"会话是否在跑"混淆，用户以为活干完了 |
| `session` | host 是否答得上（attach 成功） | 与 `link` 混淆，重连按钮按了没反应 |
| `remote` | 远端机器是否可达（档 A 是 SSH 进程存活，档 B 是 relay 段 + agent 在线） | 与 `session` 混淆，用户去查错地方 |

`reachable` 这个现有布尔要拆开；`unknown` 永远是 `unknown`，不当作完成。

## 六、手机端的边界（只读、批注、审批）

**做**：看会话、读 diff、写评论、回答权限问题、取消回合、看用量。

**不做**（配对设备凭据较弱，破坏性与外发操作留在桌面）：

- 丢弃变更、推送、开 PR
- 任意终端（PTY 会话的键盘输入）
- 改适配器/开关等本机配置
- 删除会话

这与用户在 Codex-Android 里核实的 `host_routes.go` 一致：内核把
`POST /changes/discard`、`GET/POST /git/push` 和终端路由列进 host-only 表，
手机不渲染这些按钮。我们的实现方式是在 daemon 侧按 `Role` 拒绝，而不是靠手机自觉。

## 七、不做什么

- 不做 accounts 服务（账号层留给 magpie）。
- 不做多区域 relay 编排（没有那个规模）。
- 不做 HTTP/SSE 门面（Flotilla 的协议是 JSON-RPC over 流，不需要）。
- 不做明文兼容模式（E2E 是默认且唯一）。
- 不为了远程引入 cloudflared 之类的外部二进制。

## 八、里程碑落点

| 档 | 落在 | 前置 |
|---|---|---|
| A | PLAN M3「多机（SSH 节点入列）」 | `ipc.Address` 加 `Remote`；`flt serve --stdio` |
| B | PLAN M3「跨设备 relay」 | 档 A 的传输抽象；relay-kit 协议互操作测试 |
| C | 不做 | — |

档 A 可以提前到 M2 末尾做，因为它不依赖 Orchestrator，且能立刻解决"两台机器一个窗口"。

## 九、未核实项（如实列出）

- 档 A 的 `ssh` stdio 在 Windows 上的行缓冲/二进制模式：`ssh.exe` 默认二进制安全，
  但**未在本机实测**；实现前要跑一次 `ssh host cat` 传二进制帧的探针。
- 档 B 的 Go 侧 WSS 客户端选型（`nhooyr.io/websocket` vs `gorilla/websocket`）未定；
  relay-kit 的 Node fixture 用 `ws`，互操作要实测。
- 手机端技术栈未定（Capacitor 复用 renderer，还是原生）；用户两套先例分别是
  Capacitor 壳与原生 Kotlin，代价差异很大，见 Codex-Android 的 `PRIOR_ART.md`。
- 二维码/指纹的具体编码未定。
