# StackPilot 详细设计方案

> 状态：评审修订稿
> 日期：2026-08-17
> 上游文档：[总体设计方案](./overall-design.md)
> 交互基线：[Web 交互原型](./stackpilot-prototype.html)
> 首发范围：Phase 0、Phase 1（Windows-first）；兼顾 Phase 2 扩展边界

## 1. 文档目的

本文把总体设计中的架构决策细化为可编码、可测试、可验收的实现契约，供后端、前端、CLI、测试和 PMS/AIWS/BTC 接入共同使用。

本文重点回答以下问题：

1. 各模块负责什么，允许依赖什么，不允许绕过哪些边界。
2. 系统清单如何解析、校验、快照和展开为运行时规格。
3. 启动、停止、取消、重启、恢复核对如何落到事务和状态迁移。
4. 进程、端口、健康、日志、事件和 SQLite 如何保持一致。
5. REST、SSE、CLI 和 Web 使用什么稳定契约。
6. Phase 1 如何形成 BTC 的最小闭环，Phase 2 如何在不推翻核心设计的前提下扩展。

本文不重新讨论已在总体设计中确认的产品目标。若两份文档冲突，以总体设计中的范围和原则为准，以本文中的字段、时序和错误语义为实现基线；冲突必须先通过设计评审消除，不由实现自行选择。

## 2. 范围与关键约束

### 2.1 Phase 1 必须实现

- 单用户、本机、回环地址访问。
- 工作区注册、清单刷新和 Schema 校验。
- Windows 原生进程启动、进程树监管、优雅停止和强制终止。
- Process Driver 的 `daemon` 模式、统一 RunnerResolver 抽象，以及 Maven、npm、Java Runner 的可用解析实现；Python venv 只保留接口和 capability gate。
- 服务 DAG、HTTP/TCP/process readiness、系统启停和服务重启。
- 启动前整套端口规划、粘性复用、请求覆盖和租约。
- Operation、OperationStep、取消、幂等和实时事件。
- stdout/stderr 持久日志、历史读取和 SSE 跟随。
- StackPilot 重启后的实例核验与重新接管。
- BTC Backend/Web 的真实接入。
- 原型中 Phase 1 对应的系统总览、系统详情、服务详情、操作中心和基础配置页面。

### 2.2 Phase 1 明确不实现

- 远程监听、用户体系、RBAC、多 Agent。
- 任意命令执行和 Web 编辑启动命令。
- 自动执行诊断建议。
- 资源趋势、全文索引、Prometheus/OpenTelemetry 导出。
- Linux/macOS 的完整进程监管与正式运行承诺。
- 删除 Compose 数据卷或自动修改业务源码。

### 2.3 为 Phase 2 预留但不提前实现

- `oneshot` 进程模式与 `completed` 依赖条件。
- Docker Compose Driver。
- Python venv Runner 的生产实现和 Secret Provider。
- liveness、自动重启和事故规则诊断。
- PMS、AIWS 接入。

预留的含义是领域模型、接口枚举和数据库迁移可兼容，不是 Phase 1 创建空包或不可验证的占位实现。

## 3. 设计追踪关系

| 总体设计能力 | 详细设计落点 | Phase 1 验证 |
|---|---|---|
| 声明式系统清单 | 第 7 章 | Schema、语义校验、BTC 清单测试 |
| 服务 DAG 与 readiness | 第 8、10 章 | 慢启动、超时、循环依赖测试 |
| 原生进程监管 | 第 9 章 | Windows Job Object 进程树测试 |
| 端口规划与替换 | 第 11 章 | 双系统争用、模板传播测试 |
| Operation 与 SSE | 第 8、14 章 | 幂等、取消、断线续传测试 |
| 日志 | 第 13 章 | stdout/stderr、滚动、重连测试 |
| SQLite | 第 15 章 | migration、事务、崩溃恢复测试 |
| Web 控制台 | 第 17 章 | BTC 启停端到端测试 |
| 安全与 Secret | 第 18 章 | 命令边界、脱敏、令牌测试 |
| 重启恢复 | 第 16 章 | 服务运行中重启 StackPilot 测试 |

## 4. 进程内架构与模块边界

### 4.1 组件关系

```mermaid
flowchart TB
    HTTP["HTTP Server / Middleware"] --> APP["Application Service"]
    CLI["CLI HTTP Client"] --> HTTP
    WEB["Vue Web"] --> HTTP

    APP --> REG["Registry Service"]
    APP --> ORCH["Orchestrator"]
    APP --> QUERY["Query Service"]
    APP --> INCIDENT["Incident Service"]

    REG --> MANIFEST["Manifest Loader"]
    ORCH --> PORTS["Port Planner"]
    ORCH --> DRIVER["Driver Registry"]
    ORCH --> HEALTH["Health Engine"]
    ORCH --> EVENTS["Event Bus"]

    DRIVER --> PROCESS["Process Driver"]
    DRIVER --> COMPOSE["Compose Driver / Phase 2"]
    PROCESS --> PLATFORM["Platform Adapter"]

    PROCESS --> LOGS["Log Manager"]
    HEALTH --> EVENTS
    LOGS --> EVENTS

    REG --> REPO["Repositories"]
    ORCH --> REPO
    QUERY --> REPO
    INCIDENT --> REPO
    EVENTS --> REPO
    REPO --> DB["SQLite"]
```

### 4.2 包职责

| 包 | 核心职责 | 禁止事项 |
|---|---|---|
| `internal/domain` | 领域实体、值对象、状态枚举、领域错误 | 不访问数据库、文件、网络或 OS API |
| `internal/application` | 用例编排、事务边界、DTO 映射 | 不直接执行进程或拼 SQL |
| `internal/manifest` | YAML 读取、Schema/语义校验、模板编译 | 不启动服务，不保存运行状态 |
| `internal/orchestrator` | DAG、Operation、状态迁移、取消和补偿 | 不依赖 HTTP DTO，不直接调用平台 API |
| `internal/driver` | 统一 Driver 接口和具体驱动 | 不决定系统级启动顺序 |
| `internal/platform` | 进程树、信号、身份核验、系统数据目录 | 不理解 System/Service 业务语义 |
| `internal/ports` | 候选生成、占用检测、租约和运行时端口计划 | 不修改业务项目文件 |
| `internal/health` | 检查执行、阈值聚合和调度 | 不自行改变系统期望状态 |
| `internal/logs` | 捕获、脱敏、落盘、滚动和读取游标 | 不把完整日志写入 SQLite |
| `internal/events` | 领域事件发布、持久化、订阅和 SSE 恢复 | 不承载无限原始日志正文 |
| `internal/storage` | migration、repository、事务实现 | 不包含用例决策 |
| `internal/api` | HTTP 路由、中间件、请求校验和响应编码 | 不绕过 application 层调用 driver |
| `internal/security` | 本地令牌、脱敏、路径约束；Phase 2 增加 Secret Provider | 不记录 Secret 明文 |
| `web` | 展示、用户意图采集、SSE 状态同步 | 不推测服务状态，不实现启动逻辑 |

### 4.3 依赖规则

依赖方向固定为：`api -> application -> domain/ports/orchestrator interfaces`，基础设施实现通过构造函数注入。`domain` 不依赖任何内部基础设施包。

平台相关文件使用 Go build tags 隔离，例如 `process_windows.go`、`process_unix.go`。公共包的 Windows/Linux/macOS 交叉编译由 CI 强制执行，禁止通过运行时 `GOOS` 分支直接引用平台专属符号。

### 4.4 后台协程所有权

所有长生命周期协程都必须归属一个可取消的根上下文：

```text
server root context
├── reconciliation loop
├── health scheduler
├── log retention worker
├── event retention worker
└── per-operation context
    └── per-service start/stop/check tasks
```

Windows 上另有每个活动 SystemInstance 一个的内部 Supervisor 子进程。Supervisor 不属于 HTTP Server 根上下文，负责跨控制面重启持有 Job Object 和进程句柄，详见 9.3。

应用关闭顺序为：停止接收变更请求、取消未完成 Operation、停止调度新检查、等待数据库写入完成、关闭订阅者、关闭 SQLite。已监管业务进程和 Supervisor 默认不随 StackPilot Server 普通退出被终止，以便重启后接管；显式 `server --shutdown-managed` 才执行系统停止流程。

## 5. 运行目录与启动配置

### 5.1 数据目录

数据根目录记为 `DATA_DIR`，Windows 用户级默认值为 `%LOCALAPPDATA%\StackPilot`，系统服务模式默认值为 `%ProgramData%\StackPilot`。Phase 1 支持通过服务端参数 `--data-dir` 覆盖，路径在启动时转为绝对规范路径。

```text
DATA_DIR/
├── stackpilot.db
├── stackpilot.db-wal
├── config.yaml
├── auth/
│   └── token.meta
├── runtime/
│   ├── operations/<operation-id>/resolved-spec.json
│   └── instances/<instance-id>/
│       ├── identity.json
│       └── supervisor.json
├── logs/
│   └── <system-id>/<instance-id>/<service-id>/YYYY-MM-DD.NNN.ndjson
├── incidents/<incident-id>/context.json
└── stackpilot/
    └── YYYY-MM-DD.NNN.ndjson
```

`resolved-spec.json` 不包含 Secret 值，只记录 Secret 名称和版本。运行目录采用仅当前用户或服务账号可写权限。

目录按需创建；Phase 1 不创建 `incidents/`，启用 Phase 2 规则诊断后才创建。

### 5.2 服务端配置

```yaml
server:
  listenAddress: 127.0.0.1
  port: 32100
  shutdownTimeout: 15s
database:
  busyTimeout: 5s
events:
  retention: 24h
  heartbeatInterval: 15s
logs:
  segmentMaxBytes: 20971520
  totalMaxBytes: 2147483648
  retentionDays: 14
  flushInterval: 250ms
operations:
  maxConcurrentSystems: 4
  defaultStartTimeout: 10m
  defaultStopTimeout: 2m
health:
  maxConcurrentChecks: 16
security:
  allowRemote: false
```

配置优先级为：命令行参数、允许的环境变量、`config.yaml`、内置默认值。安全边界字段如 `allowRemote` 不允许由被管理系统清单覆盖。未知配置字段启动时报错，避免拼写错误被静默忽略。

## 6. 核心领域对象

### 6.1 标识与时间

- `systemId`、`serviceId`：清单内稳定、小写 kebab-case，正则为 `^[a-z][a-z0-9-]{0,62}$`。
- `workspaceId`：注册时生成 ULID，对用户展示可附别名。
- `instanceId`、`operationId`、`incidentId`：带类型前缀的 ULID，如 `op_01...`。
- `eventId`：SQLite 自增 64 位整数，用作 SSE `id`。
- 所有持久化时间为 UTC RFC 3339 纳秒字符串；前端按浏览器时区显示。
- 运行时耗时使用单调时钟计算，持久化 `started_at`、`finished_at` 和最终 `duration_ms`。

### 6.2 定义、解析规格与实例

必须区分三层对象：

1. `SystemDefinition`：清单解析后的声明，保留模板，不含机器相关解析结果。
2. `ResolvedSystemSpec`：一次启动操作的不可变快照，包含绝对路径、Runner 解析结果、端口计划、展开后的非敏感环境变量和健康检查地址。
3. `SystemInstance`/`ServiceInstance`：实际运行身份和观测状态。

定义刷新不影响已经运行的实例。运行中的实例继续引用启动时 `manifestDigest` 和 `resolvedSpecDigest`；新定义只对下一次启动或显式重启生效。界面需要显示“运行实例使用旧清单”。

### 6.3 关键类型

```go
type OperationType string // start, stop, restart, service-restart, port-plan, refresh, analyze
type OperationState string // queued, running, cancelling, succeeded, failed, cancelled

type ServiceState string // stopped, waiting_dependency, starting, waiting_ready,
                         // ready, degraded, completed, stopping, failed, unknown

type DependencyCondition string // ready, completed
type DriverKind string           // process, compose
type ProcessMode string          // daemon, oneshot

type ProcessIdentity struct {
    PID            int
    StartedAt      time.Time
    ExecutablePath string
    CommandDigest  string
    PlatformToken  string // Supervisor/Job identity; opaque outside platform package
}
```

`unknown` 只用于恢复核对期间无法确认实际身份的实例。它不是正常启动路径中的状态，也不能满足任何依赖。

### 6.4 系统聚合状态

系统状态由后端聚合并随事件发送，前端不得自行合成。优先级如下：

| 条件 | 系统状态 |
|---|---|
| 停止 Operation 正在执行 | `stopping` |
| 任一必需服务为 `failed` 或 `unknown` | `failed` |
| 启动 Operation 未结束且必需服务未全部满足条件 | `starting` |
| 必需服务已满足，但存在 `degraded` 或可选服务失败 | `degraded` |
| 所有必需服务为 `ready`/`completed` | `running` |
| 所有 daemon 服务为 `stopped` 且没有活动 Operation | `stopped` |

### 6.5 不变量

- 同一 `workspaceId` 在 MVP 同时最多存在一个非终止 SystemInstance。
- 同一工作区同时最多有一个变更型 Operation；只读查询和 SSE 不受限制。
- Operation 进入终态后不可回到非终态。
- ServiceInstance 的状态变化和对应状态事件在同一数据库事务提交。
- 只有 `ready` 可满足 daemon 的 `ready` 依赖，`degraded` 不释放尚未启动的下游。
- `completed` 实例没有活跃 PID，不执行停止信号，只参与依赖判断。
- 已占用 PortLease 必须归属于一个活动 Operation 或非终止实例。

## 7. 系统清单详细契约

### 7.1 文件发现与注册

工作区根目录必须包含 `.stackpilot/system.yaml`。注册流程：

1. 将输入路径解析为绝对路径，解析符号链接/目录联接后的真实路径。
2. 验证目录存在且当前服务账号可读。
3. 读取固定相对路径 `.stackpilot/system.yaml`，不允许请求指定其他清单文件。
4. 限制清单大小为 1 MiB，拒绝重复 YAML key、未知顶层字段和多文档 YAML。
5. 执行 JSON Schema 校验、语义校验和安全校验。
6. 对规范化内容计算 SHA-256 digest。
7. 在事务中写入 workspace、system 和 service 摘要及最后成功快照。

刷新失败时保留上一次成功快照，但将 `manifest_status` 标为 `invalid` 并记录错误。新启动被拒绝；已运行实例不被自动停止。

### 7.2 顶层结构

```yaml
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: btc
  name: BTC
  description: Bid Travel Cloud local development
spec:
  capabilities: {}
  policies: {}
  ports: {}
  services: {}
```

`apiVersion` 和 `kind` 必填且必须精确匹配。`metadata.id`、`metadata.name`、`spec.services` 必填。至少包含一个服务。

### 7.3 系统策略

```yaml
spec:
  policies:
    failFast: true
    cleanupOnFailure: false
    keepReadyServices: true
    stickyPorts: true
    startTimeout: 10m
    stopTimeout: 2m
```

| 字段 | 默认值 | 约束 |
|---|---:|---|
| `failFast` | `true` | 必需服务失败后不再调度新的下游 |
| `cleanupOnFailure` | `false` | 为 true 时仅清理本次启动创建的资源 |
| `keepReadyServices` | `true` | 与 cleanup 同时为 true 时，保留不依赖失败链的已就绪服务 |
| `stickyPorts` | `true` | 优先复用最近成功端口 |
| `startTimeout` | `10m` | 1s 至 30m，单服务超时仍由服务配置决定 |
| `stopTimeout` | `2m` | 1s 至 10m |

### 7.4 端口声明

```yaml
ports:
  backend:
    protocol: tcp
    preferred: 8081
    fallbackRange: 8200-8399
    conflictPolicy: auto
    exposure: loopback
```

端口名使用与 serviceId 相同的命名规则。端口范围为 1024 至 65535；范围起点不得大于终点，单个范围最多 2000 个候选。MVP 仅支持 `tcp`；`exposure` 仅允许 `loopback`，其他值在远程能力设计完成前拒绝。

### 7.5 服务声明

```yaml
services:
  backend:
    displayName: Backend
    required: true
    driver: process
    mode: daemon
    runner: maven
    workingDirectory: ./bidtravel-backend
    arguments: [spring-boot:run]
    environment:
      SERVER_PORT: "${ports.backend}"
    dependsOn: {}
    readiness:
      type: http
      url: "http://127.0.0.1:${ports.backend}/actuator/health"
      timeout: 180s
      interval: 2s
      successThreshold: 1
      failureThreshold: 1
    stop:
      gracefulTimeout: 15s
    restart:
      policy: never
```

| 字段 | 规则 |
|---|---|
| `required` | 默认 true；可选服务失败使系统降级但不阻止 Running 条件 |
| `driver` | Phase 1 仅 `process`；Phase 2 增加 `compose` |
| `mode` | 默认 `daemon`；`oneshot` 在 Phase 2 启用 |
| `workingDirectory` | 必须为工作区内相对目录；解析后仍须位于工作区真实路径内 |
| `arguments` | 字符串数组，逐项传递，不经过 shell 二次拼接 |
| `environment` | key 必须匹配平台安全变量名；value 支持受限模板 |
| `dependsOn` | key 必须引用同一清单服务，禁止自依赖和环 |
| `readiness` | daemon 必填；oneshot 禁止配置 readiness |
| `liveness` | Phase 2 可选；语法与 readiness 相同 |

服务依赖对象写法为：

```yaml
dependsOn:
  backend: ready
```

Phase 1 只接受 `ready`。Schema 可以声明 `completed` 枚举，但功能开关未启用时注册包含 `oneshot/completed` 的清单必须返回明确的 `FEATURE_NOT_ENABLED`，不能注册后在启动期才失败。

### 7.6 Runner 解析

Runner 接口：

```go
type RunnerResolver interface {
    Resolve(ctx context.Context, req ResolveRunnerRequest) (ResolvedCommand, error)
}

type ResolvedCommand struct {
    Executable      string
    ArgsPrefix      []string
    Version         string
    ResolutionKind  string // path, workspace, venv
    ExecutableDigest string
}
```

解析顺序：清单显式且允许的工具路径、工作区工具、服务账号 PATH。各 Runner 的启用阶段如下：

| Runner | Windows 解析 | 最低校验 | 启用阶段 |
|---|---|---|---|
| `maven` | Maven Wrapper `mvnw.cmd`，否则 `mvn.cmd` | `--version` 可执行 | Phase 1 |
| `npm` | `node_modules/.bin` 不直接作为 npm；解析 `npm.cmd` | `--version` 可执行 | Phase 1 |
| `java` | `JAVA_HOME/bin/java.exe`，否则 PATH | `-version` 可执行 | Phase 1 |
| `python-venv` | `<virtualEnvironment>/Scripts/python.exe` | 路径位于工作区，`--version` 可执行 | Phase 2A |

Phase 1 的 Schema 可以识别 `python-venv`，但注册包含该 Runner 的清单时返回 `FEATURE_NOT_ENABLED`。Phase 2A 完成生产解析、路径和版本测试后才启用，避免把未验证 Runner 延迟到启动期失败。

`.cmd` Runner 由平台适配器使用系统命令解释器执行，但 HTTP 请求和用户输入不能控制解释器参数。最终命令摘要记录可执行路径和参数散列，不记录 Secret 展开值。

### 7.7 模板语法与展开

允许的模板仅包括：

- `${ports.<name>}`
- `${workspace.root}`
- `${instance.id}`
- `${system.id}`
- `${secret.<name>}`

禁止任意环境变量读取、函数、条件、文件包含和命令替换。模板在 YAML 解析后按字符串值处理，不对 key 展开。未定义引用、循环引用和 Secret 用于非环境字段均为校验错误。

`${secret.<name>}` 是为 Phase 2 保留的受限语法。Phase 1 注册包含 Secret 引用的清单时返回 `FEATURE_NOT_ENABLED`，不创建 Secret 存储或半实现的运行路径。Phase 2 启用后，展开分两步：先形成可持久化的非敏感 `ResolvedSystemSpec`，再在进程创建前把 Secret 注入独立的内存环境数组；日志和错误信息始终使用带占位符的安全版本。

### 7.8 语义校验清单

- 服务 DAG 无环且至少有一个根节点。
- 所有依赖条件与上游 mode 匹配。
- 所有端口引用存在，每个监听端口有唯一逻辑所有者。
- readiness 地址中的端口与服务声明的端口传播关系完整。
- 工作目录、wrapper、venv、Compose 文件解析后位于工作区内。
- daemon 必须有 readiness；若只使用 `process` 检查，界面标记为弱 readiness。
- timeout、interval、阈值在安全范围内，`interval * failureThreshold <= timeout`。
- 环境变量不能覆盖 StackPilot 保留变量和 Windows 危险启动变量。
- `metadata.id` 与同一工作区已注册系统一致；变更 id 视为新系统，需重新注册。

## 8. Operation 与编排器

### 8.1 Operation 生命周期

```mermaid
stateDiagram-v2
    [*] --> Queued
    Queued --> Running: worker acquired
    Queued --> Cancelled: cancel
    Running --> Cancelling: cancel requested
    Running --> Succeeded: all steps succeeded
    Running --> Failed: terminal step failed
    Cancelling --> Cancelled: compensation finished
    Cancelling --> Failed: compensation failed
```

Operation 的取消是协作式取消。已经创建的进程是否停止由操作类型和补偿策略决定：启动操作被取消时，默认停止本次操作创建且尚未在操作前存在的服务；用户可在请求中选择保留已就绪服务。停止操作一旦开始不能通过 API 取消，避免停到一半形成更难理解的状态。

### 8.2 锁、并发与幂等

- 变更型 Operation 以 `workspaceId` 为锁键，数据库唯一条件保证跨协程互斥。
- 锁在创建 Operation 的同一事务获取，Operation 到终态时释放。
- 服务级重启与系统启停争用同一工作区锁。
- 不同工作区可并行；全局并行数受 `maxConcurrentSystems` 限制。
- `Idempotency-Key` 在调用主体、路由语义和 workspace 范围内唯一，保留 24 小时。
- 同 key、同请求摘要返回原 Operation；同 key、不同摘要返回 `409 IDEMPOTENCY_KEY_REUSED`。
- 未提供 key 时服务端仍接受请求，但 CLI/Web 必须总是提供。

### 8.3 OperationStep

步骤必须是结构化数据而非日志文本：

```text
validate-manifest
acquire-lock
preflight-runners
build-dag
plan-ports
resolve-spec
start:<service-id>
wait-ready:<service-id>
aggregate-state
```

每个步骤记录 `state`、`attempt`、`startedAt`、`finishedAt`、`durationMs`、`errorCode`、`detailRef`。步骤状态为 `pending/running/succeeded/failed/skipped/cancelled`。同一 Operation 内步骤使用递增序号，供前端稳定排序。

### 8.4 启动算法

```mermaid
sequenceDiagram
    participant C as Client
    participant A as API/Application
    participant O as Orchestrator
    participant P as Port Planner
    participant D as Process Driver
    participant H as Health Engine
    participant S as SQLite/Event Bus

    C->>A: POST start + Idempotency-Key
    A->>S: create queued operation + acquire lock
    A-->>C: 202 operationId
    O->>S: mark running
    O->>O: validate snapshot / preflight / DAG
    O->>P: plan and lease all ports
    P-->>O: immutable PortPlan
    O->>S: persist ResolvedSystemSpec metadata
    loop each ready DAG layer
        par independent services
            O->>D: start service
            D-->>O: ProcessIdentity
            O->>H: await readiness
            H-->>O: ready or terminal failure
        end
    end
    O->>S: commit instance state + terminal operation + events
```

详细步骤：

1. 从最后成功清单快照创建本次解析上下文，拒绝 invalid/stale workspace。
2. 获取工作区锁；若实例已经 Running 且请求规格相同，返回幂等成功 Operation，不重复创建进程。
3. 执行所有 Runner、目录、权限和能力预检。预检必须在任何业务进程创建前完成。
4. 构建 DAG，计算拓扑层、下游闭包和逆拓扑顺序。
5. 规划全部端口并写入 `reserved` 租约。
6. 生成 `ResolvedSystemSpec`，计算摘要并安全落盘。
7. 创建 SystemInstance，将根服务置为 `starting`，其他服务置为 `waiting_dependency`。
8. 同一拓扑层中的可运行服务通过有界并发组启动。
9. Driver 返回身份后，事务写入 ServiceInstance 和状态事件，再开始 readiness。
10. readiness 成功后把服务置为 `ready` 并激活下游；失败时按失败策略冻结未启动下游。
11. 所有必需服务 ready 后写入粘性端口历史，将 Operation 置为 `succeeded`。
12. 失败时记录第一个主错误和所有并发子错误；按策略执行补偿，再结束 Operation。

### 8.5 失败策略精确定义

| 配置 | 行为 |
|---|---|
| `failFast=true` | 首个必需服务终态失败后，不再调度新服务；正在启动的同层服务允许完成或响应取消 |
| `cleanupOnFailure=false` | 保留已创建进程、日志和租约，Operation 为 failed |
| `cleanupOnFailure=true, keepReadyServices=false` | 逆拓扑停止本次创建的全部 daemon |
| `cleanupOnFailure=true, keepReadyServices=true` | 停止失败服务及依赖它的本次服务，保留与失败链无关的 ready 服务 |

端口租约只在对应进程确认停止或身份确认消失后释放，不能因为 Operation failed 就无条件删除。

### 8.6 停止算法

1. 获取工作区锁并创建 stop Operation。
2. 从运行实例快照读取逆拓扑顺序，不使用可能已刷新的新清单。
3. 按逆拓扑层执行；同层服务可并行停止。
4. 每个进程先重新核验 PID、启动时间、可执行路径和命令摘要。
5. 身份不匹配时不发送终止信号，标记 `unknown` 并返回 `PROCESS_IDENTITY_MISMATCH`。
6. 发送优雅停止，等待 `gracefulTimeout`；超时后终止完整进程树。
7. 关闭日志 writer、完成 segment 元数据、停止健康调度。
8. 确认端口不再由目标进程监听后释放租约。
9. 所有服务停止后将 SystemInstance 置为 stopped，结束 Operation。

部分停止失败时继续尝试停止其他独立服务，最终 Operation 为 failed，并返回未停止服务列表。重复 stop 必须安全：已停止服务步骤记为 skipped。

### 8.7 重启语义

系统重启是同一 Operation 内的 `stop -> fresh start`，使用最新有效清单，并重新规划端口；sticky 使其尽量复用原端口。停止失败时不进入 start。

服务重启仅允许目标服务及其传递下游：

1. 计算目标服务的下游闭包。
2. 逆拓扑停止下游和目标。
3. 保留不受影响的上游服务。
4. 正拓扑启动目标和下游，复用实例的端口计划。

若新清单摘要与运行实例不同，服务级重启返回 `409 MANIFEST_CHANGED`，要求用户执行系统重启，避免混合两份规格。

## 9. Process Driver 与 Windows 监管

### 9.1 Driver 接口

```go
type Driver interface {
    Preflight(context.Context, ResolvedServiceSpec) error
    Start(context.Context, StartRequest) (RuntimeIdentity, error)
    Stop(context.Context, StopRequest) error
    Inspect(context.Context, RuntimeIdentity) (RuntimeObservation, error)
    Recover(context.Context, RuntimeIdentity) (RecoveredRuntime, error)
}
```

Driver 只处理单个服务，不更新系统聚合状态。状态持久化由 Orchestrator 在 Driver 调用前后完成。

### 9.2 创建进程

- `workingDirectory` 使用已校验绝对路径。
- 环境为服务账号基线环境加清单允许项；Windows 环境 key 按大小写不敏感去重。
- 参数使用参数数组传入。除受控 `.cmd` Runner 外不调用 shell。
- 创建时隐藏窗口，不为每个服务打开控制台。
- Supervisor 把 stdout/stderr 分别连接到受限 spool 文件，Server 的 Log Manager 持续 tail；进程创建失败时关闭全部句柄。
- Windows 上由实例 Supervisor 以 suspended 状态创建服务进程，加入该服务专属 Job Object 后再恢复线程，避免子进程在入 Job 前逃逸。
- Job Object 使用 `KILL_ON_JOB_CLOSE`，句柄由独立 Supervisor 持有而不是 HTTP Server 持有。控制面普通退出不关闭 Job；Supervisor 自身异常退出时，Windows 自动终止其监管的服务树，避免产生无法证明所有权的孤儿进程。

### 9.3 Windows Supervisor

Supervisor 是同一发布二进制的内部子命令 `stackpilot internal-supervisor`，不属于公开 CLI。每个活动 SystemInstance 启动一个 Supervisor，并为每个 daemon 服务持有独立 Job Object、主进程句柄和 spool 文件句柄。

Supervisor 以隐藏、脱离 Server 生命周期的进程创建，且自身不加入任何被管服务的 Job Object；否则 Server 退出或停止某个服务可能错误地连带终止 Supervisor。

```mermaid
flowchart LR
    SERVER["StackPilot Server"] <-->|"ACL-protected named pipe"| SUP["Instance Supervisor"]
    SUP --> JOB1["Backend Job Object"]
    SUP --> JOB2["Web Job Object"]
    JOB1 --> P1["Maven / Java process tree"]
    JOB2 --> P2["npm / Node process tree"]
```

通信使用随机命名的 Windows Named Pipe，ACL 只允许当前用户或服务账号及 SYSTEM。`supervisor.json` 记录 Supervisor PID、创建时间、pipe 名和协议版本。Server 与 Supervisor 分别核验对端 PID、创建时间、可执行文件路径和运行账号；单用户本机模式不再增加 HMAC challenge 和第二份 secret 文件，因为同账号进程本就处于同一信任边界。

协议只允许固定消息：`hello`、`start-service`、`inspect-service`、`stop-service`、`shutdown-if-empty`。请求采用长度前缀 JSON，限制为 1 MiB。它不接受任意 shell 字符串；`start-service` 只接受 Server 已校验的结构化可执行文件、参数数组、工作目录和一次性环境块。

Supervisor 生命周期规则：

- Server 正常退出或崩溃时继续运行，业务进程不受影响。
- 新 Server 核验 Supervisor PID/创建时间后重新连接，并逐项核对服务身份。
- Supervisor 在恢复服务主线程前先原子写入 `identity.json`，Server 随后持久化数据库状态，以覆盖“进程已创建但 Server 尚未提交”的崩溃窗口。
- 所有服务停止后，Supervisor 响应 `shutdown-if-empty` 并退出。
- Supervisor 异常退出时 Job Object 的 kill-on-close 终止服务树；Server 恢复时将实例标记 failed 并记录 `SUPERVISOR_EXITED`。
- 协议版本不兼容时不得接管或终止现有进程；实例置 unknown，要求用户显式处理。

这里的两个异常场景没有冲突：Server 崩溃时 Job 句柄仍由 Supervisor 持有；只有 Supervisor 自身退出时才触发 `KILL_ON_JOB_CLOSE`。若省略 Supervisor 而由 Server 直接持有带 kill-on-close 的最后一个 Job 句柄，“关闭后再启动”之间的空档会终止业务进程，不能满足 P1-06。除非技术验证证明可用更简单的可靠句柄交接，否则不采用该简化。

Linux/macOS 后续可使用常驻 Agent 或可重连的进程组监管实现同一语义，不复用 Windows Named Pipe 细节。

### 9.4 Windows `.cmd` Runner

Windows Maven/npm wrapper 需要通过 `%COMSPEC% /d /s /c`。实现必须使用固定解释器开关和经过 Windows 命令行规则引用的参数，禁止拼接来自 HTTP 的原始字符串。清单参数仍是数组；测试覆盖空格、中文路径、引号和尾随反斜杠。

### 9.5 进程身份

创建成功后记录：PID、创建时间、规范化 executable path、非敏感 command digest、工作目录摘要。恢复或停止时四项必须匹配；权限不足导致无法读取时标记 Unknown，不以 PID 单独判断。

`identity.json` 使用临时文件写入、flush、原子替换，数据库记录其摘要。数据库与文件不一致时以实时 OS 核验为最终事实，并生成 reconciliation 事件。

### 9.6 日志接管限制

普通匿名 pipe 无法在 StackPilot 重启后重新连接。因此 Phase 1 启动服务时采用“子进程直接追加到持久 spool 文件 + 当前进程 tail”的方式，或使用可恢复的命名管道实现；首选 spool 文件方案：

```text
child stdout/stderr -> per-stream spool file -> tailer -> redact -> NDJSON segment/SSE
```

spool 文件按实例保留，tail offset 定期写入 segment 元数据。重启后从已提交 offset 继续读取，允许最多一次重复投递，由 `serviceId + stream + sequence` 去重。

### 9.7 终止流程

1. 再次核验身份。
2. 尝试对受控控制组发送优雅中断；不支持时调用 Runner 可配置 stop hook（仅允许清单 Schema 定义的受控类型）。
3. 等待主进程和 Job 内活跃进程归零。
4. 超时后调用 Job Object 终止整个树。
5. 记录退出码、终止原因和是否强制终止。

不能只终止 Maven/npm 父进程；测试必须验证 Java/Node 子进程和孙进程均退出。

## 10. 健康检查引擎

### 10.1 检查接口

```go
type Checker interface {
    Check(context.Context, ResolvedHealthSpec) HealthResult
}
```

`HealthResult` 包含检查类型、开始时间、耗时、成功标志、稳定错误码、脱敏摘要和可选状态码。原始 HTTP 正文不落库，最多保留经裁剪和脱敏的 2 KiB 摘要。

### 10.2 检查类型

| 类型 | 成功条件 | 主要错误码 |
|---|---|---|
| `process` | 身份匹配且进程未退出 | `PROCESS_EXITED`、`PROCESS_IDENTITY_MISMATCH` |
| `tcp` | 在单次 timeout 内完成 TCP connect | `TCP_REFUSED`、`TCP_TIMEOUT` |
| `http` | 状态码在允许范围，且可选 body 断言成立 | `HTTP_STATUS_MISMATCH`、`HTTP_BODY_MISMATCH`、`HTTP_TIMEOUT` |
| `compose` | Phase 2：容器运行且 health 为 healthy | `CONTAINER_UNHEALTHY` |

HTTP 默认只接受 200-299，不跟随跨主机重定向，响应体上限 64 KiB。MVP 只允许 loopback/清单声明的本机地址，防止健康检查成为任意网络探测接口。

### 10.3 Readiness 调度

- 进程创建后立即执行第一次检查，后续按 `interval` 加小幅随机抖动。
- 单次检查 timeout 不得大于 interval；未完成检查不会并发重入。
- 在总 `timeout` 前达到 `successThreshold` 即 ready。
- 进程提前退出立即终止 readiness，不等待总 timeout。
- 启动期默认 `failureThreshold=1` 仅用于记录，不提前失败；明确不可恢复错误除外。
- Operation 取消时检查上下文立即取消。

### 10.4 Liveness 与抖动控制

Phase 2 启用 liveness。Ready 后连续 `failureThreshold` 次失败进入 degraded，连续 `successThreshold` 次成功恢复 ready。状态变化才写持久事件；每次检查写入有界 `health_results`，后台按“最近 N 条 + 小时聚合”清理，避免无限增长。

## 11. 端口规划器

### 11.1 输入与输出

输入：工作区、清单 digest、全部端口需求、请求覆盖、工作区覆盖、sticky 历史、当前有效租约和 OS 监听状态。

输出为不可变 `PortPlan`：

```json
{
  "planId": "pp_01...",
  "workspaceId": "ws_01...",
  "assignments": {
    "backend": {"port": 8081, "source": "preferred", "replaced": false},
    "web": {"port": 5201, "source": "fallback", "replaced": true, "conflictPort": 5173}
  },
  "expiresAt": "2026-08-17T08:30:00Z"
}
```

### 11.2 候选优先级

每个逻辑端口按以下顺序生成去重候选：请求覆盖、工作区覆盖、仍适用的 sticky 值、preferred、fallbackRange 从小到大。`strict` 只允许请求覆盖或 preferred；`override-only` 只允许请求/工作区覆盖。

覆盖值即使可用也必须满足端口范围和暴露策略。一个系统内两个逻辑端口不能得到同一宿主机端口。

### 11.3 事务与 OS 探测

SQLite 无法独占 OS 端口，因此采用数据库租约与绑定探测双重校验：

1. `BEGIN IMMEDIATE`，读取未过期活动租约。
2. 对候选创建独占 TCP listener；Windows 禁止地址复用。
3. 保持所有 probe listener，直到整套候选选定。
4. 同一事务插入 `reserved` leases；唯一索引冲突则重选。
5. 提交事务并返回计划。
6. 启动对应服务前才关闭该端口 probe，紧接着创建进程。
7. 进程报告端口绑定失败时，整个启动按 `PORT_CONFLICT` 失败并执行既定失败策略，不做局部重规划。用户重试时重新生成整套计划，避免在同一实例中混用两份运行时规格。

同一 StackPilot 进程内 Port Planner 持有 probe listener；服务端崩溃后 listener 自动释放，数据库 reserved 租约通过短 TTL（默认 60 秒）在 reconciliation 后清理。

### 11.4 租约状态

`reserved -> bound -> released/expired`。服务 readiness 首次证明端口由目标服务监听后标记 `bound`。进程退出不立即假定端口释放，需 OS 复查；确认后才标记 released。

### 11.5 端口传播验证

模板展开器输出反向引用表，记录每个逻辑端口传播到哪些字段。接入测试必须断言：

- Backend 监听变量。
- Web 监听参数。
- Web API proxy URL。
- readiness URL。
- 最终访问入口。
- Phase 2 的 CORS、OIDC 和 Compose host mapping。

端口声明存在但没有监听所有者，或服务引用未声明端口，均拒绝注册。

## 12. Docker Compose Driver（Phase 2）

### 12.1 运行时文件

Compose 服务声明引用工作区内 compose 文件。StackPilot 在 `runtime/operations/<op>/` 生成 `compose.override.yml`，只包含：

- 宿主机端口映射覆盖。
- 必要的非敏感环境覆盖。
- StackPilot 实例标签。

原 compose 文件不修改。生成文件写入后重新解析并验证，不允许通过 override 添加特权模式、宿主根目录挂载或任意额外命令。

### 12.2 项目标识

项目名为 `sp-<system-id>-<workspace-short>-<instance-short>`，满足 Compose 字符限制。所有容器增加标签：`stackpilot.system`、`stackpilot.workspace`、`stackpilot.instance`、`stackpilot.service`。

### 12.3 调用与停止

- 预检：`docker version`、`docker compose version`、daemon 可用性和文件解析。
- 启动：`docker compose ... up -d --wait`，超时由服务配置决定。
- 观察：优先使用 `docker compose ps --format json` 的结构化输出。
- 日志：`docker compose logs --no-color --timestamps --follow`，转换为统一日志事件。
- 停止：`docker compose stop`；普通停止禁止 `down -v`。

Compose 命令的 stdout/stderr 也进入 OperationStep 日志，但需区分工具日志和容器服务日志。

## 13. 日志子系统

### 13.1 统一事件格式

```json
{
  "timestamp": "2026-08-17T07:30:00.123456Z",
  "systemId": "btc",
  "instanceId": "si_01...",
  "serviceId": "backend",
  "stream": "stderr",
  "level": "error",
  "message": "Failed to bind to port 8081",
  "operationId": "op_01...",
  "sequence": 1024,
  "truncated": false
}
```

`sequence` 在单个 ServiceInstance 内严格递增。时间戳优先使用采集时间；若解析出应用时间，可作为 `sourceTimestamp` 附加字段但不影响排序。

### 13.2 行处理

- stdout/stderr 独立读取，按到达顺序分配 sequence，不保证两个流的源端绝对顺序。
- 单行最大 256 KiB；超出后分块并标记 truncated，防止 scanner 卡死。
- 支持 `\n`、`\r\n`；末尾无换行内容在流关闭时提交。
- level 只做保守识别：明确 JSON level 字段或行首常见级别；无法判断为 `unknown`。
- 脱敏在写入最终日志文件和发送 SSE 之前执行，原始 spool 位于受限目录并按最短周期清理。

### 13.3 脱敏

Phase 1 脱敏器依次使用 Authorization/cookie/query 参数规则、连接串规则和用户配置规则；Phase 2 启用 Secret Provider 后，再增加已加载 Secret 的精确值匹配。替换文本统一为 `[REDACTED:<type>]`，保留足够上下文用于诊断。

Secret 值不得写入错误日志。脱敏器本身发生异常时采用 fail-closed：该日志事件不向客户端发送，只记录不含正文的安全告警。

### 13.4 文件与索引

最终 NDJSON segment 先写 `.active`，达到大小或跨日后 flush、关闭、原子改名并写 `log_segments`。SQLite 只保存路径、sequence/time 范围和大小，不为本地开发日志计算逐段 SHA-256。

历史查询通过 segment 元数据缩小文件范围，再流式读取；Phase 1 支持 service、时间范围、level、cursor 和 limit，不承诺任意全文搜索性能。原型中的当前日志筛选可在前端对已加载窗口执行。

保留 `log_segments` 和 sequence 范围是为了让游标跨文件轮转后仍可定位；单一“当前文件字节 offset”在文件改名、截断或清理后会失效。Phase 1 只保留定位与清理所需的最小元数据，不做内容完整性校验。

### 13.5 背压与保留

- 每个 SSE 客户端有有界缓冲；慢客户端超过阈值时断开并发送最后可恢复 cursor。
- 落盘路径不因 SSE 客户端变慢而阻塞。
- 磁盘达到高水位时先执行已关闭 segment 清理，仍不足则暂停新启动并返回 `LOG_STORAGE_PRESSURE`，但不杀死已运行服务。
- 删除只处理数据库已登记且位于 `DATA_DIR/logs` 内的关闭 segment。

## 14. 事件与 SSE

### 14.1 两类流

1. `/events`：低频、持久化的状态、Operation、健康状态变化和审计事件，支持 `Last-Event-ID`。
2. `/log-stream`：高频日志流，以 `instanceId/serviceId/sequence` cursor 恢复，不把每行日志写入 `events` 表。

### 14.2 领域事件 envelope

```json
{
  "id": 1842,
  "type": "service.state.changed",
  "occurredAt": "2026-08-17T07:30:00.123Z",
  "systemId": "btc",
  "workspaceId": "ws_01...",
  "instanceId": "si_01...",
  "operationId": "op_01...",
  "data": {
    "serviceId": "backend",
    "from": "waiting_ready",
    "to": "ready",
    "reasonCode": "READINESS_PASSED"
  }
}
```

事件类型使用过去式稳定命名。事件 payload 有版本字段或保持向后兼容；删除/改名需提升 API 版本。

### 14.3 事务提交与实时通知

状态表更新与 `events` 插入在同一事务完成。事务提交后，当前写入方以非阻塞方式把 event id 通知内存订阅者；不设置 `dispatched_at`，也不运行常驻 outbox 轮询器。SSE 订阅者始终以数据库事件为事实来源：收到通知后按 id 读取，慢消费者缓冲溢出时断开并用 `Last-Event-ID` 恢复。

若 Server 在提交后、通知前崩溃，现有 SSE 连接也会同时断开；客户端重连后从 `events` 表补读，因此不会丢事件。客户端仍应按 event id 去重。

### 14.4 SSE 协议

```text
id: 1842
event: service.state.changed
data: {...single-line JSON...}

```

- `Content-Type: text/event-stream`，禁用代理缓冲。
- 每 15 秒发送注释心跳 `: heartbeat`。
- `Last-Event-ID` 优先于 query cursor。
- cursor 早于保留窗口时返回 `409 EVENT_CURSOR_EXPIRED`，客户端重新拉取 REST 快照后再订阅。
- 先建立订阅水位，再查询数据库补历史，最后切换实时流，避免快照与订阅间丢事件。

## 15. SQLite 详细设计

### 15.1 连接与 migration

- 启动执行 `PRAGMA journal_mode=WAL`、`foreign_keys=ON`、`busy_timeout=5000`、`synchronous=NORMAL`。
- 单进程写入使用受控连接池；写事务保持短小，绝不在事务内等待进程或网络。
- migration 使用单调版本号和 checksum，启动时自动向前执行；checksum 不一致拒绝启动。
- 生产数据不支持自动降级 migration。

### 15.2 Phase 1 核心表

| 表 | 关键字段 | 索引/约束 |
|---|---|---|
| `systems` | `id`, `name`, `current_digest`, `created_at`, `updated_at` | PK `id` |
| `workspaces` | `id`, `system_id`, `root_path`, `canonical_path`, `manifest_status`, `last_valid_digest`, `last_error_code` | unique `canonical_path` |
| `manifest_snapshots` | `digest`, `system_id`, `api_version`, `normalized_yaml`, `parsed_json`, `created_at` | PK `digest`; 内容不含 Secret 值 |
| `services` | `workspace_id`, `service_id`, `driver`, `mode`, `required`, `definition_digest` | PK `(workspace_id, service_id)` |
| `system_instances` | `id`, `workspace_id`, `manifest_digest`, `resolved_spec_digest`, `state`, `started_at`, `stopped_at`, `last_reconciled_at` | partial index on active workspace |
| `service_instances` | `id`, `system_instance_id`, `service_id`, `state`, `pid`, `process_started_at`, `executable_path`, `command_digest`, `exit_code`, `state_version` | unique `(system_instance_id, service_id)` |
| `operations` | `id`, `workspace_id`, `type`, `state`, `idempotency_key`, `request_digest`, `error_code`, timestamps | partial unique active workspace; idempotency unique |
| `operation_steps` | `operation_id`, `step_no`, `step_key`, `state`, `attempt`, timestamps, `error_code`, `detail_ref` | PK `(operation_id, step_no)` |
| `port_leases` | `id`, `workspace_id`, `instance_id`, `operation_id`, `logical_name`, `protocol`, `host`, `port`, `state`, `expires_at` | partial unique `(protocol, host, port)` for active leases |
| `health_results` | `id`, `service_instance_id`, `kind`, `success`, `duration_ms`, `error_code`, `summary`, `checked_at` | index instance/time |
| `log_segments` | `id`, `service_instance_id`, `stream`, `path`, seq/time bounds, `size_bytes`, `closed_at` | unique path; index instance/sequence |
| `events` | `id`, `event_type`, scope ids, `payload_json`, `occurred_at` | PK autoincrement; indexes on operation/system/time |
| `auth_tokens` | `id`, `token_hash`, `created_at`, `last_used_at`, `revoked_at` | 不保存明文 |

Phase 1 migration 不创建事故分析或 Secret 空表。对应功能启用时再通过独立 migration 增加：

| Phase | 表 | 关键字段 |
|---|---|---|
| Phase 2 规则诊断 | `incidents` | `id`, `service_instance_id`, `kind`, `severity`, `state`, window, `fingerprint` |
| Phase 2 规则诊断 | `incident_analyses` | `id`, `incident_id`, `engine`, `schema_version`, `result_json`, `created_at` |
| Phase 2 Secret | `secret_metadata` | `system_id`, `name`, `provider`, `version`, `updated_at` |

### 15.3 乐观并发

`service_instances.state_version` 每次状态迁移加一。更新使用 `WHERE id=? AND state_version=?`；影响行数为 0 表示并发状态已变化，编排器重新加载并判断，而不是覆盖。

workspace 锁只串行化用户发起的变更 Operation，不能覆盖健康检查调度器、进程退出回调、Supervisor 通知和 reconciliation 的并发状态更新。因此 Phase 1 保留 `state_version`，防止较旧的健康结果把已经进入 `stopping/failed` 的实例覆盖回 `ready`。

### 15.4 活动实例唯一性

SQLite partial unique index保证同一 workspace 只有一个 `state NOT IN ('stopped')` 的 SystemInstance。处于 `failed` 但仍有进程的实例仍是活动实例，必须先显式停止后才能新建。

### 15.5 数据保留

- Operation、事件和审计默认保留 30 天；活动事故引用的数据不清理。
- 健康明细默认保留 24 小时，之后只保留聚合（Phase 2）。
- manifest snapshot 只要被运行实例或 Operation 引用就不删除。
- 清理任务每次小批量事务执行，失败不影响主服务。

## 16. Reconciliation 与故障恢复

### 16.1 启动恢复

StackPilot HTTP 就绪前执行一次快速恢复扫描：

1. 将数据库中 `running/cancelling` 且没有活跃 worker 的 Operation 标记为 `failed`，错误码 `CONTROL_PLANE_RESTARTED`。
2. 查询活动 SystemInstance 及其 ServiceInstance。
3. Windows 上先核验 Supervisor PID、创建时间、可执行路径、运行账号和协议版本，再恢复受保护 Named Pipe 连接。
4. 通过 Driver `Inspect/Recover` 核验每个实际身份。
5. 仍运行且身份匹配：恢复日志 tail、健康调度和实例监管。
6. 已退出：记录退出码（可得时），置 failed/stopped 并形成事件。
7. 身份不确定：置 unknown，不自动停止任何 PID。
8. 核验租约，释放无实例、无监听者的过期 reserved lease。
9. 重新计算系统聚合状态。

快速扫描完成后 API 才报告 `ready`。耗时较长的健康检查可后台继续，初始展示 `reconciling` 标志。

### 16.2 周期核对

Phase 1 每 10 秒检查受管进程身份，每 30 秒核对活动租约；周期可配置但有下限。周期核对只修正观测状态，不自动重启。Phase 2 根据 restart policy 创建新的系统内部 Operation。

### 16.3 崩溃窗口处理

| 崩溃点 | 恢复策略 |
|---|---|
| Operation 已建、未启动任何进程 | Operation 标 failed，释放过期 reserved lease |
| 进程已创建、身份未写库 | 扫描 runtime identity 原子文件；匹配后补写，否则记录 orphan candidate，不自动终止 |
| 状态已提交、实时通知未发出 | SSE 随 Server 断开；客户端重连后按 event id 从数据库补读 |
| 日志已落 spool、offset 未提交 | 从旧 offset 重读，sequence 去重 |
| 进程已退出、租约仍 bound | OS 核验后释放并更新实例 |

任何无法证明所有权的进程都不得由自动恢复流程终止。

## 17. REST API 详细契约

### 17.1 通用规则

- 基础路径 `/api/v1`，JSON 使用 camelCase。
- `Content-Type: application/json; charset=utf-8`。
- 列表默认 `limit=50`、最大 200，使用不透明 cursor，不使用页码。
- 状态类接口使用 `Cache-Control: no-store`；Phase 1 不实现 ETag 和条件请求。
- 变更请求必须带认证、合法 `Origin`（浏览器）和 `Idempotency-Key`（客户端应带，服务端兼容不带）。
- 未知请求字段返回 `400 REQUEST_VALIDATION_FAILED`。

### 17.2 错误 envelope

```json
{
  "error": {
    "code": "PORT_CONFLICT",
    "message": "端口 5173 已被占用，且当前策略不允许替换。",
    "details": {"logicalPort": "web", "port": 5173},
    "operationId": "op_01...",
    "traceId": "tr_01..."
  }
}
```

`message` 可面向用户，程序判断只使用稳定 `code`。`details` 不包含命令全文、Secret、未脱敏日志或服务器任意路径；路径仅在本机受信任管理界面按需返回。

### 17.3 主要资源 DTO

系统摘要：

```json
{
  "id": "btc",
  "name": "BTC",
  "workspaceId": "ws_01...",
  "workspacePath": "E:\\BidTravelCloud\\BTC",
  "manifestStatus": "valid",
  "state": "stopped",
  "serviceSummary": {"ready": 0, "total": 2},
  "endpoints": [],
  "activeOperationId": null,
  "updatedAt": "2026-08-17T07:30:00Z"
}
```

Operation：

```json
{
  "id": "op_01...",
  "type": "start",
  "state": "running",
  "workspaceId": "ws_01...",
  "systemId": "btc",
  "progress": {"completed": 4, "total": 8},
  "steps": [],
  "error": null,
  "createdAt": "2026-08-17T07:30:00Z",
  "startedAt": "2026-08-17T07:30:00Z",
  "finishedAt": null
}
```

### 17.4 系统接口

| 方法与路径 | 说明 | 成功响应 |
|---|---|---|
| `GET /systems` | 筛选 `status/query/cursor/limit` | `200` 列表 |
| `GET /systems/{id}?workspaceId=` | 系统详情、服务和当前实例 | `200` |
| `POST /systems/{id}/refresh` | 重新读取清单 | `202 Operation` |
| `GET /systems/{id}/status` | 轻量状态快照 | `200` |
| `POST /systems/{id}/port-plan` | 预览，不创建长期 lease | `200 PortPlanPreview` |
| `POST /systems/{id}/start` | 异步启动 | `202 OperationRef` |
| `POST /systems/{id}/stop` | 异步停止 | `202 OperationRef` |
| `POST /systems/{id}/restart` | 异步系统重启 | `202 OperationRef` |

若同一 systemId 注册多个 workspace，所有变更请求必须显式传 `workspaceId`；只有一个时可省略。

启动请求：

```json
{
  "workspaceId": "ws_01...",
  "portOverrides": {"backend": 8081, "web": 5201},
  "failurePolicy": {
    "failFast": true,
    "cleanupOnFailure": false,
    "keepReadyServices": true
  }
}
```

请求只允许覆盖已声明端口和失败策略，不接受 command、arguments、workingDirectory 或任意 environment。

### 17.5 服务与日志接口

| 方法与路径 | 说明 |
|---|---|
| `GET /services/{systemId}/{serviceId}?workspaceId=` | 服务定义摘要、实例身份、安全命令摘要、健康状态 |
| `POST /services/{systemId}/{serviceId}/restart` | 重启目标及其下游 |
| `GET /services/{systemId}/{serviceId}/logs` | 历史窗口，支持 instance/cursor/limit/level/from/to |
| `GET /log-stream?instanceId=&serviceId=&afterSequence=` | 实时日志 SSE |

日志响应默认最新 500 行，最大 5000 行。前端向上加载旧日志使用 cursor，不以 sequence 猜文件位置。

### 17.6 Operation 接口

| 方法与路径 | 说明 |
|---|---|
| `GET /operations?workspaceId=&state=&cursor=` | 操作历史 |
| `GET /operations/{id}` | 完整步骤和错误 |
| `POST /operations/{id}/cancel` | 取消可取消 Operation；返回 202 |

取消成功表示请求已记录，不表示补偿已完成。客户端等待 Operation 进入 cancelled/failed 终态。

### 17.7 工作区接口

| 方法与路径 | 说明 |
|---|---|
| `POST /workspaces` | `{ "path": "..." }`，读取固定清单并注册 |
| `GET /workspaces` | 工作区和清单状态 |
| `DELETE /workspaces/{id}` | 仅无活动实例/Operation 时解除注册；不删除项目文件 |

解除注册属于管理操作，需二次确认。它只删除 StackPilot 注册信息和无引用缓存，不触碰工作区内容。

### 17.8 服务可用性接口

- `GET /health/live`：进程存活，不检查数据库写入。
- `GET /health/ready`：migration 完成、数据库可写、首次 reconciliation 完成。
- `GET /version`：版本、commit、build time、API version 和启用能力。

## 18. 认证、安全与审计

### 18.1 本地认证

首次启动生成 256 bit 随机令牌，明文只写入 OS 安全存储，SQLite 保存带随机 salt 的 Argon2id 摘要。CLI 从安全存储读取并发送 `Authorization: Bearer`。

Web 使用短期 HttpOnly、SameSite=Strict 会话 cookie：`stackpilot open` 把一次性 bootstrap code 放在 URL fragment，前端交换会话后立即清除 fragment。普通 API 不接受 query token。会话创建接口仅监听回环地址，并校验 Origin。

### 18.2 CSRF 与本地恶意网页

回环监听并不能单独防止浏览器访问本机服务。所有浏览器变更请求必须满足：

- 已认证会话 cookie。
- `Origin` 精确匹配 StackPilot 地址。
- 自定义 CSRF header 与会话绑定值匹配。
- JSON Content-Type，不接受简单表单提交。

CLI Bearer 请求不使用 cookie，但仍受令牌认证和命令边界约束。

### 18.3 路径安全

- 所有清单相对路径先 join，再 canonicalize，再验证位于工作区根内。
- 拒绝通过 `..`、符号链接、junction 或大小写差异逃逸。
- 输出文件只允许位于 DATA_DIR 的固定子目录。
- 删除前同时验证数据库登记路径、canonical path 前缀和文件类型。

### 18.4 Secret Provider（Phase 2）

Phase 1 不实现 Secret Provider，也不创建 `secret_metadata` 表；包含 Secret 引用的清单在注册时返回 `FEATURE_NOT_ENABLED`。BTC 首个接入不依赖此能力。

Phase 2 启用后，统一接口只暴露 `Set`、`Resolve`、`Metadata`、`Delete`。Windows 实现优先使用 Credential Manager；若选用 DPAPI 文件，必须绑定当前用户/机器范围并限制 ACL。

Secret 只在进程创建前解析，使用版本号写入实例元数据。任何 DTO 的环境变量只返回 key 和 `secretRef`，不返回 resolved value。

### 18.5 审计事件

Phase 1 记录工作区注册/删除、清单刷新、系统/服务启停、取消、令牌轮换和高风险失败。Phase 2 增加 Secret 元数据变更。字段包括主体类型、动作、目标、结果、traceId、operationId、时间和客户端类型；不记录令牌、Secret 和完整命令。

## 19. 事故与规则诊断（Phase 2）

本章固定 Phase 2 的扩展边界，不属于 Phase 1 实现清单。Phase 1 不创建事故表、事故目录、诊断包或相关 API；功能启用时再增加 migration 和实现，避免空模块占位。

### 19.1 事故触发

- 进程异常退出。
- readiness 超时。
- liveness 连续失败进入 degraded。
- 自动重启达到上限。
- 端口绑定失败或身份不匹配。

相同服务、相同规则分类和滑动窗口内的事件按 fingerprint 合并，防止告警风暴。

### 19.2 IncidentContext

上下文生成器使用显式预算：时间窗默认异常前 2 分钟、后 1 分钟；日志最多 500 行/256 KiB；相邻重复行折叠计数。所有内容先脱敏，再写 `context.json`。

证据引用必须可追溯到 event id、health result id 或日志的 instance/sequence，不复制无法定位的结论。

### 19.3 规则接口与优先级

```go
type DiagnosticRule interface {
    ID() string
    Match(IncidentContext) (RuleResult, bool)
}
```

首批规则：端口已占用、进程异常退出、readiness 连接拒绝、HTTP 状态异常、依赖未就绪、已知 Java/Node/Python 启动错误。规则结果按证据特异性和置信度排序，同一证据不重复生成相同建议。

所有建议默认 `automatic=false`。原型中的“重新执行 readiness”属于只读低风险检查，可以创建独立 diagnose Operation，但不能隐式重启服务。

## 20. Web 控制台详细设计

### 20.1 前端结构

```text
web/src/
├── api/               typed client、error mapping、SSE client
├── router/            route definitions 和 guards
├── stores/            systems、operations、logs、settings、session
├── views/             Dashboard、System、Service、Operations、Settings
├── components/        状态、步骤、日志、端口计划等领域组件
├── composables/       useEventStream、useLogStream、useOperation
└── types/             与 OpenAPI 对齐的 DTO
```

Phase 1 路由：

```text
/                         系统总览
/systems/:systemId        系统详情；workspaceId query
/systems/:systemId/services/:serviceId
/operations               操作中心
/operations/:operationId  操作详情
/settings/workspaces
/settings/ports
/settings/security
```

### 20.2 状态管理原则

- REST 快照是页面初始事实，SSE 是增量更新。
- Store 保存 `lastEventId`，只接受更新版本更高的实例状态。
- SSE 断开显示非阻塞连接状态并指数退避重连；超过保留窗口后重新拉 REST 快照。
- 按钮可用性由服务端能力字段和活动 Operation 决定，不能只按视觉状态推断。
- 页面离开不取消 Operation；Operation 属于服务端持久任务。

### 20.3 系统总览

与原型一致展示系统名、工作区、聚合状态、服务就绪数、访问入口和快捷操作。筛选在服务端支持 status/query，前端对当前页即时过滤。

快捷启动先调用端口计划预览，再打开确认对话框。若无冲突可直接展示首选/粘性结果；有替换时明确显示原端口、占用原因和新端口。

### 20.4 系统详情

包含四个页签：概览、服务与端口、服务配置、清单摘要。

- 概览：依赖图、当前 Operation、服务状态。
- 服务与端口：实际端口计划、来源、冲突替换和访问入口。
- 服务配置：只读展示 Driver、Runner、mode、工作目录相对路径、依赖、健康和已脱敏环境引用。
- 清单摘要：展示来源、digest、校验状态和规范化只读内容。

启动/停止确认框提交后立即关闭，页面定位到 Operation 进度。重复点击由本地 disabled 和服务端锁双重保护。

### 20.5 服务详情与日志

- 顶部展示服务状态、PID、端口、readiness 和最近检查。
- 初次加载最近 500 行，滚动到顶部按 cursor 加载历史。
- “暂停”只暂停 UI 追加，客户端继续有界缓存；恢复后批量追加。缓存溢出时提示并重新请求窗口。
- level 和关键字筛选不改变后端运行状态。
- 复制/下载只包含用户当前选中或当前查询范围内已脱敏日志。
- 日志区使用固定高度和虚拟列表，避免长日志导致页面抖动。

### 20.6 操作中心

左侧历史列表，右侧步骤时间线。Operation 失败时显示稳定错误消息、失败步骤、技术详情引用和可执行的安全下一步。取消按钮只在服务端返回 `cancellable=true` 时启用。

### 20.7 基础配置

- 工作区：添加、刷新、解除注册，展示清单状态。
- 端口：全局默认 sticky/auto 策略；清单 strict 仍不能被全局 auto 放宽。
- 安全：只展示令牌创建时间、最近使用和轮换操作，不显示明文。

### 20.8 可访问性与错误处理

- 状态不只依赖颜色，使用文字和图标。
- 对话框管理焦点，Esc 只关闭未提交对话框。
- SSE 更新通过适度的 live region 汇报，不逐行朗读日志。
- 所有请求错误映射到页内错误或 toast；Operation 失败不能只用短暂 toast。
- 破坏性操作使用明确对象名和影响范围，不用泛化“确定吗”。

## 21. CLI 详细设计

CLI 是 API 客户端，退出码稳定：`0` 成功、`2` 参数/配置错误、`3` 服务端不可达、`4` Operation 失败、`5` 认证失败、`6` 冲突。

`stackpilot up --wait` 流程：发现当前工作区、提交 start、订阅 Operation 事件、终端显示步骤；SSE 不可用时回退轮询。Ctrl+C 第一次只停止等待并询问/按参数决定是否取消 Operation，不直接杀业务进程。

支持 `--output table|json`；JSON 模式 stdout 只输出机器可读结果，进度写 stderr。CLI 不打印本地令牌和 Secret。

## 22. 错误码

### 22.1 分类

| 前缀 | 含义 | 示例 |
|---|---|---|
| `MANIFEST_` | 清单解析/语义 | `MANIFEST_CYCLE_DETECTED` |
| `WORKSPACE_` | 工作区 | `WORKSPACE_PATH_OUTSIDE_TRUST` |
| `RUNNER_` | 工具解析/预检 | `RUNNER_NOT_FOUND` |
| `PORT_` | 端口规划/绑定 | `PORT_CONFLICT` |
| `PROCESS_` | 进程创建/身份/退出 | `PROCESS_IDENTITY_MISMATCH` |
| `HEALTH_` | readiness/liveness | `HEALTH_READINESS_TIMEOUT` |
| `OPERATION_` | 锁、取消、状态 | `OPERATION_ALREADY_ACTIVE` |
| `AUTH_` | 认证/会话 | `AUTH_TOKEN_INVALID` |
| `LOG_` | 日志存储/游标 | `LOG_CURSOR_EXPIRED` |
| `INTERNAL_` | 未分类内部错误 | `INTERNAL_ERROR` |

每个错误码在 `docs/error-codes.md` 或 OpenAPI components 中维护 HTTP 状态、用户消息模板、是否可重试和允许暴露的 details。未知内部错误统一返回 500 和 traceId。

### 22.2 HTTP 映射

- 400：请求或清单字段错误。
- 401/403：未认证/无权执行本地管理操作。
- 404：资源不存在。
- 409：活动 Operation、端口、幂等 key 或状态冲突。
- 422：请求结构正确但当前系统定义无法执行。
- 429：全局 Operation/检查并发限制。
- 500：内部一致性错误。
- 503：恢复未完成、数据库不可写或日志存储压力阻止新启动。

## 23. 可观测性

StackPilot 自身使用结构化 `slog`，字段至少包括 `trace_id`、`operation_id`、`workspace_id`、`instance_id`、`service_id` 和 `error_code`。禁止默认记录 HTTP Authorization、请求完整 body、子进程完整环境和未脱敏日志。

每个入站请求生成请求级 traceId，并传播到由该请求直接创建的 Operation 及其结构化日志。领域事件以 event id、operationId 和实例标识关联，不强制重复携带 traceId。Phase 1 不要求 OpenTelemetry，但内部 context 传播要兼容后续接入。

关键内部指标先以状态端点或结构化日志记录：活动 Operation 数、健康检查队列、SSE 客户端数、日志写入错误、数据库 busy 次数和 reconciliation 结果。

## 24. 测试设计

### 24.1 测试夹具

`tests/fixtures` 提供可控小程序，而不是依赖真实 Maven/npm 完成所有边界测试：

- `slow-ready`：延迟监听端口。
- `exit-immediately`：指定退出码和 stderr。
- `child-tree`：创建子进程和孙进程。
- `ignore-terminate`：验证强制停止。
- `flapping-health`：按序列返回成功/失败。
- `large-log`：长行、高速和 Secret 模式。
- `bind-race`：模拟探测后抢占端口。

真实 BTC 作为端到端验收，不替代可重复的夹具测试。

### 24.2 单元测试

- YAML 重复 key、未知字段、模板非法引用、路径逃逸。
- DAG 拓扑排序、循环、下游闭包、可选服务聚合。
- 所有服务与 Operation 合法/非法状态迁移。
- 端口候选优先级、范围耗尽、sticky、事务冲突。
- Windows 参数引用和 Runner 解析。
- readiness 阈值、timeout、取消和进程提前退出。
- 日志分行、长行、level、脱敏和 cursor。
- DTO 校验、错误码映射和幂等摘要。

### 24.3 存储集成测试

- 空库 migration 到最新版本。
- 每个历史 migration 升级路径和 checksum。
- 活动 workspace/port unique constraint。
- 状态和持久事件的事务原子性。
- 并发 `BEGIN IMMEDIATE` 与 busy retry。
- retention 不删除活动引用数据。

### 24.4 Windows 集成测试

- Maven/npm `.cmd` 在含空格和中文路径下启动。
- 无可见终端窗口。
- Job Object 覆盖子孙进程并在超时后全部终止。
- PID 复用/身份不匹配时拒绝停止。
- StackPilot 重启后核验和日志续读。
- 探测后端口被抢占时启动以 `PORT_CONFLICT` 失败，重试后生成新计划。

### 24.5 API/SSE 契约测试

- OpenAPI 请求/响应示例可解析。
- Idempotency-Key 同请求复用、异请求冲突。
- SSE 心跳、事件顺序、重复容忍、过期 cursor。
- 日志 SSE 慢消费者断开，不阻塞落盘。
- 浏览器 Origin/CSRF 和 CLI Bearer 两条认证路径。

### 24.6 Web 端到端测试

1. 添加 BTC 工作区并看到有效清单。
2. 打开启动确认，5173 冲突时显示替代端口。
3. Backend 未 ready 前 Web 保持 waiting dependency。
4. Operation 时间线随 SSE 更新，成功后入口可点击。
5. 服务详情持续接收、筛选、暂停和下载日志。
6. 停止系统后二者逆序退出，无遗留进程。
7. 刷新页面或断开 SSE 后状态恢复一致。
8. readiness 失败时保留现场并展示明确错误。

### 24.7 质量门槛

- Go：格式、静态检查、race test（适用包）、Windows 测试、多平台公共核心编译。
- Web：type-check、lint、组件测试、生产构建。
- OpenAPI 与前端 DTO 差异检查。
- migration checksum 检查。
- Phase 1 主流程端到端测试必须在发布前通过。

## 25. BTC 首个接入纵切面

### 25.1 服务定义

| 服务 | Runner | 端口 | 依赖 | Readiness |
|---|---|---:|---|---|
| `backend` | Maven | preferred 8081，fallback 8200-8399 | 无 | `/actuator/health` HTTP 2xx |
| `web` | npm | preferred 5173，fallback 5200-5299 | backend ready | Web 根路径 HTTP 2xx |

### 25.2 业务项目一次性改造要求

- Backend 读取 `SERVER_PORT`。
- Vite 启动端口从 Runner 参数接收，不能被配置文件硬编码覆盖。
- Vite proxy 读取 `VITE_API_TARGET`。
- Backend health endpoint 不依赖需要较长初始化的非关键外部系统，或明确返回可解释状态。
- CORS 允许来源由规划后的 Web 地址注入，不能只写死 5173。

### 25.3 验收证据

- BTC 清单 Schema 和语义校验报告。
- 启动 Operation 的步骤、耗时和最终 resolved spec 摘要。
- 8081/5173 被占用时的端口计划及传播断言。
- Backend -> Web 的 readiness 时序记录。
- 停止前后进程树快照和端口释放结果。
- StackPilot 重启前后的同一实例身份和日志 sequence 连续性。

## 26. 分阶段实施顺序

### 26.1 Phase 0 工程基线

1. 建立 Go module、Vue/Vite、构建嵌入和 CI。
2. 固化领域枚举、错误 envelope、OpenAPI 和清单 Schema。
3. 建立 SQLite migration、repository 测试基座。
4. 建立平台接口和公共核心多平台编译门槛。

退出条件：空服务可启动、Web 静态资源可访问、migration 可重复执行、Schema 示例通过、CI 产出 Windows 二进制。

### 26.2 Phase 1A 注册与只读控制面

1. Workspace 注册/刷新。
2. 清单解析、验证、快照。
3. Systems/Services 查询 API。
4. Web 总览、系统配置和清单只读页。

退出条件：BTC 清单可注册，非法路径/模板/DAG 被稳定拒绝，前端只读信息与清单一致。

### 26.3 Phase 1B 单服务运行

1. Runner 解析与 Windows Process Driver。
2. 日志 spool、脱敏、segment 和历史查询。
3. process/TCP/HTTP readiness。
4. 单服务 start/stop Operation 和事件。

退出条件：测试夹具与 BTC Backend 可无窗口启动、查看日志、ready、停止且无遗留子进程。

### 26.4 Phase 1C 系统编排

1. DAG、有界并发、失败策略和取消。
2. 端口整套规划、sticky、租约和传播。
3. 系统/服务重启。
4. BTC Backend/Web 完整接入。

退出条件：BTC 端到端和端口冲突场景通过。

### 26.5 Phase 1D 恢复与体验收口

1. 启动 reconciliation、身份文件和日志续读。
2. 操作中心、SSE 恢复、错误呈现。
3. 本地认证、CSRF 和审计。
4. 安装/升级/卸载与发布验证。

退出条件：总体设计 26.1 的十项验收标准全部有自动化或可重复验收记录。

### 26.6 Phase 2

按顺序启用 Secret Provider、Python venv Runner、oneshot/completed、Compose Driver、AIWS、PMS、liveness/自动重启、事故规则诊断。每项使用 capability flag 交付，未完成能力不接受对应清单。

## 27. 验收追踪矩阵

| 编号 | 验收要求 | 设计机制 | 主要测试 |
|---|---|---|---|
| P1-01 | 不依赖项目脚本启停 BTC | Runner + Process Driver | BTC E2E |
| P1-02 | 异步操作和实时步骤 | Operation + 持久事件 SSE | API/SSE E2E |
| P1-03 | Backend ready 后才启动 Web | DAG 条件释放 | slow-ready 集成测试 |
| P1-04 | 启动前发现端口冲突 | PortPlan + probe + lease | 双实例/占用测试 |
| P1-05 | 端口完整传播 | ResolvedSpec 反向引用 | BTC 配置断言 |
| P1-06 | 重启后接管 | identity + reconciliation | 控制面重启测试 |
| P1-07 | 不遗留子进程 | Job Object | child-tree 测试 |
| P1-08 | 日志实时与续读 | spool + segment + cursor | 重连/重启测试 |
| P1-09 | 无任意命令接口 | 清单信任边界 + DTO allowlist | 安全契约测试 |
| P1-10 | 清单唯一事实来源 | digest snapshot + refresh | 刷新一致性测试 |

## 28. 评审前待确认项

以下事项不阻塞工程基线，但必须在对应纵切面开发前确认：

1. BTC Backend 的实际 readiness 路径、响应约定及健康端点是否需要认证。
2. BTC Vite 当前端口和 proxy 配置读取方式，5173/5175 的历史不一致以哪份实际配置为准。
3. Windows 首发采用用户级常驻进程还是 Windows Service；两者决定默认数据目录和 Secret 保护范围。
4. Windows Supervisor 的 Named Pipe ACL、Server 崩溃接管和 Supervisor 崩溃 kill-on-close 行为需要在 Phase 1B 前完成技术验证。
5. 前端首次认证采用 `stackpilot open` bootstrap，还是安装器创建本机浏览器会话；必须避免 URL query token。

第 4 项技术验证结论应形成 ADR；若 Windows 实测行为与本文假设不符，必须先更新监管架构，不得退化为仅凭 PID 终止进程。

已确认的范围决策：Phase 1 只保留 Python venv Runner 接口和 capability gate，生产实现归入 Phase 2A；该项不再作为待确认事项。

## 29. 完成定义

详细设计对应功能只有同时满足以下条件才算完成：

- 实现与本文件及 OpenAPI/Schema 契约一致。
- 状态迁移、事务边界和错误码有自动化测试。
- 不记录或返回 Secret、令牌、未脱敏日志和任意命令输入。
- Windows 进程树、端口冲突、SSE 重连和控制面恢复通过集成测试。
- BTC 主流程通过 Web 和 CLI 两条入口验证，最终状态一致。
- 相关设计变更同步更新总体设计、详细设计、OpenAPI、Schema 和验收记录。
