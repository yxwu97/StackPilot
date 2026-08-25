# StackPilot 工作区导入、详情与编辑功能实现 Prompt

> 状态：待执行（已吸收 2026-08-19 外部审核意见）
> 日期：2026-08-19
> 适用仓库：`E:\StackPilot`
> 计划文件：`plan/plan-20260819-01-workspace-import-and-management.md`

## 1. 任务

在 StackPilot 中实现完整的工作区首次接入和后续管理体验：当用户添加一个尚未包含 `.stackpilot/system.yaml` 的工作区时，允许用户选择工作区内的 BAT 启动文件，由系统以受控、可解释的方式分析启动脚本及其直接引用配置，形成结构化草案，经用户确认后生成并校验 `.stackpilot/system.yaml`，最后完成注册。用户不应被要求手工编写 YAML。

同时为“工作区”提供独立的详情和编辑能力。详情用于查看注册信息、导入来源、清单状态、服务、端口、依赖、readiness、历史摘要及相关操作；编辑采用结构化表单和重新分析流程，不提供任意命令输入或原始 YAML 编辑器。

## 2. 开始前必须遵守

1. 完整读取并遵守 `AGENTS.md`、`CLAUDE.md` 和 `code_rule.md`。
2. 按事实优先级读取 `docs/detailed-design.md`、`docs/phased-development-plan.md`、`docs/overall-design.md`、OpenAPI、JSON Schema、migration 和错误码注册表。
3. 本需求改变现有“注册只读取固定清单”的交互，新增业务工作区文件写入、BAT 来源分析、Runner 和预注册持久化操作。编码前必须先更新设计并形成 ADR，不能绕过阶段和安全边界直接实现。
4. 修改 `AGENTS.md` 或 `CLAUDE.md` 时必须同步另一份，并在交付前校验两者 SHA-256 完全一致。
5. 保留用户已有修改；不得创建 Git 提交、推送、变基或改写历史。

## 3. 当前事实与问题

- `POST /api/v1/workspaces` 当前要求工作区已经存在 `.stackpilot/system.yaml`，否则返回 `WORKSPACE_MANIFEST_UNAVAILABLE`。
- Web 当前只有注册路径输入、列表、刷新和解除注册，没有工作区详情、编辑或导入向导。
- `.stackpilot/system.yaml` 必须继续作为运行期系统定义的唯一事实来源；SQLite 只保存注册、导入元数据、不可变快照和运行状态。
- BAT/PowerShell 启动器不能成为正式运行入口。导入完成后，StackPilot 必须通过受支持的 Runner/Driver 直接监管服务，不能在启动 Operation 中再次调用用户 BAT。
- BAT 是动态语言，无法对任意脚本做完整可靠的静态求值。系统必须明确支持范围、证据、置信度和未解析项，禁止静默猜测。
- 当前 `operations.workspace_id/system_id` 均为非空外键，Operation type 是数据库闭合枚举，活动锁和幂等索引也以 workspace ID 为作用域；预注册导入不能只增加一个可选字段，必须设计向前表重建及跨领域兼容。
- 当前 OpenAPI/实现是 `POST /workspaces/{workspaceId}/refresh -> 200 Workspace`，详细设计则是 `POST /systems/{id}/refresh -> 202 Operation`，同时与“变更必须是持久化 Operation”存在 drift；WI-00 必须同时裁决资源路径所有权和同步/异步返回，选择修正、版本化兼容或形成有期限的正式豁免。
- 当前 Web 由单体 `App.vue` 自管理视图，与设计中的 router/views 结构存在 drift。本专项只拆出工作区功能所需组件，不顺带重构全站，但不得继续把多步向导、详情和编辑全部堆入 `App.vue`。

## 4. 核心设计原则

### 4.1 导入而非执行

- BAT 只作为迁移输入，不作为运行命令。
- 首版分析过程只读且不得执行 BAT、PowerShell、Node、npm、Maven、Java、Cocos Creator 或脚本内的任何命令。
- 不读取任意宿主环境变量，不展开命令替换，不访问工作区外文件，不跟随越界符号链接或 junction。
- 仅对明确支持的 BAT 语法和已知工具调用生成确定性结果。未支持语句产生带位置和原因的诊断。

### 4.2 结构化确认

- 用户确认的是服务、Runner、工作目录、参数数组、端口、环境模板、依赖、readiness 和策略等结构化字段。
- UI/API 不提供 `command` 字符串、任意 executable、任意环境变量注入或原始 YAML 编辑入口。
- 所有生成内容先构造成现有强类型 Manifest，再通过 `yaml.v3` 序列化，并完整执行 JSON Schema、语义、安全和 capability 校验。

### 4.3 可解释与保守

- 每个发现项都携带来源文件、行号或结构化字段路径、探测器名称和置信度。
- 置信度至少区分 `confirmed`、`inferred`、`unresolved`。只有 `confirmed` 字段可默认接受；`inferred` 必须明显标记并由用户确认；`unresolved` 不能生成可执行定义。
- 不能确认服务边界、端口传播、依赖或安全监听范围时，应阻止应用草案并给出可操作原因。
- 不通过宽松兜底把未知命令转换为 shell Runner。

### 4.4 文件与并发安全

- 工作区根、BAT 路径和所有引用路径都必须 absolute/join/canonicalize，并验证真实路径仍位于工作区内。
- 只接受工作区内的普通文件；限制单文件大小、递归深度、文件数量和总读取字节数；引用图必须检测循环。
- 首版接受 ASCII 和 UTF-8（可带 BOM）。其他编码返回稳定错误，不以本机代码页猜测。
- 生成清单采用同目录临时文件、flush/close 和原子替换；检查 `.stackpilot` 目录及目标文件没有被重解析点替换。
- 编辑时使用当前 manifest digest 作为乐观并发条件。发现外部改动时返回冲突，不覆盖用户文件。
- 缺失清单的首次生成不得覆盖任何已有文件；已有清单必须经过独立编辑流程并明确确认。
- Manifest digest 必须沿用现有快照语义：对生成的 YAML 重新执行 Loader/Schema/Validator，使用重载后规范化 JSON 计算的快照 digest；禁止直接对 YAML 字节求哈希形成第二套 digest。

## 5. 用户流程

### 5.1 添加工作区

1. 用户输入或选择工作区根目录。
2. 服务端执行只读探测：路径有效性、固定清单状态、候选启动文件。
3. 如果固定清单存在，显示摘要并沿用现有校验注册流程。
4. 如果固定清单不存在，探测 DTO 返回正常状态 `initialization_required`，不使用错误 envelope；Web 进入“初始化工作区”。
5. 系统列出工作区内有界搜索得到的 `.bat` 候选，用户也可填写工作区内相对路径。
6. 分析 BAT 及受支持的直接引用配置，返回结构化草案、证据、警告和阻断项。
7. 用户在向导中检查“基本信息 -> 服务 -> 端口 -> 依赖与就绪 -> 预览与确认”。
8. 应用草案是持久化、可审计的变更操作；成功后生成清单并注册工作区。
9. 失败时保留可恢复状态，不留下半写文件、错误数据库关联或未清理临时文件。

### 5.2 工作区详情

新增 `GET /api/v1/workspaces/{workspaceId}` 对应的详情能力和 Web 详情视图，至少展示：

- 工作区 ID、根路径、规范路径摘要、系统 ID/名称、创建及更新时间。
- 清单状态、当前 digest、API version、最后校验结果和安全错误码。
- 来源类型：已有清单、BAT 导入或后续结构化编辑；来源 BAT 使用工作区相对路径。
- 服务列表：ID、显示名、driver、mode、runner、工作目录摘要、required、定义 digest。
- 端口：逻辑名、首选值、fallback、冲突策略、暴露范围及引用服务。
- 服务依赖和 readiness/liveness 摘要。
- 当前活动 Operation、运行状态以及最近的导入/编辑/刷新记录摘要。
- 规范化 YAML 只读预览；不得包含 Secret 值、完整未脱敏环境或内部数据目录。

详情 DTO 只返回页面实际需要的字段。不要复用包含内部路径或敏感信息的存储模型。

### 5.3 编辑工作区

编辑入口至少支持：

- 重新关联工作区根路径。仅当系统完全停止且没有活动 Operation 时允许；新路径必须存在、可读、规范路径唯一，并包含同一 system ID 的有效清单或经确认生成的新清单。
- 修改或重新选择来源 BAT，并重新分析形成草案。
- 通过结构化表单编辑 system name/description、服务显示名、受支持 Runner 参数、工作目录、端口策略、环境模板、依赖、readiness 和安全停止策略。
- 查看变更前后差异以及生成后的只读 YAML，再确认应用。
- system ID 在已注册工作区中不可原地修改；需要新 system ID 时必须作为新工作区注册。
- 运行中可以保存未应用草案，但不得应用会改变清单或根路径的编辑。

编辑应用必须校验基础 digest，写入清单后刷新不可变快照，并记录审计与领域事件。文件更新和 SQLite 不能形成单一事务，因此 ADR 必须定义分步提交、失败恢复、重试和崩溃窗口；不得用“先写后忽略数据库失败”掩盖不一致。

### 5.4 CLI 兼容语义

- `stackpilot workspace add <path>` 对已有有效清单继续保持非交互注册行为。
- 缺少清单时，CLI 必须消费正常探测状态并输出可机器解析的 `initialization_required`，不能只透传 `WORKSPACE_MANIFEST_UNAVAILABLE`。
- 首版 CLI 不复制 Web 的复杂结构化编辑器；表格模式给出可执行的 Web 接续入口，并支持显式 `--open` 打开已认证的初始化向导。JSON 模式输出稳定 probe/handoff DTO，不自动打开浏览器。
- “尚未注册且需要用户确认”必须使用文档化的 action-required 退出语义，不能以成功注册的退出码冒充完成。具体退出码在 ADR/OpenAPI/CLI 契约中固定并测试。

## 6. BAT 分析器最低能力

### 6.1 语法边界

实现有界词法/语法分析，不以几条跨行正则代替解析器。首版至少识别：

- `@echo`、`rem`、`setlocal/endlocal`。
- `set "NAME=value"`、受控的同文件变量引用。
- `cd /d`、`pushd/popd` 和 `%~dp0`。
- label、`goto`、简单 `if exist/not exist`、`if errorlevel` 分支。
- `call` 到工作区内 BAT 或已知包装器，受递归边界限制。
- `where`、输出、`chcp`、`start` 等非服务语句的明确分类。
- npm/Maven/Java/Node/Docker Compose/Cocos Creator 等已知调用的参数数组。

复杂动态扩展、`for /f` 命令替换、管道、重定向到可执行内容、PowerShell/cmd 嵌套、下载执行、注册表修改及工作区外脚本调用必须标记为不支持或危险，不得执行。

### 6.2 配置探测器

- JSON 使用标准 JSON 解析器；YAML 使用 `yaml.v3`；XML 使用 Go 标准 XML 解析器。
- 解析 `package.json` scripts、Maven POM/Wrapper、Compose 文件和受支持的 Vite/服务配置。
- JS/TS 源码只允许经过有明确语法边界的探测器读取；不能可靠解析时返回证据不足，不用任意文本猜端口。
- 端口来源必须区分 BAT 参数、环境变量、package script、项目配置、默认值和运行时自动选择。
- 依赖关系仅从明确顺序、等待逻辑、URL/端口引用或用户确认建立。
- readiness 优先使用明确 HTTP/TCP 检查；无证据时可建议弱 `process` 检查，但必须标注。

### 6.3 WFGame 验收样例

以可复制到测试临时目录的 fixture 覆盖 `E:\WFGame\run.bat` 的结构，不允许测试直接读写真实 WFGame：

- 识别可选 Cocos Creator 构建步骤和 Node 静态服务步骤。
- 追踪 `BUILD_DIR`、`START_SCENE`、`%CD%` 和 `tools\serve.js` 引用。
- 识别静态服务的默认端口来源；如果无法通过受支持配置探测器确认，应阻止自动应用并要求用户确认。
- 检测服务是否监听非 loopback；与 StackPilot 当前安全范围冲突时必须阻止应用或要求先完成受控项目改造，不能错误标记为 loopback。
- 为“仅服务已有构建”和“构建后服务”给出两个结构化候选；后者只有在 Cocos oneshot Runner capability 可用时才能应用。
- 在 WI-03 实现前，经用户明确授权执行一次早期只读采集，将必要结构最小化、去除机器特定路径或潜在敏感值后固化为仓库 fixture；若未获授权，WFGame 专项解析与 Gate 必须标记 blocked，不能边写 Parser 边猜输入。

## 7. Runner 与 capability

- 新增 `node` Runner 前，必须同步 ADR、设计、Schema enum、类型、解析器、版本预检、Windows 参数引用、Supervisor 集成、capability、OpenAPI/DTO 和真实进程树测试。
- Node Runner 的可信解析来源、平台最低版本和项目版本约束必须在 ADR 中分别固定。项目版本只读取明确的 `package.json.engines.node` 等契约，不得从 `@types/node` 或 StackPilot 自身 Node 24 工具链要求推断业务项目版本。
- Cocos Creator 构建作为独立、受控、capability-gated 的 `oneshot` Runner 设计，不接受 HTTP 提交绝对 executable。解析规则必须来自可信配置或固定发现策略。
- 在 Cocos Runner 未完成时，导入器可以生成不可应用的候选和明确 `FEATURE_NOT_ENABLED`，不能生成运行时必失败的清单。
- 不增加通用 `shell`、`bat`、`powershell` 或任意 executable Runner。

## 8. 持久化、Operation 与审计

- 分析是有界只读请求，可以同步返回草案。
- 应用、重新关联、生成/覆盖清单和结构化编辑必须是持久化 Operation，并支持幂等键、工作区或规范路径互斥、失败终态和恢复。
- 预注册阶段尚无已持久化 workspace ID。ADR 必须先确定 Operation 的合法作用域模型和 migration；不得塞入虚假 workspace/system 记录，也不得用进程内锁代替数据库约束。ADR 至少要给出 `operations` 表重建、nullable workspace/system 或等价 scope 设计、Operation type CHECK、scope 唯一锁、canonical target key、幂等索引、DTO 可空语义、事件关联及 reconciliation 的完整方案。
- 现有 `events` 强制关联 workspace/system，无法直接记录预注册 Operation 的领域事件；ADR 必须决定泛化事件作用域或建立同等可恢复的 Operation 事件事实来源。`audit_events` 已有通用 target 字段，但仍需同步动作、校验和映射。
- 导入草案和来源元数据必须有 TTL/容量上限，不保存 BAT 全文、Secret、完整环境或不必要的绝对内部路径。
- 审计至少记录分析、应用、重新分析、编辑、重新关联和失败原因的稳定错误码。

## 9. API 与错误契约

先在 OpenAPI 中定义并评审，再实现。语义上至少需要：

- 工作区探测/导入分析。
- 获取导入草案及诊断。
- 应用导入草案并返回 Operation 引用。
- 获取工作区详情。
- 创建编辑草案、预览差异、应用编辑。
- 重新关联路径和重新分析来源。

具体 URI 和 DTO 以 ADR 与 OpenAPI 为准。所有 mutation 继续要求会话认证、精确 Origin、CSRF header、JSON Content-Type 和审计。新增稳定错误至少分别覆盖：无候选脚本、选定脚本不存在、脚本路径越界、非普通文件、编码不支持、文件过大、语法不支持、危险语句、引用循环、分析不完整、端口未确认、依赖未确认、来源已变化、manifest digest 冲突、运行中禁止编辑、重新关联 system 不一致和原子写入失败。Runner capability 未启用复用统一 `FEATURE_NOT_ENABLED` 并返回白名单 capability detail。

`initialization_required` 是探测 DTO 状态，不注册为错误码。`WORKSPACE_MANIFEST_UNAVAILABLE` 保留给直接调用旧注册语义和清单后续丢失场景；Web 添加流程应将“首次缺少清单”转换为初始化向导，而不是把该错误作为死路。

## 10. 模块落点

- BAT Lexer/Parser、只读引用图和工具探测器放入独立 `internal/importer`，不得依赖 HTTP、SQLite、Windows 进程 API 或前端 DTO。
- 现有仓库实际由 `internal/workspace.Manager` 承担工作区用例，而详细设计列出的 `internal/application` 尚不存在。WI-00 必须关闭此 drift。
- 本专项默认由 `internal/workspace` 编排导入 apply、详情、编辑和重新关联，依赖最小 importer/repository/operation 接口；如果 ADR 决定恢复 `internal/application`，必须一次性迁移清晰的用例边界，禁止增加空壳转发层。
- API handler 仍只负责边界映射，Importer 不写文件、不写数据库、不创建进程。

## 11. Web 体验

- 沿用 Vue 3、TypeScript、Pinia、Element Plus 和现有视觉语言，不引入第二套组件库。
- 将现有工作区表格补充“查看详情”和“编辑”图标按钮及 tooltip；行本身可进入详情。
- 导入向导必须有稳定步骤、加载/空/失败/冲突/恢复状态，错误持续显示并带安全 traceId。
- 服务、端口和依赖使用适合扫描比较的表格或紧凑布局，不使用嵌套卡片。
- 编辑控件按字段类型选择输入、选择器、开关和步进器；端口、Runner、readiness 等选项不得用自由文本代替。
- 清单差异以结构化 diff 为主，YAML 仅只读预览。
- 所有按钮可用性由 capability、manifest 状态、活动 Operation 和运行状态共同决定。
- 覆盖键盘操作、焦点返回、对话框 Esc、提交中禁用、窄屏文字不溢出和非颜色状态表达。
- REST/SSE、错误映射和认证继续集中在 `web/src/api`，共享状态进入 Pinia。

## 12. 测试要求

- BAT 词法/语法、变量、路径、分支、递归、循环、大小和编码单元测试。
- npm/Maven/Java/Node/Compose/Cocos 探测 fixture；不支持和恶意脚本必须明确失败。
- 端口来源、依赖推断、readiness 建议、置信度和证据稳定性测试。
- Manifest 生成结果通过真实 Loader、Schema 和 Validator；重复生成必须确定性一致。
- 真实 SQLite migration/repository 测试覆盖空库、历史升级、重复启动、checksum 异常、TTL 和并发约束。
- Operation migration 测试必须覆盖旧表数据前向复制、CHECK/index/FK 重建、预注册 scope、同 canonical target 并发初始化、幂等重放以及无 workspace 中间态的重启收口。
- 文件集成测试覆盖缺失目录、已有文件、外部并发修改、junction/符号链接逃逸、只读文件、原子替换失败和崩溃恢复。
- API 契约测试覆盖认证、Origin、CSRF、Content-Type、幂等、错误 details 白名单和敏感信息不泄露。
- Web 组件与真实浏览器 E2E 覆盖：已有清单注册、缺失清单导入、分析警告、用户修正、应用成功、详情、编辑冲突、运行中禁止应用、重新关联和解除注册。
- Windows Node/Cocos Runner 使用可控 fixture 验证参数、隐藏窗口、完整进程树、日志、取消和停止；真实 WFGame 除 WI-00 经授权的最小只读 fixture 采集外，只用于最终人工/集成 Gate，不替代 fixture。
- 完成后运行与改动匹配的 Go、Web、Schema/OpenAPI、migration、安全和 Windows 集成验证，并报告实际命令、结果与未执行项。

## 13. 文档同步

同一变更必须按实际设计同步：

- `docs/overall-design.md`
- `docs/detailed-design.md`
- `docs/phased-development-plan.md`
- 新 ADR
- `api/openapi.yaml`
- `docs/error-codes.md`
- `schemas/system-v1alpha1.schema.json` 及示例
- `docs/storage-schema.md` 和新增 migration
- 开发/接入/故障排查文档
- 对应 progress/evidence 与 Gate

## 14. 明确不做

- 不执行任意 BAT/PowerShell 来推断服务。
- 不承诺解析所有 BAT 语法。
- 不提供 `/exec`、shell Runner、原始命令编辑或任意环境变量入口。
- 不静默修改 BAT、`package.json`、源码、Vite 配置或其他业务文件；除 `.stackpilot/system.yaml` 外的项目改造必须单独展示并由用户在对应项目完成。
- 不在导入时自动启动服务、打开浏览器、下载依赖或访问外网。
- 不使用 AI 推断结果直接生成可执行清单；模型增强留到后续 capability。

## 15. 完成定义

只有同时满足以下条件才能声明完成：

1. 缺少清单的工作区可从 Web 进入 BAT 导入向导，无需手工编写 YAML。
2. 支持范围内的脚本能生成确定、可解释、可校验的 Manifest；不支持内容不会被猜测或执行。
3. 应用成功后 `.stackpilot/system.yaml` 成为唯一运行定义，后续启停不调用 BAT。
4. 工作区列表具备详情和编辑入口，详情数据完整且安全，编辑具备 diff、并发冲突和运行状态保护。
5. Node Runner 可用；Cocos 构建候选按 capability 正确启用或返回 `FEATURE_NOT_ENABLED`。
6. API、Schema、migration、错误码、DTO、前端和文档一致。
7. 安全、存储、Windows 进程和浏览器主流程测试通过，无遗留进程、临时文件或敏感测试产物。
8. `AGENTS.md` 与 `CLAUDE.md` 哈希一致，所有实际验证和剩余限制已记录。
