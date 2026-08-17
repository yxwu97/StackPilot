# StackPilot - AI 编程规范

> **镜像维护要求**：`AGENTS.md` 与 `CLAUDE.md` 必须始终逐字一致。修改任一文件时，必须在同一变更中同步另一份文件，并在交付前校验文件哈希。

StackPilot 是面向本地开发环境的声明式系统编排与控制工具。首发目标是 Windows 单用户本机模式，以 Go 单一二进制承载 REST/SSE、CLI 和嵌入式 Vue Web 控制台，使用 SQLite 保存控制面状态；Phase 1 首个真实接入系统为 BidTravel Cloud（BTC）。

## 事实来源与优先级

实现前先读取与任务直接相关的文档，发生冲突时按以下顺序处理：

1. 用户当前明确要求。
2. 本文件与 `CLAUDE.md`。
3. `code_rule.md`：日常编码、修改、验证和交付规则。
4. `docs/detailed-design.md`：模块、状态机、接口、存储、安全和测试的实现基线。
5. `docs/phased-development-plan.md`：阶段范围、依赖、Gate 和完成定义。
6. `docs/overall-design.md`：产品目标、总体架构和技术选型。
7. `docs/stackpilot-prototype.html`：交互基线，不作为后端契约或运行状态的事实来源。

OpenAPI、清单 JSON Schema、SQLite migration 和错误码注册表建立后，它们分别是对应机器可读契约的唯一事实来源。设计与契约不一致时，不得自行猜测；应先修正文档或在同一变更中同步修正。

## 阶段与范围控制

- 严格遵守分阶段计划，不提前实现后续阶段能力。
- 未启用能力必须通过 capability flag 或清单校验返回 `FEATURE_NOT_ENABLED`，不得提供半实现路径。
- Phase 1 仅支持 Windows、本机单用户、`process/daemon`、Maven/npm/Java Runner、process/TCP/HTTP readiness、REST/SSE、BTC Backend/Web 编排。
- Secret、Python venv、`oneshot/completed`、Docker Compose、liveness/自动重启和事故诊断属于 Phase 2。
- Linux/macOS 正式运行支持属于 Phase 3；模型增强、多 Agent 和自动恢复属于 Phase 4。
- 跨阶段新增范围、状态机变化、认证/监听范围变化、任意命令入口或进程监管语义变化，必须先更新设计或形成 ADR。

## 技术栈

| 层级 | 技术与约束 |
| --- | --- |
| 核心服务与 CLI | Go；单一可执行文件 |
| HTTP | `chi`，REST `/api/v1`，SSE 用于领域事件和日志流 |
| 配置 | YAML + `yaml.v3`；严格解析并由 JSON Schema 和语义校验共同约束 |
| 状态存储 | SQLite + `modernc.org/sqlite`；禁止引入 CGO 依赖 |
| 平台日志 | Go `slog`，结构化 UTF-8 日志 |
| 前端 | Vue 3 + TypeScript + Vite + Element Plus + Pinia |
| 发布 | Go `embed` 嵌入 Web 制品；GoReleaser 生成发布制品和校验和 |
| 容器能力 | Docker Compose v2，Phase 2 可选启用 |

依赖版本必须由 `go.mod`、前端 lockfile 和工具链说明锁定。新增依赖前确认维护状态、许可证、跨平台支持和是否扩大二进制或安全边界；能用标准库或现有依赖清晰实现时，不新增库。

## 模块与依赖边界

- `cmd/stackpilot` 只负责进程入口、依赖装配和命令注册，不承载领域规则。
- `internal/domain` 保存领域对象、状态和不变量，不依赖 HTTP、SQLite、Windows API 或前端 DTO。
- `internal/orchestrator` 负责 Operation、DAG、状态迁移、取消和补偿策略。
- `internal/driver` 只通过明确接口承载 process/compose 驱动；平台差异放在 `internal/platform/<os>`。
- `internal/storage` 实现 repository 和 migration，不把 SQL 类型泄漏到领域层。
- `internal/api` 负责鉴权、输入校验、DTO 映射和错误映射，不编写编排规则。
- `internal/security` 统一处理认证、Secret、脱敏、审计和路径信任边界。
- `web/src/api` 是前端 API 访问入口；页面不得散落原始请求和协议解析逻辑。

依赖方向保持为入口/适配层 -> 应用编排层 -> 领域层。禁止循环依赖、空壳转发层和为了未来可能性提前创建无实现抽象。

## 核心实现约束

### 清单与路径

- `.stackpilot/system.yaml` 是系统定义的唯一事实来源；Web/API 不提供任意 command、arguments 或 working directory 编辑入口。
- YAML 必须拒绝重复 key、未知字段、多文档和超过 1 MiB 的输入。
- 所有相对路径必须 join、canonicalize，并验证解析符号链接/junction 后仍位于工作区真实根目录内。
- 模板仅允许设计文档列出的白名单；禁止环境变量读取、函数、条件、文件包含和命令替换。
- 清单刷新失败时保留最后成功快照，但禁止基于无效清单创建新实例。

### Operation、并发与状态

- 变更操作必须建模为持久化 Operation；HTTP 请求不得同步等待完整启停流程。
- 状态迁移必须集中定义并验证合法性，禁止调用方直接改写终态。
- 状态更新与领域事件写入必须处于同一 SQLite 事务。
- 工作区锁、幂等键、取消和恢复语义按详细设计实现；不得用进程内布尔变量替代数据库约束。
- 时间统一使用 UTC，持久化时间不得依赖本地时区。

### Windows 进程监管

- 不得依赖业务项目 BAT/PowerShell 脚本完成受管服务生命周期。
- Windows 进程树必须由 Supervisor + Job Object 监管；不得退化为仅凭 PID 终止。
- PID、创建时间、可执行路径、运行账号和协议版本必须共同参与身份核验。
- Maven/npm `.cmd` 只能通过固定 `%COMSPEC% /d /s /c` 开关和正确的 Windows 参数引用规则执行；不得拼接来自 HTTP 的原始字符串。
- 启动、停止、崩溃接管和完整子孙进程终止必须通过真实 Windows 集成测试。
- P0-08 Spike 和 ADR 未通过前，不实现被其阻塞的 Process Driver 主路径。

### API、SSE 与错误

- API 统一使用 `/api/v1`；请求/响应必须与 OpenAPI 一致。
- 错误必须使用稳定错误码和统一 envelope；未知内部错误只返回安全消息和 `traceId`。
- SSE 以数据库事件或持久日志为事实来源，支持 cursor/`Last-Event-ID` 恢复；慢消费者不得阻塞写入链路。
- REST 快照是初始事实，SSE 是增量通知；不得假设连接永不丢失或事件只到达一次。
- DTO 只暴露调用方所需字段，不返回完整命令、令牌、Secret、未脱敏环境变量或内部文件路径。

### SQLite 与 migration

- migration 使用单调递增版本和 checksum，按版本号向前执行。
- 已合入或发布的 migration 只增不改；修复通过新增 migration 完成。
- SQL 变更前必须核对当前 schema、相关 repository 和历史 migration，不凭印象使用表名或列名。
- 启用 WAL、foreign keys、busy timeout；关键唯一性和并发不变量由数据库约束保证。
- migration 交付前必须覆盖空库升级、历史版本升级、重复启动和 checksum 异常场景。

### 安全与日志

- 默认只监听 `127.0.0.1`；远程访问、RBAC 和多用户模式不在当前范围。
- 浏览器变更请求必须具备会话认证、精确 Origin 校验、CSRF header 和 JSON Content-Type。
- Authorization、令牌、Secret、完整子进程环境和未脱敏日志不得写入日志、数据库、SSE、DTO、Operation 快照或测试产物。
- 删除或写入文件前必须同时验证登记路径、canonical path 边界和文件类型。
- 结构化日志至少在适用时携带 `trace_id`、`operation_id`、`workspace_id`、`instance_id`、`service_id` 和 `error_code`。

## 编码约定

### Go

- 所有 Go 文件必须通过 `gofmt`；生产代码须通过 `go test`、`go vet` 和项目配置的静态检查。
- 错误应保留原因链并在边界映射一次；使用 `errors.Is/As`，禁止靠错误字符串判断类型。
- `context.Context` 作为长耗时或 I/O 调用的首个参数，不存入结构体，不用 `context.Background()` 绕过取消。
- goroutine 必须有明确所有者、退出条件和错误处理；禁止无界队列、无界并发和泄漏 ticker。
- 接口定义在使用方，并保持最小；不为测试而给生产接口增加无关方法。
- 单个生产函数不超过 50 行；超出时按职责拆分，但不得破坏事务、状态机或资源清理的可读性。

### Vue 与 TypeScript

- 启用 TypeScript 严格检查，禁止无理由使用 `any`、非空断言或关闭类型检查。
- REST/SSE 调用、错误映射和认证逻辑集中在 `web/src/api`；跨页面状态进入 Pinia，局部交互保留在组件内。
- 服务端状态以 API 为准；按钮可用性由 capability 和活动 Operation 决定，不能只依赖前端推断。
- 日志等高频列表使用稳定尺寸、有界缓存和虚拟化；动态内容不得造成布局跳动或控件文字溢出。
- 沿用 Element Plus 和现有交互原型，不引入第二套组件库或自绘已有标准图标。
- 状态不能只依赖颜色；对话框、焦点、键盘操作和错误持久呈现必须可访问。

## 测试与质量门槛

- 测试覆盖成功、失败、取消、超时、重连、恢复和适用的并发分支。
- 领域状态机、DAG、端口规划、清单校验、参数引用和脱敏必须有单元测试。
- repository/migration 使用真实 SQLite 集成测试，不用 mock 替代事务和约束验证。
- Windows Process Driver 使用可控 fixture 验证 slow-ready、异常退出、完整进程树、忽略优雅终止、大日志和端口竞争。
- API/SSE 必须有契约测试；Web 主流程必须有真实浏览器端到端测试。
- BTC 是最终真实接入验收，不替代可重复 fixture；fixture 也不能替代 BTC Gate。
- 交付前运行与变更范围匹配的最小充分测试，并报告实际命令、结果和未执行项。

## 文档与变更同步

- API 变化同步更新 OpenAPI、DTO、错误码和契约测试。
- 清单字段变化同步更新 JSON Schema、示例、语义校验和设计文档。
- 存储变化同步新增 migration、schema 说明、repository 和升级测试。
- 状态机、监管架构、认证、监听范围或危险能力变化必须同步设计文档，并在要求时形成 ADR。
- 阶段工作包完成时同步验收证据；不得用代码行数或提交数代替 Gate。

## 工作方式与全局要求

- 修改前先阅读目标文件及其直接依赖，优先使用 `rg` 做定向检索；避免无目的加载整个仓库。
- 保持改动聚焦，不修改无关模块，不删除或覆盖用户已有变更。
- 不编造 API、字段、状态、工具输出或测试结果，不提交伪代码和空实现。
- 对安全、持久化、状态语义或阶段范围存在实质疑问时先核对设计；仍无法确定且不同选择会改变结果时再请求确认。
- 除非用户明确要求，不主动初始化 Git、创建提交、推送、变基或改写历史。
- 源码、配置、日志、接口和文档统一使用 UTF-8；新建文本文件使用 UTF-8 无 BOM 和 LF，禁止中文乱码。
- 完成变更后检查格式、测试、文档同步和敏感信息泄漏，并明确说明仍有的风险或环境限制。
