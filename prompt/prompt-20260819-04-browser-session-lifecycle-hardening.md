# StackPilot 浏览器会话生命周期与失效恢复 Prompt

> 状态：待执行
> 日期：2026-08-19
> 适用仓库：`E:\StackPilot`
> 工作包性质：Phase 1D 本地认证与浏览器安全的维护性加固，不新增远程访问或多用户能力
> 计划文件：`plan/plan-20260819-04-browser-session-lifecycle-hardening.md`

## 1. 任务

修复 Web 控制台在持续使用一段时间后固定出现 `AUTH_SESSION_INVALID: The browser session is invalid or expired.` 且无法在当前页面正确收口的问题。将当前 15 分钟绝对会话改为有上限的滑动会话，建立到期前续期、REST/SSE 统一失效处理和明确的重新认证体验，同时保留一次性 fragment bootstrap、HttpOnly Cookie、同源 Origin、CSRF 绑定、服务重启失效和令牌轮换失效等安全边界。

此外，对本机已观察到的控制面异常停止建立独立诊断闭环：区分用户/升级触发的正常停止、信号停止、HTTP serve 错误和没有完整退出记录的非正常终止。该诊断不得与浏览器会话续期混为同一根因，也不得在没有 ADR 和真实 Windows Gate 的情况下引入 watchdog、自动拉起或更改当前用户后台进程架构。

## 2. 开始前必须遵守

1. 完整读取并遵守 `AGENTS.md`、`CLAUDE.md` 和 `code_rule.md`。
2. 定向读取 `docs/adr/0002-web-authentication-bootstrap.md`、`docs/adr/0003-windows-user-background-task.md`、`docs/detailed-design.md` 的认证/CSRF、控制面生命周期、安全、日志和测试章节，以及 Phase 1D 的 progress/evidence。
3. 完整核对 `internal/security/auth.go`、`internal/api/auth.go`、`internal/api/router.go`、`web/src/api/client.ts`、`web/src/api/stream.ts`、相关 Pinia Store、`web/src/App.vue`、`cmd/stackpilot` 和 `internal/platform/windows/usertask` 的实现、调用方与测试。
4. OpenAPI 是 REST 契约事实来源。变更会话刷新语义时，同一变更必须同步 OpenAPI、DTO、错误码说明和契约测试；设计语义变化必须先更新 ADR-0002 和详细设计。
5. 保留用户已有修改；不初始化 Git，不创建提交，不推送、变基或改写历史。

## 3. 已确认事实与问题

- `defaultSessionTTL` 当前固定为 15 分钟。
- `browserSession` 只保存 CSRF 摘要和单个 `expiresAt`，没有创建时间、绝对上限或滑动续期窗口。
- `RefreshCSRF` 会轮换 CSRF，但返回原来的 `expiresAt`，不会延长服务端会话。
- `GET /api/v1/auth/session` 不重新设置 Cookie；即使未来只延长服务端状态，浏览器 Cookie 仍会按旧 `Max-Age` 消失。
- Web 只在首次挂载时调用 `initializeAuthentication`，没有按 `expiresAt` 调度续期，也没有恢复前台时的过期检查。
- REST 请求各自抛出 `APIError`；Store 会把错误显示在局部区域，但 `AUTH_SESSION_INVALID` 没有统一切换顶层认证状态。
- SSE 已在认证失败后停止重连，但没有与顶层认证状态共享统一失效通知。
- 当前顶层“重试”会再次调用 `GET /auth/session`。Cookie 已过期、控制面已重启或令牌已轮换时，这个动作不可能建立新会话。
- 浏览器不能安全地自行签发 bootstrap；新会话仍必须由 `stackpilot open` 使用 OS 保护的长期令牌创建。
- ADR-0002 当前明确规定服务重启和令牌轮换立即使内存 bootstrap/session 失效，此行为必须保留。
- 2026-08-19 本机平台日志记录了 `14:16:46` 和 `14:17:03` 的正常停止，但最近一次启动后后台状态又变为 `stopped` 且没有对应停止记录。此证据只能说明需要调查非正常终止，不能据此编造退出原因。
- 当前平台日志没有足够的启动来源、关闭触发源和运行收口信息，原始 HTTP `traceId` 也不能单独还原进程退出原因。

## 4. 固定安全与产品决策

### 4.1 会话时限

- 浏览器会话滑动续期窗口固定为 30 分钟；30 分钟内没有成功续期即失效。该窗口不等同于对键盘、鼠标等“用户空闲”的监控。
- 单次 bootstrap 创建的会话绝对最长存活 8 小时；任何续期都不得越过该上限。
- 有效期计算统一为 `min(now + 30m, absoluteExpiresAt)`，所有时间使用 UTC；测试使用注入时钟，不依赖本机时区或真实 sleep。
- 续期成功必须同时原子更新服务端会话期限、返回新的 `expiresAt`，并重新设置同名 HttpOnly Cookie 的 `Max-Age`/`Expires`。
- 到达绝对上限、服务重启、显式注销或令牌轮换后，必须重新执行 `stackpilot open`；不得静默降级认证。

### 4.2 认证材料边界

- 长期本地令牌继续只存在于当前用户 OS 保护存储和 CLI 进程中，不进入浏览器、URL query、localStorage、sessionStorage、IndexedDB、日志或 DTO。
- bootstrap 继续使用 URL fragment、60 秒、单次有效、仅在服务端内存保存摘要，并在交换后立即从地址栏移除。
- session Cookie 继续使用 `HttpOnly`、`SameSite=Strict`、`Path=/`；Phase 1 回环 HTTP 不伪装使用 `Secure`。
- CSRF 值继续只保存在前端内存并与当前会话绑定；浏览器 mutation 继续要求精确 loopback Origin、JSON Content-Type 和 `X-StackPilot-CSRF`。
- 不增加匿名续期、query token、浏览器读取 DPAPI、本地长期 refresh token或从旧失效 Cookie 重新签发会话的路径。

### 4.3 会话续期并发

- 前端必须使用单飞续期：同一页面同一时刻最多一个 `/auth/session` 刷新请求，所有等待者共享结果。
- 必须在设计中明确 CSRF 轮换与并发 mutation 的顺序，保证刷新不会让已经排队或正在发送的合法 mutation 随机得到 403。
- 推荐由集中 API 会话协调器串行化“刷新临界区”和 mutation 发出阶段；如果选择短期保留前一 CSRF 或改变刷新时的 CSRF 语义，必须先在 ADR-0002 中说明安全理由、窗口上限和测试，不得隐式改变。
- 多标签页不共享 CSRF 内存。每个标签页可通过现有有效 Cookie 调用刷新接口取得自己的当前 CSRF，但不能因此突破同一服务端 session 的绝对期限。若共享 session 下的跨标签 CSRF 轮换存在冲突，必须在 Design Gate 明确解决方案后再实现。

### 4.4 失效体验

- `AUTH_SESSION_INVALID` 是认证状态迁移，不是普通业务错误。REST 和 SSE 任一路径发现后，都必须幂等触发同一个全局失效动作。
- 全局失效动作至少清空前端 CSRF、取消续期 timer、停止领域事件和日志流、阻止新的 mutation，并将顶层状态切换为“会话已失效”。
- 页面必须明确区分：会话失效、bootstrap 无效、控制面不可达、普通业务失败。不得把所有错误都显示为“无法建立本地会话”。
- 对已失效会话不得提供会重复发送无效 Cookie 的普通“重试”。恢复说明必须明确要求重新运行 `stackpilot open` 并使用新打开的页面。
- 控制面暂时不可达时可以提供有界的“重新检测”；网络恢复后若原内存会话仍有效可继续，否则进入会话失效状态。不得无限高频重试。
- 错误 UI 使用 Element Plus 现有组件和图标，支持键盘/焦点，状态不能只靠颜色，长错误码和 `traceId` 不得溢出。

## 5. 服务端会话模型要求

- `browserSession` 至少能表达创建时间/绝对期限、当前滑动期限和 CSRF 绑定状态；字段命名以实现评审后的最小模型为准。
- 交换 bootstrap 时创建绝对期限和初始滑动期限，返回当前滑动 `expiresAt`；如对前端公开绝对期限，必须先加入 OpenAPI 且说明用途，否则保持服务端内部字段。
- 刷新必须在同一 mutex 临界区完成存在性检查、时限检查、期限推进和 CSRF 状态变化，不能留下部分更新。
- `AuthenticateSession`、`ValidateCSRF` 和 prune 对边界时刻使用一致的严格比较；恰好到期的会话视为失效。
- 无效刷新不得复活、延长或重新插入已过期/已撤销会话。
- `maxSessions`、有界 prune、摘要存储和令牌轮换清空行为保持有效；不得新增无界会话历史。
- Cookie 剩余秒数使用服务器的同一时钟基准计算，避免测试时钟与 `time.Until` 的真实时钟混用；小于等于零时必须过期 Cookie，不能强制产生 1 秒“复活窗口”。
- 注销和令牌轮换应设置明确的过去 `Expires` 与 `Max-Age=-1`，并维持 `Path`、`HttpOnly`、`SameSite` 属性一致。

## 6. API 与契约要求

- 保留 `/api/v1/auth/session` 的 POST/GET/DELETE 资源路径，除非 Design Gate 证明现有 GET 刷新语义无法安全兼容；不得新增第二套隐式认证入口。
- GET 的语义从“仅轮换 CSRF”调整为“验证并续期浏览器会话，同时返回可用 CSRF 和当前滑动期限”。
- GET 续期成功必须发送更新后的 `Set-Cookie`；POST 交换和 DELETE/令牌轮换的 Cookie 属性必须与之保持一致。
- 所有认证响应继续使用 `Cache-Control: no-store`，不返回 session Cookie 明文、长期令牌、bootstrap 摘要或内部时间字段。
- `AUTH_SESSION_INVALID` 保持稳定错误码和 401；无需为普通到期创建新错误码。前端可根据已有错误码决定恢复路径。
- OpenAPI summary/description、响应头/Cookie 行为、`SessionResponse` 字段和 Unauthorized 描述必须与实现一致。
- API 契约测试必须覆盖交换、续期后新过期时间、Cookie 更新、绝对上限、无效会话不续期、注销和轮换失效。

## 7. Web 会话协调器要求

- 认证生命周期从 `App.vue` 的一次性局部函数中拆出，放入职责明确的 Pinia Store 或 composable；`web/src/api` 继续负责协议和请求封装，不允许页面散落 fetch/错误码判断。
- 协调器至少维护 `initializing/ready/refreshing/expired/unreachable` 状态、当前 `expiresAt`、单飞 refresh promise、timer owner 和统一 invalidation callback。
- 续期时间基于服务器返回的 `expiresAt` 计算，并预留明确安全窗口；不得固定每 N 分钟盲刷。推荐在剩余 5 分钟时刷新，同时在页面恢复可见、窗口获得焦点和发送 mutation 前检查。
- timer 必须有唯一 owner，组件卸载、会话失效和重新 bootstrap 时释放；不得泄漏 ticker/timeout 或形成并发刷新风暴。
- 系统休眠、浏览器后台节流、时钟跳变和网络短暂失败后，页面恢复时必须重新比较服务器期限；客户端时间只能用于调度，服务端仍是有效性权威。
- 刷新网络失败使用有界退避且不得越过已知 `expiresAt`；到期仍未成功时进入失效状态。401 不重试，403 不伪装成会话到期。
- REST wrapper 和 SSE stream 通过统一、类型化的认证事件通知协调器；不得用错误字符串匹配。
- 所有 Store 在会话失效后停止新加载/变更，并避免多个局部 Alert 重复刷同一个认证错误；恢复新页面后以 REST 快照重新建立状态。
- 不在浏览器保存长期认证材料；页面刷新时可使用仍有效的 HttpOnly Cookie，通过 GET 重新取得内存 CSRF。

## 8. 控制面停止诊断要求

- 先复现和分类，不先实现自动重启。检查 `service start/stop/upgrade`、登录启动、信号取消、control Pipe stop、HTTP serve error、父 CLI 退出和异常终止路径。
- 平台日志增加安全的结构化生命周期事件，至少能区分启动、收到停止请求、信号取消、升级触发停止、HTTP serve 失败、正常退出和启动失败；适用时记录 UTC、PID、版本、退出码/错误类别和安全的 reason code。
- 日志不得包含控制 Pipe secret、Authorization、Cookie、CSRF、bootstrap、完整命令行、任意工作区路径或未脱敏环境。
- 必须明确“没有退出事件”只能推断为未完成正常收口，不能编造 crash 原因。若要通过运行 marker 识别上次非正常退出，先定义原子写入、权限、路径、升级兼容和崩溃窗口；不为此修改 SQLite schema，除非设计证明必要。
- 当前 HKCU Run 只负责登录激活，不是 watchdog。自动恢复控制面、改用任务计划程序/Windows Service或新增守护进程均不在本工作包实现范围；如调查证明必须改变，先更新 ADR-0003 并另立工作包。
- 使用隔离 `--data-dir` 和可控 fixture 做真实 Windows 验证，不访问或修改用户真实数据目录，不遗留后台进程、注册项、端口或日志。

## 9. 测试要求

### 9.1 Go 单元与 API 测试

- 注入时钟覆盖：初始 30 分钟、到期前续期、连续续期、8 小时截断、恰好到期、超过绝对上限、prune、撤销、服务重建和令牌轮换。
- 覆盖并发刷新、刷新与认证/CSRF 校验并发、`maxSessions` 和 race detector；不得依赖 sleep 证明时序。
- handler 测试断言 POST/GET/DELETE/rotate 的完整 Cookie 属性、`Set-Cookie` 剩余期限、`Expires`、`no-store`、Origin、JSON Content-Type 和错误码。
- 契约测试核对 OpenAPI operation、response schema、401 和 Cookie 行为。

### 9.2 Web 单元与组件测试

- 使用 fake timer 覆盖到期前调度、单飞刷新、成功推进、绝对上限、后台节流后恢复、网络失败有界重试、卸载清理和时钟跳变。
- 覆盖刷新与 mutation 竞争，证明合法 mutation 不因 CSRF 轮换随机失败，失败请求不会被无界重复提交。
- REST 与领域事件/日志 SSE 分别触发 401 时，统一且只触发一次全局失效；403 和普通业务错误保持原路径。
- 覆盖页面刷新后用有效 Cookie 恢复 CSRF、无 Cookie 显示明确恢复动作、bootstrap 失效、控制面不可达后重新检测。
- 组件测试检查可访问性、焦点、按钮状态、长错误文本和小视口布局。

### 9.3 真实浏览器与 Windows Gate

- Playwright 使用隔离 Server 验证会话持续超过旧 15 分钟边界仍可用；测试应通过可注入短 TTL 或测试专用时钟加速，不能让 CI 固定等待 15 分钟。
- 验证页面刷新、睡眠/恢复模拟、并发 REST/SSE、绝对上限、服务重启、令牌轮换和重新执行 `stackpilot open` 的完整流程。
- 验证地址 fragment 立即清除，Cookie 仍不可被 JavaScript 读取，浏览器存储中没有长期令牌、bootstrap 或 Cookie 明文。
- Windows 生命周期 fixture 覆盖正常 stop、upgrade restart、signal/serve error和非正常终止识别；结束后确认无遗留进程、注册项、端口和隔离数据。

## 10. 文档与证据同步

同一实现变更至少同步：

- `docs/adr/0002-web-authentication-bootstrap.md`
- `docs/detailed-design.md`
- `api/openapi.yaml`
- `docs/error-codes.md`（语义不变也要确认无需修改）
- `docs/development.md` 或用户恢复说明
- Phase 1D 对应 progress/evidence，以新增补充记录描述维护性变更，不改写旧 Gate 的历史事实
- 若控制面生命周期决策发生变化，同步 `docs/adr/0003-windows-user-background-task.md` 和对应 Windows evidence

## 11. 明确不做

- 不允许浏览器读取或保存长期本地令牌。
- 不实现匿名续期、永久会话、“记住我”、远程登录、RBAC 或多用户会话。
- 不持久化浏览器 session/CSRF 到 SQLite；服务重启仍使其失效。
- 不改变精确 loopback Origin、CSRF、JSON Content-Type 或 SameSite 安全要求。
- 不自动重复可能产生副作用的业务 mutation。
- 不因会话到期停止、重启或修改任何受管业务系统。
- 不在本工作包引入控制面 watchdog、Windows Service、计划任务重构或自动恢复架构。

## 12. 完成定义

只有同时满足以下条件才能声明完成：

1. 活跃 Web 会话可以安全续过旧 15 分钟边界，连续 30 分钟没有成功续期或达到 8 小时绝对上限后确定失效。
2. 续期原子推进服务端期限并更新 Cookie，绝不复活已到期、撤销、重启失效或轮换失效的会话。
3. CSRF 轮换与并发 REST mutation、多标签页行为有明确设计和确定性测试，不产生随机 403。
4. REST/SSE 的 `AUTH_SESSION_INVALID` 统一收口，timer/stream/mutation 停止且页面给出可执行的重新认证路径。
5. 浏览器仍无法取得长期令牌或 HttpOnly Cookie，bootstrap/Origin/CSRF/no-store 边界全部回归通过。
6. OpenAPI、ADR、详细设计、错误码说明、前端 DTO 和契约测试与实现一致。
7. 控制面正常停止路径具有安全、结构化、可分类的生命周期记录；对无退出记录场景不编造原因。
8. Go 单元/API/race、Web 单元/组件、生产构建、真实 Playwright 和适用的 Windows 生命周期 Gate 均通过，实际命令和环境限制已记录。
9. 验证未产生令牌、Cookie、CSRF、bootstrap、完整命令或用户真实路径泄漏，且无遗留进程、端口、注册项和测试数据。
