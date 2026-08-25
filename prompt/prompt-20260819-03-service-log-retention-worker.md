# StackPilot 服务日志自动保留与存储压力处理 Prompt

> 状态：待执行
> 日期：2026-08-19
> 适用仓库：`E:\StackPilot`
> 工作包性质：独立后端工作包，不与服务日志查看器前端改造混合交付
> 计划文件：`plan/plan-20260819-03-service-log-retention-worker.md`
> 关联需求：`prompt/prompt-20260819-02-service-log-viewer-enhancements.md`
> 审核状态：已吸收 2026-08-19 第二轮外部审核中经仓库事实验证的意见

## 1. 任务

补齐详细设计中已经定义、但当前尚未实现的服务日志自动保留与存储压力处理能力。使用 `retentionDays` 和 `totalMaxBytes` 对 `DATA_DIR/logs` 下已关闭的 NDJSON segment 进行有界、可恢复的清理，并同步维护 SQLite `log_segments` 元数据；不得影响活动日志写入、SSE、历史 cursor、服务运行或事故证据。

本工作包解决长时间、高吞吐运行造成的日志磁盘增长。日志正文不存入 SQLite；SQLite 只保存 segment 元数据，因此不能把数据库清理误写成“删除 SQLite 日志正文”。元数据行仍必须随 segment 生命周期正确收敛，不能假定其增长永远可以忽略。

## 2. 开始前必须遵守

1. 完整读取并遵守 `AGENTS.md`、`CLAUDE.md` 和 `code_rule.md`。
2. 定向读取 `docs/detailed-design.md` 第 5.2、13、15、16、17、23、24 章，以及 `docs/overall-design.md`、`docs/phased-development-plan.md` 的日志保留、恢复和阶段边界。
3. 核对 `internal/logs`、`internal/storage/log_segment_repository.go`、日志 migration、`cmd/stackpilot/server.go`、OpenAPI 和错误码注册表。设计与机器可读契约不一致时先同步设计/契约，不得自行猜测。
4. 删除文件属于高风险能力。实现前必须形成明确的失败恢复和崩溃窗口设计；若现有设计不足，先更新详细设计或形成 ADR。
5. 保留用户已有修改，不创建 Git 提交、推送、变基或改写历史。

## 3. 当前事实

- `logs.Config` 当前只有 `DataDir`、`SegmentMaxBytes`、`LineMaxBytes`、`PollInterval` 等捕获/轮转配置，没有 `TotalMaxBytes` 或 `RetentionDays`。
- Server 只构造 Log Manager，没有日志 retention worker 的装配、生命周期或配置接线。
- 当前 Server 配置入口是 `serverConfig` 和命令行 flags；详细设计中的 YAML 配置层尚未实现，本工作包不为日志保留引入第二套配置体系。
- segment 已支持达到单文件大小或跨日后关闭并登记；这只是轮转，不会限制目录总容量。
- `LogSegmentRepository` 当前只有登记、列表、sequence 边界和最新时间查询，没有候选枚举、删除或清理状态能力。
- `log_segments` 保存相对路径、sequence/time 范围、大小和关闭时间，不保存 message 正文。
- Incident 证据引用当前位于有界 `context_json` 中，`EvidenceRef` 以 `ServiceInstanceID` 和 `LogSequence` 定位日志；尚无规范化的 Incident-segment 关系表。
- 现有 health retention 已提供可复用的有界模式：`HealthRetentionPolicy`、`CompactDefault`、批次上限 500，以及 `runReconciliationLoop` 所有的 ticker/关闭链路。
- `LOG_STORAGE_PRESSURE` 在详细设计中已有语义，但当前实现、错误码注册表和 API 映射尚未形成完整闭环。

## 4. 范围与设计原则

### 4.1 独立生命周期

- retention worker 由控制面明确拥有，使用可取消 `context.Context`，启动、周期执行、关闭和错误记录均有确定生命周期。
- 实现前必须对照 health retention 的 repository、batch、ticker 和 shutdown 模式，书面决定日志 retention 是并入现有 reconciliation owner 还是使用独立 worker，并记录理由。
- 每轮只处理有界候选和有界数量，不能无限扫描、无限事务、无限并发或长时间持有数据库锁。
- 清理失败不终止已运行服务、不阻塞日志落盘主链路；错误使用不含日志正文和内部敏感路径的结构化日志记录。

### 4.2 受信任删除边界

- 只处理 SQLite 已登记、`closed_at` 有效且不属于当前活动 writer 的关闭 segment。
- 删除前同时验证登记相对路径、canonical path、普通文件类型和最终路径仍位于真实 `DATA_DIR/logs` 根目录内；拒绝符号链接、junction、目录、设备文件和路径逃逸。
- 不删除 `.active` segment、原始活动 spool、未登记文件、工作区文件、Docker volume、StackPilot 平台日志或其他数据目录内容。
- 活动 Incident 引用的日志 evidence 必须受保护。保护查询必须将 `state='open'` Incident 的 `(serviceInstanceId, logSequence)` 确定性映射到 segment；只能选择有界强类型 JSON 解码或规范化引用关系，不得对 JSON 做字符串搜索。

### 4.3 文件与 SQLite 一致性

- 文件系统与 SQLite 无法共享原子事务。实现前必须定义可恢复的分步协议，覆盖“文件已处理/元数据未提交”“元数据已处理/文件仍存在”和控制面崩溃重启。
- 不能简单删除文件却永久保留可查询元数据，否则历史读取会访问不存在的已登记路径；也不能静默删除元数据后留下永不收敛的孤儿文件。
- 可通过显式待删除状态、受控隔离/重命名或其他经设计评审的机制实现幂等恢复。已合入 migration 只增不改；如需状态字段或清理记录，新增 migration 并覆盖全部历史升级路径。
- 清理完成后，`SequenceBounds`、历史窗口、cursor 过期判断和 `LastTimestamp` 必须反映实际保留范围。

## 5. 保留策略

- 固定沿用现有 flags -> `serverConfig` -> `logs.Config` 配置链路，不引入 YAML 配置层；同步修正详细设计中与实现不符的配置描述。
- `retentionDays` 默认 14 天，`totalMaxBytes` 默认 2147483648 字节。实现前在 ADR 中一次性固定 flag 名称、worker interval、batch limit、压力高/低水位、单位、边界、溢出校验和兼容行为；非法配置在启动边界失败。
- 提供默认启用、可在启动时关闭的 retention 逃生开关。关闭时不启动 worker、不执行新增删除；压力检测/guard 是否继续只读运行必须在 ADR 中单独固定并测试，不得使 off 模式加剧磁盘耗尽。日志捕获/读取和服务停止仍可用。
- 时间策略只淘汰超过保留天数且未受保护的关闭 segment。
- 容量策略按确定、稳定的最旧优先顺序清理未受保护关闭 segment，直到已登记关闭 segment 总大小回到目标范围。必须定义是否以及如何计入 `.active`、spool 和无法删除文件，不能用数据库声明大小冒充实际可用磁盘空间。
- 最旧优先顺序必须是跨服务、实例和 stream 的全局稳定顺序，含完整 tie-breaker；同时保证每个服务实例的保留集是 sequence 连续后缀，不得制造无法由 `LOG_CURSOR_EXPIRED` 解释的中间空洞。
- 时间和容量策略联合生效，但一次执行仍受 batch 上限和时间预算约束；未完成部分由后续轮次继续处理。
- 保留策略不要求全文索引，不改变日志正文格式，不重写现有关闭 segment。

## 6. 存储压力语义

- 把“策略配额超限（`totalMaxBytes`）”与“文件系统剩余空间压力”建模为两个独立信号。ADR 必须固定各自的数据源、检查频率、高/低水位、恢复条件和最小持续时间/连续样本数；解除阈值必须严于进入阈值，防止抖动。
- 达到压力阈值时先尝试清理符合条件的关闭 segment；清理后仍不足，阻止新的服务启动并返回稳定 `LOG_STORAGE_PRESSURE`。
- 存储压力不得杀死已经运行的服务，不得截断活动日志，不得把未脱敏内容转发到其他位置，也不得让 SSE 慢消费者反向阻塞落盘。
- 压力解除后新启动能力应自动恢复；状态判定不能依赖只存在于单个 HTTP 请求中的布尔值。
- 如新增 capability、状态端点字段或错误 details，先更新 OpenAPI、错误码注册表和契约测试，details 只暴露安全且必要的信息。

## 7. 并发与恢复

- 清理与 segment 关闭登记、历史读取、SSE catch-up、reconciliation 和 Incident 生成并发时必须有明确一致性策略。
- Windows 上已打开文件可能无法删除；将 sharing violation 作为可重试失败处理，不能扩大删除范围或强制终止读者。
- 重启时恢复未完成清理，并安全处理“登记存在但文件缺失”“待删除文件仍存在”“隔离文件存在”等设计中允许的中间态。
- 现有日志 recovery 对未登记/半完成 segment 有自己的修复逻辑；retention 恢复不得把已确认淘汰的 segment 重新登记。
- 删除导致旧 cursor 落在保留范围之前时，继续按 `LOG_CURSOR_EXPIRED` 契约响应，不静默跳过缺口。

## 8. Repository 与接口边界

- 在使用方定义最小 retention repository 接口，不把 SQL 类型泄漏到 `internal/logs`。
- 候选选择、保护条件和元数据状态变化由数据库约束及有界事务保证；不要先在内存列出整个目录再拼接删除 SQL。
- 删除或状态迁移必须使用稳定 segment ID 和受校验路径，不使用调用方提交的任意路径。
- 若统计实际目录用量，使用有界、安全的遍历并明确未登记文件的处理方式；不得顺手删除未登记内容。

## 9. 测试要求

- Log Manager/worker 单元测试覆盖默认配置、非法配置、时间策略、容量策略、联合策略、batch 边界、取消和重复执行。
- 使用真实 SQLite 和真实临时文件覆盖：空库、历史 migration 升级、登记 segment 清理、元数据/文件一致、sequence bounds、cursor expired 和 checksum 异常。
- 安全测试覆盖路径逃逸、绝对路径、符号链接/junction、目录替换、非普通文件、未登记文件、`.active` 文件和 TOCTOU 窗口。
- 并发/恢复测试覆盖正在登记、正在读取、Windows 文件占用、各崩溃窗口、重复启动和失败重试。
- Incident 测试证明活动 evidence 不被删除，Incident 解除保护后的后续清理行为确定。
- 容量顺序测试覆盖相同 timestamp、跨服务/实例/stream、tie-breaker、batch 截断和每实例连续后缀；清理后的旧 cursor 必须稳定返回 `LOG_CURSOR_EXPIRED`。
- 存储压力集成测试证明：符合条件的 segment 先被清理；清理不足时新启动返回 `LOG_STORAGE_PRESSURE`；运行中服务、日志落盘和停止流程不受破坏；压力解除后恢复。
- 压力测试分别注入策略配额和文件系统两类信号，覆盖高/低水位之间的抖动、恢复和重启后状态重算。
- 测试不得包含 Secret、Authorization、Cookie、完整环境或未脱敏日志；清理后扫描临时数据根，确认没有越界删除。

## 10. 文档与契约同步

同一变更按实际实现同步：

- `docs/detailed-design.md`
- `docs/overall-design.md`
- `docs/phased-development-plan.md`（仅在工作包或 Gate 变化时）
- `docs/storage-schema.md`
- 新 migration 及升级测试（若需要持久清理状态）
- `api/openapi.yaml` 和 `docs/error-codes.md`
- 运行配置和运维说明
- 对应 progress/evidence 与 Gate

## 11. 明确不做

- 不实现前端“删除历史日志”按钮或任意路径删除 API。
- 不清理日志正文所在目录以外的任何文件。
- 不删除活动 segment、活动 Incident evidence 或未登记文件。
- 不实现全文搜索、压缩归档、远程上传、OpenTelemetry/Prometheus 导出。
- 不在普通停止、卸载或“清空当前视图”时隐式删除持久日志。

## 12. 发布止损与观察

- 上线前验证 retention 逃生开关；关闭后不得再 claim、隔离或删除新 segment。
- 发布回退沿用 ADR-0003 的不可变版本和 verified marker 原子切换机制，数据库恢复遵循现有升级备份策略。二进制回退和 off 开关只能阻止后续清理，不得宣称能恢复已物理删除的日志。
- 候选版在隔离数据根上至少观察 24 小时且覆盖不少于两个 retention 周期。期间不得有越界/活动/受保护 segment 处理、永久中间态、无界重试或新启动误拦截。
- Prometheus/OpenTelemetry 导出、多主机和灰度发布在本地单用户工作包中明确为 N/A。可观测性以结构化日志、安全计数和 Pressure/Recovery Gate 证据为准。

## 13. 完成定义

只有同时满足以下条件才能声明完成：

1. `retentionDays` 和 `totalMaxBytes` 已接入真实配置与 Server 装配，默认值和校验有测试。
2. retention worker 生命周期有明确所有者、取消和有界批处理，不阻塞捕获/SSE 主链路。
3. 只清理受信任日志根内、已登记且已关闭的 segment，活动文件和 Incident evidence 保持不变。
4. NDJSON 文件与 `log_segments` 元数据在成功、失败和崩溃恢复后最终一致，无永久悬挂元数据或孤儿清理文件。
5. 清理后的历史范围、sequence bounds 和 `LOG_CURSOR_EXPIRED` 行为正确。
6. 清理不足时 `LOG_STORAGE_PRESSURE` 阻止新启动但不杀死运行服务，压力解除后可恢复。
7. migration、repository、Windows 文件语义、安全边界、并发和恢复测试通过。
8. 设计、配置、OpenAPI、错误码、存储说明和验证证据与实现一致，未泄露敏感信息或删除范围外数据。
9. retention off 开关、版本回退、候选版观察窗口和结构化可观测证据已验证，且没有把版本回退误表述为已删日志恢复手段。
