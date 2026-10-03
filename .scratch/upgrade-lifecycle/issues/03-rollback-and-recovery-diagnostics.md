# 03: 补齐失败回滚与人工恢复提示

**What to build:** 当 binary 替换、service 启动或启动后的 running 确认失败时，用户得到一次完整的自动恢复尝试：旧 binary 和升级前的 instance-state 被恢复；如果恢复也失败，CLI/TUI 明确保留两类错误并提示需要人工恢复。

**Blocked by:** 02: 实现保持运行状态的 binary 升级

**Status:** resolved

- [x] 替换失败、启动失败或启动后确认非 running 时，旧 binary 被恢复。
- [x] 升级前为 running 的实例回滚后尝试恢复 running；升级前为 stopped 的实例回滚后保持 stopped。
- [x] 取消发生在停止前或替换后时，旧 binary、临时文件和 instance-state 按事务边界得到安全处理。
- [x] 回滚失败时，结果同时保留原始失败与恢复失败，并在 CLI/TUI 中提示可能需要人工恢复。
- [x] 失败路径的进度和最终状态不会伪报成功，TUI 完成后显示真实 service 状态。
- [x] manager 测试覆盖普通回滚、running/stopped 状态恢复、取消、回滚失败和错误聚合；CLI 覆盖非零退出码及关键提示。

**Verification:** `go test -count=1 ./internal/manager ./internal/cli .` (220 passed)

**Review:** full dual-axis review passed at `9e91022`; incremental rollback review passed at `c2f7c7b`. `issue_base=74f0231`, `review_head=c2f7c7b`.
