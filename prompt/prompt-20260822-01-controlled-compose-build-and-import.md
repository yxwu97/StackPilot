# StackPilot 受控 Compose Build 与脚本导入增强 Prompt

> 状态：待执行
> 日期：2026-08-22
> 适用仓库：`E:\StackPilot`
> 来源问题：`E:\GNMarket` 选择 `start-gnmarket.bat` 注册时返回 `WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED`
> 关联 traceId：`tr_3ae6dfb01ee26ea0797a24d9d2d3dba1`
> 计划文件：`plan/plan-20260822-01-controlled-compose-build-and-import.md`

## 1. 任务

在不执行用户 BAT/PowerShell、不增加通用 shell Runner 的前提下，使 StackPilot 能够只读分析“BAT 包装器 -> 工作区内受限 PowerShell 脚本 -> Docker Compose 文件”的固定启动链，生成可解释、可确认的 Compose 导入草案，并通过独立 capability 安全地管理确实需要本地镜像构建的 Compose 系统。

首个真实目标是 GNMarket：其 `start-gnmarket.bat` 包含 Docker/Compose 前置检查、`if errorlevel` 控制流和对 `scripts/dev-up.ps1` 的固定 `-File` 调用；PowerShell 脚本执行 `docker compose -f compose.yaml up --build -d` 与 `docker compose ... ps`；Compose 定义中的 `web`、`job`、`frontend` 使用本地 `build:`。完成后，支持范围内的该结构应产生结构化 Compose 候选，而不是笼统返回语法不支持；用户确认并应用后，运行期仍由 Manifest、Orchestrator 和 Compose Driver 直接监管，不调用原 BAT 或 PowerShell。

## 2. 开始前必须遵守

1. 完整读取并遵守 `AGENTS.md`、`CLAUDE.md` 和 `code_rule.md`。
2. 定向核对 `docs/adr/0005-compose-base-command-boundary.md`、`docs/adr/0006-compose-preflight-starts-docker-desktop.md`、`docs/adr/0007-workspace-import-and-management.md`、`docs/detailed-design.md` 的 Compose 与工作区导入章节，以及当前 OpenAPI、Manifest Schema、错误码和 capability。
3. 本需求扩大 Docker 构建执行边界，并改变工作区脚本分析语义。任何生产实现之前必须先形成新 ADR，并同步修改 ADR-0005/0007 或明确其被替代条款；不得先放宽校验再补设计。
4. OpenAPI、Manifest JSON Schema、错误码注册表和 capability 建立或更新后分别作为机器契约事实来源，实现必须与之同步。
5. 保留用户已有修改；不初始化 Git，不创建提交，不推送、变基或改写历史。
6. 修改 `AGENTS.md` 或 `CLAUDE.md` 时必须逐字同步，并在交付前校验 SHA-256 完全一致。

## 3. 已确认事实与根因

- 当前 `internal/importer/bat.go` 只提取 Maven/npm/Java/Node 命令。`if errorlevel ... (`、右括号和 `docker compose` 会形成不支持诊断，`powershell.exe` 会形成危险诊断。
- 当脚本没有任何已支持的服务命令时，Parser 最终返回 `ErrScriptUnsupported`，此前积累的行级 finding 不会进入导入草案，因此用户只能看到通用错误和 traceId。
- ADR-0007 明确禁止执行 BAT/PowerShell，并将嵌套命令解释器列为危险输入；该安全结论继续成立。需要增加的是只读、严格受限的引用分析，不是执行权限。
- 当前 Import Draft/DTO 的服务模型偏向 process runner，不能表达 Compose 文件、受管 Compose services、端口映射或 build policy。
- `phase2.compose` 已启用，但 ADR-0005、详细设计和 `internal/driver/compose/preflight_windows.go` 明确拒绝任意受管服务的 `build` 配置。
- 当前 Compose 启动使用固定参数数组执行 `up -d --wait --no-deps`，没有独立的持久化 build 步骤、build timeout、build 错误码或 build 恢复语义。
- GNMarket 的 Compose 文件包含五个服务；`web`、`job`、`frontend` 使用工作区本地 build context，`mysql` 和 `gateway` 使用镜像，宿主 HTTPS 默认端口为 8443。`job` 与 `gateway` 没有 Compose healthcheck，而当前 Driver 要求每个受管容器同时为 `running` 和 `healthy`，因此 readiness 契约也必须显式处理。
- 只让 Importer 识别 `docker compose` 并不能解决问题；如果不同时改变 Compose build 契约，生成的清单会在运行预检阶段失败。

## 4. 目标结果

1. 支持固定、可证明的 BAT -> PS1 -> Compose 只读来源图，所有来源文件纳入 digest 和 apply 前复核。
2. 对支持的 Compose 配置生成 `driver: compose` 草案，展示 Compose 文件、受管服务、build policy、端口、依赖、readiness、证据和 capability。
3. 新增独立的受控 Compose build capability；未启用时，含 build 的 Manifest/草案稳定返回 `FEATURE_NOT_ENABLED`，普通 Compose 不受影响。
4. Build 作为 Orchestrator/Compose Driver 的显式步骤运行，使用固定 Docker CLI 参数数组，不通过 BAT、PowerShell、cmd 或 HTTP 任意命令入口。
5. Build 失败、超时、取消、控制面重启和 build 成功后 up 失败均有确定状态、错误码、日志和恢复行为。
6. Compose readiness 默认继续要求 Docker health 为 `healthy`；对确实没有 healthcheck 的 daemon 服务，允许通过闭合、逐服务、显式确认的 `running` 要求判断就绪，容器退出或不存在仍立即失败。
7. GNMarket 最小 fixture 完整通过自动化验证；获得单独授权后，再对真实 `E:\GNMarket` 执行只读分析和受控接入 Gate。
8. 用户遇到不支持或危险脚本时，错误分类、文件与行号证据准确，不再因“没有已识别命令”覆盖更具体诊断。

## 5. 固定安全与产品决策

### 5.1 不执行启动脚本

- BAT 和 PowerShell 始终只是只读迁移输入。分析、apply 和运行 Operation 均不得执行它们。
- 不新增 `shell`、`bat`、`powershell`、任意 executable 或任意 Docker 参数 Runner。
- 只允许识别字面量 `powershell.exe|pwsh.exe` 加固定安全开关和单个 `-File` 工作区相对路径；不允许 `-Command`、`-EncodedCommand`、stdin、profile、动态参数、变量拼接或工作区外脚本。
- 被引用 PS1 使用独立的受限分析器，不调用 PowerShell AST 执行接口，不求值任意表达式。超出白名单的语句必须阻断并保留证据。

### 5.2 独立 capability 与显式 opt-in

- 新 capability 建议命名为 `phase2.compose-build`，最终名称在 Design Gate 固定后同步所有契约。
- `phase2.compose` 不隐式获得构建权限。没有 build 的现有 Compose 系统保持原行为和兼容性。
- Manifest 必须显式声明 build policy；不得仅因 Compose 文件出现 `build:` 就自动执行构建。
- 首版只需要表达 `never` 与 `always` 两种策略；默认和缺省均为 `never`。若 Design Gate 选择布尔字段，必须保持同等闭合语义，禁止自由文本。
- 导入器可以基于明确的 `docker compose ... up --build` 证据建议 `always`，但 UI 必须展示并要求用户确认；API 不接受任意 build flags。

### 5.3 受控 build 配置边界

- 仅允许本地目录 build context，canonicalize 后必须位于工作区真实根目录内。
- Dockerfile 必须是 context 内的普通文件，解析符号链接/junction 后仍位于工作区；缺省仅使用 Docker 约定的 `Dockerfile`。
- 禁止 URL/Git context、绝对或越界 context、additional contexts、SSH forwarding、build secrets、特权 build、host network、entitlements 和任意工作区外文件引用能力。
- `args`、target、cache import/export、extra hosts、platform、provenance、SBOM 等字段首版默认拒绝；确有真实需求时另行设计，不通过宽松透传提前开放。
- 固定基础 Compose 的容器 `command` 继续按 ADR-0005 允许；`entrypoint`、特权容器、宿主根目录挂载和未声明依赖继续拒绝。
- BuildKit/Docker daemon 是共享宿主资源。取消 CLI 后必须确认不会继续进入 up；不能声称可强制回滚已经写入 daemon cache 的构建副作用。
- 生产路径不清理镜像或 BuildKit cache。测试也禁止全局 `docker builder prune`；只允许删除通过严格 fixture 身份记录的精确镜像/容器资源，无法安全归属的 cache 必须保留并报告。

### 5.4 Secret、日志和网络

- build args/secrets/SSH 首版禁止，从源头避免 Secret 进入命令、日志、缓存或镜像层。
- Docker stdout/stderr 经过现有有界捕获和脱敏链路，不能把完整 Compose config、完整环境、宿主路径或敏感构建输出写入 DTO、SQLite、SSE 或测试证据。
- 不自动 pull 未确认镜像，不把网络访问伪装成纯本地分析。真实 build 的镜像拉取与 Dockerfile 网络行为必须在确认界面和文档中明确为受信工作区副作用。
- StackPilot 生成的端口 override 继续只绑定 loopback；不得沿用基础 Compose 中可能暴露到所有接口的宿主端口绑定。

### 5.5 Compose readiness

- 现有 `healthy` 语义保持默认，已有 healthcheck 的服务不得由导入器自动降级为只检查 `running`。
- 对 Compose 文件中确实没有 healthcheck 的 daemon 服务，Manifest 可以按服务显式声明闭合要求 `running`；未声明时仍按 `healthy` 处理。
- 导入器只能把“服务无 healthcheck”作为 `running` 建议证据，必须由用户明确确认。API/UI 不接受任意 health 命令或自由文本状态条件。
- `running` 只表示 Docker 观察到匹配严格项目身份的容器处于 running；容器缺失、退出、重启中、身份不匹配或观察失败均不得 Ready。
- 该策略必须进入 Manifest Schema、definition digest、Compose identity、Health Engine、详情 DTO 和恢复测试，不能只在前端绕过 blocker。

## 6. Manifest、Schema 与运行模型

- 在现有 `compose` 结构中增加闭合的 build policy，具体字段名由 ADR 固定；不得在 process service 上出现。
- 为 Compose 受管服务增加闭合的逐服务 readiness requirement，默认 `healthy`，首版仅允许 `healthy|running`；具体字段位置由 ADR 固定。
- build policy 为启用时，`driver` 必须为 `compose`、`mode` 必须为 `daemon`、`readiness.type` 必须为 `compose`，并要求 `phase2.compose-build` capability。
- validator 必须区分普通 Compose 与 Compose build，不用错误字符串判断；Schema、Go 类型、规范化 JSON 和 definition digest 同步变化。
- Resolved spec 和持久 Compose project identity 必须包含 build policy、允许构建的服务集合及相关定义摘要，避免恢复时用不同定义观察或停止项目。
- Resolved spec 和持久 Compose project identity 必须同时包含逐服务 readiness requirement，避免恢复时以更宽松规则接管旧实例。
- 只有受管服务中存在已验证的 build 定义时才执行 build；启用 build policy 但没有 build 服务应由 Design Gate 决定拒绝还是规范化为 `never`，并写入契约测试。
- 不把 Dockerfile 内容、完整 Compose config 或构建日志保存进 Manifest snapshot；仅保存声明、规范相对路径、服务名和摘要。

## 7. BAT、PowerShell 与 Compose 只读分析

### 7.1 BAT 控制流

- 正确分类 `if errorlevel`、括号块、`where`、`echo`、`pause`、`exit /b` 和 `%ERRORLEVEL%` 保存/比较，不把启动前置检查误认为服务命令。
- Parser 必须保持有界，不尝试完整执行控制流。仅用于识别前置检查和固定调用链；无法证明的动态分支产生阻断 finding。
- 新 ADR 必须固定条件块嵌套深度、条件块总数、逻辑语句数、括号/`if`/`else` 配对规则和超限错误。Parser 只做一次结构遍历，不进行指数级分支展开；包含服务启动命令的动态分支首版阻断。
- `powershell -File` 只有满足固定开关、单个字面量工作区内 PS1 和无额外动态命令时才进入受限引用分析；其他 PowerShell 继续使用危险错误码。
- `parseBAT` 不得在返回通用错误时丢弃更具体的危险/不支持 finding。错误优先级、多个 finding 的排序和 API 暴露方式必须在契约中确定并测试。

### 7.2 PowerShell 子集

首版只需支持 GNMarket 所需的可证明子集：

- `$ErrorActionPreference = 'Stop'` 等固定安全偏好赋值，可分类但不用于任意求值。
- 字面量 `docker compose` 命令，允许固定 `-f|--file`、`up`、`--build`、`-d|--detach`、`ps`，以及经设计明确允许的无副作用查询参数。
- `Write-Host`/空输出等纯展示语句可忽略。
- Compose 文件路径必须为工作区相对字面量，并通过 canonical 边界检查。

变量展开、函数、脚本块、管道、重定向、命令调用运算符、dot sourcing、模块导入、下载、注册表、进程启动、动态参数、环境读取和任意非 Docker 命令均阻断。不得为了兼容 GNMarket 而建立通用 PowerShell 解释器。

### 7.3 Compose 静态探测

- 使用 `yaml.v3` 严格解析工作区 Compose 文件，拒绝重复 key、多文档、未知或超出支持范围的构建字段；分析阶段不得调用 Docker。
- 提取受管服务、显式依赖、build 服务、context/Dockerfile、端口默认值、healthcheck 缺失/存在状态和固定镜像证据。
- 变量只支持 Compose 字面量默认值中设计允许的确定子集，例如 `${HTTPS_PORT:-8443}`；不得读取宿主 `.env` 或进程环境来完成分析。
- 无法确认宿主端口、容器目标或依赖时生成明确 blocker。服务缺少 healthcheck 时生成需要用户确认的 `running` readiness 建议；未经确认不得 apply。
- BAT、PS1、Compose 文件及影响生成结果的受支持直接引用都进入确定性来源图 digest；apply 前任何变化都返回 `WORKSPACE_IMPORT_SOURCE_CHANGED`。

## 8. Compose build 生命周期

- 推荐把 build 与 up 分为两个固定调用：先 `docker compose ... build <build-services...>`，成功后再以 `--no-build` 执行既有 `up`，避免隐式构建并使 Operation 步骤、错误和恢复边界清晰。
- build 服务集合必须来自已验证 Manifest 与 Compose config 的交集，排序稳定，不接受请求端覆盖。
- build 使用独立 timeout，纳入 Operation 取消；参数数组、working directory 和环境均由受控 resolver 生成。
- build 成功后、up 前崩溃可以安全重跑受控 build；不得把缓存命中等同于未执行，也不得伪造事务回滚。
- build 只属于用户显式提交的系统级 `start` 和完整 `restart` Operation。任何 `service-restart`，无论由用户还是 liveness/退出恢复自动创建，都固定跳过 build 并使用 `--no-build`；需要重新构建时必须执行显式系统级 Restart。
- build 失败时不得启动容器；up 失败时保留既有 Compose 项目标识、观察和停止语义，允许用户显式 Stop 清理运行现场。
- 恢复必须先观察严格项目身份，再决定重跑 build 或收敛运行状态，避免控制面重启后重复创建项目或把无关容器认领为本实例。
- 普通 Stop 仍只停止受管容器，不执行 `down -v`，不删除 volume、镜像、BuildKit cache 或业务文件。镜像/cache 生命周期超出本专项时必须明确记录残留风险。
- Compose readiness 按 Manifest 中每个服务的闭合要求观察：`healthy` 同时要求 running/healthy，`running` 只要求严格身份下处于 running；规则变化必须改变定义摘要并触发既有 Manifest 变更保护。
- 逐服务 requirement 由 Compose Lifecycle 的 `CheckCompose`/identity 消费，Health Engine 继续负责调度、阈值、结果持久化和错误传播；必须核对 `health_results` 既有语义是否可直接兼容，不能只修改 Manifest 或 UI。
- 新错误至少评审 `COMPOSE_BUILD_CONFIG_INVALID`、`COMPOSE_BUILD_FAILED` 和 `COMPOSE_BUILD_TIMEOUT`；capability 未启用继续使用 `FEATURE_NOT_ENABLED`。最终集合必须同步错误码注册表、API 映射和测试。

## 9. API 与 Web 要求

- Import Draft 服务 DTO 必须能区分 `process|compose`，Compose 条目只暴露页面所需的相对文件、受管服务、build policy、逐服务 readiness requirement、端口和 readiness 摘要，不返回内部 Manifest 或完整配置。
- OpenAPI 先定义 DTO、枚举、findings 和 capability detail；前端类型从契约同步，不使用 `any` 或字符串猜测 driver。
- 导入确认页明确显示“将构建本地镜像”、构建服务、Compose 文件、可能发生镜像拉取/网络访问，以及端口将被限制为 loopback。
- 对无 healthcheck 的服务单独展示 `running` readiness 确认及其较弱语义，不用笼统警告或颜色暗示代替。
- build policy 使用闭合的选择控件或只读确认，不提供任意 flags、Dockerfile、context、命令或 YAML 编辑器。
- 不支持的脚本应展示稳定错误码、相对证据路径和行号；安全消息不得包含完整命令、工作区外路径或脚本正文。
- 按钮可用性由 `phase2.compose`、`phase2.compose-build`、candidate blockers 和活动 Operation 共同决定。
- capability 的公共发布、Manifest validator 映射与 Web 判断必须有一致性测试；实现前核对 `cmd/stackpilot/server.go` 的硬编码发布列表及所有其他枚举位置，避免新增第二份不一致事实源。
- 应用后沿用现有持久 workspace import Operation；构建发生在后续显式 system start Operation，不在导入分析或 manifest 发布阶段偷跑。

## 10. 测试与 Gate

### 10.1 Parser 与生成器

- BAT fixture 覆盖 GNMarket 风格的前置检查、括号、错误码分支、固定 PowerShell `-File` 引用及恶意变体。
- BAT fixture 还需覆盖最大合法嵌套、超深嵌套、过多条件块、`if/else` 配对、孤立/缺失括号和条件分支内服务命令拒绝。
- PS1 fixture 覆盖允许的 Compose up/build/ps、相对路径、空格/中文路径，以及变量、管道、下载、`-Command`、越界脚本和非 Docker 命令拒绝。
- Compose fixture 覆盖本地 context、默认 Dockerfile、多个 build 服务、镜像服务、依赖、healthcheck 存在/缺失、逐服务 `healthy|running`、`${VAR:-default}` 端口及所有禁止 build 字段。
- 来源图 digest 覆盖 BAT、PS1、Compose 任一文件变化、循环、大小/数量上限、编码和 junction/符号链接逃逸。
- 生成的 Manifest 必须经过真实 Loader、Schema、Validator；重复生成和重载后 digest 确定一致。

### 10.2 Driver、Orchestrator 与 Windows

- 单元测试断言固定 Docker 参数数组、稳定服务排序、build/up 分步、`--no-build`、错误分类、timeout 和敏感输出不泄露。
- 真实 Windows Docker fixture 使用最小本地 Dockerfile，覆盖 build -> up -> `healthy`/`running` 混合 compose readiness -> logs -> stop。
- 覆盖构建失败、大日志、取消、超时、端口竞争、Docker daemon 冷启动、build 成功后 up 失败、控制面在 build/up 窗口重启和重复启动幂等。
- 覆盖路径含空格与中文、Dockerfile/context 越界、远程 context、SSH/secrets/args/host network 等拒绝。
- 验证结束后无遗留容器、端口和测试文件；镜像/cache 若因“不自动删除”策略保留，fixture 必须使用可识别标签并在测试专用清理协议中安全处理。

### 10.3 API、Web 与真实目标

- API 契约测试覆盖 Compose Import DTO、capability gating、findings 证据白名单、认证/Origin/CSRF/Content-Type 和 traceId。
- Web 组件/E2E 覆盖分析成功、build 确认、capability 缺失、危险脚本、来源变化、应用成功和启动 build 失败。
- 从 `E:\GNMarket` 提取去机器化、无 Secret 的最小 fixture；自动化测试不得依赖真实项目目录。
- GNMarket 的服务名、build 集合、端口和 readiness 只能来自 fixture/只读探测证据；生产代码不得硬编码 `mysql/web/job/frontend/gateway` 或任何 GNMarket 专属路径/名称。
- 真实 GNMarket Gate 分两次授权：先只读 analyze/preview，再授权写入 `.stackpilot/system.yaml` 和执行 Docker build/start。未经授权不得修改真实仓库、拉取镜像或启动容器。

## 11. 文档与契约同步

实现变更至少同步：

- 新 Compose build ADR，以及受影响的 ADR-0005/0007。
- `docs/overall-design.md`、`docs/detailed-design.md`、`docs/phased-development-plan.md`。
- `schemas/system-v1alpha1.schema.json`、Manifest 类型/validator 和示例。
- `api/openapi.yaml`、`docs/error-codes.md`、API DTO 和契约测试。
- capability 发布位置、`/version` 响应和 Web capability 判断。
- `docs/development.md` 中 Compose build 的信任边界、前置条件、残留镜像/cache 和恢复说明。
- 若新增 migration，同步新增 migration、repository/升级测试及 `docs/storage-schema.md`；若不需要 migration，在 evidence 中记录核对结论。
- 新的 progress/evidence；不得回写已有 Gate 的历史结果。

## 12. 明确不做

- 不执行 BAT 或 PowerShell，不增加通用脚本 Runner。
- 不接受 HTTP/CLI 提交 Docker executable、Compose flags、build args、Secret、SSH、context 或 Dockerfile 路径。
- 不支持远程 Git/URL context、任意 build 扩展字段或特权构建。
- 不自动修改 GNMarket 的 BAT、PS1、Compose、Dockerfile 或业务源码。
- 不自动删除 volume、镜像、BuildKit cache 或用户 Docker 资源。
- 不把真实 GNMarket Gate 替代可重复 fixture，也不在没有单独授权时写入或运行真实项目。
- 不顺带实现 Cocos、任意 PowerShell、远程 StackPilot、多用户或 RBAC。

## 13. 完成定义

只有同时满足以下条件才能声明完成：

1. 新 ADR 明确接受并限制本地 Compose build 风险，ADR-0005/0007、详细设计和实现没有冲突。
2. Manifest/Schema/capability 能显式区分普通 Compose 与受控 build；默认关闭且未启用时稳定拒绝。
3. GNMarket 风格 BAT -> PS1 -> Compose 来源图可只读分析，未知或危险语法保留精确证据且绝不执行。
4. 导入器生成经过真实 Schema/语义校验的 Compose 草案，build、服务、端口、依赖和逐服务 readiness 证据可解释；无 healthcheck 服务必须显式确认。
5. Build 与 up 使用固定参数数组和独立 Operation 边界；失败、超时、取消、重启恢复和日志脱敏均有自动化测试。
6. build 仅由显式系统 Start/Restart 触发；用户或自动 `service-restart` 均不会反复构建，相关 Operation/argv 契约测试通过。
7. 既有 process 导入、Node 导入和无 build Compose 生命周期全量回归通过。
8. OpenAPI、Schema、错误码、DTO、Web、capability、设计文档和证据同步一致。
9. Go 格式/单元/集成/vet、OpenAPI/Schema、安全、前端 type-check/build/test、Playwright 和适用 Windows Docker Gate 均通过，并报告实际命令与未执行项。
10. 自动测试不依赖真实 GNMarket；真实 Gate 仅在独立授权后执行，并证明 register -> start/build -> ready/logs -> stop 完整闭环。
11. 无 Secret、完整环境、脚本正文、敏感 Docker 输出或任意命令泄漏；无遗留进程、容器、端口和测试文件。
12. `AGENTS.md` 与 `CLAUDE.md` SHA-256 完全一致。
