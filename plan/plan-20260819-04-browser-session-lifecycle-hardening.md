# StackPilot 浏览器会话生命周期与失效恢复开发计划

> 状态：已完成（证据：`docs/evidence/browser-session-lifecycle-hardening-20260819.md`）
> 日期：2026-08-19
> 来源 Prompt：`prompt/prompt-20260819-04-browser-session-lifecycle-hardening.md`
> 阶段归属：Phase 1D 本地认证、浏览器安全与后台生命周期的维护性加固

## 1. 目标结果

把当前“15 分钟绝对到期且只能重新开页面”的浏览器认证改为安全、有上限、可观测的会话生命周期：30 分钟滑动续期窗口、8 小时绝对上限、到期前单飞续期、REST/SSE 全局失效收口，并保留一次性 bootstrap、HttpOnly Cookie、精确 Origin、CSRF 和服务重启/令牌轮换失效语义。

同时补齐控制面正常退出路径的安全结构化诊断，并用独立 Windows fixture 调查“日志没有 stop 记录但状态为 stopped”的现象。本计划不把控制面自动重启作为认证修复的一部分。

## 2. 固定决策

1. 滑动续期窗口 30 分钟，绝对上限 8 小时；续期结果为 `min(now + 30m, absoluteExpiresAt)`。30 分钟内没有成功续期即失效，不将其表述为键盘/鼠标空闲检测。
2. 服务重启、令牌轮换、注销、续期窗口到期和绝对到期仍立即使会话不可恢复，必须重新执行 `stackpilot open`。
3. 浏览器不接触长期令牌，不增加长期 refresh token，不持久化 session/CSRF。
4. 续期同时更新服务端会话和 HttpOnly Cookie；服务端时间是权威。
5. 前端单飞续期并集中协调 CSRF 轮换与 mutation，不自动重放业务 mutation。
6. REST/SSE 认证失效进入同一全局状态，不作为普通 Store 错误重复展示。
7. 控制面异常停止先调查和增强诊断；watchdog、Windows Service 和后台架构变化不在本计划实现范围。
8. API、设计、安全语义与测试证据必须在同一变更同步，不以 UI 演示替代 Gate。

## 3. 实现前待决事项

以下事项必须在 AS-00 形成书面结论，未关闭前不得改动生产认证路径：

1. CSRF 并发协议：刷新与在途 mutation 的互斥范围、失败行为以及多标签页共享同一 Cookie 时的 CSRF 所有权。
2. Cookie 期限算法：注入时钟如何传到 API 层，`Max-Age` 舍入规则、`Expires` 和恰好到期处理。
3. 前端续期安全窗口与退避：剩余 5 分钟触发、可见性/焦点/mutation 前检查的合并规则，以及不越过服务端期限的重试上限。
4. GET 刷新兼容性：确认继续使用现有 `GET /auth/session`，或在 ADR 中说明必须调整方法的理由；默认保持现有路径和方法。
5. 控制面退出分类：现有 context/signal/control Pipe/upgrade/serve error 能提供哪些可靠 reason，哪些只能标记为“上次未正常收口”。
6. 是否需要运行 marker。若仅靠结构化日志足以满足诊断，不新增 marker；若需要，必须先定义原子协议、权限和崩溃窗口。

## 4. 工作包总览

| ID | 工作包 | 依赖 | 主要交付物 | Gate |
| --- | --- | --- | --- | --- |
| AS-00 | 设计、安全与契约裁决 | 无 | ADR-0002 修订、并发协议、契约差异表、退出分类 | Design Gate |
| AS-01 | 服务端会话模型 | AS-00 | 滑动期限、绝对上限、原子刷新、时钟一致性 | Security Gate |
| AS-02 | API、Cookie 与契约 | AS-01 | refresh handler、Cookie 更新、OpenAPI/契约测试 | API Gate |
| AS-03 | Web 会话协调器 | AS-00/02 | 单飞续期、timer/visibility、全局认证状态 | Web Unit Gate |
| AS-04 | REST/SSE 与页面收口 | AS-03 | 统一 invalidation、流停止、恢复页面、Store 接线 | UX Gate |
| AS-05 | 控制面停止诊断 | AS-00 | 生命周期 reason 日志、隔离 Windows 复现、调查结论 | Lifecycle Gate |
| AS-06 | 集成、安全回归与交付 | AS-01..05 | Playwright/Windows Gate、文档、证据、发布检查 | 专项完成 |

## 5. AS-00：设计、安全与契约裁决

### 任务

- 逐项盘点 ADR-0002、详细设计、OpenAPI、Go Authenticator 接口、前端 SessionResponse 和现有测试的当前语义，形成变更前后差异表。
- 修订 ADR-0002：固定 30 分钟滑动期限、8 小时绝对期限、刷新上限、Cookie 更新、页面刷新恢复和失效后的重新 bootstrap。
- 明确 session 状态模型和时序：create、authenticate、refresh、renewal-expired、absolute-expired、revoked、restart-invalidated、rotation-invalidated。
- 为刷新与 mutation 画出并发时序，确定前端刷新临界区。设计必须覆盖两个并发 refresh、refresh 与 mutation、多标签页 refresh、旧响应后到和页面卸载。
- 默认方案是集中 API 协调器保证单飞 refresh，并在 CSRF 轮换期间阻止新 mutation 发出；在途请求如何收口必须有确定结论。若该方案不能解决多标签冲突，再评估短期前一 CSRF 宽限或不同 CSRF 模型，并记录额外攻击面。
- 确认 `/auth/session` GET 仍承担“恢复前端内存 CSRF + 续期”职责，并明确 GET 的状态变化和 no-store 约束。
- 定义 Cookie 算法：服务端同一注入时钟计算秒数；向下取整不得提前跨过测试边界，向上取整不得越过绝对期限；0/负数只用于删除。
- 盘点 `web/src/api/stream.ts` 的认证失败路径，定义不让 API 层直接依赖 Pinia 的类型化 invalidation 事件边界。
- 盘点 Windows usertask 从 service CLI 到 Server shutdown 的每个 context 和错误边界，形成可靠退出 reason 表。

### Design Gate

- ADR-0002 和详细设计草案经过安全评审，且不削弱长期令牌、bootstrap、Origin、CSRF、Cookie 或重启失效边界。
- CSRF/mutation/多标签并发方案无未决竞态，能够被确定性测试。
- OpenAPI 差异、Go/TS 接口变化和 Cookie 行为已列全。
- 控制面调查与认证实现保持独立，未把 watchdog 偷渡进范围。

## 6. AS-01：服务端会话模型

### 实现

- 在 `internal/security/auth.go` 扩展最小 `browserSession` 状态，保存绝对期限和当前滑动期限；如创建时间可由绝对期限推导且不影响审计，不重复存储。
- 将 `AuthConfig` 的当前 `SessionTTL` 拆分或重命名为语义清晰的 renewal/absolute 配置。内部默认值固定为 30 分钟/8 小时；若不需要运行时可配置，不新增公开 flag。
- 交换 bootstrap 时生成新会话，初始滑动期限不得超过绝对期限。
- 将 refresh 实现为单个 mutex 临界区内的校验、prune、期限推进和 CSRF 更新；随机数生成失败不得改变旧会话。
- 统一 `AuthenticateSession`、`ValidateCSRF`、refresh 和 prune 的到期判断。
- 保留最大 256 sessions、摘要 key、摘要 CSRF、单次 bootstrap 和 rotation 清空。
- 根据 AS-00 的并发裁决实现必要的最小 CSRF 状态；不为方便而保留无界历史 token。

### 测试

- 更新 `internal/security/auth_test.go`，使用注入时钟验证初始 30 分钟、29 分钟刷新、连续刷新、接近 8 小时截断、恰好到期和超过上限。
- 验证无效/过期/撤销/rotation 后 refresh 不复活；重建 AuthManager 后旧 session 失效。
- 并发测试覆盖 refresh-refresh、refresh-authenticate、refresh-CSRF validation 和 prune，不依赖真实 sleep。
- 运行 `go test -race ./internal/security -count=1`；Windows DPAPI 集成测试仅使用临时数据根。

### Security Gate

- 所有时间边界测试确定通过，期限永不越过绝对上限。
- 无明文 session/CSRF 新增到日志、SQLite、错误或持久文件。
- race detector 无数据竞争，session 容量仍有界。

## 7. AS-02：API、Cookie 与契约

### 实现

- 调整 API `Authenticator` 最小接口和 refresh handler，以获得新的当前期限并在成功响应中重新设置 Cookie。
- 抽取单一 Cookie 构造函数，供 exchange、refresh、revoke 和 token rotate 使用，避免属性漂移。
- Cookie 设置同时包含正确的 `Max-Age` 与 UTC `Expires`；删除 Cookie 使用过去时间、`Max-Age=-1` 和一致 Path/SameSite/HttpOnly。
- Cookie 剩余期限使用与 AuthManager 相同的注入 clock 或直接由安全层返回安全 duration，禁止生产路径用 `time.Until` 而测试层使用另一时钟。
- 更新 `api/openapi.yaml` 中 GET summary/description、响应语义和 Cookie 描述；确认 `SessionResponse` 继续只暴露 `csrf/expiresAt`。
- 保持 `AUTH_SESSION_INVALID` 为 401、`AUTH_BROWSER_REQUEST_REJECTED` 为 403，保持所有 JSON `no-store`。

### 测试

- 扩展 `internal/api/auth_test.go`：POST/GET 设置 Cookie，GET 的期限向前推进但不越过上限，DELETE/rotate 清除 Cookie。
- 精确断言 HttpOnly、SameSite=Strict、Path、Max-Age、Expires 和不使用 Secure 的当前回环契约。
- 覆盖无 Cookie、过期、错误 Origin、非 JSON mutation、错误 CSRF 和 bearer 路径互不降级。
- 更新 `internal/api/contract_test.go`，保证 OpenAPI operation/response/error enum 与 handler 一致。

### API Gate

- `go test ./internal/security ./internal/api -count=1` 通过。
- OpenAPI 解析/契约检查通过，未增加匿名续期或敏感字段。
- Cookie 在交换、续期、注销和轮换四条路径属性一致。

## 8. AS-03：Web 会话协调器

### 结构

- 新建职责聚焦的 auth Store/composable，拥有认证状态、`expiresAt`、CSRF 生命周期、单飞 refresh promise、timer 和 invalidation。
- `web/src/api/client.ts` 保留 fetch、DTO、错误解析和 CSRF header 注入；通过最小类型化接口与协调器协作，避免 API 模块导入 Vue 组件或形成循环依赖。
- `App.vue` 只消费认证状态并编排初始 catalog/stream，不继续持有 timer 和协议细节。

### 续期算法

- bootstrap exchange 或 cookie refresh 成功后解析 `expiresAt`，拒绝无效日期或已过期服务端响应。
- 调度点为到期前 5 分钟；如果剩余时间已经小于窗口，立即单飞 refresh，不建立 0ms 循环。
- `visibilitychange` 回到 visible、window focus 和 mutation 发出前执行 freshness check；多个触发合并到同一 promise。
- 网络错误按 AS-00 的有界退避重试，任何重试不得越过已知期限。401 立即失效，不重试。
- timer 在 reinitialize、expired、logout 和 unmount 时清理；成功续期只保留一个新 timer。
- 按 AS-00 的协议协调 CSRF 轮换和 mutation，确保旧 CSRF 不会在 refresh 完成后才发出。

### 测试

- 在 `web/test` 使用 fake timer 和 mock fetch 覆盖调度、单飞、成功推进、失效、网络恢复、后台恢复、时钟前后跳和 cleanup。
- 并发触发 timer/focus/mutation 只产生一次 refresh。
- refresh 与两个 mutation 的测试证明顺序确定，且不自动重放已经失败或可能有副作用的请求。
- 页面刷新无 fragment 但 Cookie 有效时取得新内存 CSRF；bootstrap fragment 被立即清除且只交换一次。

### Web Unit Gate

- auth 状态机、timer owner、单飞和竞态测试通过。
- TypeScript strict 无 `any`、无无理由非空断言、无循环依赖。
- 浏览器存储扫描不含长期令牌、bootstrap、session 或 CSRF 持久副本。

## 9. AS-04：REST/SSE 与页面收口

### 实现

- 在 REST error parser 识别稳定 `AUTH_SESSION_INVALID` code 并发布全局认证失效事件，不使用 message 字符串。
- 让领域事件 SSE 和日志 SSE 的 401 使用同一事件；保留它们“认证失败后停止自动重连”的现有行为。
- invalidation 必须幂等：清 CSRF、停 refresh timer、停领域流和日志流、阻止 mutation、切换顶层状态只执行一次。
- Catalog/runtime/incidents Store 对认证失效不再重复写局部业务错误；普通 4xx/5xx 保持现有可追踪错误展示。
- 顶层页面区分 `expired`、`bootstrap-invalid`、`unreachable`。expired 明确说明需执行 `stackpilot open` 并使用新页面；不显示无效的普通“重试”。
- unreachable 可执行有界重新检测；一旦得到 401，转为 expired，而不是无限探测。
- 恢复后必须重新拉 REST 快照再启动 SSE，不复用失效前的前端推断状态。

### 测试

- REST 401、领域 SSE 401、日志 SSE 401 分别和同时发生时只触发一次 invalidation。
- 403 CSRF/Origin 拒绝、404、409 和 500 不错误地转成 session expired。
- 验证流和 timer 停止、mutation 禁用、重新初始化后的快照顺序。
- 组件/浏览器测试覆盖键盘焦点、屏幕阅读文本、长 `traceId`、窄屏和无重叠。

### UX Gate

- 用户不再在业务区域只看到裸 `AUTH_SESSION_INVALID`；页面给出真实可执行且不降低安全性的恢复路径。
- 失效后无请求风暴、SSE 重连风暴或 mutation 重放。
- 临时网络错误与认证失效可明确区分。

## 10. AS-05：控制面停止诊断

### 调查

- 使用隔离安装根、隔离数据根和非默认端口，逐条复现 service start、stop、upgrade、登录启动等价路径、Ctrl+C/SIGTERM、control Pipe stop 和 HTTP serve error。
- 检查 `exec.CommandContext` + detached process + `Process.Release` 在调用 CLI context 结束后的真实 Windows 行为，不能只凭文档推断子进程存活。
- 检查 `RunInstalled`、control server、Server `Shutdown` 和 main signal context 的收口顺序，记录每条路径的 exit code 与现有日志。
- 对没有 stop 日志的强制终止 fixture，只记录“未观察到正常退出事件”；不得推断具体外部终止者。

### 最小实现

- 为正常可观察路径增加结构化 lifecycle reason code，例如 `control_stop`、`signal`、`upgrade`、`serve_error`、`startup_error`、`normal_exit`；最终命名在 AS-00 固定。
- 启动事件记录安全的 PID、版本和 UTC；退出事件记录 reason、exit code/error code，不记录完整命令或用户路径。
- 如 AS-00 批准运行 marker，使用数据根下固定文件名、原子替换和受限 ACL，启动时只报告上次是否缺少正常收口；不得存敏感内容。
- 不改变 HKCU Run、安装目录 marker、control Pipe 鉴权或强制停止身份验证。

### Lifecycle Gate

- 正常 stop、upgrade、signal、serve error 和 startup error 能从日志确定分类。
- 非正常终止不会被误报为正常退出，也不会产生虚假具体原因。
- 真实 Windows fixture 无遗留进程、Run 注册项、端口、安装根或数据根。
- 若发现根因需要 watchdog/任务计划程序/Windows Service，形成独立缺陷和 ADR-0003 变更建议，本计划不顺带实现。

## 11. AS-06：集成、安全回归与交付

### 自动验证命令基线

按实际脚本和包名校准后至少执行：

```powershell
gofmt -w <本次修改的 Go 文件>
go test ./internal/security ./internal/api ./internal/platform/windows/usertask ./cmd/stackpilot -count=1
go test -race ./internal/security ./internal/api -count=1
go vet ./...
npm run type-check
npm run test:web
npm run build
powershell -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

若仓库固定 Go 工具链不在 PATH，使用 `docs/development.md` 指定的锁定工具链；不得把未经执行的命令写成通过。`-race` 在当前 Windows 工具链不可用时，记录真实限制并用并发定向测试补充，但不能隐瞒缺口。

### 浏览器 Gate

- 使用可注入短 TTL 或测试时钟加速验证跨越旧 15 分钟边界，不在 CI 固定 sleep 15 分钟。
- 验证活跃续期、连续 30 分钟无成功续期、8 小时绝对到期、页面刷新、后台恢复和多标签并发。
- 验证服务重启和 token rotate 后旧页面统一失效，重新运行 `stackpilot open` 可建立新会话。
- 验证 Cookie HttpOnly、fragment 清除、Origin/CSRF/no-store，以及 local/session storage/IndexedDB 无认证材料。

### 安全扫描

- 扫描服务日志、测试输出、浏览器 trace/screenshot、数据库/WAL/SHM 和 fixture 数据根，确认没有 Authorization、长期令牌、Cookie、CSRF、bootstrap、完整环境或用户真实路径。
- 检查错误 envelope 和 UI 不暴露内部命令、令牌或敏感路径。
- 检查所有测试只使用隔离数据根，结束后无进程、端口、注册项或临时安装遗留。

### 文档与证据

- 完成 ADR-0002、详细设计、OpenAPI、开发/恢复说明和 Phase 1D 补充 evidence。
- `docs/error-codes.md` 即使无需改变 code/HTTP 映射，也记录已核对；不要为了文档同步制造新错误码。
- 控制面生命周期实现若未改变架构，只补充诊断 evidence；若设计变化，必须先另行更新 ADR-0003。
- 记录所有实际测试命令、退出码、关键断言、未执行项和环境限制。

### 专项完成 Gate

- AS-00 至 AS-05 Gate 全部通过，无未决安全竞态。
- 活跃会话跨越旧 15 分钟边界，续期窗口/绝对到期行为准确。
- REST/SSE 失效统一收口且恢复路径真实可用。
- 全量 Go/Web 检查与真实 Playwright/Windows Gate 通过。
- 文档、契约、实现和证据一致，且 `AGENTS.md` 与 `CLAUDE.md` 哈希一致。

## 12. 相对排期、依赖与关键路径

当前没有已授权的实名责任人和日历截止日期，不编造负责人或承诺日期。执行责任统一记为“实现 Agent”，安全/设计批准角色在 AS-00 开始前指定。

| 工作包 | 参考工作量 | 前置条件 | 可并行项 |
| --- | --- | --- | --- |
| AS-00 | 1-1.5 人日 | 安全/设计审批者可用 | 控制面只读调查可同步进行 |
| AS-01 | 1-1.5 人日 | Design Gate | AS-05 fixture 准备 |
| AS-02 | 1 人日 | AS-01 Security Gate | 无 |
| AS-03 | 1.5-2 人日 | AS-00 协议、AS-02 DTO 稳定 | AS-05 |
| AS-04 | 1-1.5 人日 | AS-03 Web Unit Gate | AS-05 |
| AS-05 | 1-2 人日 | AS-00 reason 分类 | AS-01..04 |
| AS-06 | 1.5-2 人日 | 前述 Gate 全部通过 | 文档扫描可与测试收口交错 |

- 认证关键路径为 AS-00 -> AS-01 -> AS-02 -> AS-03 -> AS-04 -> AS-06。
- AS-05 可在 AS-00 后与认证实现并行，但其失败不得用延长会话掩盖。
- 基准工作量约 8-11.5 人日，风险集中在 CSRF 多标签竞态、浏览器后台 timer 节流、测试时钟接线和 Windows detached process 复现。

## 13. 建议执行顺序

1. 完成 AS-00 并批准 ADR/并发协议；未通过 Design Gate 不改生产认证代码。
2. 完成 AS-01 服务端状态和确定性时间测试。
3. 完成 AS-02 Cookie/API/OpenAPI 闭环，先稳定契约再接前端。
4. 完成 AS-03 单飞续期与 timer，使用 fake timer 关闭竞态。
5. 完成 AS-04 REST/SSE 全局收口与可访问恢复页面。
6. AS-05 在隔离 Windows 环境完成停止分类；需要架构改变时另立工作包。
7. AS-06 执行全量回归、真实浏览器/Windows Gate、安全扫描和文档证据收口。

任何工作包未通过自身 Gate，都不得以延长默认 TTL、吞掉 401、自动刷新页面、无限重试或重新发送 mutation 作为临时完成方案。
