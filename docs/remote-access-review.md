# 对 `docs/remote-access.md` 的复核（2026-11-22）

> 读了 `C:\Dev\Reasonix` 下四个仓库的远程实现之后，对本文档前身（同日设计稿）
> 的批判性复核。**结论：三档路线基本成立，但档 A 的形态选错了，且漏了两条更重要的教训。**
> 本文修正 `remote-access.md`，不另起一份方案。

## 一、最重要的发现：档 A 不该用 stdio 桥

上一稿说「SSH stdio 桥（herdr 形状）」，理由是零服务器、协议零改动。读了内核的实现
之后，这个判断是**错的**，至少对 Flotilla 是错的。

内核 `internal/platform/remote` 的做法是：

- 远端 bootstrap 一个 `reasonix serve` 进程，绑**随机 loopback 端口**，
  token **写文件不进 argv**（`bootstrap/bootstrap.go:3`）。
- 本地经 SSH **端口转发**连那个 loopback 端口（`internal/platform/remote/forward/`）。
- 连接由 supervisor 管：`StatusConnecting / Connected / Reconnecting / Degraded / Stopped`
  六态，带 keepalive 与指数退避（`remote.go:24-45`）。
- 有 jump chain、known_hosts（系统只读 + 自有 TOFU 文件）、凭据复用
  （`dial.go`、`knownhosts.go`、`sshconfig.go`）。

**为什么端口转发优于 stdio 桥**：

| 维度 | stdio 桥（上一稿） | 端口转发（内核做法） |
|---|---|---|
| 复用现有传输 | 需要新写一个 `net.Conn` 包装 | 直接得到一个 TCP 端口，`ipc.Dial` 原样可用 |
| 断线语义 | ssh 子进程退出 = 全断，重连要重建整条链 | SSH 断了端口转发就没了，但远端 serve 还活着，重连即续 |
| 多会话 | 一条 stdio 管道要自己多路复用 | 每个连接一条转发通道，天然独立 |
| 远端进程 | 每次连接起一个 | 常驻一个，按需复用（`EnsureServe` 有 `Reused` 字段） |
| 失败可见性 | ssh 退出码 | `StatusDegraded` 能表达"连上了但转发没挂上" |

**对 Flotilla 的落点因此改成**：

```
GUI ──pipe──▶ 本机 daemon ──TCP──▶ 127.0.0.1:<随机端口>
                                      ▲
                                      │ ssh -L 转发
                                      │
                              远端 flt-daemon（常驻，绑 loopback）
```

- `ipc.Address` 不需要 `Remote` 字段。远程档只是**把 daemon 的地址换成一个本地转发端口**。
- `ipc.Listen/Dial` 需要支持 `tcp` 传输（今天只有 pipe/UDS），这是一处很小的增量。
- 远端需要一个 `flt-daemon` 常驻进程与一个 bootstrap 流程（安装、随机端口、token 文件、
  记录状态以便复用）。这是真正的成本，比 stdio 桥大，但换来的是**会话在断线后仍在**。

## 二、漏掉的第一条教训：单写者写泵

内核的 `relay_write.go` 很短，但它解决的是一个会毁掉整条链的问题：

> `relayWriter` serializes the device socket's writers: command goroutines, push pumps
> and the connect loop share one connection, and gorilla/websocket allows only one
> concurrent write.

Flotilla 现在没有这个问题，因为**每条连接只属于一个客户端**（daemon ↔ host 是一对一）。
但远程档会引入共享：

- 多个 GUI 窗口连同一个远端 daemon；
- daemon 要把 host 的输出扇出给多个客户端。

`internal/daemon/relay.go` 已有 `peerQueue`（`relay.go:43`）做每 peer 排队，方向是对的。
**远程档必须在设计里写明：任何新增的写路径都要走同一条队列，不许有第二个写者。**
这条要加进 `remote-access.md`，并且是档 A 的前置条件，不是档 B 的优化项。

## 三、漏掉的第二条教训：状态不是布尔

上一稿写了三层（`link`/`session`/`remote`），方向对，但内核的六态比我的三态更精确：

```
Idle → Connecting → Connected → Reconnecting → Degraded → Stopped
```

关键在 **`Degraded`**：连上了，但至少一条转发没挂上。上一稿的三层模型里没有位置放
这个事实——它会落到 `link=live` 但某个会话不可达，用户看到的就是"连着呢但打不开"。

修正：`link` 的值域改为
`idle | connecting | live | degraded | reconnecting | stopped`，
并且 `degraded` 必须带上"哪一条坏了"（哪个转发/哪个会话），不能只给一个颜色。

## 四、必须否掉的一条：协议级中继

`Reasonix-Android/docs/RELAY_DESIGN.md` 明确否决了 alma-relay 的 Protocol 3
逐流流控，理由是「TCP 透传天然有背压，中继不会堆积」。这条否决**对 L4 透传仍然成立**。

但 `CLOUD_MUX_PLAN.md` 又推翻了它，理由也成立：账号中继是 **L7 消息管道**，没有字节背压，
只有消息，慢消费者会阻塞。两者的区别是**管道性质**，不是谁对谁错。

对 Flotilla 的含义：

- 如果档 B 走的是「中继转发 TCP/WebSocket 字节」，就不需要 mux——Flotilla 的帧本身就是
  长度前缀的字节流，TCP 背压天然可用。**这是 Flotilla 相对内核的结构性优势。**
- 只有当中继必须变成「消息级路由」（例如未来做多人协作、按消息做授权）时才需要 mux。
  **在那之前不做。**

上一稿说「隧道内跑 Flotilla 自己的 `kind+stream+len` 帧而不是 HTTP/SSE」——这条是对的，
而且理由比上一稿写的更强：它让背压免费，也避开了 mux 那一整套复杂度。

## 五、需要补进方案的三条硬规则

### 5.1 token 不进 argv

内核 `bootstrap.go` 的注释明确写了「file-based token (never in argv)」。理由：进程列表
在本机任何同用户进程可读，argv 里的 token 等于公开。

Flotilla 已经做对了（`ipc.WriteTokenFile` + `restrictToOwner`，`flt-daemon` 从文件读）。
**远程档要保住这条**：远端的 bootstrap 参数里不许出现 token，只许出现文件路径。

### 5.2 首次信任要人，之后每次验证

内核的 known_hosts 是「系统 known_hosts 只读 + 自有 TOFU 文件」双份
（`knownhosts.go`、`knownhosts_managed.go`）。这条比我上一稿抄的 `deviceTrust.ts`
更完整：**系统文件是只读参考，自有文件是可写的 TOFU 记录**，两者都参与判定。

Flotilla 的档 A 应该照做：读系统 `known_hosts` 只作参考，自有 TOFU 文件记录首次确认的
主机密钥；变化时拒绝，并给出"重新确认"的显式路径。

### 5.3 环回边界不可配置

内核 `STUDIO_SHELL_BOUNDARIES.md` 的第 2 条：

> `tcp4 127.0.0.1:0` — never a wildcard, never a name a resolver answers for.
> A credential minted per launch from `crypto/rand`, never read from config and never persisted.

Flotilla 的 daemon 今天绑的是 pipe（Windows）/ UDS（其他），没有网络面，天然满足。
**远程档要显式保住这条**：远端的 daemon 只许绑 `127.0.0.1` 的随机端口，
不许 `0.0.0.0`，不许绑定到主机名。这条要写进代码而不是文档。

## 六、对上一稿其他部分的确认

以下是读完之后仍然成立的，不改：

- 「远程不是新协议，是 `ipc.dial` 的第二种传输」——**成立**，而且端口转发比 stdio 桥
  更彻底地证明了这一点（连 `net.Conn` 包装都不需要）。
- 手机端只读/批注/审批——**成立**，与内核 `scopes.go` 的 `tunnelPaths` 表同构。
  内核把路径映射到 scope（files/terminal/desktop），未映射的归 desktop；我们照这个形状，
  在 daemon 侧按 `Role` 拒绝。
- 不自建 accounts、不做多区域、不做明文兼容——**成立**。
- 档 C 托管隧道不做——**成立**，而且内核也没做（它走的是 frp/Tailscale 路线）。

## 七、修正后的落点

| 档 | 形态 | 与上一稿的差异 |
|---|---|---|
| A | SSH **端口转发** + 远端常驻 `flt-daemon` | 从 stdio 桥改为端口转发；成本更大，但会话断线可续 |
| B | 字节级中继（不是消息级） | 明确否掉 mux，除非将来需要按消息授权 |

档 A 的成本因此上修：需要 bootstrap（远端安装、随机端口、token 文件、状态记录）、
supervisor（六态 + 退避）、known_hosts 双份。这不是 1–2 天，是**一到两周**。
但它同时解决了「两台机器一个窗口」与「断线不丢会话」两件事，而 stdio 桥只能解决前者。

## 八、仍未核实

- 内核的 bootstrap 如何处理 Windows 远端（`windowsshell.go` 有 10KB，未细读）。
- `forward.Set` 的端口冲突处理（`addrinuse.go` 存在，逻辑未细读）。
- Flotilla 的 `ipc` 加 `tcp` 传输时，capability gate 是否需要新能力位（倾向不需要：
  传输变了，协议没变）。
- 远端 `flt-daemon` 与本地 daemon 的关系：两个独立 daemon 各管各的会话，
  还是本地 daemon 代理远端？上一稿假定后者（一个窗口看两台机器），但内核的做法是
  **每个远端一个 `serve` 进程，本地按 workspace 路由**。这条要重新设计，是档 A 的核心问题。
