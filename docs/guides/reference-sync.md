# Reference Sync and Failure Recovery

**Status:** current
**Last verified:** 2026-10-05

> **Ownership:** operating reference synchronization and interpreting bounded
> retries, transport fallback, and deferred post-update summaries

同步失败后直接重跑同一命令，不需要删除 lock、清理 checkout 或回退已合入提交。
脚本会重试可恢复的传输/模型错误，并在后续运行补做已登记的失败汇总。
本页面向定时任务维护者；命令、重试规则或恢复记录格式变化时需更新。

## 执行一轮同步

在项目根目录运行：

```bash
go run ./scripts/reference_sync --config docs/migration/reference/reference-repositories.yaml sync
```

以 [reference-repositories.yaml](../migration/reference/reference-repositories.yaml)
为配置权威。只有 enabled 且干净的 checkout 可以 fetch；成功分析精确且受限的
合入 diff 后才能 `merge --ff-only`。两个冻结 reference 不会 fetch 或 merge。
不要手工清理 dirty checkout，也不要把上游变更自动写成 YHC backlog。

## 哪些失败会自动重试

`git_fetch`、`model_summary`、`subagent_summary` 分别配置 `retry.attempts`
和 `retry.backoff_seconds`。attempts 包含首次调用，默认 2 次、最多 3 次；退避
默认 3 秒，后一次翻倍。Git 每次调用默认超时 300 秒，配置可缩短；两阶段模型
各自使用其 `timeout_seconds`，重试仍使用同一模型和同一受限输入。

连接中断、HTTP/2 错误、单次超时等已识别的瞬时失败才会重试。认证、额度、配置、
未知错误以及取消不会重试。用户取消会中止退避和已拥有的子进程。

Git 重试使用单次命令的 HTTP/1.1 参数；GitHub SSH origin 还可通过单次 URL
映射使用 HTTPS。不会修改 origin 或永久 Git 配置，不扩大到其他 SSH 主机。
每次 fetch 只更新配置的 upstream tracking branch，不拉取其他 topic 分支或跟随 tags；
Git 仍可写入该 fetch 的对象及 FETCH_HEAD，不等于完全无磁盘副作用。
每次尝试前重新验证 origin、HEAD 和 dirty 状态；merge 不做自动重试。

Codex CLI 失败优先记录 JSON 中的终态错误，再取脱敏的 stderr 尾部，避免启动
警告遮住真实原因。无可用模型分析时，仍然保留 `blocked_summary`，不生成模板兜底。

## 汇总失败后如何恢复

已合入更新的分析保存在 `.reference/.sync-memory/updates/`。同批更新登记到
`summary-jobs/`，记录相对路径及 SHA-256，模型完成后才标记 completed。
写入部分 updates 后发生 IO 错误时，已成功落盘的子集也会登记。

每轮先处理新 updates，再至多补做一个旧 pending 批次，即使本轮无新更新也会补做。
历史补做只读该原批次的 updates，不读参考仓库源码；输入变更或路径越界会拒绝恢复。
新汇总注明 Source batch / Source update，并留下单独的 summary-recovery run。
旧失败文件不会覆盖，成功任务不会再次派发，也不会重复合入。

旧版明确标记 failed 的汇总可以按时间戳及文件数量匹配导入；不会把任意历史 updates
自动当成待处理任务。磁盘无法保存 job，或进程在更新记录登记前崩溃，仍需要人工审查，
不能声称已恢复；脚本不会为此回滚成功 fast-forward。

## 如何判断结果

查看终端状态及 `.reference/.sync-memory/runs/`、`summaries/`、`summary-jobs/`：

- updated / unchanged / frozen 是本轮 checkout 结果；recovered 是旧批次汇总补做。
- blocked_dirty / blocked_changed / blocked_diverged 保留现场，需操作员判断。
- 模型或补做失败、写入失败仍返回非零，不能将部分更新报告为整轮成功。
- `.lock` 是持久文件，存在不代表被占用；不要删除正在使用的锁文件。

## 维护者验证

运行 `go test ./scripts/reference_sync -count=1`；并发/取消验证加 `-race`。
确定性 fixture 覆盖瞬时错误、有限预算、传输参数、dirty/HEAD 变化、取消、
历史补做、部分写入和输入完整性；它们不证明外部网络或实时模型一直可用。

实现 owner：[`retryTransient` / `gitClient.fetch`](../../scripts/reference_sync/retry.go)
负责有限重试；[`recoverPendingSummaries`](../../scripts/reference_sync/summary_recovery.go)
负责补做；[`summarizeUpdateFiles`](../../scripts/reference_sync/subagent_summary.go)
负责只读模型派发与持久化。最终变更验证遵循
[verification.md](../contributing/verification.md)。
