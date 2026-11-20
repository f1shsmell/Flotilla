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

## 复现

```bash
# 两条验收测试(零 token)
go test ./internal/daemon/ -run TestThreeWorktreesRunThreeTasksWithoutCrossing
pnpm --dir gui test store.test.ts

# 真 agent 取证花你自己的 token,需要本机 claude-agent-acp 已登录;
# 配方见上文,探针不入库。
```