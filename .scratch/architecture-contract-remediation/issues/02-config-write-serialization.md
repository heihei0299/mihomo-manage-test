# 02: 统一配置写操作的序列化边界

**What to build:** 让 `subscription set`、scheduled `subscription update` 和 `config adopt` 等会改变持久化配置状态的操作共享同一个写入序列化规则。并发操作不得产生部分来源标记、旧 URL、旧本地数据或不完整覆写文件；失败时保留可识别的错误状态。

**Blocked by:** None (can start immediately).

**Status:** resolved

- [x] `SetSubscriptionSource`、`UpdateConfig` 和 `AdoptConfig` 等配置写操作使用同一个配置写入锁或等价的序列化边界。
- [x] 并发来源切换和订阅更新不会留下不匹配的 source marker、URL、subscription-data 或部分状态文件。
- [x] 并发 `config adopt` 和配置更新不会产生不完整的 override-file。
- [x] 锁等待、锁占用和上下文取消继续返回稳定、可分支的错误契约。
- [x] 写入失败时已有持久化状态保持可恢复，不引入新的部分提交状态。
- [x] 使用 fake filesystem、fake lock 和行为测试验证外部状态，不依赖真实 mihomo、网络或系统服务。
- [x] 不改变 YAML merge、override-file 或 `config adopt` 的业务语义。

## Comments

### Reimplementation record

- `issue_base`: `afcd6a3`
- `review_head`: `8b92a84`
- `issue_head`: `ca53786` (one review finding-fix commit)
- Verify: focused config lock/write tests passed; final `go test ./internal/manager -count=1` also passed.
- Review: dual-axis review completed once; legacy migration retry after lock contention was fixed in `ca53786`; the remote-cache transaction concern was deferred to blocked issue 03; no open findings for this issue.
