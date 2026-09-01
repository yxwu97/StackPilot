# StackPilot 系统可观测性、变更规划与验证式重启 Prompt

> 状态：待执行
> 日期：2026-08-31
> 适用仓库：`E:\StackPilot`
> 工作包性质：Phase 3C 增强可观测性、复用已完成的 Phase 2E liveness 引擎开展五系统真实启用与验证，以及 Phase 3A 运维入口增强；本专项不重新评审 Phase 2E Gate
> 计划文件：`plan/plan-20260831-01-system-observability-and-change-planning.md`

## 1. 任务

在不进入通用业务升级、数据库备份/迁移或任意命令执行范围的前提下，为 StackPilot 实现以下五项近期能力：

1. 运行监测增强：进程树或 Compose 服务组的 CPU、内存、运行时长、进程/容器数量，以及已有重启次数、健康耗时、启动耗时和日志错误率的有界聚合。
2. 持续健康检查：让已实现的 liveness、抖动阈值、Incident 和有限自动重启在真实接入系统中按清单显式启用，并关闭 process-only 健康判断带来的虚假健康。
3. 版本与环境快照：形成不含 Secret 的不可变系统修订快照，记录当前运行实例和当前工作区的清单、源码、依赖、Runner、Compose 与 Secret 元数据身份。
4. 变更计划与 dry-run：只读比较“当前运行修订”和“若现在重启将使用的候选修订”，输出结构化差异、影响范围、阻断项、风险等级和验证要求，不修改源码或运行状态。
5. 验证式重启：基于终态成功且内容未漂移的变更计划执行持久化重启 Operation，在原有 readiness 后增加 liveness 稳定观察和受限验证，失败时保留真实现场并形成证据。

该任务建立未来业务升级所需的观测、身份、计划和验证底座，但不把这些基础能力包装成已经具备制品切换、数据回滚或一键升级。

## 2. 开始前必须遵守

1. 完整读取并遵守 `AGENTS.md`、`CLAUDE.md`、`code_rule.md`、`docs/detailed-design.md`、`docs/phased-development-plan.md` 和相关 ADR；开始与交付前校验两份 Agent 指令 SHA-256 一致。
2. 修改前定向核对 Manifest Go 类型/Schema/validator、ResolvedSystemSpec、Runner/Process/Compose Driver、Supervisor 协议、health/liveness、Incident、Operation、SQLite migration/repository、OpenAPI、Web API/Pinia/App 和现有真实 Gate。
3. 任何新增 Manifest 字段、Operation 类型、Supervisor 协议、SQLite 表或系统状态语义都必须先更新详细设计并按要求形成 ADR；契约、实现、测试和文档在同一变更中同步。
4. 保留用户已有修改，不初始化 Git，不自动 commit/tag/push，不修改或清理五个业务工作区的源码、依赖、数据库、容器卷、镜像或缓存，除非对应真实 Gate 获得单独明确授权。
5. 新能力必须使用显式 capability。完成全部生产实现和 Gate 前不得在 `/version` 发布对应 capability；未启用时返回 `FEATURE_NOT_ENABLED`，不得保留半实现入口。
6. 所有后台采样、检查、队列、查询窗口、响应和持久化均有界、可取消、可恢复；基础启停、日志落盘和 reconciliation 不得依赖可观测性子系统成功。
7. 不得读取或持久化 `.env`、完整子进程环境、Secret 值、Authorization、连接串、完整命令或未脱敏日志。环境快照只保存允许的名称、来源类别、digest 和 Secret 元数据版本。

## 3. 已确认事实

- Phase 2.1 Gate 已通过，Process/Compose Driver、readiness、liveness、有限自动重启、Incident、规则诊断和受信 Runner 注册表已有生产基础。
- 当前 Operation 类型只有 `start`、`stop`、`restart`、`service-restart`、`port-plan`、`refresh` 和 `analyze`；变更计划和验证式重启尚无领域契约。
- `ResolvedSystemSpec` 已持久化 Manifest digest、端口计划、DAG、Runner 解析结果、可执行文件 digest、readiness/liveness/restart 和安全的启动规格；每个服务实例已记录实际使用的 Secret 元数据版本。
- `health_results`、健康小时聚合、Incident 和重启尝试已存在；Phase 3C 资源采样、资源聚合与趋势页面尚未实现。
- Windows Supervisor 持有受管进程 Job Object。若要准确获得完整进程树资源而不是根 PID 近似值，需要扩展兼容的监管观测协议并通过真实 Windows 进程树测试。
- Compose 预检已能取得 Docker/Compose 版本和受管服务集合，但持续容器资源采样尚未建立；采样必须使用严格身份和固定 argv，不能按名称模糊查询。
- 当前五份正式清单位于 BTC、AIWS、PMS、AgentHub 和 GNMarket 工作区，均有 readiness，但没有显式 liveness/restart 配置。
- BTC、AIWS、PMS 和 GNMarket 当前 Git 工作区存在用户修改；AgentHub 当前缺少可直接依赖的统一 Git 发布身份。自动 Git 切换、覆盖或清理不在本任务范围。
- 五个系统以 `spring-boot:run`、`npm dev`、`python-venv`、`go run` 和 Compose 为主，不能把 POM/package 版本单独当作不可变业务制品版本。
- AIWS/AgentHub 的 PostgreSQL 位于受管 Compose；BTC、PMS、GNMarket 仍有外部数据库或中间件依赖。本任务只观察和标识这些依赖，不备份、迁移、升级或停止它们。

## 4. 目标模型与术语

### 4.1 Runtime Metrics

`RuntimeMetricSample` 是一个运行中服务实例在 UTC 时刻的有界资源事实。Process 服务使用 Supervisor/Job Object 的完整受管树口径；Compose 服务使用严格项目/容器身份口径。无法取得同口径事实时标记 `unavailable` 和稳定原因，不以根 PID、进程名或容器名猜测。

### 4.2 System Revision Snapshot

`SystemRevisionSnapshot` 是一个不可变、可摘要、不含敏感值的系统修订身份，至少区分：

- `running`：当前实例启动时实际使用的 resolved spec 和相关组件身份。
- `workspace`：当前工作区如果现在重新解析/预检将形成的候选身份。

快照不是 Git commit、Manifest snapshot 或 ResolvedSystemSpec 的替代品，而是引用这些事实并补充源码状态、依赖文件 digest、Runner/Compose 版本与 Secret 元数据版本的上层安全摘要。

### 4.3 Change Plan

`ChangePlan` 是一次持久化、只读、可审计的比较结果。它引用确定的 from/to revision digest，列出 Manifest、服务、DAG、Runner、端口策略、依赖文件、Compose 镜像/构建、Secret 版本和健康/验证契约变化，并给出 `info|low|medium|high|blocked` 风险。风险由确定性规则产生，不由模型自由判断。

### 4.4 Verified Restart

验证式重启是“按候选工作区规格重新启动并证明进入稳定状态”，不是升级或自动回滚。执行前必须重新采集候选快照并与 ChangePlan 的 to digest 精确匹配；漂移时返回稳定错误并拒绝停止现有系统。

## 5. 固定产品与安全边界

- 不提供 raw command、脚本、Git ref、环境变量值、Docker flags、SQL、备份命令或自定义 HTTP header 输入。
- 不执行 `git pull/reset/checkout/switch/merge`，不创建 worktree，不安装依赖，不拉取或替换业务制品。
- 不读取通用 `.env`，不递归 hash 整个工作区；只采集 ADR/Schema 白名单中的版本文件和已登记来源。
- 初版候选仅表示同一注册工作区的当前安全快照，不接受调用方传入任意候选路径。
- 变更计划绝不停止服务、构建 Compose、执行 oneshot、解析 Secret 值或占用端口租约；需要 Runner/Docker/Git 探测时使用受信固定工具、固定参数、canonical 已登记工作目录、有界环境、超时和 stdout/stderr 上限，不经 shell，不接受模板或调用方参数。
- 验证式重启不承诺恢复旧源码或旧数据库。停止完成后若新启动失败，沿用现有失败现场和显式人工处置语义，不自动尝试无法证明安全的回滚。
- 资源采样失败不得导致服务状态变化或自动重启；liveness/verification 事实与资源指标严格分离。
- 初版不采集业务目录、数据库、Docker volume 或镜像磁盘占用，不把 SQLite 变成无限时序库。
- AI/模型不参与本任务的风险判定或执行决策。

## 6. 运行监测增强要求

### 6.1 指标口径

初版至少提供：

- Process：Job Object 级累计 CPU、采样区间 CPU 使用率、工作集/私有内存的已裁决口径、活动进程数和运行时长。
- Compose：严格身份下各受管容器和 Manifest Compose 服务组的 CPU、内存、容器数、状态和运行时长；UI 以 StackPilot service 为主层级，可展开容器明细。
- 派生指标：OperationStep 启动耗时、health_results 检查耗时/失败率、已持久化重启次数、结构化日志级别形成的有界错误速率。

单位、采样间隔、缺失值、进程树聚合、CPU 核数归一化、容器组求和和重启后计数边界必须在 ADR 中固定。不得把累计 CPU 时间直接标成 CPU 百分比。

### 6.2 所有权与背压

- Resource Sampler 由 Server 组装层单一持有，只有已核验的活动 daemon 实例进入调度；oneshot 完成后不持续采样。
- 每个实例同一时间最多一个采样任务；全局并发、单次 timeout、输出大小和队列长度有固定上限。
- 控制面关闭时统一取消并等待 goroutine；reconciliation 后按实际活动实例恢复，不为已停止或身份未知实例采样。
- SQLite busy、Supervisor 旧协议、Docker 不可用或存储压力只产生安全状态/结构化日志，不阻塞生命周期主链。

### 6.3 保留与聚合

沿用 health_results 的“短周期明细、长周期小时聚合、小批量清理”模式。RO-00 必须从已登记的五系统真实 Manifest 快照或经单独授权的真实 Manifest 只读获取服务规模，不在生产代码硬编码外部路径；以现有 health 默认策略（24 小时明细、每实例最多 1000 条近期记录、每批清理 500 条、每小时执行 retention）作为对照基线，并对资源采样单独压测。具体采样间隔、明细保留、聚合保留和容量上限由容量证据冻结，不能凭感觉写入生产。

## 7. 持续健康检查要求

- 为五个实际 Manifest 逐服务确认 liveness，不允许批量复制 readiness 后不验证运行期语义。
- HTTP liveness 继续受 loopback、响应上限、timeout、重定向和 body 断言限制；不得成为任意网络探测器。
- `process` liveness 只证明身份仍在，不证明 Worker/Job 正常处理任务。AgentHub Worker/Analysis Runner/Report Worker 和 GNMarket Job 在缺少业务健康端点时必须显示“仅进程存活”，并阻断该系统的完整验证式重启 Gate。
- readiness/liveness 失败阈值、成功恢复阈值和 interval 按服务实际启动/运行特征确定；不得用固定 sleep 或无限重试缓解抖动。
- 自动重启保持 `never|on-failure|always`、指数退避、最大次数和稳定窗口语义。真实系统默认先启用 liveness 观察，不因本任务自动打开 restart policy；每项自动重启必须单独评审。
- liveness 失败继续通过同事务状态/事件、health_results、Incident 和规则诊断形成证据；资源指标只作为 IncidentContext 的有界补充。

## 8. 版本与环境快照要求

### 8.1 白名单来源

快照可包含：

- Workspace/System/Manifest/ResolvedSpec digest 和当前实例 ID。
- 受控 Git 探测得到的 revision、branch 可选摘要和 dirty 布尔值；Git 不存在或不可信时记录 unavailable，不失败回退为伪版本。
- 已登记相对路径下的 `pom.xml`、`package.json`、lockfile、`requirements*.txt|lock`、`pyproject.toml`、`go.mod`、`go.sum` 和 Compose 文件的 SHA-256；内容解析只使用结构化解析器，无法安全解析时只保留 digest/类型。
- Runner kind、版本、resolution kind 和可执行文件 digest；DTO 不返回可执行文件绝对路径。
- Docker client/server、Compose 版本和严格运行身份下的 image digest；只有 tag 没有 digest 时明确区分。
- Secret system/name/provider/version 和环境变量名称；不保存 Secret 值、普通环境值或连接串。

白名单文件数、单文件大小、总字节数、canonical path、普通文件和 symlink/junction 边界必须有上限。文件不存在是结构化事实，不得为了补全而执行安装或生成命令。

### 8.2 一致性

- 快照规范 JSON 排序稳定，digest 由规范字节计算；相同输入幂等复用，不生成无意义重复行。
- running snapshot 必须忠实引用启动时事实，不能用刷新后的 Manifest 或当前 Runner 覆盖历史。
- workspace snapshot 在计划创建和验证式重启执行前分别采集；任一白名单来源改变都会形成新 digest。
- DTO 默认只返回安全摘要和相对标识。内部绝对路径、完整 argv、环境和原始文件内容不得暴露。

## 9. 变更计划与 dry-run 要求

- ChangePlan 通过异步持久化 Operation 创建，因为 Git/Runner/Docker/文件探测可能耗时；HTTP 立即返回 `202` 和 Operation 引用。
- 推荐新增独立 Operation 类型 `change-plan`，最终名称在 OpenAPI/领域 Design Gate 固定，不能复用 `analyze` 或 `port-plan` 模糊语义。
- 计划只比较事实，不执行真实 start preflight 的副作用部分：不申请 PortLease、不拉起 Docker Desktop、不 build、不解析 Secret 值、不运行 oneshot。
- 差异必须结构化，至少覆盖 added/removed/changed service、driver/mode、DAG、Runner/toolchain、Manifest/arguments digest、端口策略、health/restart、依赖文件、Compose image/build policy 和 Secret 版本变化。
- 风险规则登记为确定性、可测试的注册表。示例：删除必需服务、driver/mode/DAG 变化、dirty workspace、Runner executable digest 变化、数据库 migration 文件变化、Compose image digest 变化、缺少 liveness/verification 均不得被归为 low。
- 数据库 migration 只能识别“白名单文件集合/digest 发生变化”，不能推断可逆性、执行顺序或数据库已升级状态。
- 计划状态必须区分可执行、需确认和 blocked。阻断项存在时，验证式重启入口不可用；Web 不得只靠前端推断，服务端再次校验。
- from/to digest、规则版本、Manifest digest、生成时间、Operation 和安全摘要持久化；重复幂等请求返回同一结果或等价稳定结果。

## 10. 验证式重启要求

- 推荐新增 `verified-restart` Operation，而不是给现有 restart 增加含糊布尔参数；现有 `/restart` 语义保持兼容。
- 请求只接收 workspace/system 和 ChangePlan ID；不得接收命令、路径、环境、Git ref、端口覆盖或跳过检查开关。
- 执行顺序固定为：加载计划 -> 校验计划终态/归属 -> 重新采集候选快照 -> digest/风险/capability 校验 -> 现有逆序 stop -> 现有 fresh start -> readiness -> liveness/验证稳定窗口 -> 形成最终证据。
- 在停止前发现计划 stale、Manifest 无效、活动 Operation、身份不明或验证契约不完整时必须拒绝，现有系统保持运行。
- stop/start 继续使用各自边界时的 resolved spec；不得在停止旧实例时错误使用候选 stop policy。
- 验证至少要求所有 required daemon Ready，并在 ADR 固定的稳定窗口内无 required service 进入 Degraded/Failed；oneshot 仍以 Completed 满足依赖。
- 如新增声明式 post-start verification，首版只允许与 serviceId 绑定的 loopback HTTP GET、状态/body 断言和固定 timeout，不允许 header、Secret、写请求、脚本、浏览器动作或外部 URL。
- 验证失败使 Operation 失败并生成 Incident/证据，但不得直接覆写不合法服务终态。系统聚合状态仍由领域状态事实推导。
- 取消只在现有安全边界生效：停止开始前可无副作用取消；进入 stop/start 后沿用当前 Operation 取消和清理语义，不承诺恢复旧源码。

## 11. API、CLI 与 Web 要求

- 所有新 API 使用 `/api/v1`，以 OpenAPI 为唯一契约；错误使用统一 envelope 和稳定 error code。
- API 至少支持资源指标窗口查询、系统 revision 摘要、ChangePlan 创建/读取，以及 verified restart 创建。精确路径、分页/cursor 和 DTO 在 RO-00/RO-01 冻结。
- 资源趋势查询必须限制时间窗、粒度、点数和服务范围；服务端聚合/降采样，前端不下载无限明细。
- CLI 增加只调用 REST 的 `observe`/`plan`/`restart --verified` 或 Design Gate 选定的等价命令，提供稳定 JSON 输出；CLI 不直接读取业务工作区或调用 Git/Docker。
- Web 在现有系统详情/服务详情/操作中心内增加紧凑的“监测”和“变更计划”视图，不新建营销式 Dashboard，不使用卡片套卡片。
- 趋势图显示明确单位、时间范围、缺失区间和数据来源；状态不只靠颜色。长 revision/digest 使用截断展示和可访问完整文本，不泄露路径。
- Verified Restart 按钮必须由服务端 capability、最新计划状态、活动 Operation 和系统验证覆盖共同决定；提交前展示影响范围、阻断项和“本阶段无自动源码/数据回滚”说明。
- REST 快照是初始事实，SSE 只推送 Operation/状态增量。高频资源点不通过领域事件逐点广播；采用有界轮询或单独设计的低频摘要机制。

## 12. SQLite、保留与恢复要求

- 实现前核对当前 migration 头；本 Prompt 创建时为 `000016_workspace_imports.sql`。所有新增 schema 使用后续单调 migration，禁止修改 000001-000016。
- 预计需要 revision snapshot、change plan、resource sample/hourly aggregate，以及必要的 verification result 存储；最终表拆分由查询、不变量和保留需求决定，不把所有事实塞入无约束 JSON。
- JSON 列必须 `json_valid`，记录 schema version、大小上限和 digest；关键归属、唯一性、终态和幂等由数据库约束保证。
- 指标清理小批量、可取消、路径无关；聚合成功提交后才能删除对应明细。清理失败不影响运行控制。
- 控制面重启后恢复 sampler 和 liveness 所有权；终止未完成的 ChangePlan/Verified Restart Operation 按既有恢复设计收口，不重复停止或启动。
- migration 测试覆盖空库、Version 15/16 历史升级、重复启动、checksum 异常、保留数据和并发约束。

## 13. 五个真实系统的最低验收

### 13.1 BTC

- Backend/Web 增加经过真实运行验证的 HTTP liveness；资源采样覆盖 Maven/Java 与 npm/Node 完整 Job 树。
- ChangePlan 能识别 Git dirty、POM/package-lock/Manifest/Runner 变化，但不触碰外部业务数据库。
- Verified Restart 通过 Backend -> Web readiness 和稳定窗口；不宣称业务数据库回滚。

### 13.2 AIWS

- Infrastructure 使用 Compose health/liveness，Server、Agent Runtime、Web 使用 HTTP liveness，Keycloak Configure 保持 oneshot/completed。
- 指标同时展示 Compose 服务组与受管容器明细；快照记录 Flyway 文件集合、Python/npm locks 和 image digests。
- Verified Restart 验证五个 StackPilot services、十三端口和 OIDC 基础可用事实；完整登录浏览器流程仍作为真实 Gate，不塞入控制面任意验证动作。

### 13.3 PMS

- Backend/RAG/Web 使用真实 HTTP liveness；MySQL、Redis、Qdrant 只显示为外部依赖事实，不采集凭据、不停止、不升级。
- 快照覆盖 Maven、Python venv requirements、npm lock 和 Secret 元数据版本。
- Verified Restart 必须证明三服务稳定；外部依赖不可用时失败保留现场。

### 13.4 AgentHub

- Infrastructure/API/Web 可先建立完整 liveness；Worker、Analysis Runner、Report Worker 在获得业务健康端点前只能报告 process-alive，系统完整 Verified Restart Gate 保持 blocked。
- 快照覆盖 Go module/sum、npm lock、Compose image digests 和递增 migration/checksums；缺少 Git 身份时使用明确 unavailable，不伪造 revision。

### 13.5 GNMarket

- Web/Frontend 使用 HTTP liveness；Job 在业务健康端点完成前只报告 process-alive，并阻断完整 Verified Restart Gate。
- 快照识别 Maven/npm 版本、Flyway 文件变化和外部 MySQL 依赖，但不连接、备份或迁移数据库。

真实业务项目修改必须先读取各自 `AGENTS.md`/规则，并作为独立变更和 Gate 执行；StackPilot 生产代码不得硬编码上述系统 ID、路径、服务名或端口。

## 14. 测试、文档与证据要求

- 单元测试覆盖指标计算/缺失、规范快照/digest、白名单/路径边界、差异分类、风险规则、计划 stale、稳定窗口和取消。
- repository/migration 使用真实 SQLite，覆盖并发、幂等、聚合后清理、历史升级和 checksum。
- Windows fixture 覆盖多级子进程、CPU/内存负载、Supervisor 新旧协议、控制面重启、身份不匹配和完整 Job 资源口径。
- Compose fixture 覆盖多容器服务组、容器重启/消失、严格身份、Docker 不可用、stats timeout/大输出和控制面恢复。
- API/OpenAPI/CLI 覆盖鉴权、Origin/CSRF、幂等、错误 envelope、查询上限和 DTO 脱敏。
- Web 组件和真实浏览器覆盖桌面/移动趋势、缺失数据、blocked plan、stale plan、Verified Restart 进度/失败及无重叠。
- 五系统 Gate 分级记录；不能用 fixture 冒充真实系统，也不能因某个业务健康端点未完成而跳过 blocker。
- 同步 `docs/overall-design.md`、`docs/detailed-design.md`、`docs/phased-development-plan.md`、`docs/storage-schema.md`、OpenAPI、Schema、错误码、ADR、README/development 和进度/evidence。

## 15. 明确不做

- 业务系统一键升级、Git 自动更新、候选 worktree、制品下载/切换。
- 数据库/volume 备份、migration 执行、restore 或自动回滚。
- 远程 Agent、多用户/RBAC、Prometheus/OpenTelemetry 导出和日志全文索引。
- 磁盘/目录递归扫描、网络/外部 URL 探测和自定义脚本验证。
- 让模型生成风险、命令或恢复动作。
- 因资源采样失败改变服务健康，或因验证失败直接强制改写服务状态。

## 16. 完成定义

只有同时满足以下条件才能声明完成：

1. 五项能力均有 Accepted 设计、机器契约、capability、错误码和测试，不存在只做 UI 的空壳路径。
2. Process 指标基于完整受管 Job 树，Compose 指标基于严格身份；不可用时明确缺失，不伪造近似值。
3. 指标明细/聚合/保留有容量证据，不影响启停、日志和健康主链。
4. 五系统 liveness 覆盖矩阵真实验证，process-only 服务被明确标识并正确阻断完整验证式重启。
5. revision snapshot 可重复、不可变、无 Secret，running/workspace 语义不混淆。
6. ChangePlan 完全只读、差异和风险确定性、stale/blocked 服务端强校验。
7. Verified Restart 使用持久 Operation 和既有 stop/start 状态机，停止前校验无副作用，失败不虚构回滚。
8. OpenAPI、Schema、migration、repository、API/CLI/Web、SSE、错误码、文档和 evidence 一致。
9. Go/Web/SQLite/Windows/Compose/浏览器回归及获授权的真实系统 Gate 通过，无遗留进程、容器、端口、测试文件或敏感数据。
10. `AGENTS.md` 与 `CLAUDE.md` 最终 SHA-256 一致，用户已有修改未被覆盖。
