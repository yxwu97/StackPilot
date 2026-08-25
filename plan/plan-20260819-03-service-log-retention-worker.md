# StackPilot 服务日志自动保留与存储压力开发计划

> 状态：待执行
> 日期：2026-08-19
> 来源 Prompt：`prompt/prompt-20260819-03-service-log-retention-worker.md`
> 关联前端计划：`plan/plan-20260819-02-service-log-viewer-enhancements.md`
> 阶段归属：补齐详细设计第 13.5 节已定义的日志保留闭环；不新增 P3C-01 全文索引
> 审核状态：已吸收 2026-08-19 第二轮外部审核中经仓库事实验证的意见

## 1. 目标结果

为 `DATA_DIR/logs` 下的关闭 NDJSON segment 建立有界、可取消、可恢复的自动保留机制：按 `retentionDays` 和 `totalMaxBytes` 淘汰符合条件的历史文件，同步收敛 SQLite `log_segments` 元数据，并在无法释放足够空间时以 `LOG_STORAGE_PRESSURE` 阻止新服务启动，而不影响已运行服务和活动日志捕获。

本计划不包含前端清空按钮或手工删除 API。文件删除与 SQLite 不能原子提交，因此任何实现必须先通过一致性协议和威胁模型 Gate。

## 2. 固定决策

1. 日志正文只存在于 spool/NDJSON 文件，SQLite 只保存 segment 元数据；两者都要有界，但不能表述为“清理 SQLite 日志正文”。
2. 只清理曾由数据库登记、当前已关闭、位于 canonical `DATA_DIR/logs` 根内且未受活动 Incident 保护的普通文件。
3. 不删除 `.active`、活动 spool、未登记文件、工作区文件、平台日志、容器 volume 或其他数据目录内容。
4. 清理使用有界批次和可取消 context；失败不阻塞日志落盘，不杀死运行服务。
5. `LOG_STORAGE_PRESSURE` 只阻止新的实际资源创建；所有 process/compose/oneshot/restart/自动恢复创建路径必须使用同一服务端 guard。
6. 文件与元数据删除采用经 ADR 明确的可恢复协议，不接受“先删文件再忽略数据库失败”或“先删行后永久遗留孤儿文件”。
7. 已发布 migration 只增不改；需要清理状态、隔离记录或 Incident 关系时新增 migration。
8. 配置沿用 flags -> `serverConfig` -> `logs.Config` 单一链路，不在本工作包引入 YAML 配置层；同步修正详细设计中的历史漂移。
9. `retentionDays` 默认 14 天，`totalMaxBytes` 默认 2147483648 字节；必须有默认启用、启动时可关闭的 retention off 逃生开关。
10. 二进制回退或 off 开关只能阻止后续清理，不能恢复已物理删除的 segment。

## 3. 实现前待决事项

以下事项必须在 LR-00 形成书面结论，未关闭前不得编写正式删除路径：

1. 文件/SQLite 两阶段协议：待删除状态、同根隔离重命名、提交顺序、重试和各崩溃窗口。
2. Incident 保护查询：当前 Incident context 以 JSON 保存 sequence 引用，决定使用有界强类型解码还是新增规范化引用表；不得靠字符串搜索 JSON。
3. 容量口径：登记关闭 segment、`.active`、spool、隔离文件分别如何计入 `totalMaxBytes`，以及如何区分策略超限和文件系统剩余空间压力。
4. 配置契约：在已固定的 flags 配置链路上，一次性定义 flag 名称、worker interval、batch limit、压力高/低水位、单位、边界、溢出校验、off 行为和旧版本兼容；不得将参数选择延后到 LR-04/LR-05。
5. worker 调度：启动时是否先 compact、周期、batch、时间预算、失败 backoff、shutdown 顺序和与 reconciliation 的所有权关系。
6. Windows 正在读取文件的 sharing violation 处理和历史查询与隔离/删除并发语义。
7. `LOG_STORAGE_PRESSURE` 的 Operation 失败步骤、API/CLI 映射、retryable、allowed details 和恢复判定。
8. 发布止损：off 开关验证、ADR-0003 版本回退、数据库备份/恢复边界、不可恢复删除的明示以及候选版观察标准。

## 4. 工作包总览

| ID | 工作包 | 依赖 | 主要交付物 | Gate |
| --- | --- | --- | --- | --- |
| LR-00 | ADR、威胁模型与契约 | 无 | 一致性协议、配置/压力/Incident 决策 | Design Gate |
| LR-01 | Migration 与 Repository | LR-00 | 清理状态/引用 schema、候选与状态迁移 | SQLite Gate |
| LR-02 | 保留策略引擎 | LR-01 | 时间/容量选择、保护与有界批次 | Policy Gate |
| LR-03 | 文件执行器与恢复 | LR-01/02 | canonical 删除、隔离、幂等恢复 | Recovery Gate |
| LR-04 | Worker 生命周期与配置装配 | LR-02/03 | Config、owner、ticker、shutdown | Lifecycle Gate |
| LR-05 | 存储压力启动保护 | LR-00/04 | guard、错误码、所有启动路径 | Pressure Gate |
| LR-06 | 安全回归、文档与最终 Gate | LR-00..05 | 集成测试、Windows Gate、证据 | 专项完成 |

## 5. LR-00：ADR、威胁模型与契约

### 任务

- 盘点 segment 写入/关闭、recovery、history/window、SSE catch-up、Incident context、所有启动路径和 control-plane 生命周期。
- 显式对照 `HealthRetentionPolicy`、`CompactDefault`、`health_result_repository.go` 的有界事务和 `runReconciliationLoop` 的 ticker/owner/shutdown 先例，决定并记录日志 retention 并入现有 owner 还是使用独立 worker 的理由。
- 更新详细设计或新增 ADR，画出 segment 状态和文件状态转换，至少覆盖：registered、pending/quarantined（若采用）、metadata committed、file removed、retryable failure。
- 对每个崩溃点定义重启后的权威事实和幂等恢复动作。
- 定义清理锁和并发边界：不能持数据库事务执行无界文件 I/O，也不能让历史 reader 打开已被错误换出的路径。
- 定义 Incident 保护的最小数据模型。活动 Incident 的 evidence/log sequence 所在 segment 必须可被确定性识别；Resolved Incident 的保留期需与现有 Incident/Event 策略一致，不自行猜测。
- 在“有界强类型解码 `context_json`”与“新增规范化引用表”之间做出可验证决策，定义 `state='open'` Incident 的 `(service_instance_id, log_sequence)` -> segment 查询；禁止字符串搜索 JSON。
- 固定 flags 的名称及 `retentionDays`、`totalMaxBytes`、worker interval、batch limit、off 开关和压力阈值的默认值、单位、最小/最大值、溢出处理和兼容升级行为。已知默认值不得偏离 14 天和 2147483648 字节。
- 定义跨服务/实例/stream 的全局稳定清理顺序及完整 tie-breaker，并证明每个服务实例的保留 segment 仍构成 sequence 连续后缀。
- 分别定义策略配额与文件系统压力的数据源、检查频率、进入/解除阈值、最小持续时间/连续样本数和重启后重算行为；解除阈值必须严于进入阈值。
- 定义存储压力 guard 的接口和所有调用点，确保在创建进程、Job Object、Compose project 或端口副作用之前失败。
- 定义发布止损契约：off 开关生效点、worker/guard 关闭语义、ADR-0003 回退操作、数据库恢复边界和“已物理删除日志不可由版本回退恢复”的明示。
- 建立删除威胁模型：相对路径篡改、junction/符号链接交换、TOCTOU、目录/设备文件、硬链接风险、Windows sharing violation、数据库 busy、恶意超大目录和日志正文泄漏。

### 退出条件

- 第 3 节八项均有明确结论，无“实现时再决定”。
- ADR/设计与当前 migration、API、错误码和阶段计划无冲突，或同步变更已列入后续工作包。
- 删除目标和恢复协议可通过可控 fixture 完整模拟。

## 6. LR-01：Migration 与 Repository

### Schema

- 按 LR-00 决策新增单调 migration；不修改 `000003_runtime_logs.sql` 或其他历史 migration。
- 如采用持久清理状态，使用数据库约束限制合法状态、时间字段和唯一目标；如新增 Incident 引用表，使用稳定 Incident/service-instance/sequence 外键或受约束引用。
- 为候选查询建立必要索引，避免按时间/大小扫描整表；索引必须服务真实查询而非未来假设。
- Incident 保护查询按 LR-00 决策实现，必须有确定性 sequence -> segment 测试；若新增关系表，同步外键、索引和历史升级测试。
- 更新 `docs/storage-schema.md`，明确正文仍不进入 SQLite。

### Repository

- 在 retention 使用方定义最小接口，至少提供：有界候选、登记总量/范围、状态 claim、完成/失败收口和启动恢复所需查询。
- 候选按 ADR 指定的全局稳定顺序和完整 tie-breaker 分页，必须有 batch 上限；不得因分页或跨实例排序在单实例 sequence 中间留洞。
- claim 使用事务和条件更新防止两个 worker 重复处理同一 segment；不能用进程内布尔值替代数据库约束。
- SQL 使用稳定 segment ID，不接收任意删除路径；返回领域结构，不泄漏 `sql.Null*`。
- `SequenceBounds`、`ListAfter`、`LastTimestamp` 对中间状态的可见性必须符合 ADR，不能返回已经不可读的文件。

### SQLite Gate

- 空库升级、当前最新版本升级、每个历史正式版本升级、重复启动、checksum 异常和 FK integrity。
- 并发 claim、batch 上限、Incident 保护、状态非法转换、busy/rollback/commit 失败。
- migration 回填不读取或复制日志正文，不产生不受限 JSON 展开。

## 7. LR-02：保留策略引擎

### 任务

- 实现纯策略层，根据 UTC `now`、配置、已登记关闭 segment 摘要、保护集合和容量统计选择候选；不在策略函数中删除文件或执行 SQL。
- 时间策略只选择超过 `retentionDays` 且未保护的关闭 segment。
- 容量策略按稳定最旧优先顺序选择，直到预计登记关闭 segment 总量回到目标；受 batch/time budget 限制时返回“仍超限”而非假装完成。
- 明确相同时间、跨 stream、跨服务/实例的稳定排序，避免每轮选择抖动。
- `.active`、spool、隔离文件和实际磁盘剩余空间按 ADR 作为独立统计输入，不将 `SUM(size_bytes)` 等同于文件系统全部占用。
- 所有大小计算使用带溢出保护的 `int64`；天数边界使用 UTC，不依赖本地时区。

### Policy Gate

- 覆盖零候选、仅时间超限、仅容量超限、联合超限、全部受保护、batch 截断、同 timestamp、跨服务/实例/stream tie-breaker、单实例 sequence 连续后缀、超大 size、时钟边界和取消。
- 相同输入产生相同候选顺序；策略层没有文件、数据库或 goroutine 副作用。

## 8. LR-03：文件执行器与崩溃恢复

### 删除边界

- 从已 claim 的登记相对路径开始，构造绝对路径并 canonicalize；同时验证真实日志根、普通文件类型、非 `.active` 和 segment 命名/登记一致。
- 文件打开/重命名/删除前后按 ADR 重新验证边界，覆盖 junction/符号链接交换和目录替换。
- 隔离目录如存在必须位于同一受信任日志根、权限受限、不可被 history 当成可读 segment。
- 删除错误按 `errors.Is/As` 分类；Windows sharing violation 可重试，路径逃逸/类型异常进入安全失败且不扩大范围。

### 恢复

- 控制面启动时在有界批次内恢复 pending/quarantined 状态；未完成时由后续轮次继续。
- 覆盖文件存在/缺失、元数据存在/缺失、隔离文件存在和重复恢复。
- 现有 `internal/logs/recovery.go` 不得重新登记已经进入淘汰协议的 segment。
- 清理后旧 cursor 落在保留范围前时继续返回 `LOG_CURSOR_EXPIRED`；不能静默跳过 sequence 缺口。

### Recovery Gate

- 使用真实临时目录和真实 SQLite，在每个协议步骤注入失败并重启，最终无永久悬挂元数据或孤儿清理文件。
- history reader 并发、Windows 文件占用、数据库 busy 和 context cancel 均有确定结果。
- 未登记文件、`.active`、目录、junction/符号链接和根外目标从未被删除。

## 9. LR-04：Worker 生命周期与配置装配

### 任务

- 扩展 `logs.Config` 或建立职责更清晰的 retention 配置类型，校验默认值、范围和组合；不要把无关 Server 配置塞入 Log Manager。
- 按 LR-00 结论接入当前 `serverConfig`/flag 入口并更新帮助与开发文档；不同时引入未使用的 YAML 配置层。
- 按 LR-00 对 health retention 先例的结论确定 owner；若复用 `runReconciliationLoop`，保持其有界 compactor 接口和 `SingleService.waiters` 关闭链路，若使用独立 worker，必须证明现有 owner 不能满足生命周期。worker 必须可取消、可等待，不得创建无人等待的 goroutine。
- 启动顺序保证 migration/repository 就绪后先恢复未完成清理，再进入周期执行；关闭顺序保证 worker 退出后再关闭 SQLite。
- ticker、失败 backoff、单轮时间预算和 batch 均有上限；错误只记录 segment ID、安全错误码和必要关联字段，不记录日志正文或任意绝对路径。
- 暴露最小只读状态给 storage pressure guard；不为 UI 新增未要求的监控页面。

### Lifecycle Gate

- 配置默认/非法值、启动失败、取消、重复停止、慢清理、ticker 释放和数据库关闭顺序测试通过。
- off 开关测试证明不创建 worker、不产生 claim/隔离/删除副作用；pressure guard 严格遵循 LR-00 固定的 off 契约，且日志捕获/读取和服务停止仍可用。
- `go test -race` 在环境支持时验证 worker/repository 并发；无 C 编译器时明确记录未执行，不伪报通过。

## 10. LR-05：存储压力启动保护

### 契约

- 策略配额状态与文件系统剩余空间状态分开计算和记录；两者任一进入压力都先触发有界清理，只有清理不足才拦截新资源创建。
- 使用 LR-00 固定的高/低水位和连续样本/最小持续时间实现迟滞；不得在一次瞬时样本上反复进入/解除 pressure。
- 在 `internal/logs` 或应用层定义最小 `StorageGuard`，返回可由 `errors.Is/As` 识别的稳定领域错误，不用错误字符串分支。
- 将 `LOG_STORAGE_PRESSURE` 登记到 Go 错误注册表、OpenAPI enum、错误码文档和契约测试；明确 HTTP 状态、retryable 和安全 details。
- 如果错误 details 暴露用量，只允许经过边界限制的数值/类别，不暴露数据目录路径或日志内容。

### 所有启动路径

- 核对 system start、single-service start/restart、system restart、auto-restart、process daemon、oneshot 和 Compose 的最终资源创建入口。
- guard 在端口/进程/Compose 副作用前执行；同一 Operation 内重复步骤可以复用有时效限制的结果，但不能用全局永久缓存。
- 压力存在时 Operation 以稳定失败步骤收口；已运行服务、健康检查、日志捕获、普通停止和事故查看继续工作。
- worker 成功释放空间或磁盘恢复后，guard 自动恢复允许新启动，不要求重启控制面。

### Pressure Gate

- 可控 fixture 分别制造策略配额超限、文件系统剩余空间压力、高/低水位之间抖动、全部 segment 受保护和 Windows 无法删除。
- 断言清理优先执行、清理不足返回 `LOG_STORAGE_PRESSURE`、所有启动路径一致、无进程/容器/端口副作用。
- 断言运行中服务不中断、日志继续落盘、停止可用，压力解除后下一次启动成功。

## 11. LR-06：安全回归、文档与最终 Gate

### 定向验证

```powershell
go test ./internal/logs ./internal/storage -count=1
go test ./internal/api ./internal/orchestrator -count=1
go vet ./internal/logs ./internal/storage ./internal/api ./internal/orchestrator
```

### 全量验证

```powershell
go test ./... -count=1
go vet ./...
./scripts/check.ps1
```

涉及真实 Windows 删除/文件占用的 Gate 必须使用隔离临时数据根，执行前记录 resolved absolute target，结束后仅清理该隔离根，并确认没有遗留进程、句柄、测试数据库或越界删除。

### 文档与契约

- 同步 `docs/detailed-design.md`、必要的 overall/phased plan、`docs/storage-schema.md`、运行配置/运维说明。
- 同步 migration、OpenAPI、`docs/error-codes.md`、DTO/错误映射和契约测试。
- 新增 progress/evidence，记录实际配置、批次、崩溃注入、Windows 文件占用和压力恢复结果；不改写历史 Gate。

### 发布止损与观察

- 使用候选二进制在隔离数据根上至少观察 24 小时且覆盖不少于两个 retention 周期；记录清理数、重试数、未完成中间态、两类 pressure 的进入/恢复和 guard 结果。
- 验证 off 开关可在重启时停止新 claim/隔离/删除，pressure guard 按 LR-00 契约进入可预测状态；不得要求删除数据目录或已管服务。
- 验证 ADR-0003 上一已验证版本的 marker 回退路径，并按现有升级策略处理 migration 前备份；明确记录该操作无法恢复已物理删除的 segment。
- Prometheus/OpenTelemetry、多主机和灰度发布明确为 N/A。可观测性使用不含日志正文/任意路径的结构化日志、安全计数和 Gate 证据。

### 安全扫描

- 扫描测试数据根、日志、数据库/WAL/SHM 和证据，确认无 Secret、Authorization、Cookie、完整环境和未脱敏正文泄漏。
- 对每个删除测试断言范围内目标被处理、范围外 sentinel 保留。

### 专项完成条件

- 来源 Prompt 第 13 节全部满足。
- 文件与 SQLite 元数据在成功、失败、取消和重启恢复后最终一致。
- 活动 segment、未登记文件和活动 Incident evidence 未被删除。
- `LOG_STORAGE_PRESSURE` 覆盖全部资源创建路径，且不影响运行服务和停止流程。
- 所有 migration 升级/checksum、定向/全量 Go 测试、vet 和 Windows Gate 结果已记录。
- off 开关、24 小时/两周期观察、ADR-0003 回退和 migration 备份边界已实测，无越界删除、永久中间态、无界重试或新启动误拦截。
- `AGENTS.md` 与 `CLAUDE.md` SHA-256 完全一致。

## 12. 责任、相对排期与关键路径

当前无已授权的日历排期和实名人员表，因此不编造截止日期或责任人。执行责任为“实现 Agent”，每个 Gate 的复核/批准角色必须在该工作包开始前指定；未指定审批者时不得自行宣告 Gate 通过。

| 工作包 | 执行角色 | 复核/批准 | 基准工作量 | 最晚前置条件 |
| --- | --- | --- | --- | --- |
| LR-00 | 实现 Agent | 待指定设计/安全审批者 | 1-1.5 人日 | 编码前完成 |
| LR-01 | 实现 Agent | 待指定存储审批者 | 1.5-2 人日 | LR-00 Design Gate |
| LR-02 | 实现 Agent | 待指定代码审批者 | 1-1.5 人日 | LR-01 SQLite Gate |
| LR-03 | 实现 Agent | 待指定 Windows/安全审批者 | 2-3 人日 | LR-01/02 Gate；Windows 文件占用/删除环境已就绪 |
| LR-04 | 实现 Agent | 待指定代码审批者 | 1-1.5 人日 | LR-03 Recovery Gate |
| LR-05 | 实现 Agent | 待指定编排/API 审批者 | 1.5-2 人日 | LR-04 Lifecycle Gate |
| LR-06 | 实现 Agent | 待指定发布批准者 | 1.5-2 人日 + 24 小时观察 | LR-05 Pressure Gate；Windows 真实 Gate 环境已就绪 |

- 串行关键路径为 LR-00 -> LR-01 -> LR-02 -> LR-03 -> LR-04 -> LR-05 -> LR-06；不以并行编码绕过前置 Gate。
- 基准开发/验证工作量约 9.5-13.5 人日，另预留约 20% 风险缓冲，用于 Windows sharing violation、崩溃注入、SQLite busy 和恢复协议返工；24 小时观察窗口不折算为人日。
- Windows 真实删除/文件占用验证环境是硬阻塞条件：必须在 LR-03 开始前可用，并在 LR-06 开始前再确认；不能以非 Windows fixture 代替最终 Gate。

## 13. 建议执行顺序

1. LR-00 完成 ADR、威胁模型与契约决策。
2. LR-01 完成 schema/repository；未通过 SQLite Gate 不进入文件删除。
3. LR-02 完成纯策略并通过确定性测试。
4. LR-03 完成文件协议和崩溃恢复；未通过 Recovery Gate 不启动后台 worker。
5. LR-04 接入配置和生命周期。
6. LR-05 在稳定 worker/状态之上接入所有启动路径。
7. LR-06 执行安全、Windows、全量回归和文档收口。

LR-00、LR-01 或 LR-03 任一 Gate 未通过时，本专项必须保持未完成，不能以“前端清空视图”或“单文件轮转正常”替代长期保留验收。
