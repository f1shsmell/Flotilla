# 远程设计：四个未决项的展开调研（2026-11-22）

> 续 `remote-access-review.md` 第八节。本文回答四项，每项都给出**内部实现证据**
> （`C:\Dev\Reasonix`）与**外部实现对照**（refs 四家 + 联网查证）。
> 结论优先，证据在后。

## 结论摘要

| # | 问题 | 结论 |
|---|---|---|
| 1 | 远端 daemon 与本地 daemon 的关系 | **不是代理关系，是并列关系。** 远端 serve 按 **workspace** 分片（不是按机器），本地为每个 (host, workspace) 建一条独立连接；GUI 侧把多个连接聚合成一个舰队视图。照内核，不照我上一稿的"本地代理远端" |
| 2 | Windows 远端 bootstrap | 用 PowerShell `-EncodedCommand`（UTF-16LE base64）绕开 cmd 引号；home 用 SFTP `RealPath(".")` 而不是 `~`；路径大小写与斜杠有归一化规则 |
| 3 | `forward.Set` 的端口冲突 | 本地 `-L` 监听器**跨重连保留**（URL 稳定）；远端 `-R` 每次重连重建，**先试原端口再退回新端口**；冲突用 typed error `ErrBindBusy` 而非字符串匹配 |
| 4 | `ipc` 加 `tcp` 是否需要新 capability 位 | **不需要。** 传输变化对协议不可见；但需要新增 `Address.Kind`，且**握手里的 token 校验必须保留**（TCP 上比 pipe 更需要） |

## 一、远端 daemon 与本地 daemon：并列，不是代理

### 1.1 内核的事实

- `remote.Client` 是**每条 SSH 连接一个**（`internal/platform/remote/client.go` 的
  `type Client struct`）。它持有 `forward.Set`、SFTP volume、jump 链，**不持有任何
  workspace 概念**——`grep workspace client.go` 无结果。
- workspace 出现在 `bootstrap.Options.Workspace`（`host.go:29`）与
  `ResolvedHost.Workspace`（`host.go:29` 的字段），**只用于状态分片**：

  ```
  StatePaths.StateJSON = <home>/.reasonix/remote/<workspace-slug>.serve.json
  StatePaths.TokenFile = <home>/.reasonix/remote/<workspace-slug>.token
  ```

  证据：`bootstrap/state.go` 的 `pathsFor(home, workspace)`，
  slug 来自 `store.RemoteWorkspaceSlug(workspace)`。
- slug 是「可读前缀 + 路径哈希」（`internal/state/store/remote_test.go` 的断言：
  `/home/dev/projects/app` → `home-dev-projects-app-<hash>`），带碰撞测试
  `TestRemoteWorkspaceSlugNoCollision`。
- 复用判定在 `EnsureServe` 里：先 `tryReuse`（读 state 文件 + 查 pid 是否活着），
  再抢远端锁（`lock.go`，`serveLockStaleAfter = 60s`）后**再查一次**（避免并发重复拉起）。
- 版本不够的旧内核**会被停掉再拉起**（`TestEnsureServeStopsTheOutdatedKernelItReplaces`）。

### 1.2 这否掉了我上一稿的假设

上一稿写「本地 daemon 代理远端」，即一个 daemon 成为另一台的网关。内核**没有这么做**，
而且这个选择的理由很清楚：

- 代理要转发**全部**方法（会话、diff、worktree、git），等于把远端 daemon 的整个
  方法面在本地重新实现一遍，且每加一个方法都要改两处。
- 并列则每台机器各跑各的 daemon，本地只是**多连几条**。新方法只在远端实现一次。
- 断线语义也更好：一条连接断了只影响那一个 workspace，不拖累其他机器。

### 1.3 对 Flotilla 的落点

```
GUI ──┬── pipe ──▶ 本机 daemon（channel=stable）
      ├── tcp ───▶ 127.0.0.1:P1 ──ssh -L──▶ 远端A daemon（workspace=/srv/app）
      └── tcp ───▶ 127.0.0.1:P2 ──ssh -L──▶ 远端B daemon（workspace=/home/dev/api）
```

- **每个 (host, workspace) 一个远端 daemon**，与内核一致。
- GUI 侧持有 N 个 `DaemonClient`，`machineGroups()`（`ui-remote-design.md`）把它们聚合。
- 会话 id 需要机器作用域：`<host>/<session-id>`。Paseo 的 `AgentAddress{serverId, agentId}`
  就是这个形状，注释写得很准：**「Agent identity is qualified by its daemon;
  transport and reachability are separate.」**
- Orca 的手机侧是同样的形状：一个 catalog、多个 `HostProfile`，按 `lastConnected` 排序
  （`host-catalog-selection.ts`），**不是代理**。

**这条同时回答了上一稿的「一个窗口看两台机器」**：能做，但实现方式是**并行多连接**，
不是代理。代价是 GUI 的连接管理从 1 条变成 N 条。

## 二、Windows 远端 bootstrap

### 2.1 内核的做法

`bootstrap/windowsshell.go` 三处值得抄：

1. **`-EncodedCommand`**：命令以 base64(UTF-16LE) 传给 PowerShell，**cmd 的特殊字符问题
   被消除而不是被解决**。注释原话：「cmd's escaping rules are where this would have
   become a security bug.」这是对的取舍——不要在 Windows 引号规则上做安全。
2. **home 用 SFTP `RealPath(".")`**：Windows OpenSSH 不解析 `~`，但每个 SFTP 会话从用户
   目录开始，所以文件层自己的 `.` 就是答案，不需要第二条命令。
3. **路径归一化**：`Absolute()` 把 `\` 统一成 `/`、盘符大写、去尾斜杠；`hasDriveRoot()`
   明确区分 `C:`（该盘的当前目录，**相对**）与 `C:\`（根，**绝对**）。
   `Paths()` 保留 SFTP 拼写，shell 形态在使用处派生——**单向转换**。

### 2.2 一个必须注意的坑

`bootstrap.Result.Workspace` 的注释：

> State.Workspace is the file layer's spelling, and on Windows the two are different
> strings — handing that one back to the kernel asks it to open `C:\C:\Users\...`

即：**SFTP 路径（`/C:/Users/...`）与内核原生路径（`C:\Users\...`）是两种拼写**，
混淆会导致双重盘符。Flotilla 若做 Windows 远端，必须有显式的 `toSFTPPath` /
`toNativePath` 一对函数，且**只允许单向派生**。

### 2.3 Flotilla 的差异

Flotilla 的远端 daemon 不需要「装一个 20MB 的内核」——`flt-daemon` 是几 MB 的 Go 二进制。
但 `-EncodedCommand` 与 home 解析这两条照样适用，因为**问题在 Windows shell，不在被装的
东西**。

## 三、`forward.Set` 的端口冲突

### 3.1 三类冲突，三种处理

| 方向 | 生命周期 | 冲突处理 |
|---|---|---|
| 本地 `-L` | **跨重连保留** | 首次 `net.Listen` 失败即 `ErrBindBusy`，不重试 |
| 远端 `-R` | 每次重连重建 | 先试上次的端口，失败则重新要一个 |
| 显式非 0 端口 | 按用户意图 | 失败即报错，**不静默换端口** |

关键代码在 `forward/set.go`：

```go
// Detach: 本地监听器不关，只关远端
func (s *Set) Detach() {
    for _, r := range s.runs {
        if r.remote != nil { r.remote.Close(); r.remote = nil }
        ...
    }
}

// listenRemote: 重连时先要回原端口
func listenRemote(r *runner, cl *ssh.Client) (net.Listener, error) {
    if r.bound != "" && ephemeralBind(r.spec.BindAddr) {
        if ln, err := cl.Listen("tcp", r.bound); err == nil { return ln, nil }
    }
    return cl.Listen("tcp", r.spec.BindAddr)
}
```

注释点明了为什么：**「whatever was told where to find it — a serve launched with the
address of a `:0` forward — outlives the reconnect that would otherwise move it.」**
即远端 serve 记着自己被发布在哪个地址，端口一换它就成了孤儿。

### 3.2 对 Flotilla 的落点

Flotilla 只有 `-L`（本地监听、远端 daemon 在远端 loopback 上）。所以：

- 本地监听器**必须跨重连保留**，否则 GUI 里存的地址每次断线都变。
- 用 `127.0.0.1:0` 让 OS 分配，绑定后把实际端口记下来。
- 冲突（极少见，因为 `:0`）用 typed error，不要字符串匹配。

## 四、`ipc` 加 `tcp` 是否需要新 capability 位

### 4.1 不需要，理由

capability 位回答的是「**对端能不能读懂这个字段**」。传输是**连接怎么建立**，
不是「发了什么字段」。同一份 `Handshake` 与同一套帧在 pipe 和 TCP 上没有区别。

现有能力位只有一项 `diagnostics_v1`（`internal/ipc/capability.go`），语义是
「能读 status 的诊断字段」——与传输无关。**加一个 `tcp_v1` 会是类别错误**：它不裁剪任何
字段，只会让对端误以为要改行为。

### 4.2 但有三件必须做的事

1. **`Address` 要能表达传输类型。** 现在是 `{Channel, Name}`，`platformAddr` 按平台拼
   成 pipe 路径或 UDS 路径。加 `Kind`（`pipe`/`unix`/`tcp`）与 `Addr`（tcp 时的
   `host:port`）。这是**协议外的改动**，不进 wire。
2. **token 校验必须保留且更要紧。** pipe 靠 DACL 限本机同用户；TCP 上任何人能连
   `127.0.0.1:P`，**token 成为唯一闸门**。所以握手里的 `Token` 比较（`EqualToken`，
   常量时间）不能因为「已经在 SSH 隧道里」而放松——远端 daemon 的端口在远端机器上，
   那台机器上的任何进程都能连它。
3. **环回绑定不可配置。** 远端 daemon 只许绑 `127.0.0.1`，不许 `0.0.0.0`。这条已在
   `remote-access.md` 的硬规则里，这里补上实现位置：`ipc.listen` 的 tcp 分支要**拒绝**
   非环回地址，而不是信任调用方。

### 4.3 调用点很少

`ipc.Dial` / `ipc.Listen` 的非测试调用点只有 4 处：

```
internal/daemon/attach.go:101   ipc.Dial
internal/daemon/client.go:23    ipc.Dial
internal/daemon/daemon.go:221   ipc.Listen
internal/host/host.go:288       ipc.Listen
```

所以「加 tcp」是**一个传输实现 + 一个 Address 字段**，不需要动上层。

## 五、外部对照：多机模型的三种写法

| 产品 | 身份 | 传输与可达性 | 证据 |
|---|---|---|---|
| Paseo | `AgentAddress{serverId, agentId}` | 明确分离，注释写明 | `packages/protocol/src/addresses.ts`（已联网核对原文） |
| Orca | `HostProfile{id, endpoint, publicKeyB64, relay?}` | `credentialStatus: ready/unavailable/missing` 单独一维 | `mobile/src/host-catalog/host-profile.ts` |
| happier | `machineId` + `machine-scoped` socket | 连接类型三档（user/session/machine-scoped） | `docs/protocol.md` |
| 内核 | `Client`（每 SSH 连接一个）+ workspace slug | `Status` 六态独立于 workspace | `internal/platform/remote/` |

**四家的共识**：身份按机器（或机器+workspace）限定；**可达性不是身份的一部分**。
Flotilla 的 `Link` 六态与 `machineGroups` 正是这个形状。

## 六、对 `remote-access.md` 的再次修正

1. **「本地 daemon 代理远端」作废**，改为「每个 (host, workspace) 一个远端 daemon，
   GUI 并行多连接」。这条同时改掉 `remote-access-review.md` 第八节的第一条未决项。
2. **会话 id 需要机器作用域**：`<host>/<session>`。这是新要求，会影响
   `session.ID` 的表示与 GUI 的选择逻辑。
3. **`ipc.Address` 加 `Kind` + `Addr`**，不加 capability 位。
4. **Windows 远端**要 `-EncodedCommand` + SFTP `RealPath(".")` + 单向路径转换。

## 七、仍未核实

- 内核如何处理**同一 workspace 被两台机器上的两个客户端同时要**（`serveLock` 是远端
  文件锁，跨机器不互斥；推测靠 state 文件的 pid 检查，未验证）。
- Flotilla 的会话 id 加机器作用域后，**daemon 侧是否要改**（倾向不改：daemon 仍只见
  自己的会话，作用域是 GUI 的投影）。
- 内核 `bootstrap.install.go`（11KB）的安装路径选择逻辑未细读；Flotilla 的
  `flt` 二进制更小，可能不需要那套复杂度。
- 手机端是否需要「同时连多台机器」——Orca 的 catalog 支持，但 Flotilla 手机端
  先做单机是否够，未定。
