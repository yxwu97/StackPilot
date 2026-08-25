# StackPilot 工作区导入、详情与编辑开发计划

> 状态：已完成（2026-08-22；WI-07 Cocos Runner 按独立 capability 保持未启用）
> 日期：2026-08-19
> 来源 Prompt：`prompt/prompt-20260819-01-workspace-import-and-management.md`
> 计划属性：跨阶段安全与产品能力专项；不改变已完成阶段的历史结论

## 1. 目标结果

交付一个不要求用户手写 `.stackpilot/system.yaml` 的工作区接入流程，并补齐工作区详情和结构化编辑。BAT 仅作为只读迁移输入；系统生成并验证清单后，所有运行仍由 Manifest、Runner、Orchestrator 和 Supervisor 驱动。

最终主流程：

```text
添加路径 -> 探测 -> 选择 BAT -> 静态分析 -> 结构化确认
        -> 持久化应用 Operation -> 原子生成 Manifest -> 注册
        -> 工作区详情 -> 结构化编辑/重新分析 -> diff -> 应用
```

## 2. 固定决策

1. 不执行 BAT，不增加通用 shell/BAT/PowerShell Runner。
2. `.stackpilot/system.yaml` 继续是唯一运行定义；数据库保存来源、草案、快照和操作状态，不成为第二份可编辑定义。
3. BAT 分析器只支持明确语法子集，并输出证据、置信度、警告和阻断项。
4. UI 只编辑结构化字段，YAML 只读预览。
5. 已注册 system ID 不可原地修改；根路径重新关联只允许同一 system ID，且系统停止、无活动 Operation。
6. 应用草案、编辑和重新关联必须持久化、幂等、可审计并定义崩溃恢复。
7. `node` Runner 纳入本专项，但通用导入 apply 不硬依赖 Node；包含 Node 服务的草案在 Runner capability 未启用时返回 `FEATURE_NOT_ENABLED`。Cocos Creator oneshot Runner 同样独立 gating。
8. WFGame 使用复制 fixture 自动测试。WI-00 经用户明确授权只读采集最小 fixture，WI-09 再单独授权真实 Gate；任何阶段都不得默认修改真实 `E:\WFGame`。
9. “缺少清单”在 probe 中是 `initialization_required` 正常状态，不是错误码；旧注册 API 的 manifest unavailable 语义单独保留。

## 3. 工作包总览

| ID | 工作包 | 依赖 | 主要交付物 | Gate |
| --- | --- | --- | --- | --- |
| WI-00 | 现状基线与 ADR | 无 | ADR、威胁模型、阶段归属、兼容策略 | 设计评审通过 |
| WI-01 | 契约先行 | WI-00 | OpenAPI、错误码、DTO、capability 草案 | 契约测试可生成/校验 |
| WI-02 | 持久化与 Operation 作用域 | WI-00/01 | migration、repository、恢复语义 | SQLite 升级 Gate |
| WI-03 | BAT 解析核心 | WI-00 | AST/IR、诊断、边界与 fixture | Parser Gate |
| WI-04 | 工具与配置探测器 | WI-03 | npm/Maven/Java/Node/Compose/Cocos 探测 | Discovery Gate |
| WI-05 | Manifest 草案与生成器 | WI-01/03/04 | 结构化草案、diff、确定性 YAML | Manifest Gate |
| WI-06 | Node Runner | WI-00/01/05 | resolver、预检、Windows 监管与 capability | Windows Runner Gate |
| WI-07 | Cocos Runner | WI-00/01/06 | capability-gated oneshot Runner | 独立可选 Gate |
| WI-08 | 导入 API 与应用流程 | WI-02/05 | analyze/apply/get draft、审计、恢复 | API/Security Gate |
| WM-01 | 工作区详情后端 | WI-01/02 | detail repository/use case/API | Detail Contract Gate |
| WM-02 | 工作区结构化编辑后端 | WI-02/05/08/WM-01 | draft/diff/apply/relink | Edit Consistency Gate |
| UI-01 | 添加/导入向导 | WI-08 | Vue/Pinia/API/E2E | Import UX Gate |
| UI-02 | 工作区详情与编辑 | WM-01/02 | 详情页、编辑页、冲突与状态保护 | Management UX Gate |
| WI-09 | 文档、真实接入与发布 Gate | WI-00..06、WI-08、WM/UI；WI-07 按 capability | 文档、证据、WFGame 验收、回归报告 | 专项完成 |

完成记录：WI-00 至 WI-06、WI-08、WM-01/02、UI-01/02 和 WI-09 已通过专项 Gate。WI-07 没有提供半实现路径，`workspace.runner.cocos` 继续未公布；WFGame 的 build-and-serve 候选因此保持阻断。验收命令与结果见 `docs/evidence/workspace-import-and-management-20260822.md`。

## 4. WI-00：设计、ADR 与威胁模型

### 任务

- 盘点现有 workspace、manifest、operation、runner、storage、API、CLI 和 Web 调用链，记录当前契约基线。
- 新建 ADR，明确 BAT 是迁移输入而非运行入口、支持语法范围、来源证据模型、文件写入协议、编辑语义和 capability。
- 解决“预注册阶段没有 workspace ID，但变更必须是 Operation”的作用域模型。推荐将 Operation 扩展为受约束的 target scope，而不是创建虚假 workspace/system；ADR 必须给出 `operations` 表重建方案、workspace/system 可空或等价 scope 列、Operation type CHECK、canonical target key、活动唯一索引、幂等索引、旧数据复制和回滚验证。
- 列出跨层影响并逐项定责：domain Operation/CreateInput、repository scan/query、OpenAPI/DTO 的 workspaceId/systemId 可空语义、operation list/filter、SSE/事件事实、audit action/target、active lock、reconciliation 和所有直接测试 fixture。
- 当前 `events` 表强制 workspace/system 外键；ADR 必须决定泛化事件 scope 或提供同等持久、可恢复、可续读的预注册 Operation 事件来源。`audit_events` 表本身目标通用，不预设需要重建，但动作和校验必须更新。
- 定义文件与数据库无法同事务提交时的状态机：草案已确认、文件暂存、文件已替换、快照已提交、完成/恢复失败。
- 定义工作区根路径重新关联的身份、并发、运行状态和回滚规则。
- 明确 refresh drift：当前 OpenAPI/实现为 `/workspaces/{workspaceId}/refresh -> 200 Workspace`，详细设计为 `/systems/{id}/refresh -> 202 Operation`。ADR 必须同时裁决资源路径所有权和同步/异步返回，选择对齐、版本化兼容或形成有期限且有测试的正式豁免，不能保持两套契约。
- 明确 CLI：已有清单继续注册；缺少清单输出 probe/handoff 状态，表格模式支持显式 `--open`，JSON 模式稳定输出，不以成功退出码冒充已注册。
- 明确用例包落点：默认保留 `internal/workspace` 编排并新增纯分析 `internal/importer`；如恢复 `internal/application`，必须迁移完整职责而非增加转发壳。
- 固定公开和内部 capability 的准确名称及映射，并同步 `/version`、Schema annotation、Validator 和 Web；未完成此项不得进入 WI-01 实现。
- 经用户明确授权，对真实 WFGame 进行一次只读、最小化 fixture 采集；清除绝对机器路径和潜在敏感值后评审入库。未授权则将 WFGame 专项标记 blocked，不猜测语法。
- 建立威胁模型：路径逃逸、junction 交换、TOCTOU、脚本炸弹、引用循环、危险语句、环境泄漏、命令注入、端口暴露误判、并发覆盖和审计泄漏。
- 更新 overall/detailed/phased design；如需修改全局规范，同步 `AGENTS.md` 与 `CLAUDE.md`。

### 退出条件

- 高影响决策无悬空项。
- 设计明确哪些内容属于首版、哪些 capability-gated。
- Operation 表重建、事件关联、CLI、refresh drift、包落点和 capability 命名均有明确结论。
- ADR 与现有安全边界不冲突，或已同步修正所有事实来源。

## 5. WI-01：OpenAPI、错误码与类型契约

### 任务

- 设计工作区探测、导入分析、草案查询、应用、详情、编辑草案、diff、应用编辑和重新关联 API。
- 所有 mutation 定义认证、Origin、CSRF、JSON Content-Type、Idempotency-Key、OperationRef 和错误响应。
- 定义紧凑 DTO：ImportDraft、Finding、Evidence、ServiceDraft、PortDraft、DependencyDraft、WorkspaceDetail、WorkspaceEditDraft、ManifestDiff。
- 限制数组、字符串和 details 大小；证据仅返回工作区相对路径和安全位置。
- probe DTO 用 `state: initialization_required` 表达正常分支，不创建对应 ErrorCode。
- 新增错误码并登记 HTTP 状态、retryable 和 allowedDetails。至少覆盖：
  - `WORKSPACE_SCRIPT_CANDIDATE_NOT_FOUND`
  - `WORKSPACE_SCRIPT_NOT_FOUND`
  - `WORKSPACE_SCRIPT_OUTSIDE`
  - `WORKSPACE_SCRIPT_TYPE_UNSUPPORTED`
  - `WORKSPACE_SCRIPT_ENCODING_UNSUPPORTED`
  - `WORKSPACE_SCRIPT_TOO_LARGE`
  - `WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED`
  - `WORKSPACE_SCRIPT_DANGEROUS`
  - `WORKSPACE_SCRIPT_REFERENCE_CYCLE`
  - `WORKSPACE_IMPORT_INCOMPLETE`
  - `WORKSPACE_IMPORT_PORT_UNCONFIRMED`
  - `WORKSPACE_IMPORT_DEPENDENCY_UNCONFIRMED`
  - `WORKSPACE_IMPORT_SOURCE_CHANGED`
  - `WORKSPACE_MANIFEST_CONFLICT`
  - `WORKSPACE_MANIFEST_WRITE_FAILED`
  - `WORKSPACE_EDIT_RUNTIME_ACTIVE`
  - `WORKSPACE_RELINK_SYSTEM_MISMATCH`
- Runner 缺失复用 `FEATURE_NOT_ENABLED`，allowed details 只包含契约登记的 capability 名。
- 将 Node runner enum、准确 capability 名及 gate 契约放入本工作包；Runner 尚未实现时 Node 草案可以保存/预览，但不能 apply。
- 保留旧注册 API 的兼容行为，Web 用探测结果选择注册或初始化流程。

### 验证

- OpenAPI 语法与仓库契约检查。
- 错误码注册表与实现映射的一致性测试。
- DTO 不包含 BAT 全文、Secret、完整环境或不必要绝对路径。

## 6. WI-02：SQLite、Repository 与 Operation

### 任务

- 核对 `000001` 至当前 migration、workspace/operation repository 和外键后，只新增向前 migration。
- 持久化有界导入草案、来源摘要、诊断摘要、状态、过期时间和应用结果。
- 保存已注册工作区的来源类型、相对入口脚本、来源 digest 和最后分析时间；不要把运行定义复制成可编辑数据库字段。
- 实现草案 TTL、容量上限和清理，清理不能影响已应用快照或审计。
- 实现规范路径级互斥和幂等，覆盖两个客户端同时初始化同一路径。
- 按 ADR 以前向 migration 重建 Operation 相关约束和索引，扩展 target scope、状态机、事件和恢复；不得修改既有 migration，也不得用内存布尔锁。
- 更新 `docs/storage-schema.md`。

### 验证

- 空库升级、上一正式版本升级、带历史 Operation/step 的表重建、重复启动、migration checksum 异常以及 FK integrity check。
- 预注册 Operation 创建/查询/DTO、同 canonical target 并发初始化、workspace scope 与 path scope 隔离、幂等重放、TTL、崩溃中间态和控制面重启收口。
- 真实 SQLite 约束测试，不使用 mock 代替事务。

## 7. WI-03：BAT 解析核心

### 实现边界

- 新包建议放在明确的导入模块，如 `internal/importer`；领域 Manifest 不依赖 BAT AST。
- Lexer、Parser、控制流归一化和诊断分层，生产函数遵守 50 行限制。
- IR 只表达分析需要的赋值、目录、分支、调用和工具命令，不实现 cmd.exe。

### 任务

- 支持 ASCII/UTF-8、CRLF/LF、BOM、注释和行位置。
- 支持 prompt 中列出的安全语法子集。
- 对变量引用建立受控符号表；只展开同一分析图内可证明的值和白名单内建变量。
- label/goto 构建有界控制流图；递归、循环和分支爆炸必须提前终止并返回稳定诊断。
- 所有脚本和引用路径做 canonical 边界验证；限制深度、文件数、单文件与总字节。
- 危险或不支持结构不得降级为字符串命令。
- WI-00 已授权并完成 WFGame 最小 fixture 采集后再固定 WFGame 语法覆盖；若未完成，Parser 通用能力可以推进，但不得宣称通过 WFGame Gate。

### Fixture 矩阵

- 空格、中文路径、引号、尾随反斜杠、CRLF、UTF-8 BOM。
- 简单 npm/Maven/Java/Node、嵌套 call、goto、if exist/errorlevel。
- `for /f`、PowerShell、管道、下载执行、动态 call、越界路径、junction、循环引用和超限输入。
- 经授权最小化后的 WFGame `run.bat` fixture；fixture 不含真实绝对路径、Secret 或用户数据。

## 8. WI-04：探测器与候选合成

### 任务

- 定义最小 Detector 接口，输入为已解析 IR 和有界文件访问器，输出候选与证据。
- 实现 npm/package.json、Maven/POM/Wrapper、Java、Node、Compose 和 Cocos Creator 探测器。
- 对 JSON/XML/YAML 使用结构化解析器；JS/TS 仅处理有明确语法支持的配置，不做全语言猜测。
- 发现服务边界、工作目录、参数、环境、端口、依赖和 readiness 候选。
- 同一字段多来源冲突时保留冲突，不能按探测顺序静默覆盖。
- 稳定排序发现结果和诊断，保证相同输入生成相同草案摘要。
- 对非 loopback 服务或无法确认监听范围生成阻断诊断。

### WFGame 结果要求

- 给出“仅服务已有构建”和“构建后服务”两个候选。
- Node 服务、build 目录、serve.js、默认端口和自动递增行为都有证据或明确 unresolved。
- Cocos 候选在 capability 缺失时不可应用，但其他安全候选不受影响。

## 9. WI-05：草案、diff 与 Manifest 生成

### 任务

- 建立独立 Draft 类型，不直接把未经确认的发现塞入 `manifest.Manifest`。
- 实现字段确认状态和跨字段校验：端口所有者、引用、DAG、daemon readiness、runner capability、路径与 exposure。
- 用户修正仅接受结构化白名单字段和长度/数量上限。
- 从已确认 Draft 构造强类型 Manifest，使用 `yaml.v3` 输出 UTF-8 无 BOM、LF。
- 通过现有 Loader、JSON Schema、Validator 和 capability 校验后才允许暂存。
- digest 必须取“生成 YAML 重载并完成验证后”的规范化 JSON 快照 digest，与 `workspace.Manager` 现有 SHA-256 语义一致；禁止对 YAML 文件字节直接求 digest。
- 输出语义 diff：服务增删改、端口、依赖、health、policy；同时提供只读规范化 YAML。
- 确定性测试：同一草案重复生成、重载后的 normalized content 和 snapshot digest 一致。

### 文件应用协议

- 再次核验 root/script/source digest、目录和目标文件身份。
- 同目录临时写入、flush/close、原子替换、临时文件清理。
- 首次初始化只允许目标不存在；编辑要求 If-Match digest。
- 按 ADR 完成 SQLite 快照提交和崩溃恢复，不覆盖外部并发修改。

## 10. WI-06：Node Runner

### 任务

- 先更新 Schema、Manifest 类型、capability 和设计，再实现 resolver。
- 按 ADR 只从受信任显式配置/allowed tool roots 和 PATH 解析 `node.exe`；首版不得自动信任工作区内自带 executable。
- 平台支持的最低 Node 版本与项目声明分开校验。项目约束只读取明确的 `package.json.engines.node`；不得从 `@types/node` 或 StackPilot 自身 `engines.node >=24` 推断业务项目要求。
- 参数始终为数组；不经 shell，不接受 HTTP executable 字段。
- 接入现有 Supervisor、Job Object、日志、readiness、停止和身份核验。
- 覆盖 node 父子进程树、端口冲突、大日志、异常退出、取消和 StackPilot 重启恢复。

### Gate

- Node fixture 能 start -> ready -> logs -> stop，且完整进程树退出。
- 路径/参数包含空格和中文时行为正确。
- API 调用方仍不能指定 executable 或任意命令。

## 11. WI-07：Cocos Creator Runner（独立 capability）

### 任务

- 形成单独 ADR 补充或在 WI-00 ADR 中完整定义可信发现与版本约束。
- 只允许声明项目内构建目标、平台和受限选项；不可提交任意 Creator CLI 参数。
- Cocos 路径来自受信任本机配置或固定发现策略，不写入 Manifest 绝对命令。
- 作为 oneshot 运行，按受控产物检查决定成功；明确处理当前 Creator 特殊退出码但不泛化吞错。
- 清理 `ELECTRON_RUN_AS_NODE` 等已知危险环境覆盖，并记录非敏感预检结果。

### Gate

- capability 未启用时返回 `FEATURE_NOT_ENABLED`。
- fixture 覆盖成功、失败、超时、取消、产物缺失和异常退出码。
- 真实 Creator 验证作为环境依赖项单独报告，不能用 fixture 冒充。

## 12. WI-08：导入 API 和应用编排

### 任务

- Handler 只处理认证、解析、校验、DTO 和错误映射；分析与应用逻辑位于用例层。
- analyze 只读、有界、可取消；apply 返回持久 Operation 引用。
- 应用步骤建议至少包括：锁定目标、复核来源、校验草案、生成暂存文件、提交文件、注册/刷新快照、记录来源、发布事件、完成。
- 支持 Idempotency-Key，同一草案重复应用返回相同结果或稳定冲突。
- 所有失败映射一次并带安全 traceId；日志带适用的 operation/workspace/error code。
- 审计分析、应用、失败和恢复，不记录脚本全文或敏感内容。
- 旧 `POST /workspaces` 行为保持兼容；新增探测供 Web/CLI 选择流程。
- CLI 对 `initialization_required` 输出稳定 probe/handoff DTO；表格模式仅在显式 `--open` 时打开 Web，JSON 模式不产生 GUI 副作用，并使用文档化 action-required 退出语义。
- 通用 WI-08 不依赖 Node Runner 实现。含 Node/Cocos 的草案在对应 capability 关闭时可查看但 apply 返回 `FEATURE_NOT_ENABLED`；Maven/npm/Java 等已支持工作区不受阻塞。

### 验证

- API 合约、认证、Origin、CSRF、Content-Type、body 上限、未知字段。
- 源文件在 analyze/apply 间变化、清单同时出现、并发 apply、取消和服务重启恢复。
- 慢客户端和 SSE 不阻塞应用链路。

## 13. WM-01：工作区详情

### 后端

- 增加单工作区详情 use case 和 repository 查询，避免页面拼接多个不一致快照。
- 返回注册、来源、Manifest、服务、端口、依赖、health、活动 Operation 和近期变更摘要。
- DTO 映射严格脱敏；规范路径可按产品需要显示根路径，但不暴露控制面数据目录和完整运行环境。
- 补齐 OpenAPI 和契约测试。

### Web

- 工作区表增加详情图标按钮和行导航。
- 详情采用可扫描的全宽布局：基本信息、清单、服务/端口、来源与历史、操作区。
- 支持刷新、编辑和解除注册；按钮由运行状态、活动 Operation 和 capability 控制。
- YAML 预览只读、有界，长内容不撑破布局。

## 14. WM-02：结构化编辑与重新关联

### 任务

- 从当前有效 snapshot 创建带 base digest 的 EditDraft。
- 表单可编辑字段严格受 Manifest Schema 和 capability 限制，不允许 raw command/YAML。
- 提供 server-authoritative diff 和应用前复核。
- apply 为持久 Operation；运行中或活动 Operation 时返回稳定冲突。
- 写文件前验证最后有效 digest、磁盘当前 digest 和草案 base digest 一致。
- 重新关联验证新 canonical path 唯一、system ID 相同、清单有效，并定义旧路径仅解除引用、不删除文件。
- 外部修改、文件只读、原子替换失败、刷新失败和崩溃中间态都可诊断和恢复。
- 审计编辑字段类别和 digest，不记录 Secret 或完整环境值。

## 15. UI-01/UI-02：前端实现

### 结构

- API 类型和调用继续集中在 `web/src/api`，工作区导入/详情/编辑状态进入 Pinia。
- 从现有单体 `App.vue` 中只拆出本功能确实需要的工作区视图和对话框，不做无关全站重构。
- 至少拆分工作区列表、详情、导入向导和编辑界面为独立组件/视图，由 `App.vue` 只保留顶层导航与选择状态；本专项不新增 router 依赖，除非 WI-00 ADR 证明深链/恢复需求必须引入。
- 使用 Element Plus 和现有 icon 体系。

### 导入向导

- 路径探测、脚本选择、分析、服务确认、端口/依赖/health、diff/YAML 预览、Operation 进度。
- 明确展示 confirmed/inferred/unresolved 和证据；阻断项不能被普通确认跳过。
- 支持关闭后从持久草案恢复；过期草案给出明确重新分析入口。

### 详情与编辑

- 表格提供详情、编辑、刷新、解除注册四类动作及 tooltip。
- 编辑用 select、switch、number input 等结构化控件；最长路径和错误文本在桌面/移动视口不溢出。
- 冲突时保留用户草案，展示重新加载和重新比较入口，不自动覆盖。
- 对运行中、无 capability、manifest invalid 和活动 Operation 状态提供稳定禁用原因。

### E2E

- 使用临时 fixture 工作区，不操作真实项目。
- 覆盖已有 Manifest 注册、缺少 Manifest 导入、警告修正、应用、详情、编辑、外部冲突、运行中禁止应用、重新关联和解除注册。
- 截图检查桌面和移动视口，无重叠、溢出或布局跳动。

## 16. WI-09：文档、接入与最终 Gate

### 文档同步

- OpenAPI、错误码、Schema 示例、storage schema。
- overall/detailed/phased design、ADR、开发与用户接入文档。
- 新工作包 progress/evidence；历史 Gate 不回写虚假结果。
- UI 文案说明通过交互表达，不在页面堆叠功能说明文字。

### 自动验证命令基线

执行前以仓库实际脚本为准，不编造命令。至少覆盖：

```powershell
gofmt -w <本次修改的 Go 文件>
go test ./...
go vet ./...
npm run type-check
npm run build
```

还必须运行仓库已有的 OpenAPI/Schema、migration、安全、Windows Runner 和浏览器 E2E 检查。若工具链使用 `.tools` 中固定版本，应使用仓库规定版本而不是系统偶然版本。

### WFGame 真实 Gate

- WI-00 的早期采集授权只允许读取并最小化 fixture；WI-09 真实 Gate 需要再次明确授权，默认不写入。
- 展示识别证据、两个候选、端口及 exposure 警告。
- 用户确认后才生成清单；如安全监听或 Cocos capability 未满足，应明确阻断并记录未完成项。
- 成功场景验证生成清单可重新加载、详情可查看、结构化编辑可产生稳定 diff。

### 专项退出条件

- Prompt 第 15 节完成定义全部满足。
- 所有新增 migration 可从当前正式 schema 升级且 checksum 稳定。
- API、DTO、Schema、错误码、Web 和文档一致。
- BAT 分析不执行命令，恶意 fixture 全部被拒绝或隔离为 unresolved。
- Node Runner 真实 Windows 进程树验证通过；Cocos 未完成时 capability 正确关闭。
- 工作区详情和编辑 E2E 通过，外部并发修改不被覆盖。
- 无遗留进程、端口、临时文件、测试数据库或敏感产物。
- `AGENTS.md` 与 `CLAUDE.md` SHA-256 完全一致。

## 17. 建议执行顺序

1. WI-00（含授权后的 WFGame 最小 fixture 采集）-> WI-01。
2. WI-02 与 WI-03 可在契约稳定后并行推进。
3. WI-04 -> WI-05；Node enum/capability 在 WI-01 固定，完整 WI-06 可独立推进。
4. WI-08（不依赖 WI-06）-> UI-01；Node 工作区 apply 仍受 capability gate。
5. WM-01 -> WM-02 -> UI-02。
6. WI-07 独立 capability 交付，不阻塞不依赖 Cocos 的导入主流程。
7. WI-09 汇总验证、文档和真实接入证据。

任何工作包未通过自身 Gate，不得以 UI 演示、代码行数或 fixture 成功替代后续真实验收。

## 18. 实现前阻断项

以下事项必须在 WI-00/WI-01 关闭后才能进入 migration、Parser 之外的主路径实现：

1. Operation scope 的表重建、索引、DTO、事件和恢复方案完成评审。
2. `initialization_required` 正常探测状态与完整错误码集合进入 OpenAPI/错误码注册表。
3. refresh drift、CLI handoff、用例包落点和 capability 准确命名有书面结论。
4. Node 不再阻塞通用 apply，Node/Cocos 草案的 gate 行为已进入契约测试。
5. WFGame fixture 已获只读采集授权并完成最小化，或 WFGame 专项明确标记 blocked。
