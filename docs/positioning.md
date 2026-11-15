# 舰阵 / Flotilla — 定位一页

> Every agent in formation. / 百舸布阵,一人掌舵。

## 问题

编码 agent 的一轮跑几分钟到几小时，人的注意力是间歇的。终端没有舰队视图：
不知道谁在跑、谁卡住等你拍板、谁的改动值得合并；关掉窗口，活儿就死。

## 是什么

ACP 原生的 AI 编码 agent 舰队管理器。Windows 优先。结构化会话给说 ACP 的
agent，完整交互的 PTY 兜底给任何其它 CLI。每个会话在自己的 worktree 里并行，
diff 审查、一键 commit/push/开 PR 不离开应用。

## 原则

- **ACP-first**：协议是一等公民，PTY 是底线而非主路径。
- **会话比应用活得久**：GUI 重启、daemon 重启不丢会话。诚实边界：session host
  崩溃即会话丢失（PTY 与 ACP 一样——agent 句柄与 ACP 会话 id 都在 host 里，
  崩溃后的 resume 尚未接入）；OS 重启只恢复布局与历史。不承诺进程级复活。
- **BYO 订阅**：不转售 token、不做网关、不锁模型。凭证是你自己的。
- **适配器是数据**：多数新 agent = 一个 JSON 定义，社区可贡献覆盖。

## 首发支持矩阵（营销话术以此为限）

claude-agent-acp 适配器、Gemini CLI（原生 ACP）、另选 1 个社区 ACP agent 证明
通用性。PTY 层承诺"任何 CLI 可开、可交互"，不承诺结构化视图。

## 不做

token 转售与账号网关；语音助手；模型托管；"接管一切"的 IDE 替代。舰阵是舰队
控制台，编辑器仍是你自己的。

## 状态与通道

pre-alpha。stable 与 preview 双通道并存（发布后）。MIT 许可。