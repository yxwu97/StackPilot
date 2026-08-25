# StackPilot 受控 Compose Build 与脚本导入增强开发计划

> 状态：自动化开发与仓库内 Gate 已完成；GNMarket 真实授权 Gate 待执行
> 日期：2026-08-22
> 来源 Prompt：`prompt/prompt-20260822-01-controlled-compose-build-and-import.md`
> 阶段归属：Phase 2 Compose 能力与工作区导入专项的安全扩展
> 首个真实目标：`E:\GNMarket`（真实写入与运行需要独立授权）

## 1. 目标结果

将当前只能识别 Maven/npm/Java/Node 的 BAT 导入链扩展为严格受限、只读的 BAT -> PS1 -> Compose 来源分析，并为使用本地 Dockerfile 的已登记工作区提供默认关闭、显式 opt-in、可取消和可恢复的 Compose build 生命周期。

最终流程：

```text
选择 BAT
  -> 有界分析 BAT 控制流与固定 powershell -File 引用
  -> 有界分析 PS1 中固定 docker compose 调用
  -> 严格解析 Compose 与本地 build context
  -> 展示 Compose/build/端口/依赖/health 证据
  -> 用户确认并通过持久 Import Operation 发布 Manifest
  -> Start Operation: preflight -> build -> up --no-build -> readiness -> logs
  -> Stop Operation: 身份核验 -> stop，不删除 volume/image/cache
```

## 2. 固定决策

1. BAT/PowerShell 永不执行；只增加受限引用分析，不增加 shell Runner。
2. Compose build 使用独立 capability，建议名 `phase2.compose-build`；Design Gate 固定最终名称。
3. Manifest 显式声明闭合 build policy，缺省 `never`；GNMarket 需要的行为为用户确认后的 `always`。
4. Compose readiness 默认 `healthy`；只有 Compose 文件确实没有 healthcheck 时，才允许用户按服务显式确认 `running`，已有 healthcheck 不自动降级。
5. build 与 up 分步执行；build 成功后 up 固定携带 `--no-build`，避免运行路径隐式构建。
6. build 仅在用户显式系统级 `start` 和完整 `restart` Operation 中执行。任何 `service-restart`，无论由用户还是 liveness/退出恢复自动创建，均固定跳过 build 并以 `--no-build` 重启现有镜像。
7. 首版只允许工作区内本地 context 和默认/工作区内 Dockerfile；远程 context、args、Secret、SSH、host network、entitlements 及高级 build 字段全部拒绝。
8. Importer 只生成结构化 Compose 草案；apply 不构建，首次显式 Start 才产生 Docker 副作用。
9. 现有无 build Compose、process/Node 导入和运行行为必须保持兼容。
10. 普通 Stop 不删除 volume、镜像或 BuildKit cache；残留与测试专用清理策略必须文档化。
11. 真实 GNMarket 默认只读。写 Manifest、拉取/构建镜像和启动容器分别需要明确授权。

## 3. 实现前阻断项

以下事项必须在 CB-00/CB-01 关闭后才能修改生产 build 路径：

1. 明确信任模型：为何用户登记的本地 Dockerfile 可执行，以及该权限与固定 Compose `command` 的风险差异。
2. 固定 Manifest 字段、默认值、capability 名称和 validator gate，避免实现后再改机器契约。
3. 固定允许/禁止的 Compose build 字段集合与 canonical path 规则。
4. 固定 build/up Operation 步骤、timeout、取消、失败、重试和控制面崩溃窗口语义。
5. 固定 Compose project/image 命名与重复 Start 的缓存、镜像残留和身份摘要行为。
6. 确认现有 OperationStep/ResolvedSpec/Compose identity 能否承载 build 信息；确需存储变化时先设计 migration，否则不新增数据库结构。
7. 固定 Parser finding 与顶层错误的优先级，避免危险诊断再次被通用 `ErrScriptUnsupported` 覆盖。
8. 明确基础 Compose 的非 loopback ports 如何被 override 安全替换，并用真实 Compose config Gate 证明不会合并出重复或公网绑定。
9. 固定逐服务 Compose readiness requirement 的 Schema、默认值、确认语义和恢复兼容，解决 GNMarket `job`、`gateway` 无 healthcheck 的真实约束。
10. 固定 BAT 条件语法的数值边界与结构规则：最大嵌套深度、条件块/逻辑语句上限、括号和 `if/else` 配对、动态分支内服务命令处置及稳定超限错误。
11. 固定显式系统 Start/Restart 与用户/自动 `service-restart` 的 build 差异，禁止故障循环反复构建。

## 4. 工作包总览

| ID | 工作包 | 依赖 | 主要交付物 | Gate |
| --- | --- | --- | --- | --- |
| CB-00 | 基线、威胁模型与 ADR | 无 | 调用链盘点、新 ADR、ADR-0005/0007 兼容裁决 | Design Gate |
| CB-01 | 契约与 capability | CB-00 | Manifest Schema、OpenAPI、错误码、DTO、capability | Contract Gate |
| CB-02 | BAT/PS1 来源图分析 | CB-00/01 | 有界 Parser、证据、digest、恶意 fixture | Script Analysis Gate |
| CB-03 | Compose 静态探测与草案生成 | CB-01/02 | 严格 Compose 探测器、Compose Candidate、Manifest 生成 | Import Gate |
| CB-04 | Compose build 预检与生命周期 | CB-00/01 | build config 校验、固定 CLI、timeout/cancel/log | Driver Gate |
| CB-05 | Orchestrator、身份与恢复 | CB-04 | build/up 步骤、状态、重试、恢复、停止兼容 | Recovery Gate |
| CB-06 | API 与 Web 导入体验 | CB-01/03/05 | DTO 映射、确认 UI、capability 与错误呈现 | UX Gate |
| CB-07 | 自动化回归与 Windows Docker Gate | CB-02..06 | 最小 GN fixture、真实 Docker fixture、回归报告 | Integration Gate |
| CB-08 | 文档、发布与 GNMarket 真实 Gate | CB-00..07 | 设计/用户文档、evidence、授权后真实验收 | Exit Gate |

## 5. CB-00：基线、威胁模型与 ADR

### 任务

- 盘点 `internal/importer`、Manifest Loader/Validator、workspace import apply、resolved spec、Compose preflight/lifecycle/recovery/logging 和 Web 导入向导的真实调用链。
- 记录当前行为基线：普通 Compose、固定 base command、build 拒绝、Docker Desktop 拉起、项目身份、override、日志、停止和恢复。
- 新建 ADR，明确本地 Dockerfile 是已登记工作区的额外可执行信任面，必须通过 Manifest opt-in 和独立 capability。
- 裁决 ADR-0005 的“build 始终拒绝”和 ADR-0007 的“PowerShell 嵌套解释器危险”条款：保留不执行结论，仅对受限只读引用和显式 build 做窄范围替代。
- 形成威胁表：恶意 Dockerfile、remote context、symlink/junction 逃逸、Secret/SSH/args、build 网络、缓存污染、磁盘占用、取消后 daemon 继续工作、日志泄漏、项目身份混淆和端口公网暴露。
- 明确 build/up 分步、幂等和崩溃恢复：build cache 是外部副作用，不宣称事务回滚；只有 build 成功才进入 up。
- 明确 Operation 类型边界：显式系统 Start/Restart 按 build policy 构建；用户或自动 `service-restart` 一律跳过 build。自动重启重试与 backoff 不得因 build cache 命中改变计数语义。
- 为 BAT/PS1 分析固定数值与结构上限，包括条件嵌套深度、条件块/逻辑语句数、括号/`if`/`else` 配对和动态分支处置；Parser 不做分支组合展开。
- 决定项目名生成导致的镜像命名与残留影响。若维持实例级 project name，说明重复实例的镜像/cache 行为；若调整命名，必须评估身份、并发和恢复兼容性。
- 明确 buildx/BuildKit cache 清理归属：生产不清理；测试禁止全局 prune，仅删除严格记录的精确 fixture 资源，无法安全归属的 cache 保留并报告。
- 核对是否需要 migration。优先复用 Manifest snapshot、resolved spec 和 OperationStep；只有存在不可替代的持久不变量时才新增单调 migration。

### Gate

- ADR 状态 Accepted，固定 capability、Schema 字段、build 字段白名单、CLI 序列、超时/取消/恢复和残留策略。
- ADR-0005/0007、总体/详细设计不存在互相冲突的有效条款。
- 没有通过执行 BAT/PS1、HTTP 任意参数或放宽全部 Compose build 配置来实现的路径。

## 6. CB-01：契约与 capability

### Manifest/Schema

- 为 Compose service 增加闭合 build policy；默认 `never`，首版支持 `never|always` 或 ADR 选定的等价闭合模型。
- 更新 Go 类型、JSON Schema、normalized JSON、definition digest、示例和 semantic validator。
- build policy 只允许 `driver: compose`、`mode: daemon`、`readiness.type: compose`，并要求普通 Compose 与 build 两层 capability。
- 增加逐服务闭合 readiness requirement，默认 `healthy`、首版仅允许 `healthy|running`；`running` 只允许用于无 healthcheck 的服务并需要显式确认。
- 禁止 process 字段与 Compose/build 字段混用；错误 location/field 保持稳定。

### OpenAPI/API

- 扩展 Import Candidate Service DTO，显式包含 driver；Compose 使用专用摘要结构，不复用 runner/arguments 空字符串冒充。
- 定义 compose file、受管 services、build policy/build services、逐服务 readiness requirement、port mapping 和 evidence 的最小字段。
- 保持 DTO 脱敏，不返回 Dockerfile 内容、完整 Compose config、环境或内部绝对路径。
- 新增/确认 `COMPOSE_BUILD_CONFIG_INVALID`、`COMPOSE_BUILD_FAILED`、`COMPOSE_BUILD_TIMEOUT`；capability gate 复用 `FEATURE_NOT_ENABLED` 和允许的 capability detail。
- `/version` 只有生产实现和 Gate 全部完成后才发布 `phase2.compose-build`，不提前广告。
- 盘点 `cmd/stackpilot/server.go` 的公共 capability 发布列表、Manifest validator capability 映射及 Web 判断位置；建立单一公共注册表或一致性测试，避免新增第二份枚举事实源。

### Gate

- Schema 正反例、OpenAPI 校验、错误码注册和契约测试先通过。
- capability 关闭时普通 Compose 仍可注册/启动，含 build policy 的 Manifest 稳定被拒绝。
- 前后端 DTO 不使用 `any`，不暴露 raw command/flags。

## 7. CB-02：BAT/PS1 来源图分析

### BAT

- 将 BAT 行分类从简单前缀过滤提升为足以表达当前受支持子集的有界状态机/Parser，不执行控制流。
- 支持 GNMarket 所需 `if errorlevel` 括号块、`where`、Compose 版本检查、`%ERRORLEVEL%` 保存比较、`pause` 和 `exit /b` 分类。
- 实现 ADR 固定的嵌套深度、条件块/逻辑语句上限及括号/`if`/`else` 配对；只做一次结构遍历，不进行分支组合展开。
- 条件分支中出现服务启动或动态解释器调用时首版阻断；诊断/退出分支只有完全落在白名单时才能忽略。
- 仅识别固定 `powershell.exe|pwsh.exe`、安全开关和单个 `-File` 工作区相对字面量引用。
- `-Command`、`-EncodedCommand`、stdin、额外动态参数、越界路径和未知解释器稳定阻断。
- 修复 finding 丢失：危险语法优先于通用不支持；多个诊断排序确定，错误响应或草案 evidence 与 OpenAPI 一致。

### PS1

- 新建职责独立的受限 PS1 分析器，仅支持固定偏好赋值、`Write-Host` 和字面量 `docker compose` 命令。
- 识别 `-f|--file`、`up --build -d`、`ps`；不透传任意 flags。
- 变量、插值表达式、函数、脚本块、管道、重定向、调用运算符、dot source、下载、注册表、Start-Process 和非 Docker 命令全部阻断。
- 对所有引用执行 existing canonical path、普通文件、工作区边界、大小、编码、文件数、总字节数和循环限制。

### 来源 digest

- 将 BAT、受支持 PS1、Compose 及确实影响生成结果的直接引用纳入排序稳定的 source graph digest。
- Analyze/apply 间任一来源变化都返回 `WORKSPACE_IMPORT_SOURCE_CHANGED`，且不发布 Manifest。

### Gate

- GNMarket 最小脚本 fixture 产生 Compose 调用事实，不产生虚假 process 命令。
- 恶意 fixture 全部拒绝且没有进程、网络或工作区外文件访问。
- 最大合法嵌套、超限、孤立/缺失括号、`if/else` 配对和分支内启动命令 fixture 具有确定结果与稳定错误码。
- 原 Maven/npm/Java/Node、嵌套 BAT、循环、编码和大小测试回归通过。

## 8. CB-03：Compose 静态探测与草案生成

### 严格探测

- 使用 `yaml.v3` 节点级检查重复 key、多文档、alias/merge 边界和已允许字段，不调用 Docker 或读取宿主 `.env`。
- 提取服务名、image/build、depends_on、healthcheck 存在/缺失、ports 和本地 build context/Dockerfile。
- 只解析允许的 Compose 默认值子集；例如从 `${HTTPS_PORT:-8443}:8443` 得到有证据的首选端口 8443。
- canonicalize build context 和 Dockerfile；拒绝 URL/Git/绝对越界路径、reparse escape 和非普通文件。
- 明确拒绝 additional_contexts、args、target、secrets、ssh、privileged、network、entitlements、cache、extra_hosts、platform、provenance、sbom 等首版未支持字段。

### Candidate/Manifest

- 为同一 Compose 项目生成一个结构化 compose service group，受管服务集合按依赖闭包验证并稳定排序。
- `compose.services` 必须包含全部允许启动的显式依赖，避免 `--no-deps` 下隐式缺失。
- 将宿主端口映射到逻辑 port，并由 StackPilot override 强制 loopback；基础 Compose 的非 loopback 声明形成可见安全提示但不得成为运行时公网绑定。
- build policy 由明确 `up --build` 证据建议，用户确认后写入 Manifest；没有证据不得推断启用。
- readiness 使用 `compose`。已有 healthcheck 的服务固定建议 `healthy`；缺失 healthcheck 的服务必须让用户显式确认 `running`，未经确认不得 apply，不能伪造 healthy。
- Candidate 通过真实 Loader/Schema/Validator 后才返回 YAML 预览与 digest。

### GNMarket fixture 预期

- 识别 `compose.yaml`、`mysql/web/job/frontend/gateway` 五个服务。
- 识别 `web/job/frontend` 为 build 服务，`mysql/gateway` 为 image 服务。
- 识别 gateway 宿主首选端口 8443、目标端口 8443，并在运行 Manifest 中保持 loopback。
- 识别完整 depends_on 闭包和已有 healthcheck；`job`、`gateway` 没有 healthcheck，候选必须分别展示 `running` readiness 的待确认项。
- 用户确认后 Manifest 对有 healthcheck 的 `mysql/web/frontend` 使用 `healthy`，对无 healthcheck 的 `job/gateway` 使用 `running`；不得把全局 readiness 一次性放宽。

### Gate

- 确定性生成、来源证据、Schema/semantic 正反例全部通过。
- GNMarket fixture 要么生成可应用候选，要么只剩被 Design Gate 明确接受的真实 blocker；不允许回落到通用语法错误。
- 上述服务名、build 集合和端口只是 fixture/真实探测预期；生产代码不得硬编码 GNMarket 服务名、路径或系统特例。

## 9. CB-04：Compose build 预检与生命周期

### Preflight

- 在现有 Docker/Compose/daemon/config 检查中解析每个受管服务的 build 对象并执行白名单验证。
- 返回安全的结构化结果：版本、排序服务、build 服务和摘要；完整 config/stderr 只在内存中处理。
- capability/build policy/build 定义三者必须一致；任一不一致返回稳定类型错误。
- Compose config 的 healthcheck 状态与 Manifest 逐服务 readiness requirement 必须一致；已有 healthcheck 不接受导入器生成的 `running` 降级。
- 对 context/Dockerfile 在分析后、Start 前再次 canonicalize，防止注册后文件或 junction 替换。

### Build 调用

- 固定 build 调用形态：`docker compose --project-name <project> --file <base> --file <override> build <sorted-build-services...>`，不隐式增加 `--pull` 或其他 flags。
- 固定 up 调用形态：`docker compose --project-name <project> --file <base> --file <override> up -d --wait --no-deps --no-build --wait-timeout <seconds> <sorted-services...>`；参数顺序与单元测试断言一致。
- 不允许请求端参数、宿主 shell、PowerShell 或 cmd；working directory 固定为 Compose 文件目录。
- build 独立 deadline、stdout/stderr 上限、日志流归属、取消 owner 和错误映射。
- build 成功后 up 显式使用 `--no-build`；build 失败、timeout 或取消绝不进入 up。
- 验证 Compose override 不注入 build、command、entrypoint 或其他执行字段。

### Gate

- 单元测试覆盖精确 argv、排序、timeout、cancel、错误链和脱敏。
- 禁止 build 字段矩阵全部拒绝；既有 fixed base command 仍允许。
- 非 Windows stub/capability 行为与当前平台范围一致，不意外宣称跨平台正式支持。

## 10. CB-05：Orchestrator、身份与恢复

### Operation 步骤

- 在 Compose Start 的持久步骤中明确 `compose-preflight`、`compose-build`、`compose-up`、`compose-readiness` 和日志接管边界；具体 key 与现有步骤命名一致。
- build policy 为 `never` 时，build 步骤应确定性 skipped 或不生成，由 ADR 固定并测试。
- build policy 为 `always` 也只在显式系统 Start/Restart 生成 build 步骤；`service-restart` 的步骤集合不得包含 build，且 Compose Start 固定 `--no-build`。
- build timeout/failed/cancelled 映射到 Operation 和 ServiceInstance 合法状态，不由调用方直接写终态。
- 取消 build CLI 后复核 context，禁止随后进入 up；记录 daemon cache 可能残留但不暴露敏感详情。

### 身份与恢复

- Compose identity/definition digest 纳入 build policy、build 服务集合、逐服务 readiness requirement、base/override 摘要。
- 控制面在 build 前、build 中、build 后/up 前、up 后/身份提交前崩溃均有恢复测试。
- 恢复先按严格标签观察现有项目；不存在容器且 Operation 未完成时可以重跑受控 build，已存在匹配项目时不得重复创建。
- up 失败后保持可诊断现场，显式 Stop 可按既有身份发现/停止规则收口。
- Stop 不触发 rebuild，不删除 volume/image/cache；Restart 的 build 行为由 Manifest policy 决定并保持确定。
- readiness 观察按服务执行：`healthy` 要求 running/healthy，`running` 要求严格身份下容器处于 running；缺失、退出、重启中或观察失败均不 Ready。
- 扩展 Compose Lifecycle `CheckCompose`/identity 以消费逐服务 requirement；核对 Health Checker、Readiness/Liveness Engine、阈值、错误传播和 `health_results` 持久结果语义。若结果表无需变更，增加兼容测试并记录结论；需要变更则进入 migration Gate。
- 覆盖 liveness/退出恢复创建内部 `service-restart` 的真实调用链，断言其不会调用 build；同时覆盖用户显式 service restart 与系统 Restart 的差异。

### Gate

- 状态机、取消、恢复、幂等、日志 owner 和资源清理测试通过。
- build 不引入进程内布尔锁、无界 goroutine/输出或绕过 context 的后台工作。
- 如新增 migration，空库、历史升级、重复启动、checksum 异常和恢复测试全部通过；无 migration 时记录核对结论。

## 11. CB-06：API 与 Web 导入体验

### API

- Handler 只做认证、输入校验、DTO/错误映射；Parser、Compose 探测和生成规则留在 importer/use case。
- 导入 DTO 返回 compose driver、相对文件、受管服务、build policy/build services、端口、逐服务 readiness requirement、capability 和 evidence。
- findings 只暴露允许的相对路径、行号/字段；未知内部错误只返回安全消息和 traceId。
- apply 继续走持久 workspace import Operation，并在 apply 前复核完整 source graph digest。

### Web

- 导入确认页按 driver 显示 process 或 Compose 摘要，不用空 runner/workingDirectory 欺骗现有表格。
- 对 build 候选显示明确确认：将执行本地 Dockerfile、可能拉取镜像/访问网络、生成 daemon cache，并列出构建服务。
- 对 `job/gateway` 等无 healthcheck 服务分别显示 `running` 确认和语义，不允许一次全局确认掩盖具体服务。
- build policy 使用闭合控件；不提供 flags、args、context、Dockerfile 或 YAML 编辑。
- capability 缺失、危险 PS1、Compose 配置禁止项、来源变化和 build runtime 失败使用稳定错误文案与 traceId。
- 长服务名/路径/错误在桌面和移动视口不溢出；状态不只靠颜色，按钮与对话框具备 tooltip、焦点和提交中状态。

### Gate

- OpenAPI 契约、API 安全、组件测试、type-check 和生产构建通过。
- Playwright 覆盖 GNMarket fixture 分析、确认、apply、start build 进度、失败呈现和 stop。

## 12. CB-07：自动化回归与 Windows Docker Gate

### Fixture

- 从已只读观察到的 GNMarket 结构制作最小仓库内 fixture，不复制业务源码、Secret、真实数据或机器特定路径。
- Docker fixture 使用本地、无外部服务依赖的最小镜像；需要拉取基础镜像时固定版本并在 Gate 记录网络前置条件。
- 测试目录、Compose project、镜像标签和端口均唯一且可验证，清理前解析 canonical 目标和严格标签。

### 场景

- build -> up -> `healthy`/`running` 混合 readiness -> logs -> stop 成功链。
- Dockerfile 失败、build timeout、大日志、用户取消、daemon 不可用/冷启动、端口竞争。
- build 成功后 up 失败、控制面崩溃窗口、重复 Start/Restart、Stop 不触发 build。
- context/Dockerfile 空格和中文路径、junction/symlink 越界、远程 context 和禁止字段矩阵。
- 普通 Compose AIWS/PMS Gate、process/Node workspace import、OpenAPI/Schema、安全和浏览器主流程回归。
- 显式 system Start/Restart 会按 policy build；用户 service restart、liveness 自动重启和退出恢复自动重启均不会 build。

### Gate

- `go test ./...`、`go vet ./...`、仓库静态检查、Schema/OpenAPI 检查、前端测试/type-check/build 和 Playwright 全部通过。
- Windows Docker 集成命令、Docker/Compose 版本、结果和跳过项写入 evidence。
- 无遗留进程、容器、网络、端口和测试文件；测试镜像/cache 按 ADR 的测试专用安全协议处理。

## 13. CB-08：文档、发布与 GNMarket 真实 Gate

### 文档同步

- 新 ADR、ADR-0005/0007、overall/detailed/phased design。
- Manifest Schema/示例、OpenAPI、错误码、capability、开发与用户说明。
- progress/evidence 记录新增专项，不修改既有 Phase 2 和 workspace import Gate 的历史事实。
- 说明 build 信任边界、网络/镜像拉取、timeout/cancel、残留镜像/cache 和排障错误码。
- 若 CB-00/CB-05 决定新增 migration，同步 migration、repository、升级测试与 `docs/storage-schema.md`；若不新增，在 evidence 中记录 schema 核对结论。

### 发布 Gate

- 只有自动化 Gate 完成后才在 server `/version` 发布 `phase2.compose-build`。
- 验证旧 Manifest normalized digest/兼容加载策略；若 Schema 新字段改变规范化输出，明确版本兼容与快照影响。
- 校验源码、文档和测试产物不包含 Secret、真实密码、脚本正文、完整 Compose config 或敏感 Docker 日志。
- 校验 `AGENTS.md` 与 `CLAUDE.md` SHA-256 完全一致。

### GNMarket 真实 Gate

1. 获得只读授权后，对 `E:\GNMarket` 执行 probe/analyze，核对 BAT -> PS1 -> Compose 证据、五个服务、三个 build 服务、`job/gateway` 的 `running` readiness 待确认项和 8443 端口；不得写文件或调用 Docker build/up。
2. 展示规范化 Manifest 预览和所有 blocker，逐项确认无 healthcheck 服务的 `running` 语义；任何其他真实契约不满足时先形成受控设计或项目改造建议，不绕过 validator。
3. 获得写入授权后，通过正常 Import Operation 原子生成 `.stackpilot/system.yaml` 并注册；不得手工写文件冒充 apply Gate。
4. 获得 Docker 副作用授权后，执行 start -> build -> ready -> logs -> stop，核对完整项目身份、loopback 8443 和所有受管容器停止。
5. 不删除 GNMarket volume、镜像或 cache；列出实际产生的可识别资源和恢复/清理建议。

## 14. 验证命令基线

执行时必须以仓库实际脚本为准，并记录真实输出。预期至少覆盖：

```powershell
gofmt -w <本次修改的 Go 文件>
go test ./...
go vet ./...
npm run test:web
npm run type-check
npm run build
```

还需执行仓库已有 OpenAPI、JSON Schema、安全、Compose Driver、workspace import、Windows Docker 和 Playwright Gate。不得编造不存在的命令；工具或环境不可用时必须说明未验证范围和风险。

## 15. 退出条件

- Prompt 第 13 节完成定义全部满足。
- 所有工作包自身 Gate 通过，未通过项不得以 UI 演示或 fixture 之外的偶然成功替代。
- 现有 Compose/Importer 行为无回归，build capability 默认关闭且边界可审计。
- GNMarket fixture 自动 Gate 通过；真实 GNMarket Gate 仅在相应授权后记录实际结果。
- 无遗留进程、容器、端口、测试文件或敏感产物。

## 16. 建议执行顺序

1. CB-00 -> CB-01，先关闭设计和机器契约。
2. CB-02 与 CB-04 可在契约固定后分别实现 Parser 与 Driver 单元边界。
3. CB-02 -> CB-03，完成确定性 Compose 草案。
4. CB-04 -> CB-05，完成运行、状态和恢复闭环。
5. CB-03/05 -> CB-06，接入 API 与 Web。
6. CB-07 执行全量自动 Gate。
7. CB-08 同步文档和发布；真实 GNMarket 分授权执行。

任何实现若需要扩大远程 context、build Secret/SSH、任意 flags、PowerShell 语法或清理 Docker 资源，必须停止当前工作包并另行更新 ADR/计划，不得借兼容问题隐式扩权。
