# 02: 实现保持运行状态的 binary 升级

**What to build:** 在升级目标已经安全准备完成后，用户可以通过现有 CLI/TUI 完成 mihomo binary 替换；升级成功保持升级前的 instance-state，并在向用户报告成功前确认 service 实际处于预期状态。

**Blocked by:** 01: 安全准备升级目标

**Status:** resolved

- [x] running 的 mihomo-instance 只在替换窗口停止，升级成功后恢复 running。
- [x] stopped 的 mihomo-instance 升级成功后仍保持 stopped，不因升级自动启动。
- [x] 旧 binary 在替换前保留为最近备份，成功升级后新 binary 成为当前版本。
- [x] 新 service 启动成功且实际处于 running 后，CLI `upgrade` 才返回成功。
- [x] CLI 显示 stop、replace、start 等进度，TUI 完成后刷新到真实 instance-state。
- [x] 升级不修改 subscription-data、config-pipeline、override-file、subscription-update 或 service 注册。
- [x] manager、CLI 和必要的 TUI 行为测试覆盖 running/stopped 两种成功路径及进度反馈。

**Verification:** `go test ./internal/manager ./internal/cli .` (207 passed)

**Review:** full dual-axis review passed at `ddf0110`; incremental stopped-state review passed at `b6b05a2`. Binary/service rollback hardening remains in issue 03.
