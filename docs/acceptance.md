# 验收取证(只写已取证的)

`docs/support-matrix.md` 取证的是「哪家 agent 能跑」;这份文件取证的是里程碑
验收标准本身。每条结论都对应一次真实运行或一个可复现的测试,没跑过的不写。

## W5-6 舰队视图

### 3 个 worktree 并行跑 3 个任务不串

两层证据,缺一不可:回归测试在 CI 每次运行中复现机制,真 agent 运行证明产品
链路在真实回合里成立。

**回归测试(零 token,CI 可复现)**:
`TestThreeWorktreesRunThreeTasksWithoutCrossing`
(`internal/daemon/fleetworktree_test.go`)。进程内 daemon 经 `session.start`
spawn 三个**真** flt-session-host 进程,各带一个 fake ACP agent,三个会话落在
同一仓库的三个 worktree。断言:

- 三个目录互不相同,各自 checkout 在自己的分支上,`.worktreeinclude` 的
  `.env` 都带进了 worktree,主 checkout 仍在 main;
- 三个回合同时跑,每条流只携带自己的任务标记,不含另外两家的;
- 每个回合各问一次权限、`end_turn` 收尾,舰队报告三个会话都是 done。

```bash
go test ./internal/daemon/ -run TestThreeWorktreesRunThreeTasksWithoutCrossing
```

**真 agent 取证(2026-11-20 · Windows 10 19045 · claude-agent-acp 本机登录)**:
临时探针驱动隔离 `FLT_HOME` 的 flt-daemon,三个 claude-agent-acp 会话
(auto 档)在三个 worktree **并行**跑真回合,任务各写一个文件。探针输出节选:

```
started wt-a pid=46216 dir=...\flt-wtacc-3538319618\repo-feat-wt-a
started wt-b pid=19804 dir=...\flt-wtacc-3538319618\repo-feat-wt-b
started wt-c pid=13380 dir=...\flt-wtacc-3538319618\repo-feat-wt-c
wt-a: stop=end_turn elapsed=3.518s
wt-b: stop=end_turn elapsed=4.139s
wt-c: stop=end_turn elapsed=4.275s
wt-a: ...\repo-feat-wt-a holds task-a.txt="flotilla-wt-a" on branch feat/wt-a, no other task's file
wt-b: ...\repo-feat-wt-b holds task-b.txt="flotilla-wt-b" on branch feat/wt-b, no other task's file
wt-c: ...\repo-feat-wt-c holds task-c.txt="flotilla-wt-c" on branch feat/wt-c, no other task's file
wt-a stream tail: Create a file named task-a.txt ... | task | -a | .txt
wt-b stream tail: Create a file named task-b.txt ... | task | -b | .txt
wt-c stream tail: Create a file named task-c.txt ... | task | -c | .txt
fleet: wt-a state=done dir=...\repo-feat-wt-a
fleet: wt-b state=done dir=...\repo-feat-wt-b
fleet: wt-c state=done dir=...\repo-feat-wt-c
WTACC-OK: three real claude turns ran in parallel worktrees without crossing, total 9.465s
```

文件级核实是硬证据:每个 worktree 只含自己任务的标记文件,另外两家的文件与
主 checkout 里都没有;三条流里也互不含对方的标记。探针是未入库的临时文件,
取证后已删除;复现配方就是上面回归测试的真 agent 版——隔离 `FLT_HOME` 起
daemon,`session.start` 三次带 `worktree`,`session.prompt` 并发各发一个写文件
任务,回合结束后核对文件落位。

### 10 个会话时「等你拍板」的那个恒在最上

`gui/test/store.test.ts` 的 `fleet order at ten sessions` 四例,走窗口自己的
投影路径(`parseStatus` / `withLifecycle`),规模就是验收的 10 个会话:

- blocked 排在报告最后一位,投影后仍是第一行;
- 10 个会话里一个 working 变 blocked,立刻升到其余九个之上;
- 两个 blocked 按报告顺序稳定占据前两位;
- blocked 被回应(转 idle)后沉到所有 done / unknown / working 之下。

```bash
pnpm --dir gui test store.test.ts
```

诚实注脚:没有做 10 个活会话的真窗口运行。排序是纯投影(稳定排序,规则与
规模无关),单元测试在验收规模上取证;真窗口取证目前只有单会话与三会话规模
(见 support-matrix 的 GUI 一节与上文)。

### 排序映射决策

排序实现为五态映射 `blocked(0) < done(1) < unknown(2) < working(3) < idle(4)`,
不是 paseo 的五桶照搬:五态闭集是不砍项,不设 failed 桶,失败可见性由
unknown + unreachable 徽标承担,「已读降级」由 seen 投影(done→idle,各客户端
独立)承担。决策与理由记录在 PLAN.md 第八节 paseo 表「状态分桶与优先级」行
(2026-11-20)。

### 取证中发现并修复的 bug

- `daemon.New(cfg, nil)` 构造的 daemon 收到 `session.start` 会因 nil spawner
  空指针**整个进程崩溃**。验收测试首跑即触发;修复为 `New` 对 nil spawner
  默认 `ExecSpawner`(生产入口本来就传它),该验收测试就是这个 bug 的回归
  测试——它用的测试 helper 恰好传 nil。

## W7-8 原生 CLI 与洪水延迟

### 构建日志洪水下输入延迟达标

`TestInputStaysFastUnderAFlood`(`internal/daemon/flood_test.go`),零 token,
CI 可复现:

- 测试二进制自 re-exec 成洪水 agent(`TestHelperFloodAgent`):ready 后持续
  写 4KB 块,每读到一行输入就回 `echo: <行>`——回声是可单独计时的离散事件;
- 进程内 `host.Start`(PTY 模式)+ 进程内 daemon,测试客户端经真 IPC pipe
  dial daemon 并 attach;agent 是真进程(测试二进制 re-exec),字节走完整
  client → daemon → host → ConPTY → agent 链路;
- 先断言洪水速率 ≥2MB/s(细水长流证明不了洪水判据),再每隔 150ms 发一条
  ping,共 20 条,量到回声出现在输出流里的往返延迟;
- 判据(PLAN.md 决策):p95 ≤ 100ms,max ≤ 500ms。

实测(2026-11-21 · Windows 10 19045):

```
flood 2.48 MB/s; input latency p95=4.0019ms max=4.003ms
```

速率下限是判据的一部分,不是可调参数:慢机器上速率断言本身可能失败
(取证机实测 2.3–2.8 MB/s)——达不到洪水速率,延迟数字就没有意义。
回声匹配在数据回调里做,不在轮询里做:一帧合帧洪水就能在两次轮询之间
换掉整个尾部缓冲,轮询会漏针。

被测机制是数据面合帧 + 背压 + 控制优先:`ipc.Writer` 数据队列满
(QueueDepth=64)时生产者阻塞,同流数据帧合并(≤64KB),控制队列优先出队,
每次数据写前先 flush 待写控制帧。

```bash
go test ./internal/daemon/ -run TestInputStaysFastUnderAFlood
```

### 控制帧不被数据积压挡住

`TestControlOvertakesTheDataBacklog`(`internal/ipc/frame_test.go`):确定性
测试,零时序依赖——底层 Write 被闸门卡住,一个数据帧已在途,数据队列灌满
64×1KB,再写一个控制帧。放闸后线上帧序必须是 data(first) → control(urgent)
→ 其余数据,且数据字节总量一字节不丢。

```bash
go test ./internal/ipc/ -run TestControlOvertakesTheDataBacklog
```

### 任意 CLI 原生可用:Claude Code 原生 PTY 真回合

Gemini CLI 本机未登录,按决策的兜底用 Claude Code 原生可执行文件取证
(2026-11-21 · Windows 10 19045 · claude.exe v2.1.312 · 本机登录)。

通用 CLI 接入是适配器驱动的:`FLT_HOME/config/adapters/claude-native.json`
声明 `mode=pty` 与 blocked 模式,daemon 的 agents 列表即出现 claude-native
(cli:true):

```json
{"id":"claude-native","name":"Claude Code (native PTY)","command":"claude",
 "mode":"pty","blocked":["one you trust?","Do you want"],"paste":"bracketed"}
```

临时探针驱动隔离 `FLT_HOME` 的 flt-daemon → 真 flt-session-host → ConPTY →
claude.exe;客户端用与 GUI 相同的 x/vt 屏幕模型渲染、同一条读源读状态。
证据链(每步都是探针输出或文件级核实):

1. 信任对话框:屏幕出现 "Is this a project you created or one you trust?",
   state=blocked;↓+Enter 选中 "Yes, I trust this folder",进主提示符;
2. 输入编辑:打 "hi",← 移光标,插 "X",屏幕读作 "hXi"——方向键与文本
   都走完整 relay;
3. shift+tab 切权限档(auto → manual);
4. 真回合:发 "Run this exact shell command: echo fleet-perm > perm.txt",
   Bash 权限对话框 "Do you want to proceed?" 出现,state=blocked;
5. Enter 批准,state → unknown,文件级核实 perm.txt="fleet-perm",回合以
   "Done — perm.txt now contains fleet-perm." 收尾。

探针输出节选:

```
[00:53:16.591] state=blocked
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and always allow access to ...
[00:53:17.691] state=unknown
file ...\work2\perm.txt = "fleet-perm"
states: blocked -> unknown
PROBE-OK
```

诚实注脚:

- 信任对话框刚渲染时立刻发键会被忽略;探针在对话框出现后等 4 秒再发键
  (node-pty 对照脚本等 10 秒)。这是 Claude 自身启动行为的观察;舰阵链路
  不闸任何按键——host.handleData 收到即写。
- auto 档会自动批准 Bash 命令,不出现权限对话框;权限取证先切到 manual 档。
- Gemini CLI 原生未取证(本机未登录)。
- 探针是未入库的临时文件,取证后已删除。复现配方:隔离 `FLT_HOME` 起
  daemon,`session.start` mode=pty command=原生 claude 路径,attach 后按
  上述步骤发键,回合结束核对文件落位。

### CJK 读源上游限制

读源用 charmbracelet x/vt v0.1.0:跨自动换行边界的宽字符会被丢弃,因此恰好
折在字中间的 CJK 提示可能读短、漏掉 blocked 匹配。`TestScreenKeepsCJKText`
钉住正常宽度下中文不丢不空格;按键永不以读源为闸,影响仅限检测,不影响输入。
见 `internal/host/screen.go` 注释。

### GUI GPU 渲染与中文 IME

真窗口 smoke(2026-11-21 在本分支重跑,零 token):PTY 会话
`node -e "process.stdin.pipe(process.stdout)"`,中文经 compositionstart /
update / end 提交进 xterm 的 textarea → onData → daemon → host → ConPTY →
回声 → xterm 缓冲;渲染器断言读 `window.__fltRenderer`。

```
smoke: started ime-echo
smoke: ok (renderer: webgl)
smoke: ok (terminal: 你好世界)
```

复现见 `gui/README.md` 的 `FLT_GUI_SMOKE_*` 表(START + OPEN + RENDERER +
IME + TERMINAL)。webgpu → webgl 决策记在 PLAN.md 第八节 GPU 行:xterm.js
没有 webgpu addon,webgl 是它唯一的 GPU 渲染器。

## 复现

```bash
# 验收测试(零 token)
go test ./internal/daemon/ -run TestThreeWorktreesRunThreeTasksWithoutCrossing
go test ./internal/daemon/ -run TestInputStaysFastUnderAFlood
go test ./internal/ipc/ -run TestControlOvertakesTheDataBacklog
pnpm --dir gui test store.test.ts

# 真 agent 取证花你自己的 token,需要本机 claude-agent-acp 已登录;
# 配方见上文,探针不入库。
```