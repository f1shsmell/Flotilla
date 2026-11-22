# 远程连接与手机端设计（2026-11-22）

> 状态：**设计稿，未实现**。本文是唯一一份设计稿，取代此前的
> `remote-access.md` / `remote-access-review.md` / `remote-access-open-questions.md` /
> `ui-remote-design.md` 四份。
> 纪律：结论必须能指到源码行号或一次真实运行；没核实的不写成结论，列在第十二节。

## 结论先行

1. **远程不是新协议，是 `ipc.dial` 的第二种传输。** `internal/ipc/conn.go:150` 的 `Dial`
   在拿到 `net.Conn` 之后才做握手、分帧与 `Call`；只要 `dial` 能返回一个可读写的
   `net.Conn`，控制面、数据面、capability gate 一行都不用改。这是全案唯一的地基。
2. **档 A 用 SSH 端口转发，不用 stdio 桥。** 早期稿写的是 stdio 桥（herdr 形状），
   读过内核之后判定错误：端口转发直接得到一个 TCP 端口，`ipc.Dial` 原样可用；
   且远端进程常驻，断线后会话仍在，而 stdio 桥一次断开要重建整条链。
   代价因此是 **1–2 周**，不是 1–2 天。**零服务器，但不是零成本。**
3. **多机是并列，不是代理。** 每个 (host, workspace) 一个远端 daemon，GUI 并行多连接。
   代理要把远端 daemon 的整个方法面在本地重新实现一遍，每加一个方法改两处。
   会话 id 因此需要机器作用域：`<host>/<session>`。
4. **token 永不上线。** 本机 pipe 的 token 处在「同用户即信任」的边界内
   （`SECURITY.md` 已声明）；跨网络后，明文 token 就是可被中转方读走的长期凭据。
   档 A 由 SSH 提供机密性，档 B 由 E2E 提供，且 token 只在两端之间出现。
5. **手机端只读、只批注、只审批。** 破坏性与外发操作留在桌面，且在 **daemon 侧按
   `Role` 拒绝**，不靠客户端自觉。
6. **不做厂商账号池，也不做远程的账号服务。** 这是两件不同的事，不能混：
   - **模型凭证层**留给 magpie（PLAN M2 已定）。它是本机的模型网关：把各家
     的 key 与 base URL 收进一个本地网关，额度用完切下一个账号。Flotilla 不自养登录流、
     不碰签到/切号/风控对抗——那是它的领域，也是厂商 ToS 的风险所在。
   - **远程配对的账号服务**（谁有资格连这台机器）**不做**。Flotilla 的远程是一次性
     code + 本机指纹，授权留在机器上，不上云。这条与 magpie 无关：它管的是
     「用哪个模型」，不是「谁能连进来」。
7. **不做消息级 mux。** Flotilla 的中继搬字节流，TCP 背压与长度前缀已经做了同样的工作。
   内核需要 mux 是因为它的中继是 L7 消息管道——那是不同的管道性质，不是更好的做法。
8. **UI 的分组键是 (host, workspace)，不是机器。** 一台机器上两个 workspace 是两个
   daemon、两条连接，合并显示会抹掉「哪个目录里的活卡住了」。

## 一、地基：`ipc.Conn` 是唯一的连接抽象

### 1.1 代码事实

| 事实 | 位置 | 对方案的含义 |
|---|---|---|
| `Address{Channel, Name}` 只有本机命名空间 | `internal/ipc/transport.go:11` | 要加 `Kind`（pipe/unix/tcp）与 `Addr`；不破坏现有语义 |
| `dial` 是每平台的私有函数 | `transport_windows.go:52`、`transport_other.go:112` | 远程档在这里分叉，上层无感 |
| `ipc.Dial`/`Listen` 的非测试调用点只有 4 处 | `daemon/attach.go:101`、`daemon/client.go:23`、`daemon/daemon.go:221`、`host/host.go:288` | 加一个传输是「一个实现 + 一个字段」，不动上层 |
| 握手带 `Token`/`Role`/`Client`/`Session` | `internal/ipc/handshake.go` | 远程复用同一套认证字段，不需要第二套 |
| 控制面 JSON-RPC；数据面 `kind(1)+stream(4)+len(4)` | `internal/ipc/frame.go` | relay 只需当哑管道转字节；WebSocket 天然承载 binary |
| capability gate 已落地，只有 `diagnostics_v1` | `internal/ipc/capability.go:23` | **传输变化不需要新能力位** |
| daemon 的客户端只有 `Connect` 一个入口 | `internal/daemon/client.go:20` | 换掉 `ipc.Dial` 的地址解析，`flt` 与 GUI 自动获得能力 |

上游（daemon、CLI、GUI）不知道也不关心底下是 pipe、UDS 还是 TCP 转发端口。**方案必须保住这条。**

### 1.2 加 `tcp` 要改什么

1. **`Address` 加 `Kind` 与 `Addr`。** 不是加 `Remote` 字段——远程只是把地址换成一个
   本地转发端口，没有第二种寻址语义。
2. **`ipc.listen` 的 tcp 分支拒绝非环回地址**，不信任调用方。
3. **不加 capability 位。** capability 回答「对端能不能读懂这个字段」，传输回答
   「连接怎么建立」。加 `tcp_v1` 不裁剪任何字段，只会误导对端——这是类别错误。
4. **token 校验更重要，不是更松。** pipe 靠 DACL 限本机同用户；tcp 环回端口上那台机器
   的任何进程都能连，token 是唯一闸门。`EqualToken` 的常量时间比较必须保留。

## 二、参考实现（只写读过的）

### 2.1 外部五家

**herdr——SSH stdio 桥**（我们不用这个形状，但它的 socket 处理值得抄）

- 入口 `herdr --remote <target>`：本地起私有 UDS 监听（`refs/herdr/src/remote/attach.rs:2996`
  的 `SshStdioBridge`），每个本地连接转发到 `ssh <target> herdr <bridge 子命令>` 的 stdio。
- 远端 `run_remote_client_bridge` 先确保 server 在跑，再连本地 client socket
  （`src/remote/host.rs`）。
- 版本闸：endpoint 协议代次不一致时**拒绝并提示重跑交互式安装**，不静默降级。
- 抄：本地 socket 权限 0600、bind 私有监听、失败时按 identity 删 socket。
- 不抄：把「远端需要更新」做成硬失败——我们用 capability gate，宁可少发字段。

**Orca——出站 WSS relay + 拼接状态机**

- 桌面与手机**都向 relay 建立出站 WSS**，relay 配对并拼帧（`refs/orca/cloud/README.md`）。
- 拼接是显式九态状态机（`cloud/packages/relay-contract/src/splice-state-machine.ts`）。
  关键不变式：**两个转发 handler 都装好之前不许回 client 成功**
  （`mayAcknowledgeClient`），否则 client 挂在假拼接上。
- 关闭码有语义（`close-codes.ts`）：`4401` 凭据坏、`4404` host 离线、`4408` 对端掉、
  `4409` 走错 cell、`4429` 超限、`4503` 排水中。
- 限额是常量表（`protocol-limits.ts`）：首帧 2s、单帧 8MiB、每 host 8 连接、空闲 10min、
  host attach 10s、resume 确认 30s、control ping 15s / 静默 75s。
- host 身份用挑战-应答（`host-proof-transcript.ts`）：`HMAC-SHA-256` 对字段化 transcript
  签名，挑战里塞加密随机 secret。
- 抄：状态机、关闭码、限额表、挑战应答。**不抄**：director/cell/lease 的多区域编排。

**Paseo——Noise 加密的 relay 通道**

- relay 不可信，两端自加密：`Noise_NK_25519_ChaChaPoly_SHA256`
  （`refs/paseo/packages/relay/src/noise/index.ts`）。NK 意味着发起方预先知道响应方静态
  公钥——那正是配对时分发的 daemon 身份，没有第二份。
- 双模迁移：`legacy`（X25519 + NaCl box，无重放保护）与 `protected`（Noise，计数器防重放）。
  两端都是新版必选 protected；对端省略才允许 legacy；**带了非法 `protected` 永不回退**。
- 记录分帧：`chunk = kind(1) || flags(1) || totalLength(u32 BE) || data`，
  `wire = (u16 len || ciphertext)+`（`protected-records.ts`），relay 既不能合并、拆分、
  截断，也不能重排。
- 抄：**「声明了但非法 → 直接失败，不回退」**。能力位给了但内容坏掉是 bug，不是降级。

**t3code——托管隧道与可达性判定**

- 出站隧道用 cloudflared（`apps/server/src/cloud/ConnectReachability.ts`）。
- 可达性是**独立探测**：Cloudflare DoH 解析 `region1/2.v2.argotunnel.com` 的 IP
  （IP 字面量避免本机 DNS 被劫持），再对 `7844` 做 connect 超时判定，缓存并 5 分钟复检。
- 隧道重启有节流：530 要持续一段时间才重启，每次重启后等更久。
- 抄：可达性是独立事实，不是「HTTP 失败了就算不可达」。**不抄**：把 cloudflared 当依赖。

**内核 `DeepSeek-Reasonix`——形态最接近我们要做的（外部参考）**

- 远端 bootstrap 一个 `reasonix serve`，绑**随机 loopback 端口**，token
  **写文件不进 argv**（`internal/platform/remote/bootstrap/bootstrap.go:3`）。
- 本地经 SSH **端口转发**连那个端口（`internal/platform/remote/forward/`）。
- supervisor 六态 `Idle/Connecting/Connected/Reconnecting/Degraded/Stopped`，带 keepalive
  与指数退避（`remote.go:24-45`）。
- `remote.Client` 是**每条 SSH 连接一个**，`grep workspace client.go` 无结果——它不持有
  workspace。workspace 只用于**状态分片**：
  `<home>/.reasonix/remote/<workspace-slug>.serve.json`，slug 是「可读前缀 +
  路径哈希」，有碰撞测试 `TestRemoteWorkspaceSlugNoCollision`。
- 主机密钥双份：系统 `known_hosts` 只读参考 + 自有 TOFU 文件（`knownhosts.go`、
  `knownhosts_managed.go`）。
- `relay_write.go` 很短但解决一个毁链问题：gorilla/websocket 只允许单写者。
- `scopes.go` 的 `tunnelPaths` 把路径映射到 scope（files/terminal/desktop），未映射归 desktop。

### 2.2 维护者自己的两套（一手材料，优先级最高）

**relay-kit（`PROTOCOL.md`，协议名 `relay-e2e/v1`）**

- 一个 256-bit `code` 是全部凭证：`routingToken = HKDF(code, salt="relay-e2e/v1/routing")`，
  `tokenHash = sha256hex(routingToken)`。桌面用 `tokenHash` 注册，手机用 `routingToken`
  寻址。relay 只见 hash 与路由键，**没有 code 连 relay 自己都握不了手**。
- 3 步 PAKE 风格握手：`hi{epk,n}` → `hi{epk,n,cr}` → `ok{ci}`。密钥派生把双方公钥与
  nonce 绑进 transcript，`code` 进 HKDF salt。
- 帧加密：`IV = 0x0000_0000 || counter(uint64 BE)`，每方向独立严格递增；
  **重放防护是接收端行为**——拒绝 `counter ≤ 已见最大值` 并作废会话。诚实的对端不可能
  触发，只有复制帧的中间盒会。
- 抢占：同 `tokenHash` 的第二个 agent 注册成功，旧连接被 `4000` 踢掉，旧 agent 进
  `replaced` **不自动重连**，等用户显式操作。
- 手机挂起：agent 不在线时等待，60s 仍无 agent → `4404`；挂起期早帧缓冲上限 256 帧 / 64 KiB。
- 明确不实现：APNs、OAuth 账号绑定、team relay、非 E2E 兼容模式。

**Codex-Android（`SELF_HOST.md` / `RELAY_WATCHDOG.md` / `src/net/*`）**

- `SELF_HOST.md` 的结论：**自建 relay 技术上可行（协议公开、300 行骨架），但要连 accounts
  一起自建，2–3 天 + 一台服务器，换回来的仍是 200ms 轮询。** → 不要为了远程先建服务器。
- `RELAY_WATCHDOG.md` 是全场最有价值的一篇：半开 socket（休眠、NAT 过期、射频漫游）
  **不产生 TCP 错误**，send 写进本地缓冲、永远等不到 close。检测靠 `pingInterval(20s)`，
  界约 40s；桥层必须把 `onFailure` 转成**合成的 `close(1006)`**。写入在丢链时
  **直接失败不重试**——内核可能已经执行了。30s 请求超时与链路看门狗是两个不同问题。
- `deviceTrust.ts`：首次信任要人确认（显示分组指纹），之后每次连接**验证 pin**，
  key 变了**拒绝**；解 pin 是重装后重新批准的唯一途径。
- `sameMachine.ts`：自动切换只在两扇门指向同一台机器时允许；**缺席是拒绝，不是猜测**。
- `connection.ts`：**reachability 与 stream health 是两个事实**，用一个数字表示会说谎。
- `transport.ts`：`EventChannel = "sse" | "mux" | "poll"`，把「这一帧走哪条车道」显式化。

### 2.3 多机模型的四种写法

| 产品 | 身份 | 传输与可达性 | 证据 |
|---|---|---|---|
| Paseo | `AgentAddress{serverId, agentId}` | 明确分离 | `packages/protocol/src/addresses.ts` |
| Orca | `HostProfile{id, endpoint, publicKeyB64, relay?}` | `credentialStatus` 单独一维 | `mobile/src/host-catalog/host-profile.ts` |
| happier | `machineId` + `machine-scoped` socket | 连接类型三档 | `docs/protocol.md` |
| 内核 | `Client`（每 SSH 连接一个）+ workspace slug | `Status` 六态独立于 workspace | `internal/platform/remote/` |

Paseo 的注释说得最准：**「Agent identity is qualified by its daemon; transport and
reachability are separate.」** 四家共识：身份按机器（或机器+workspace）限定，
**可达性不是身份的一部分**。

## 三、连接方案

### 3.1 档 A：SSH 端口转发 + 远端常驻 daemon（零服务器，先做）

```
GUI ──┬── pipe ──▶ 本机 daemon（channel=stable）
      ├── tcp ───▶ 127.0.0.1:P1 ──ssh -L──▶ 远端A daemon（workspace=/srv/app）
      └── tcp ───▶ 127.0.0.1:P2 ──ssh -L──▶ 远端B daemon（workspace=/home/dev/api）
```

**为什么不是 stdio 桥**

| 维度 | stdio 桥 | 端口转发 |
|---|---|---|
| 复用现有传输 | 要新写 `net.Conn` 包装 | 得到 TCP 端口，`ipc.Dial` 原样可用 |
| 断线语义 | ssh 退出 = 全断，重建整条链 | 转发没了但远端 daemon 还活着，重连即续 |
| 多会话 | 一条管道自己多路复用 | 每连接一条转发，天然独立 |
| 远端进程 | 每次连接起一个 | 常驻一个，按需复用 |
| 失败可见性 | 只有退出码 | 能表达「连上了但转发没挂上」 |

**实现要点**

- **每个 (host, workspace) 一个远端 daemon**（见「结论先行」第 3 条）。远端只绑 `127.0.0.1` 的
  **随机端口**，token 写文件（`ipc.WriteTokenFile` 已有，`restrictToOwner` 收紧 ACL），
  **绝不进 argv**。
- **bootstrap**：远端安装/定位 `flt`、起进程、记录端口与 token 以便复用。主要工作量。
  - Windows 远端用 PowerShell `-EncodedCommand`（base64 of UTF-16LE）——cmd 的引号规则
    **被消除而不是被解决**，那才是会变成安全 bug 的地方。
  - home 用 SFTP `RealPath(".")` 而非 `~`，因为 Windows OpenSSH 不解析 `~`。
  - **路径单向派生**：SFTP 拼写（`/C:/Users/...`）与原生拼写（`C:\Users\...`）是两种
    字符串，把文件层拼写喂回内核会让它打开 `C:\C:\Users\...`。
- **supervisor**：六态（UI 词 `idle/connecting/live/degraded/reconnecting/stopped`，
  内核词是 `connected` 而非 `live`），带 keepalive 与指数退避。
  **`degraded` 必须说明哪一条转发坏了**，不能只给颜色。
- **转发**：
  - 本地 `-L` 监听器**跨重连保留**，否则 GUI 里存的地址每次断线都变。
  - 用 `127.0.0.1:0` 让 OS 分配，绑定后记住实际端口；冲突用 typed error，不做字符串匹配。
  - **显式写 `-L 127.0.0.1:P:...`**：不写绑定地址时，某些 `ssh` 配置（`GatewayPorts`）
    会把端口绑到 `0.0.0.0`，那是把远端 daemon 暴露给局域网。
- **主机密钥**：系统 `known_hosts` 只读参考 + 自有 TOFU 文件；首次要人，之后每次验证，
  变化即拒绝并给出显式「重新确认」路径。
- **前置条件（写死）**：**单写者写泵**。远程会引入多客户端共享一个 daemon 连接，
  `internal/daemon/relay.go:43` 的 `peerQueue` 是唯一写路径，任何新增写者都必须走它。
  内核 `relay_write.go` 的注释说明了不加的后果。
- **验收** `TestRemoteForwardOverSSH`：测试里以 `TestHelperSSHBridge`（一个把 stdin/stdout
  当字节管道转发的测试二进制）代替 `ssh`；`flt status` 经转发拿到 fleet；断链后重连，
  远端会话仍在；两个远端 workspace 的会话 id 不串。
- **不解决**：手机蜂窝场景（SSH 在手机上不现实）。

### 3.2 档 B：字节级 E2E relay（手机端才需要）

**直接复用 `relay-kit` 的 `relay-e2e/v1`，不另造。**

- 已有 300 行可运行的 Node 参考实现（`test/fixtures/relay-server.mjs`）与一份 Cloudflare
  Workers 实现，可以直接对着跑互操作测试。
- 它已把最难的部分做对：code 即全部凭证、接收端重放防护、抢占语义、挂起与超时码。
- 维护者自己维护过 Alma 与 Codex-Android 两条链路，踩坑记录是现成的。
- 只需写两端：Go 的 relay agent（桌面侧，出站 WSS）与 Flotilla 手机的 E2E 客户端。
  桌面侧把 E2E 明文接到**本机 daemon 的端点**（pipe/UDS/tcp 皆可，因为 §1 的抽象），
  于是又回到「`ipc.Conn` 之上一切不变」。
- **分帧**：隧道内跑 `ipc` 的帧格式（`kind+stream+len`），不是 HTTP。这与 Alma/Codex
  不同（它们转 HTTP/SSE），因为 Flotilla 的 daemon 本来就是 JSON-RPC over 流。
- **不做消息级 mux**：内核 `CLOUD_MUX_PLAN.md` 需要 mux 是因为它的中继是 L7 消息管道，
  没有字节背压。Flotilla 的中继搬字节，TCP 背压天然可用，长度前缀自带边界。
  `Reasonix-Android/docs/RELAY_DESIGN.md` 否决 alma-relay Protocol 3 的理由
  对 Flotilla 依然成立。**只有将来需要按消息授权或多人协作时才引入。**
- **验收** `TestRelayKitInterop`：Go 桌面 agent ↔ relay-kit 的 Node fixture，跑完握手、
  重放攻击被拒、抢占后旧连接收到 4000。

### 3.3 档 C：托管隧道——不做

用 Cloudflare Tunnel 暴露本地 daemon 的 HTTP 门面，但 Flotilla 现在没有 HTTP 门面，
为了隧道加一层 HTTP 性价比低。**不做**，记在这里是为了说明「看过、为什么不做」。

## 四、认证与配对

### 4.1 配对物料（档 B）

- 一次性 256-bit code，二维码 + 可手输的分组形式（我们的 scheme 用 `flotilla://`）。
- code 同时派生路由键与 PAKE salt，**不写入磁盘**；手机只在安全存储里留 pin 的指纹。
- 二维码有效期 10 分钟、最多 5 次尝试（照 Orca 的 `inviteTtlMs` / `inviteMaxAttempts`）。

### 4.2 三条不可配置的硬规则

读内核与 `STUDIO_SHELL_BOUNDARIES.md` 之后补入，是「错了就是洞」的类型：

1. **token 不进 argv。** 进程列表在本机任何同用户进程可读。Flotilla 的
   `ipc.WriteTokenFile` 已做对；远端 bootstrap 的参数里只许出现 token 的**文件路径**。
2. **首次信任要人，之后每次验证。** 系统 `known_hosts` 只读 + 自有 TOFU 文件双份判定；
   变化即拒绝，并给出显式「重新确认」路径。
3. **环回边界不可配置。** 远端 daemon 只许绑 `127.0.0.1` 的随机端口，不许 `0.0.0.0`，
   不许绑主机名。本地 `ssh -L` 也必须显式绑 `127.0.0.1`（见 §3.1）。
   **这条写进代码，不是文档里的建议。**

## 五、状态模型（诚实优先）

### 5.1 三层，不能用一个布尔

| 层 | 含义 | 说谎的后果 |
|---|---|---|
| `link` | GUI ↔ daemon（或 relay 段）是否活着 | 与「会话是否在跑」混淆，用户以为活干完了 |
| `session` | host 是否答得上（attach 成功） | 与 `link` 混淆，重连按钮按了没反应 |
| `remote` | 远端机器是否可达（档 A 是转发是否挂着，档 B 是 relay 段 + agent 在线） | 与 `session` 混淆，用户去查错地方 |

### 5.2 `link` 是六态，不是三态

```ts
export type Link =
  | "idle"         // 还没开始连
  | "connecting"   // 首次连接中
  | "live"         // 心跳在阈值内（内核词是 Connected）
  | "degraded"     // 连上了，但至少一条转发/会话不可达；必须带 broken: string[]
  | "reconnecting" // 掉线后正在退避重连
  | "stopped";     // 主动关闭或重连预算耗尽
```

`degraded` 是关键：早期稿的三态（`live | stale | lost`）没有位置放它，它会落到
「`link` 活着但某个会话打不开」——正是用户最常撞到的「连着呢怎么打不开」。

`stale` **不是状态值，是正交标记**：`live` 但超过一个心跳周期没有新帧时打上它，
UI 说「正在确认」。它不改变 `Link`，因为「慢」和「坏」是两件事。

`reachable` 这个现有布尔要拆开；`unknown` 永远是 `unknown`，不当作完成。

## 六、桌面 UI：按 (host, workspace) 分段

### 6.1 缺口

舰队卡片现在显示 `session-id`、lifecycle badge、`mode` chip、目录 chip、command、
`unreachable` badge（`gui/src/components/FleetList.tsx`）。远程之后用户无法回答
「这条会话在哪台机器、哪个目录里」——那是第一个问题。

### 6.2 方案

舰队列表按 **(host, workspace)** 分段，每段一个头，**不引入新的导航层级**：

```
┌ 本机 · 3 会话 ────────────────────┐
│ ● blocked  sess-a   acp   ...     │
│ ● working  sess-b   acp   ...     │
└───────────────────────────────────┘
┌ build-01 · app · 2 会话 ──── 12ms ┐
│ ● done     sess-c   pty   ...      │
│ ◌ unknown  sess-d   acp   ...      │
└────────────────────────────────────┘
```

段头显示 `host · workspace`，workspace 用 `dirLabel()` 取末段（已有函数）。

三条规则：

- **段头是分隔，不是可折叠面板。** 折叠会藏住 `blocked`，与「等你拍板的恒在最上」
  直接冲突。分段后**全局排序不变**：`blocked(0) < done(1) < unknown(2) < working(3) <
  idle(4)` 仍跨段生效，分段只是视觉分组。
- **段状态与会话状态分开报。** 段头直接投影 §5.2 的六态，不另造词：
  `live`（绿点）、`degraded`（琥珀点，**不转圈**——它已经连上了，只是某条转发坏了）、
  `reconnecting`（琥珀点 + 转圈，转圈只在真重连时动）、
  `connecting`（灰点 + 转圈，首次连接）、
  `idle`（灰点，无动画）、`stopped`（红点，无动画）。
  **只有 `live` 之外的状态才让该段会话显示 `unknown`**：`live` 段保留各会话的真实五态，
  其余段一律 `unknown`，不显示上次的 `done`。
- **非 `live` 段的会话不删。** 保留最后已知状态并打 `stale` 标记，点击时尝试重连；删除是用户动作。

### 6.3 投影（纯函数，可测）

`machineGroups(status)` 输入 `Status`，输出
`{ key, host, workspace, link, broken, sessions }[]`，`key = host + "\0" + workspace`。
**排序不在这里**，继续由 `orderSessions` 负责，两者正交。

### 6.4 视觉

不新增颜色，沿用现有 tokens：

| 含义 | token | 说明 |
|---|---|---|
| live | `--ok` | 与 `done` 同色系但不共用 badge，段头用 6px 圆点 |
| degraded | `--warn` | 琥珀点，**无动画**——不是「在努力」，是「部分坏了」 |
| reconnecting | `--warn` | 琥珀，与 `blocked` 同色——**这正是问题**，见下 |
| connecting / idle | `--muted` | 灰点；`connecting` 带旋转环，`idle` 不带 |
| stopped | `--down` | 红 |
| 远端段 | `--dim` 文本 + `--sunken` 底 | 段头比会话卡轻一级 |

**冲突处理**：`reconnecting` 与 `blocked` 都用琥珀。解法是形状而非颜色——`blocked` 是
会话卡上的**实心 pill**，段头是**一个 6px 圆点 + 旋转的环**；位置与形状不同，且段头
永远在会话卡之外。`degraded` 用同一个点但**不转**，于是「在重试」与「部分坏了」靠动画
区分，不靠新颜色。若实测仍混淆，`reconnecting` 改用 `--dim` + 动画环（不引入新色相）。

## 七、手机 UI：独立信息架构

### 7.1 为什么不沿用桌面布局

桌面是「左栏舰队 + 右栏会话」双栏。`--fleet-width: 320px` 在 390pt 宽的机器上就没了主区。
Codex-Android 给出的答案是**底部三入口**（最近 / 项目 / 派发），这是经过真实使用的
结论，照抄。

### 7.2 三层结构

```
┌──────────────────────────┐
│  ←  Fleet        ⌘ 状态条 │  ① 连接与机器（一行，可展开）
├──────────────────────────┤
│  ⚠ 2 need you            │  ② 需要你：置顶注意力条
├──────────────────────────┤
│  sess-a        blocked   │
│  sess-b        working   │  ③ 会话列表（按全局排序）
│  ...                     │
├──────────────────────────┤
│  最近   项目   派发        │  ④ 底部三入口
└──────────────────────────┘
```

- **① 状态条**：一行，显示当前段 `host · workspace` + `Link` 状态。点击展开成段列表
  （切段）。**永远只有一行**，不占版面。
- **② 注意力条**：只有 `attention > 0` 时出现，点击直接跳到最上面的 `blocked` 会话。
  这是整个手机界面**唯一允许用琥珀色大面积填充**的地方。
- **③ 会话列表**：与桌面同一套排序函数、同一套 badge。**不重排、不截断**。
- **④ 三入口**：最近（跨项目聚合）/ 项目（按目录分组）/ 派发（新建任务）。

### 7.3 会话详情：单列，动作收在底栏

```
┌──────────────────────────┐
│ ← sess-a    blocked  ⋯   │
├──────────────────────────┤
│  (日志 / diff，单列滚动)   │
│                          │
├──────────────────────────┤
│ [ Ask first ▾ ] [ 回答 ]  │  ⑤ 权限卡固定在底栏上方
└──────────────────────────┘
```

- 权限卡**不是浮层**，是底栏上方的固定条：浮层会盖住上下文，而回答权限恰恰需要看上下文。
- `⋯` 菜单只放**允许的操作**（评论、取消回合、看用量）。**不放**推送、开 PR、丢弃变更、
  终端——理由见 §9，且是 daemon 侧拒绝，不是这里藏起来。
- 长按会话卡 = 多选（批量取消/已读），不做拖拽排序（触摸误触率高）。

### 7.4 手机专属的诚实规则

| 规则 | 来源 | UI 表现 |
|---|---|---|
| 半开连接不产生错误 | `RELAY_WATCHDOG.md` | 40s 内可能仍显示 `live`；**不显示「已断开」直到确定**，此时打 `stale` 标记，`Link` 本身不变 |
| 写入不重试 | 同上 | 发送失败直接标红并保留草稿，**不自动重发**；重发是用户动作 |
| 后台定时器冻结 | 同上 | 回前台立即重建流（带 cursor），**不显示「已同步」直到第一帧到达** |
| 自动切换只在同机 | `sameMachine.ts` | 自动切段只在两段指向同一台机器时允许；`unknown` 不自动切 |
| 指纹变了要拒绝 | `deviceTrust.ts` | 首次显示分组指纹；变化时红屏阻断，给出「重新配对」 |

### 7.5 触控与可读性

- 最小触控目标 44×44（`--sp-*` 最大步长 20px，需要一条新规则，不是新 token）。
- 正文 `--fs-body: 14px` 在手机上偏小，手机侧用 15px 起；行高 1.5 不变。
- 会话卡在手机上**去掉 command 一行**（长命令会撑破卡片），改为在详情页显示。
  这是唯一「移动端隐藏信息」的地方，要在 PR 里注明。

## 八、配对流程（两屏）

```
屏 1（桌面）                      屏 2（手机）
┌────────────────────┐           ┌────────────────────┐
│ Connect a phone    │           │ Scan               │
│                    │           │                    │
│   ▓▓▓▓▓▓▓▓▓▓       │  ──扫──▶  │   [相机取景]        │
│   ▓ QR ▓▓▓▓▓       │           │                    │
│   ▓▓▓▓▓▓▓▓▓▓       │           │  或粘贴链接         │
│                    │           │                    │
│ 9F3A 7C21 B4E8 ... │           │  指纹 9F3A 7C21…    │
│ 10 分钟内有效       │           │  [ 信任并连接 ]      │
└────────────────────┘           └────────────────────┘
```

- 二维码**旁边必须有可手输的分组码**（相机权限被拒、屏幕坏、远程口头转述）。
- 桌面屏明确写有效期与「仅此一次」。
- 手机屏在连接前**显示将要 pin 的指纹**，让用户能核对——这是「首次要人」。
- 失败只有三种文案：`expired` / `already paired elsewhere` / `fingerprint changed`。
  **不给「未知错误」**。

## 九、手机端权限边界

**做**：看会话、读 diff、写评论、回答权限问题、取消回合、看用量。

**不做**（配对设备凭据较弱，破坏性与外发操作留在桌面）：

- 丢弃变更、推送、开 PR
- 任意终端（PTY 会话的键盘输入）
- 改适配器/开关等本机配置
- 删除会话

与 Codex-Android 里核实的 `host_routes.go` 一致：内核把 `POST /changes/discard`、
`GET/POST /git/push` 和终端路由列进 host-only 表，手机不渲染这些按钮。
**我们的实现方式是在 daemon 侧按 `Role` 拒绝，不是靠手机自觉。**

## 十、不做什么

- 不做远程的账号服务（授权留在机器上，见「结论先行」第 6 条）。
- 不做厂商账号池与登录流（留给 magpie，同上）。
- 不做多区域 relay 编排（没有那个规模）。
- 不做 HTTP/SSE 门面（协议是 JSON-RPC over 流，不需要）。
- 不做消息级 mux（见 §3.2）。
- 不做明文兼容模式（E2E 是默认且唯一）。
- 不为了远程引入 cloudflared 之类的外部二进制。
- 不做主题切换（tokens 一次定死，见 `FLOTILLA.md`）。
- 不做 i18n（M2 起中文先行，现在不铺）。
- 不做平板专用布局（先按手机与桌面两端做好）。
- 不做会话列表虚拟化（先量；50 条以内的真实舰队没有这个需要）。
- 不在手机上渲染终端（PTY 输入是桌面专属）。

## 十一、里程碑与实现顺序

### 11.1 里程碑落点

| 档 | 落在 | 前置 |
|---|---|---|
| A | PLAN M3「多机（SSH 节点入列）」 | `ipc` 的 `tcp` 传输；远端 bootstrap + supervisor；单写者写泵 |
| B | PLAN M3「跨设备 relay」 | 档 A 的传输抽象；relay-kit 协议互操作测试 |
| C | 不做 | — |

档 A 可提前到 M2 末尾（不依赖 Orchestrator，能立刻解决「两台机器一个窗口」），
**但成本是 1–2 周**（bootstrap + supervisor + known_hosts），不是 1–2 天。

### 11.2 UI 实现顺序

| 步 | 内容 | 验收 |
|---|---|---|
| 1 | `Link` 六态投影 + `machineGroups` 纯函数 | 单测：三段分组、排序不变、`degraded` 带上 `broken`、非 `live` 段的会话一律 `unknown` |
| 2 | 桌面舰队分段头 | 真窗口截图；10 会话 + 2 段的排序回归 |
| 3 | 配对两屏（桌面 + 手机） | 截图；二维码过期/指纹变化三条错误路径 |
| 4 | 手机三入口 + 注意力条 | 截图（窄窗 390pt）；触控目标核验 |
| 5 | 手机会话详情与权限底栏 | 截图；权限回答走通 |

第 1–2 步与远程连接解耦，可先行；第 3 步起依赖档 B。

### 11.3 动效与性能（沿用既有红线）

- 段头进入：与卡片同一个 `card-in`，不新增 keyframes。
- 重连环：**只在 `reconnecting` 期间**转；进入 `live` 立即停。不用 CSS 无限动画，
  用一次性 `transition` + 状态切换（避免「常驻动画循环」违反红线）。
- 手机上禁用卡片的 stagger 入场（列表长、触摸场景，入场延迟会让首屏变慢）。

## 十二、未核实项（如实列出）

- 同一 workspace 被**两台机器上的两个客户端同时要**时：内核靠远端 state 文件的 pid 检查
  （`serveLock` 是远端文件锁，跨机器不互斥）。Flotilla 是否需要跨机互斥未定。
- 会话 id 加机器作用域后，daemon 侧是否要改（倾向不改：作用域是 GUI 的投影）。
- 内核 `bootstrap/install.go`（11KB）的安装路径选择逻辑未细读；`flt` 二进制更小，
  可能不需要那套复杂度。
- 档 B 的 Go 侧 WSS 客户端选型（`nhooyr.io/websocket` vs `gorilla/websocket`）未定；
  relay-kit 的 Node fixture 用 `ws`，互操作要实测。
- 手机端技术栈未定（Capacitor 复用 renderer，还是原生）；两套先例分别是 Capacitor 壳
  与原生 Kotlin，代价差异很大，见 Codex-Android 的 `PRIOR_ART.md`。
- 手机端是否需要同时连多台机器（Orca 的 catalog 支持；Flotilla 先做单机是否够未定）。
- 二维码/指纹的具体编码未定。
- `ssh` stdio 之外的二进制安全：端口转发本身是 TCP，无此问题；但 `TestHelperSSHBridge`
  这一测试替身的具体形态未设计。
