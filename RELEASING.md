# Flotilla 发布流程

骨架版:发布工具链落地时把占位命令替换为真实脚本。**发布必须用户明确要求;
例行开发不得改版本、打标签或发 Release。** 版本规则见
[VERSIONING.md](VERSIONING.md)。

## 1. 冻结范围

- 从最新 `origin/main` 建独立发布分支,保留开发工作区。
- 逐一确认 PR 的合并状态;未合并功能不算已发布能力。
- 同步版本:内部 version 包、`gui/package.json`、electron-builder 配置、
  CHANGELOG 版本段、SECURITY 支持版本。
- 生成第三方许可做本地检查:`bash scripts/third-party-notices.sh` 会把模块图里每个
  依赖的许可写进 `THIRD-PARTY-NOTICES.md`(不入库,见第 4 节)。Apache-2.0 依赖(当前是
  `coder/acp-go-sdk`)必须保留其许可文本;脚本报出「no license file」时停下,
  先把该依赖查清再继续。工作流在发布时会再生成一次并做同样的检查。
- 冻结最终 SHA;此后任何补修或改版本产生新 SHA,都要重新取得验证证据。

## 2. 验证冻结提交

- CI 全绿(gofmt / vet / golangci-lint / test / gui gates,Windows runner),记录 Run ID 与
  headSha,确认等于冻结 SHA。
- 本地 headless 验证;不弹 GUI 窗口。真实 agent turn 只在必要时用最便宜模型,
  其余走 Replay provider。

## 3. 候选安装包验收

- 构建 stable/preview 双通道安装包(dry run,不发 Release)。
- 在干净 Win11 上验收:**10 分钟完成安装到跑通一个会话**;
  从上一正式版升级同一安装位置,验收口径(2026-11-23 排实测钉定):
  安装器自动关闭运行中的应用并完成安装;`FLT_HOME` 的 token/transcript/seen
  与 `%APPDATA%` 的 ui-state 布局保持;升级后新会话即开即用。升级替换
  daemon/host 二进制,活会话随升级结束——结束后它们出现在舰队的 **Finished**
  分区,日志可读但不可续写;升级不得按「会话保持」字面判 PASS。
- 保活边界实测:杀 GUI、杀 daemon 各若干次,会话不断;记录 host 崩溃时的
  恢复行为与 README 承诺一致。
- 每项写 PASS / FAIL / UNANSWERED,附证据;缺环境属于 UNANSWERED,不算通过。

## 4. 打标签发布

- 确认版本号、工作区干净、HEAD 等于冻结的 `origin/main`、本地与远端均无该
  版本标签,然后:

```bash
git tag -a "v$VERSION" -m "v$VERSION"
git push origin "refs/tags/v$VERSION"
```

- `v*` 标签触发发布工作流(工具链落地后):构建、签名、创建 Release。工作流不
  代替第 3 节验收——必须先验收再打标签。
- `THIRD-PARTY-NOTICES.md` **不入库**:它是构建产物,由工作流在发布时生成并作为
  Release 附件发布。它列的是**实际链接进二进制的模块**(`go list -deps`),不是
  `go list -m all` 的整张模块图——后者会列出没参与编译的依赖。任一依赖取不到许可
  文本即失败。第 1 节手工生成的那份只用于本地检查,不要提交。

## 5. 发布后

- 核对标签 SHA、Release 版本与说明、安装包可下载、签名状态(未签名不能记作
  自动更新已通过)。
- 安装最终 Release 的包抽查;候选包验收不代表 tag 构建字节相同。
- 已公开的标签不删除、不改指向;严重问题发补丁版本,必要时 latest 指回上一
  稳定版。
- 操作记录至少包含:版本、SHA、CI Run ID、验收结果、未覆盖平台、Release URL。