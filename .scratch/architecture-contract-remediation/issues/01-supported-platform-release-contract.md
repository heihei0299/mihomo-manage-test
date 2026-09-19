# 01: 建立可信的支持平台与发布契约

**What to build:** 让发布物、运行时平台能力和项目文档表达同一份支持平台契约。用户只能获得真正适用于 Linux amd64、Linux arm64、Darwin amd64 和 Darwin arm64 的管理器；Windows 不再出现在当前发布矩阵中，未支持平台不会回退到其他平台的服务实现。Go module、内部导入、README、release workflow 和架构检查统一使用同一个 canonical repository identity。

**Blocked by:** None (can start immediately).

**Status:** resolved

- [x] 发布矩阵只包含当前正式支持的 Linux 和 Darwin 目标，并且构建命令显式使用每个目标的 OS 和架构。
- [x] 每个发布产物的实际目标平台和架构与其名称一致，而不是只改变文件名。
- [x] Windows 不再生成发布物；未支持平台返回可识别的 unsupported-platform 错误，不使用 systemd 等隐式默认实现。
- [x] README、Go module、内部导入、release workflow 和架构检查使用同一个 canonical repository identity。
- [x] 相关 CI 或架构检查能够在不启动真实服务的情况下验证支持平台契约。
- [x] 不改变 Linux/Darwin 已有的服务、调度和安装行为。

## Comments

### Reimplementation record

- `issue_base`: `95815a3`
- `review_head`: `f8c91d8`
- `issue_head`: `afcd6a3` (one review finding-fix commit)
- Verify: `go test ./internal/manager -run 'TestSupportedReleaseContract|TestRepositoryIdentityIsConsistent|TestOSServiceManagerUnsupportedOS|TestManagerProductionFilesHaveExplicitOwner|TestSchedulerDoesNotImportManager' -count=1` passed.
- Review: dual-axis review completed once; the unsupported-platform upgrade preflight finding was fixed in `afcd6a3`; no open findings.
