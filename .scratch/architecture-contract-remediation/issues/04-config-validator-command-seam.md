# 04: 收拢配置校验的命令执行 seam

**What to build:** 让配置校验通过现有命令执行 adapter 完成，不再由配置 pipeline implementation 直接创建外部进程。CLI、TUI 和 config apply status 能获得一致的 mihomo 校验诊断；测试可以使用 fake command adapter 验证参数、退出状态、命令输出和取消行为。

**Blocked by:** None (can start immediately).

**Status:** resolved

- [x] 生产 `ConfigValidator` 通过现有命令执行 seam 调用 mihomo 配置校验。
- [x] 配置 pipeline implementation 不直接创建外部进程。
- [x] 校验命令收到正确的配置目录、目标配置和上下文取消信号。
- [x] 非零退出状态被报告为配置校验失败，并保留足够的命令输出。
- [x] CLI、TUI 和 apply status 使用同一份可诊断的校验错误语义。
- [x] 使用 fake command adapter 覆盖成功、失败、输出和取消场景，不启动真实 mihomo。
- [x] 不为单一能力引入通用 process framework，也不向 `OSSystem` 添加无关职责。

## Comments

### Reimplementation record

- `issue_base`: `ca53786`
- `review_head`: `8a3cce0`
- `issue_head`: `cdfa28f` (one review finding-fix commit)
- Verify: `go test ./internal/manager -count=1` passed; focused root TUI tests passed.
- Review: dual-axis review completed once; validator cancellation evidence was strengthened in `cdfa28f`; no open findings.
