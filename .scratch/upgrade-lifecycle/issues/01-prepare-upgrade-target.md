# 01: 安全准备升级目标

**What to build:** 用户执行 `upgrade` 或查看 `versions` 时，manager 在任何停止 mihomo-instance 之前完成版本目标解析、binary 下载和 checksum 校验；失败时用户能从 CLI/TUI 得到明确反馈，而现有 binary 和运行状态保持不变。

**Blocked by:** None (can start immediately)

**Status:** resolved

- [x] `latest` 在停止实例前解析一次；解析失败返回清晰错误，且不会停止 service。
- [x] 显式版本在停止实例前完成下载与 checksum 校验；下载或校验失败不改变 binary 和 instance-state。
- [x] `versions` 继续展示最近五个版本；查询失败或结果为空时，CLI 返回非零结果并给出明确提示。
- [x] TUI 的版本选择界面对查询失败和空列表显示可理解的错误或空状态。
- [x] CLI/TUI 保留现有 `upgrade`、`versions` 入口和 check/fetch 进度反馈。
- [x] manager seam、CLI 行为和必要的 TUI 状态覆盖上述安全失败路径；不引入真实网络或真实 service 依赖。

**Verification:** `go test ./internal/manager ./internal/cli .` (201 passed)

**Review:** full dual-axis review passed at `e80de34`; incremental scope review passed at `e344179`. Unrelated stopped-state/start-confirmation findings are deferred to issues 02/03.
