# 03: 让远程订阅更新保持源缓存与生成配置一致

**What to build:** 将远程订阅更新实现为一次可恢复的配置 apply 流程：订阅先进入候选 staging，再生成和校验候选配置；只有候选配置成功提交后，新的订阅缓存才会发布。下载失败、校验失败或提交前失败都保留旧配置和旧缓存；reload 失败保留新配置并报告 `pending-reload`。

**Blocked by:** 02: 统一配置写操作的序列化边界

**Status:** resolved

- [x] 远程订阅下载结果在校验和提交前不会覆盖当前 subscription-data。
- [x] 候选订阅数据参与候选配置生成和 mihomo 配置校验。
- [x] 下载失败、生成失败、校验失败、备份失败或提交前失败时，旧配置和旧订阅缓存保持不变。
- [x] 成功 apply 后，生成配置和订阅缓存对应同一个候选版本。
- [x] 配置提交成功但 reload 失败时，状态为 `pending-reload`，并保留已提交配置和诊断信息。
- [x] 配置状态继续区分 validation failure、pre-commit apply failure、pending reload 和 applied。
- [x] 临时订阅文件、staging 目录和备份清理失败时，保留主错误与恢复错误，并记录可诊断状态。
- [x] 本地订阅源不发生无关的远程下载或缓存替换。
- [x] 通过 `ConfigManager` 的外部行为测试覆盖成功、失败、取消、恢复和状态记录，不改变调用方 interface。

## Comments

### Reimplementation record

- `issue_base`: `3e7865a`
- `review_head`: `888a238`
- `issue_head`: `d76f1ba` (one review finding-fix commit)
- Verify: `go test ./internal/manager -count=1` passed (223 tests); `go test . ./internal/cli -count=1` passed (47 tests).
- Review: dual-axis review completed once; recovery-failure write blocking, typed transaction phases, and recovery-contract tests were added in `d76f1ba`; durable config backups remain retained by the existing backup contract; no open findings.
