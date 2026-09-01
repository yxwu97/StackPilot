# StackPilot 系统可观测性、变更规划与验证式重启开发计划

> 状态：执行中（RO-00..RO-07 源码完成；Milestone A/B 已通过并发布，RO-08 ChangePlan 真实 API/Web Gate 通过；Milestone C 因四个外部系统运行 blocker 未通过，`phase3.verified-restart` 保持关闭）
> 日期：2026-08-31
> 来源 Prompt：`prompt/prompt-20260831-01-system-observability-and-change-planning.md`
> 阶段归属：Phase 3C 增强可观测性、复用已完成的 Phase 2E liveness 引擎开展五系统真实启用与验证，以及 Phase 3A CLI/Web 运维入口；不重新评审 Phase 2E Gate

## 1. 目标结果

形成一条可验证、无任意命令、无业务数据副作用的运行与变更闭环：

```text
活动实例/工作区
  -> 持续健康 + 有界资源采样
  -> running/workspace revision snapshot
  -> 只读 ChangePlan 与确定性风险
  -> 用户确认 Verified Restart Operation
  -> stop/start/readiness/liveness 稳定观察
  -> Operation、Incident、指标和修订证据
```

该闭环回答“系统现在是否稳定”“如果现在重启会改变什么”“重启后是否进入稳定状态”，但不执行 Git 更新、制品切换、数据库 migration、备份或回滚。

## 2. 固定决策

1. 本专项按三个里程碑交付：A 观测与身份，B 变更计划，C 验证式重启；任一里程碑未过 Gate，不发布该里程碑对应 capability 或以其为依赖的 capability。
2. running revision 使用启动时真实 resolved spec 和实例元数据；workspace revision 使用当前注册工作区的只读候选解析，两者不可互相覆盖。
3. ChangePlan 是持久、异步、只读 Operation；Verified Restart 是独立持久 Operation，现有 restart API/语义保持兼容。
4. 初版候选只允许同一注册工作区，不接受调用方路径、Git ref、命令、环境或端口覆盖。
5. Verified Restart 没有源码/数据自动回滚；失败保留现场并生成 Incident/证据。
6. Process 资源按 Supervisor/Job Object 完整树采集；旧 Supervisor 不支持时报告 unavailable，不退化为根 PID 冒充服务总量。
7. Compose 资源按严格 project/container identity 采集，StackPilot service 为主聚合层，容器为明细层。
8. liveness 先观察后决定 restart policy；本专项不批量打开自动重启。
9. 高频指标不进入领域 SSE；Web 使用有界 REST 时间窗和低频刷新，Operation/状态仍走现有事件模型。
10. 不在本专项实现 Prometheus/OpenTelemetry、全文检索、多 Agent 或业务系统升级。

里程碑与工作包映射固定为：Milestone A（观测与身份）=`RO-02 + RO-03 + RO-04`，Milestone B（变更计划）=`RO-05`，Milestone C（验证式重启）=`RO-06`。RO-00/RO-01 是三个里程碑的共享前置，RO-07/RO-08 负责交付入口与最终收口。Milestone A 的资源监测 capability 必须等待 RO-02/03/04 全部 Gate；Milestone B 可在 RO-02/03 Gate 后推进，不把 RO-04 错设为 ChangePlan 正确性的硬依赖；Milestone C 必须等待 RO-03/05 Gate。任一 capability 的生产实现及其实际依赖 Gate 未全部通过时不得发布。

## 3. 工作包总览

| ID | 工作包 | 主要结果 | 前置 |
| --- | --- | --- | --- |
| RO-00 | 设计、容量基线与 ADR | 冻结指标、快照、计划、验证、协议和保留语义 | 无 |
| RO-01 | 机器契约与 capability | OpenAPI、Schema、领域枚举、错误码和 migration 设计 | RO-00 |
| RO-02 | System Revision Snapshot | running/workspace 不可变安全快照 | RO-01 |
| RO-03 | 持续健康真实启用 | 五系统 liveness 覆盖与 blocker 矩阵 | RO-00/01 |
| RO-04 | Resource Sampler | Process/Compose 指标、聚合、保留和恢复 | RO-01 |
| RO-05 | ChangePlan dry-run | 只读差异、风险、stale/blocked 判定 | RO-02/03 |
| RO-06 | Verified Restart | 计划绑定重启与稳定验证 Operation | RO-03/05 |
| RO-07 | API、CLI 与 Web | 监测、修订、计划和验证式重启工作流 | RO-01..06 |
| RO-08 | 自动化、真实 Gate 与文档 | 全量回归、五系统分级验收和 evidence | RO-02..07 |

## 4. RO-00：设计、容量基线与 ADR

### 任务

- 完整核对 Prompt 指定的领域、存储、平台、API、Web 和五个真实 Manifest，形成“已有可复用/必须新增/明确不做”矩阵。
- 新建 ADR，至少固定：
  - Process Job Object 和 Compose 资源指标口径、单位、CPU delta/核数归一化、进程/容器聚合。
  - Supervisor 观测协议的向后兼容、版本协商、旧 Supervisor fallback 和升级接管窗口。
  - sampler 所有权、并发、timeout、抖动、故障隔离和 reconciliation 恢复。
  - revision snapshot 白名单、Git 固定探测、文件/digest 上限、规范 JSON 和敏感数据边界。Git probe 是本项目首个专用 Git introspection 执行边界，必须纳入受信工具/模板白名单审计：只解析固定受信 `git.exe`，使用只读命令 allowlist 和固定 argv，不经 shell，不接收模板或用户参数；working directory 必须是 canonical 已登记工作区，并设置有界环境、timeout 和 stdout/stderr 上限。
  - ChangePlan 生命周期、风险等级/规则版本、幂等、stale 和 blocked 语义。
  - Verified Restart 步骤、取消点、稳定窗口、验证失败和无自动回滚边界。
- 从 StackPilot 已登记的五系统真实 Manifest 快照读取服务规模；只有在对应真实 Gate 单独授权后才可只读访问外部 Manifest 文件，生产代码不得硬编码外部路径。以现有 `DefaultHealthRetentionPolicy`（24 小时明细、每实例最多 1000 条近期记录、每批清理 500 条）和 `healthRetentionInterval`（1 小时）作为对照基线，另行执行资源采样的 SQLite 写入/查询基准，测算不同采样间隔与保留期的日写入量、数据库增长、聚合耗时和 busy 影响；据此冻结资源指标自己的默认值和硬上限，不直接套用 health 参数。
- 核对当前 migration 头、Operation 活动唯一约束、现有 health hourly aggregation/retention worker 和 Web 高频刷新行为。
- 固定 capability 名称。建议候选为 `phase3.resource-monitoring`、`phase3.change-planning`、`phase3.verified-restart`；最终以公共注册与 Schema 一致性为准。
- 固定是否新增声明式 post-start verification。若不能在本专项内安全完成 service-bound loopback HTTP GET Schema/SSRF 约束，则初版 Verified Restart 只使用 readiness + liveness 稳定窗口，并把业务 smoke 保留为真实 Gate，不做半实现字段。

### Gate

- ADR Accepted，overall/detailed/phased design 与其一致。
- 指标单位、采样/保留、协议兼容、快照白名单、风险规则、Operation 和验证失败语义没有开放问题。
- 明确旧 Supervisor、Docker 不可用、Git 不存在、外部依赖和 process-only health 的产品呈现。
- 没有以任意命令、整个目录 hash、根 PID 近似或自动 Git/数据库动作解决问题。

### 执行记录（2026-08-31）

- 状态：已完成。
- ADR-0010 已 Accepted，overall/detailed/phased design 已同步；初版不新增 post-start verification 字段。
- 只读登记基线为 5 个工作区、19 个服务，显式 liveness 为 0，自动重启均为 never；因此 RO-03 仍是硬 Gate。
- 19 服务隔离 SQLite WAL 基准在 30 秒间隔下为 54,720 行/日、约 7.44 MB/日；据此冻结 30 秒默认、24 小时明细、30 天小时聚合和有界 worker/队列/查询参数。
- 当前 migration 头为 000016；现有健康保留对照为 24 小时、每实例 1,000 条、500 行删除批次和每小时执行。
- 外部五系统 Manifest 未被修改；真实健康端点和副作用 Gate 未执行，也未发布三个新 capability。

## 5. RO-01：机器契约、领域与 capability

### 领域与存储设计

- 增加最小领域类型：Metric source/status、Revision kind、ChangePlan state/risk/item、Verification result，以及经 RO-00 固定的 Operation 类型。
- Operation type 建议新增 `change-plan` 和 `verified-restart`；`Valid()`、存储 CHECK、事件、DTO、筛选和恢复逻辑必须同步，不能只改 Go 枚举。
- 设计下一版本 migration。本计划创建时当前头为 000016；执行时重新核对后只新增不修改历史。
- 预计表：
  - `system_revision_snapshots`：不可变 digest、workspace/system、kind、schema/version、安全 JSON、创建时间。
  - `change_plans`：plan ID、Operation、from/to digest、规则版本、风险/blocked、安全结果 JSON、终态时间。
  - `runtime_metric_samples`：service instance、source、UTC bucket、CPU/内存/数量、availability/error code。
  - `runtime_metric_hourly_aggregates`：小时、count/min/max/avg 或 ADR 固定统计量。
  - verification 如无法由 Operation/health/Incident 无损表达，再增加聚焦表；不得先建空壳表。
- 关键外键、唯一性、活动计划/幂等、JSON valid、非负数值、UTC 和删除限制由数据库保证。

### Manifest/Schema

- liveness/restart 沿用现有字段，不创建第二套健康配置。
- 如 RO-00 接受 post-start verification，增加闭合 `spec.verification` 或等价结构：service-bound、loopback HTTP GET、状态/body 断言、timeout、stableWindow；禁止 headers、Secret、method、body、外部 URL 和脚本。
- 同步 Go 类型、JSON Schema、semantic validator、模板引用、normalized digest、示例和 capability 注解。

### API 与错误

- 先更新 OpenAPI，固定：
  - 指标时间窗查询与降采样 DTO。
  - running/workspace revision 安全摘要。
  - ChangePlan 创建、读取和差异 DTO。
  - Verified Restart 创建和 Operation 关联。
- 按 `METRIC_*`、`REVISION_*`、`CHANGE_PLAN_*`、`VERIFICATION_*` 四个前缀族登记稳定错误码，至少覆盖 metric unavailable/unsupported、revision source unsafe/too large、Git probe failure、plan stale/blocked/not found、verification unavailable/failed；capability gate 继续复用 `FEATURE_NOT_ENABLED`，最终名称、HTTP、Retryable、details 和安全消息遵守现有注册表风格。
- 建立服务端 capability 单一注册源，统一公共名称及 Manifest validator 的内部映射；Web 只消费 `/version` 返回的 capability，不维护重复的完整注册表。JSON Schema annotation、OpenAPI 等机器契约与服务端注册源建立一致性测试；除非现有工具链能稳定生成，不强行用跨 Go/Schema/TypeScript 代码生成制造新的脆弱依赖。

### Gate

- OpenAPI、Schema、错误码、领域枚举、migration SQL 和前后端 DTO 的契约测试先通过。
- capability 未启用时所有新入口稳定关闭；旧客户端、旧 Manifest 和现有 restart 无行为变化。
- DTO 不暴露绝对路径、argv、环境值、Secret、原始 Git/Docker 输出或内部平台 token。

### 执行记录（2026-08-31）

- 状态：已完成；三个 Phase 3 capability 已登记但保持未发布。
- 新增 capability 单一 Go 注册源，Server、Manifest validator 和 Importer 使用同一已发布列表/别名；Schema annotation 有注册一致性测试。
- 领域层新增 metric、revision、health coverage、change plan、verification 闭合枚举，`change-plan` 与 `verified-restart` Operation 类型，以及 `rev_<ULID>`/`plan_<ULID>` 身份。
- OpenAPI 固定指标、revision、ChangePlan 和 Verified Restart 路径/安全 DTO；服务端当前只返回带 capability 名称的 `FEATURE_NOT_ENABLED`，无探测或生命周期副作用。
- 新增 migration 000017，保留 Version 16 Operation/step/event/port lease 证据，创建 revision、plan、metric detail/hourly 表；未创建 verification 空壳表。
- 错误码按 `METRIC_*`、`REVISION_*`、`CHANGE_PLAN_*`、`VERIFICATION_*` 冻结并通过 OpenAPI/Go 注册一致性测试。
- 定向 Go、Schema、OpenAPI、SQLite migration/约束测试、`go vet`、Web type-check 和生产 build 通过。全量 Go 回归的 Windows Named Pipe/Supervisor 与 DPAPI/DACL 测试因当前受限环境 Access denied/连接超时未通过，保留到具备真实 Windows 权限的 RO-08 Gate；未将其记录为通过。

## 6. RO-02：System Revision Snapshot

### 采集器

- 在独立包中实现使用方最小接口，例如 `RevisionCollector.Collect(ctx, request)`；orchestrator 依赖接口，不依赖 Git/Docker/文件细节。
- 复用 Workspace/Manifest/ResolvedSpec/Runner/Secret repository，不复制已存在的事实。
- running collector 从 SystemInstance 的 manifest/resolved spec digest、实际 service instances、Secret metadata versions 和严格 Compose identity 组装历史事实。
- workspace collector 重新读取有效 Manifest，执行无副作用的结构/语义/Runner 探测，并采集白名单版本文件 digest；不得申请端口、启动 Docker Desktop、build、运行 oneshot 或解析 Secret 值。
- Git probe 使用服务端固定、受信 `git.exe` 和固定 argv，working directory 为 canonical workspace，设置 timeout/输出上限/安全环境；不存在、非仓库、unsafe directory 和 dirty 均返回结构化状态。
- 文件 collector 使用 canonical path、普通文件、白名单、单文件/文件数/总字节上限；JSON/XML 使用结构化解析，Python/Go 等无现成安全 parser 时只记录文件 digest 和类型，不新增依赖仅为显示版本。
- Compose image digest 通过现有严格 identity/preflight 适配器获取；未运行或只有 tag 时明确标记，不拉取镜像。

### 规范化与持久化

- 定义 versioned canonical JSON，字段稳定排序，nil/unknown/unavailable 语义明确。
- SHA-256 对规范字节计算；相同 digest INSERT OR IGNORE 后校验碰撞，模式复用 ResolvedSpecRepository。
- 设置 JSON 最大长度，存储前完成脱敏和 Secret 扫描；不得在存储后依赖输出层补救。
- 提供 repository 的 Get/ListLatest，所有查询有 workspace、kind、limit 边界。

### 测试

- fixture 覆盖 Git clean/dirty/non-repo、空格/中文路径、junction escape、白名单缺失/超限、文件变化、Runner digest 变化、Compose tag/digest 和 Secret version 变化。
- 证明同输入 digest/行数稳定，running snapshot 不受后续 Manifest/工作区变化污染。
- 对五系统执行只读快照 Gate，只记录安全摘要；不得打印或扫描展示 `.env`/Secret 值。

### Gate

- running/workspace revision 可独立读取和比较，缺失来源不会被伪造。
- 快照创建没有业务进程、Docker、端口、Git 写入或工作区文件副作用。
- 安全扫描证明数据库、API、日志和 evidence 无 Secret/完整路径/完整命令。

### 执行记录（2026-08-31）

- 状态：已完成；五系统只读快照与安全扫描 Gate 已通过。Milestone A 仍等待 RO-03/RO-04 真实 Gate，capability 保持未发布。
- 新增 `internal/revision`，实现 `revision/v1` running/workspace 规范快照、稳定排序、SHA-256、不可变复用和有界 repository 查询。
- workspace 采集仅使用严格 Manifest、受信 Runner、固定无 shell Git argv 和白名单文件摘要；Git clean/dirty/non-repository、中文/空格路径、文件逃逸/上限和采集中源变化测试通过。
- running 采集只读取活动实例、启动时 ResolvedSpec 和 Secret metadata version；缺失 Compose image/Git 启动身份均写为明确 unavailable，不从当前工作区补造。
- SQLite 幂等、digest 碰撞防护、归属和敏感值排除测试通过。
- `scripts/verify-ro02-revisions.ps1` 使用隔离数据库对五个真实工作区各采集两次，五组 revision ID/digest 均稳定；数据目录由固定路径和所有权标记约束并已清理，没有启动业务进程、Docker、端口或写入外部工作区。
- 安全摘要见 `docs/evidence/ro02-real-workspace-revisions.json`：未检出绝对工作区路径、Secret/令牌/连接串，Git 状态和 Runner 探测均为实际结果。当前 Manifest 共 21 个服务，其中 AgentHub 8 个；这与 RO-00 从 Version 16 控制库读取的登记快照 AgentHub 6 个、合计 19 个不同，作为 workspace revision 漂移保留，不改写历史基线。

## 7. RO-03：持续健康真实启用

RO-03 不重新打开或重新评审已完成的 Phase 2E Gate；它复用现有 Phase 2E liveness、Incident、restart attempt 和 retention 引擎，只负责在五个真实系统上补齐声明、启用观察、验证覆盖并保留业务端点 blocker。

### StackPilot 核心

- 回归并补强现有 Liveness Engine 的单 owner、阈值、恢复、state_version、同事务事件、health retention、Incident 和 restart attempt 行为。
- 增加“健康覆盖摘要”：每个 daemon 显示 readiness/liveness 类型、业务级或 process-only 级别、最近结果和是否满足 Verified Restart 前置。
- 不自动从 readiness 推导 liveness。可提供只读建议，但 Manifest 仍需显式字段并通过 Schema/semantic validator。
- 自动 restart policy 保持默认 `never`；本工作包只在独立真实故障 Gate 后才允许某服务显式启用。

### 业务系统改造轨道

- BTC：Backend/Web HTTP liveness。
- AIWS：Infrastructure Compose、Server/Agent Runtime/Web HTTP liveness；Keycloak Configure 维持 oneshot。
- PMS：Backend/RAG/Web HTTP liveness，外部 MySQL/Redis/Qdrant 不纳入生命周期。
- AgentHub：Infrastructure/API/Web liveness；三个 Worker 新增最小、只读、无敏感数据的业务健康端点后才能达到完整覆盖。
- GNMarket：Web/Frontend liveness；Job 新增业务健康端点后才能达到完整覆盖。
- 修改每个业务仓库前必须读取其自身 Agent/设计/测试规范，单独报告实际命令和未执行项。

### Gate

- slow failure、瞬时抖动、持续失败、恢复、控制面重启、服务退出和最大重启次数 fixture 全部通过。
- 五系统覆盖矩阵由真实 Manifest 和端点证据生成；AgentHub/GNMarket 未补端点前 blocker 必须保留。
- 资源采样或快照故障不影响 liveness 调度。

### 执行记录（2026-08-31）

- 状态：已完成；五系统真实 Manifest、端点、运行状态和持久 liveness Gate 通过。资源监测、变更计划与验证式重启 capability 仍分别等待 RO-04/05/06，不因 RO-03 通过提前发布。
- readiness/liveness 结果增加显式 purpose；migration 000019 将无法证明用途的历史数据保守归为 readiness，覆盖摘要只读取最新 liveness。
- 覆盖级别固定为 business/container/process-only/unavailable，必需 daemon 只有 Ready 且最新业务/容器 liveness 成功才满足验证；process-only 和 unavailable 明确阻断。
- 多服务状态投影从持久化 ResolvedSpec 计算覆盖；历史单服务启动路径没有该快照时返回 unavailable，不从 readiness 或当前 Manifest 推导。
- 真实 SQLite + ResolvedSpec + liveness-purpose 结果的状态投影测试、覆盖纯函数测试及现有 Phase 2E orchestrator 回归通过；五个外部业务仓库均已增加或启用真实 liveness，自动重启仍保持 `never`。
- `scripts/verify-ro03-real-health.ps1` 以 SQLite `mode=ro` 和 `query_only` 读取默认 Version 19 控制库，验证 5 个活动系统、18 个 required daemon 全部 Ready 且最新 liveness 成功：AgentHub 6/6（business/container）、AIWS 4/4（business/container）、BTC 2/2、GNMarket 3/3、PMS 3/3；required oneshot 均为 Completed。证据见 `docs/evidence/ro03-real-health.json`。
- AgentHub 真实 Gate 依次暴露并修复 pgvector 镜像、规划端口未注入、对象存储合成凭据和 Worker 健康端点问题；StackPilot 专用配置禁用需要外部 PMS/Vault Secret 的可选只读集成。最终 24 步 Start Operation 全部成功，未停止用户另行运行的开发容器或测试进程。

## 8. RO-04：Resource Sampler、聚合与保留

### Windows Process 指标

- 扩展 Supervisor 协议，用 Job Object accounting 查询完整受管树的累计 CPU、内存口径、活动进程数和采样时间。
- 新字段/消息保持旧协议可识别的版本协商；Server 连接旧 Supervisor 时返回 `unsupported`，基础 inspect/stop/recover 继续工作。
- 指标身份核验继续包含账号、PID/创建时间、可执行路径、command digest 和 protocol；不得按 PID/进程名单独查询。
- 使用相邻累计样本和单调时间计算 CPU；重启、计数回退、时间反常时丢弃该区间而非产生负值/尖峰。

### Compose 指标

- 使用固定 Docker CLI/API 适配器，对严格标签/identity 解析出的精确容器 ID 执行一次性 stats；固定 argv、无 shell、timeout、stdout/stderr 上限。
- 每容器保存明细，按 Manifest Compose service group 计算聚合；容器消失、重启或 identity 不匹配形成 unavailable，不串入其他项目数据。
- Docker daemon 不可用不触发 Desktop 自动拉起；采样路径必须无生命周期副作用。

### 调度、存储和派生指标

- Sampler 只调度 Ready/Degraded daemon，使用有界 worker pool、抖动和 context；Server shutdown/reconciliation 正确移交。
- repository 批量写入明细；按 UTC 小时聚合后小批量删除，busy/commit/rollback 错误不可忽略。
- 启动耗时、健康耗时、重启次数和日志错误率优先从现有 OperationStep、health、restart、log 元数据聚合，不重复高频写相同事实。
- 提供按时间窗/粒度/服务查询，点数超过上限时服务端降采样或拒绝。

### 测试与 Gate

- Windows fixture 产生可控 CPU/内存/多级子进程，核对 Job 总量、重启边界、旧协议和控制面恢复。
- Compose fixture 覆盖多容器、restart、remove、daemon down、超时、大输出和严格隔离。
- SQLite 压力测试达到 RO-00 容量目标；Sampler 开/关对 start/stop/log/liveness 延迟无不可接受回归。
- 非 Windows 实现保持明确 unsupported，不因交叉编译成功宣称 Phase 3B 支持。

### 执行记录（2026-09-01）

- 状态：已完成；受控 Windows Job Object、Docker 双容器 stats、临时安装树跨版本接管、确定性 stats/stop 并发隔离、真实安装运行身份及生产负载 sampler Gate 均已通过，`phase3.resource-monitoring` 已发布。
- Supervisor protocol v2 以 additive `observe-service` 提供 Job 累计 CPU、class 28 `JobMemory` 和活动进程数；v1 生命周期兼容并对资源观测返回 unsupported。
- Process CPU 采用相邻累计值按墙钟和逻辑处理器归一化；Compose 使用严格 project/container identity、固定非流式 Docker argv、输出/超时上限，并保存服务聚合与容器明细。
- 新增有界 sampler（4 workers、128 queue、5 秒 timeout、抖动）、migration 000018 容器明细约束、24 小时明细/30 天小时聚合和每小时小批量清理。
- sampler 回归覆盖成功采集、容量截断、来源 timeout、单 context owner/退出、runtime/store 错误传播和 unavailable/unsupported 分类；控制面异常启动退出路径已固定为先 cancel、再等待 sampler/import/reconciliation、最后关闭数据库，端口冲突和实际 SQLite + runtime repository + active sampler 回归通过。Windows `-race` 因当前工具链 `CGO_ENABLED=0` 未执行，不将其记录为通过。
- `scripts/verify-ro04-sqlite-contention.ps1` 在自动清理的临时 WAL 数据库上按 19 服务、2,280 条预载指标执行 60 轮并发指标批次、4 轮有界 compaction、60 次 liveness 写入和 60 次 runtime reconciliation 更新。健康写 p95 为 0.684 ms，reconciliation p95 低于计时器分辨率，最大分别为 86.956/41.504 ms；指标批次 p95 5.501 ms，compaction 最大 13.189 ms，低于隔离 Gate 的 250 ms 控制写 p95、1 秒控制写最大值和 2 秒后台任务最大值。Gate 还反查 WAL 模式、预载/最终行数和 reconciliation marker，证据见 `docs/evidence/ro04-sqlite-contention.json`；这只关闭 SQLite 并发隔离检查，不代表生产负载 Gate。
- metrics、Compose、SQLite repository/聚合/保留测试通过。真实 Windows 权限下 Supervisor 与 Process Driver 全包集成测试通过：协议 v2 对完整 Job Object 返回至少两个活动进程、正内存和 UTC 观测，v1 inspect 保持兼容且资源观测明确 unsupported；临时 `versions/<sha256>` 安装树中 marker 选中的候选成功接管旧 Supervisor hello。Docker Desktop 受控 fixture 对 `database + web` 两个容器完成严格身份 stats、聚合、恢复、发现、非破坏 stop 和资源清理；确定性并发 fixture 进一步证明阻塞的 `docker stats` 不会阻塞同一 Lifecycle 的固定 Compose stop，且释放后观测正常结束。证据见 `docs/evidence/ro04-real-runtime-fixtures.json`。
- `scripts/verify-ro04-runtime-observation.ps1` 以 SQLite `mode=ro` 和 `query_only` 读取当时仍为 Version 16 的默认控制库，没有调用 migration 或写入指标。该时点 4 个活动系统中，AIWS `infrastructure` 通过严格持久身份返回 6 个容器的 CPU/内存聚合；其余 11 个 Ready process 服务均返回 `PROCESS_IDENTITY_MISMATCH`。原因是 `go run` 开发 Gate 不属于真实安装标记允许的受信控制程序，不能据此判定持久身份已损坏。安全摘要见 `docs/evidence/ro04-runtime-observation.json`，不包含 PID、路径、命令摘要、平台令牌或容器 ID。该历史 blocker 后续已由真实安装候选和 `docs/evidence/ro04-installed-metrics.json` 关闭。

### 当前执行边界

RO-02/RO-03/RO-04 Gate 已通过，资源监测 capability 已发布。RO-05 生产实现与真实计划 Gate
通过后已发布 ChangePlan；RO-06 源码与 fixture 完成，但仍必须等待五系统真实执行 Gate，
不得因 BTC 单系统满足前置条件而提前发布 Verified Restart。

## 9. RO-05：ChangePlan dry-run

### 实现

- 新建使用方接口和 orchestrator use case，提交 `change-plan` Operation，建立 `collect-running`、`collect-workspace`、`compare`、`classify-risk`、`persist-plan` 等稳定步骤。
- 同一 workspace 活动 Operation 约束按设计处理：计划是只读但会消耗探测资源。RO-00 决定是否与 mutation 共用 workspace lock；不得用进程内布尔值绕过数据库约束。
- 比较器消费两个 versioned revision，不直接读文件；输出稳定排序的 typed ChangeItems。
- 风险注册表以代码常量/规则实现并带版本，覆盖 Prompt 要求的服务、DAG、driver/mode、Runner、端口、health/restart、依赖、Compose、Secret 和 migration 文件变化。
- dirty workspace、unknown identity、缺少 required liveness/verification、Manifest invalid 和高风险未确认形成明确 blocker；不把未知自动归为安全。
- plan result 持久化 from/to digest、规则版本、摘要和 blocker；API 查询不重新计算历史。

### 幂等与并发

- Idempotency subject/key、request digest、workspace ownership 和终态沿用 Operation 规则。
- 计划采集中工作区变化时，开始/结束来源 digest 不一致则失败为 source changed，不产生混合快照计划。
- 重复相同 from/to/rule version 可复用不可变结果，但必须保持 Operation 审计语义。

### Gate

- 计划过程没有 PortLease、进程、Docker Desktop、build、oneshot、Secret resolve 或业务文件写入。
- 测试覆盖每类差异、稳定排序、risk 上卷、blocked、source race、取消、timeout 和恢复。
- 五系统当前状态生成符合事实的计划：dirty/non-Git/external dependency/process-only health 均可见，不出现“一键升级可用”误导。

### 执行状态（2026-09-01）

- 生产实现、SQLite 持久化、API/CLI、确定性规则、fixture 和真实候选 Gate 已完成，`phase3.change-planning` 已发布。
- BTC、PMS、GNMarket、AgentHub 均生成 `ready/high` 持久计划；AIWS 停止且没有 running revision 时，计划按契约失败而不是伪造 from revision。
- 真实 Web 已在 BTC 上验证 capability enabled、Operation 提交/轮询、revision/风险/差异刷新和页面恢复；截图及 Operation 证据见 `docs/evidence/ro08-real-gates.json`。

## 10. RO-06：Verified Restart

### 提交边界

- 新增独立 API/use case/Operation，输入仅包含 system/workspace、ChangePlan ID、Idempotency-Key 和必要 CSRF/auth 元数据。
- Submit 阶段验证 capability、计划归属/终态、活动 Operation 和基本 blocker；后台执行在 stop 前再次完整验证。
- 执行重新采集 workspace revision，与 plan.toDigest 精确比较；不一致返回 `CHANGE_PLAN_STALE`，旧系统不停止。

### 执行步骤

1. `load-plan`
2. `refresh-candidate-revision`
3. `validate-plan`
4. 复用现有 reverse-topology stop steps
5. 复用 fresh start/port plan/readiness steps
6. `stability-observation`
7. 可选 `post-start-verification`
8. `persist-verification`

- 不复制现有 stop/start 状态机；通过组合用例和稳定内部接口复用。
- required daemon 必须在稳定窗口持续 Ready；任一 Degraded/Failed、实例身份变化或 liveness owner 丢失使 Operation 失败。
- oneshot 必须为 Completed；可选服务失败按现有系统 Degraded 语义和计划策略处理。
- 验证失败生成 IncidentContext，引用 plan、revision、health/metric/event/log 的有界脱敏证据。
- 无自动旧版本恢复。若 stop 之前失败，保证零生命周期副作用；stop 之后失败，准确报告当前实例/服务状态并保留显式 stop/restart 操作。

### 恢复与取消

- 测试控制面在计划校验、stop 中、start 中、ready 后和稳定窗口中崩溃；恢复不得重复启动或绕过计划 digest。
- 取消遵循现有 stop/start owner，不用 `context.Background()` 绕过；稳定观察可取消并形成真实终态。
- Verified Restart 与 liveness 自动 service-restart 的并发由数据库/状态机约束解决，禁止互相重复拉起。

### Gate

- stale/blocked 计划在 stop 前拒绝并证明 PID/容器/端口不变。
- 成功、启动失败、liveness 失败、验证失败、取消、超时、崩溃恢复和并发分支全部通过。
- 旧 `/restart` 契约和现有系统/服务 restart 回归无变化。

### 执行状态（2026-09-01）

- 源码与 fixture 已完成：计划绑定异步 Operation、停止前 digest/blocked/健康覆盖校验、逆拓扑 stop、fresh start/readiness、稳定观察、取消、并发锁、无自动回滚和旧 restart 回归均有测试。
- 稳定验证失败会生成持久 Incident，使用 Operation/ChangePlan/Revision ID、Operation event 和最近 liveness result 的有界引用保存证据；不复制命令、环境、路径、响应正文或未脱敏日志。
- 通用 `RecoverInterrupted` 会将控制面重启时未终结的步骤失败/跳过并释放数据库锁，不恢复或重放 verified restart。分阶段真实崩溃注入矩阵和五系统真实执行仍属于 RO-08 Gate。
- `phase3.verified-restart` 保持未发布。2026-09-01 的获授权真实恢复中，仅 BTC 完成普通 stop/start、readiness、业务 liveness 和新计划；PMS 在 RAG readiness 超时，AIWS/AgentHub 在 Compose preflight 超时，GNMarket Web 进程启动后退出。五系统 Gate 未通过，不用单系统结果替代发布条件。

## 11. RO-07：API、CLI 与 Web

### API/CLI

- Handler 只做认证、校验、DTO、错误映射和 use case 调用；指标计算、快照和风险不进入 API 层。
- 指标 API 强制 UTC 时间窗、最大跨度、合法粒度、最大点数和 workspace/system 归属。
- CLI 通过 API 提供状态/指标摘要、plan 创建/等待/JSON 输出和 verified restart；命令名在 Phase 3A CLI 规范中固定。
- 审计记录 ChangePlan 创建和 Verified Restart 提交/取消；只读指标查询不记录敏感参数。

### Web

- 在现有系统详情增加“监测/变更”视图或紧凑 tabs；服务详情展示 liveness 覆盖、当前指标和趋势。
- 使用独立 Pinia store/composable 管理 metrics、revision、plan 和 verified restart；REST 初始化，Operation/SSE 合并，资源趋势低频有界刷新。
- 趋势页使用稳定尺寸和明确单位，缺失/unsupported/过期数据可见；不使用颜色作为唯一状态。
- 变更计划显示 from/to revision、dirty/unknown、分类差异、风险和 blocker，不显示绝对路径/完整 digest 默认值。
- Verified Restart 对话框显示服务影响、稳定验证和无自动回滚边界；blocked/stale/active Operation 状态由服务端响应权威决定。
- 复用 Element Plus 和现有图标，桌面/移动无重叠、溢出、嵌套卡片或高频布局跳动。

### Gate

- OpenAPI contract、API security、CLI JSON、Web unit/component、type-check/build 全部通过。
- Playwright 覆盖指标缺失/趋势、plan blocked/stale、成功/失败 Verified Restart、刷新恢复和移动布局。
- 高频指标未通过 SSE 逐点推送，前端缓存和轮询有上限。

### 执行状态（2026-09-01）

- API/CLI 契约已覆盖 metrics、revision、ChangePlan 和 verified restart；系统 status 新增服务端 health coverage 安全投影，OpenAPI、错误码映射和 DTO 测试同步完成。
- Web 已在系统详情增加“监测 / 变更”页签，以独立 Pinia store 管理 1/24 小时指标、revision、计划 Operation 和 verified restart Operation；指标每 15 秒单飞刷新，Operation 等待有 60 秒硬上限。
- 最新 `0.1.1` 候选已升级到当前用户安装并广告 `phase3.resource-monitoring`、`phase3.change-planning`；`phase3.verified-restart` 仍关闭。浏览器先前已验证桌面与 390px 移动布局，本轮又在 BTC 真实运行实例上验证指标、完整健康覆盖、ChangePlan enabled/刷新恢复及未发布 Verified Restart 的禁用态。
- Web 单元测试、TypeScript strict check 和生产构建已通过。浏览器在 BTC 恢复窗口记录到两次 metrics `503`，恢复后 2/2 服务指标正常渲染；这是采样不可用的显式过渡态。plan blocked/stale 和 verified restart 成功/失败未用 mock 冒充真实 Gate，继续受 Milestone C 发布条件约束。

## 12. RO-08：自动化、真实 Gate 与文档收口

### 自动验证命令基线

执行时以仓库实际脚本为准，至少覆盖：

```powershell
gofmt -w <本次修改的 Go 文件>
go test ./internal/domain ./internal/manifest ./internal/health ./internal/orchestrator ./internal/storage ./internal/api
go test ./internal/driver/process ./internal/driver/compose ./internal/platform/windows/supervisor
go test ./...
go vet ./...
npm run test:web
npm run type-check
npm run build
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

另执行：

- OpenAPI/Schema/error-code/capability 一致性测试。
- 空库、Version 15/16 到新 migration、重复启动、checksum 和容量/保留测试。
- Windows Supervisor Job 指标和新旧协议真实 fixture。
- Docker Compose stats/identity/recovery fixture。
- Playwright 桌面与移动主流程。
- Secret/路径/argv/环境泄露扫描和测试资源清理。
- `Get-FileHash AGENTS.md,CLAUDE.md`。

不得把以上命令提前写成已通过；evidence 只记录实际结果、版本、时间、环境和跳过原因。

### 五系统真实 Gate

按“只读快照 -> liveness -> metrics -> ChangePlan -> Verified Restart”逐级授权，任何一级未授权不得自动进入下一级：

- BTC：Backend/Web 全树资源、HTTP liveness、dirty plan、Verified Restart。
- AIWS：五服务/十三端口、Compose 容器指标、Flyway/lock/image snapshot、OIDC 真实浏览器验证。
- PMS：三服务资源/liveness、外部依赖边界、Secret 元数据、Verified Restart。
- AgentHub：Compose/API/Web 先验收；三个 Worker 健康端点未完成前记录 blocker，不执行完整 Verified Restart Gate。
- GNMarket：Web/Frontend 先验收；Job 健康端点未完成前记录 blocker，外部 MySQL 不操作。

真实 Gate 不打印 Secret、连接串、完整环境或绝对工具路径；结束后确认无新增遗留进程、容器、端口和临时数据目录。

### 文档同步

- ADR、`docs/overall-design.md`、`docs/detailed-design.md`、`docs/phased-development-plan.md`。
- OpenAPI、Manifest Schema/examples、`docs/error-codes.md`、`docs/storage-schema.md`。
- `README.md`、`docs/development.md`、CLI/用户操作和升级边界说明。
- Phase 3C progress 与专项 evidence；五系统 blocker 和未执行项必须保留。

### Final Gate

- Prompt 第 16 节完成定义全部满足。
- 三个 capability 只在各自生产实现和 Gate 完成后发布，未完成能力稳定关闭。
- 基础 start/stop/restart/log/liveness/Incident/恢复和五系统 Phase 2.1 回归通过。
- 资源采样关闭、失败或存储压力下，运行控制仍然可用。
- ChangePlan 无副作用，Verified Restart 不声称业务升级或自动回滚。

### 执行状态（2026-09-01）

- 最新候选构建、安装升级、控制库 Version 19 迁移、ChangePlan CLI/API/Web 和 BTC 普通恢复均已通过；最终 `scripts/check.ps1` 通过版本校验、28 个 Web 测试、严格类型检查、生产构建、全仓 Go test/vet，升级前控制库备份与候选/截图 SHA-256 已记录。
- 最终真实状态为 BTC `running` 且 2/2 Ready，PMS/GNMarket `stopped`，AIWS/AgentHub 因 Docker Compose preflight 不可用保留 `failed`；没有启动 Docker Desktop、执行 Flyway repair、修改业务数据库、Git 状态、Manifest、Secret 或 volume。
- RO-08 尚未通过 Final Gate：五系统 Verified Restart、其成功/失败/取消/恢复浏览器流程和 capability 发布均被真实外部运行条件阻塞。`phase3.verified-restart` 保持未发布是正确交付状态，不属于半实现路径。
- 机器证据见 `docs/evidence/ro08-real-gates.json`。

## 13. 相对工作量与依赖

当前未授权实名负责人和日历截止日期，责任统一记为“实现 Agent”，不编造承诺日期。

| 工作包 | 参考工作量 | 可并行项 |
| --- | ---: | --- |
| RO-00 | 1-2 人日 | 五系统健康端点盘点 |
| RO-01 | 1.5-2.5 人日 | Web 信息架构草图 |
| RO-02 | 2-4 人日 | RO-03 业务改造 |
| RO-03 | 2-5 人日 | RO-02/RO-04；受业务仓库 Gate 影响 |
| RO-04 | 4-7 人日 | RO-02/RO-03 |
| RO-05 | 2-4 人日 | RO-04 后半段 |
| RO-06 | 2-4 人日 | RO-07 API/组件准备 |
| RO-07 | 3-5 人日 | 后端契约稳定后并行 |
| RO-08 | 3-6 人日 | 文档可提前草拟 |

参考总量约 20.5-39.5 个 AI 有效开发日；业务健康端点、Docker/Windows 真实环境和五系统 Gate 等待时间单独报告。变更与验证主关键路径为 `RO-00 -> RO-01 -> (RO-02 || RO-03) -> RO-05 -> RO-06 -> RO-07 -> RO-08`；RO-04 在 RO-01 后并行，是 Milestone A 和资源监测 capability 的必要组成，但不是 ChangePlan 正确性的硬依赖。RO-06 必须等待 RO-03 真实健康覆盖 Gate。

## 14. 主要风险与止损

- Supervisor 协议改动导致旧进程无法接管：必须向后兼容并做跨版本 Gate；失败则资源指标保持 unavailable，不阻塞监管。
- Docker stats 开销或输出异常影响控制面：固定身份、低频、有界 worker/timeout/output；压力不达标则不发布 capability。
- 快照误收 Secret/路径/命令：采集前白名单、持久前脱敏/大小校验、DTO 最小化和泄露测试四层防护。
- Git dirty 被误判为可安全升级：dirty 至少 medium/high 或 blocked，由 ADR 固定；绝不自动清理。
- process-only 健康造成虚假验证成功：健康覆盖模型必须区分 process-alive 与 business-healthy，缺端点系统阻断完整 Gate。
- Verified Restart 失败被理解为可回滚：UI/API/docs 持续明确“无源码/数据自动回滚”，失败保留现场。
- 指标无限增长或 SQLite busy：容量 Gate、小时聚合、小批量清理和 hard limit 未通过则 capability 不发布。
- 计划与执行间来源变化：执行前重新采集并精确 digest 校验，stale 时停止前拒绝。

## 15. 建议执行顺序

1. 完成 RO-00，先关闭 ADR、容量、协议、风险和验证契约。
2. 完成 RO-01，建立机器契约、migration 和 capability Gate。
3. 并行推进 RO-02 Revision Snapshot、RO-03 真实 liveness 和 RO-04 Resource Sampler。
4. RO-02/03 稳定后完成 RO-05 ChangePlan；资源指标可作为计划/Incident 补充，但不得成为计划正确性的硬依赖。
5. RO-03/05 Gate 通过后完成 RO-06 Verified Restart。
6. 契约稳定后逐步完成 RO-07 API/CLI/Web，不等到后端全部结束才一次性接 UI。
7. RO-08 执行自动化、五系统分级真实 Gate、文档和 evidence 收口。

任何实现若需要 Git 切换、依赖安装、数据库/volume 备份迁移、外部 URL/写请求验证、任意命令、远程 Agent 或自动回滚，必须停止当前专项并创建新的 Prompt/Plan/ADR，不得借“验证式重启”隐式扩权。
