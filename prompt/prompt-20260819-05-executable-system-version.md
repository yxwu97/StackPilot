# StackPilot 可执行文件与系统版本管理 Prompt

> 状态：已执行
> 日期：2026-08-19
> 适用仓库：`E:\StackPilot`
> 工作包性质：Phase 0 构建/发布基线与 Web 控制台维护性增强
> 计划文件：`plan/plan-20260819-05-executable-system-version.md`

## 1. 任务

为 `stackpilot.exe` 建立唯一、可验证、可自动递增的产品版本，并在 StackPilot Web 控制台中把该值显示为“系统版本”。版本必须来自当前正在运行的可执行文件，CLI、`GET /version`、安装/升级信息、发布制品名称和 Web 展示不得各自维护互相漂移的版本值。

现有仓库已经具备 `internal/buildinfo`、`stackpilot.exe version`、公开 `GET /version`、PowerShell 构建脚本和 GoReleaser `ldflags` 注入基础。本任务必须复用并收敛这些能力，不新增第二套版本接口或由前端构建变量冒充运行中 exe 版本。

## 2. 开始前必须遵守

1. 完整读取并遵守 `AGENTS.md`、`CLAUDE.md` 和 `code_rule.md`，确认两份 Agent 指令逐字一致。
2. 定向核对 `internal/buildinfo`、`cmd/stackpilot/main.go`、`cmd/stackpilot/server.go`、`internal/api/router.go`、`api/openapi.yaml`、`scripts/build.ps1`、`scripts/check.ps1`、`.goreleaser.yml`、CI/Release workflow、根/前端 package manifest、`web/src/api`、`web/src/App.vue`、Vite proxy 和相关测试。
3. API 以 OpenAPI 为契约事实来源；版本字段语义或格式变化必须同步 DTO、示例和契约测试。
4. 保留用户已有修改；不初始化 Git，不自动创建提交、tag 或 release，不推送、变基或改写历史。
5. 只修改本任务必要文件；不得借版本功能调整安装、进程监管、认证、监听范围或受管系统生命周期语义。

## 3. 已确认事实

- `internal/buildinfo.Version` 当前默认是 `dev`，可由 Go `-ldflags -X` 覆盖。
- `stackpilot.exe version` 已输出版本、commit 和 build time。
- Server 已把 `buildinfo.Current()` 注入 API，公开根路径 `GET /version` 已返回 `version`、`commit`、`buildTime`、`apiVersion` 和 capabilities。
- OpenAPI 已登记 `/version` 和 `VersionResponse`，但 `version` 目前只约束为任意字符串，示例仍为 `dev`。
- `scripts/build.ps1` 未显式传入版本时使用 `STACKPILOT_VERSION`，否则回退为 `dev`；因此普通 `npm run build` 不能保证得到数字产品版本。
- GoReleaser 从 Git tag 解析版本并注入二进制，但仓库当前没有正式 tag，snapshot 仍可能产生 `0.0.0-SNAPSHOT-*`。
- 根 `package.json` 和 `web/package.json` 当前均为 `0.0.0`；这些是 npm workspace 元数据，不应成为运行中 exe 版本的独立事实来源。
- Web API client 只以 `/api/v1` 为 base，而产品版本接口位于根 `/version`；Vite 开发代理当前只代理 `/api`。
- Web 侧尚无 `VersionResponse` DTO、版本加载逻辑或“系统版本”展示。
- 当前侧边栏底部已有“控制面在线”和地址区域，适合增加紧凑的系统版本文本，但最终布局必须通过桌面和窄视口检查。

## 4. 版本契约与术语

### 4.1 格式

- 产品版本使用三段非负整数：`MAJOR.MINOR.PATCH`，例如 `0.1.0`、`1.2.3`。
- 用户提出的 `X.XX.XX` 视为“三段数字版本”的示意，不要求补零。为兼容 SemVer、GoReleaser、Git tag 和常用包工具，数字段除单独的 `0` 外不得有前导零。
- 本工作包建立基线时使用 `0.1.0`。项目达到稳定发布标准后再由明确发布决策提升到 `1.0.0`，不得在本任务中虚构成熟度。
- 正式产品版本只接受 `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`。Git release tag 使用精确的 `v<version>`，例如 `v0.1.0`。
- `dev`、`unknown`、`*-SNAPSHOT-*` 只能用于未发布的显式开发/诊断路径，不能成为 `npm run build` 生成的可交付 `dist/stackpilot.exe` 的系统版本。

### 4.2 唯一事实来源

- 在仓库根建立单一、受版本控制的纯文本版本文件，建议命名为 `VERSION`，内容只包含一行规范版本和结尾 LF，例如 `0.1.0`。
- `VERSION` 是源码树内产品版本的唯一事实来源。构建脚本、检查脚本、GoReleaser/Release workflow 和必要的 package manifest 同步值都从它派生或与它校验。
- `internal/buildinfo.Version` 仍是编译进运行中 exe 的值，运行时不得读取工作区 `VERSION` 文件。安装后的二进制必须自包含，不依赖源码目录。
- Web 必须请求当前 Server 的 `GET /version`，显示响应中的 `version`；不得使用 `package.json`、Vite define、静态 HTML、浏览器缓存中的旧值或前端自行拼接值替代。
- OpenAPI 文档自身的 `info.version` 表示 API 文档版本，不得在没有 API 契约版本决策时被脚本盲目当作产品版本反复改写。

### 4.3 “每次修改后自动升级”的确定语义

- “每次修改”定义为一次准备交付、合并或构建候选制品的产品变更集，不是编辑器每次保存、格式化产生的临时变化，也不是每执行一次测试或构建命令。
- 默认自动升级规则为 PATCH 加一：`0.1.0 -> 0.1.1`。MINOR/MAJOR 只能由显式发布参数或经批准的兼容性决策提升；提升 MINOR 时 PATCH 归零，提升 MAJOR 时 MINOR/PATCH 归零。
- 提供一个仓库内版本命令作为唯一写入口，例如 `scripts/version.ps1`：读取并严格解析 `VERSION`，计算下一版本，原子更新所有需要同步的受控文件，并在失败时不留下部分更新。
- 提供统一 npm 命令封装该脚本，例如 `npm run version:bump`，默认 bump patch，并支持显式 `-Part minor|major`。实际命名可按现有脚本风格调整，但不得存在多套不一致入口。
- 每个产品变更集只递增一次。版本命令以显式 base ref 或未提交场景下的 `HEAD` 为基线：若当前 `VERSION` 已高于基线且同步检查通过，必须返回可识别的 `already-bumped` 结果而不是再次递增；没有产品变化时拒绝无依据 bump。版本命令必须报告 old/new version 和基线；普通 `npm run build`、`npm run check`、Go 单元测试、Web 构建和重复执行 CI 不得隐式修改源码或再次递增。
- CI/本地检查必须强制执行“产品文件发生变化时，`VERSION` 相对基线也必须变化”以及“版本只前进、不回退”。因仓库可能没有可用远端基线，检查脚本需支持显式 base ref；无法确定基线时至少完成格式、同步和构建注入一致性校验，不得编造比较结果。
- 纯文档、Prompt/Plan、历史 evidence、测试输出或被忽略生成物是否触发 bump，必须由一份集中、可审查的路径策略确定。默认：生产源码、构建/发布脚本、API 契约、Web 生产代码和用户文档变化触发；`prompt/`、`plan/`、`docs/evidence/`、测试缓存、`dist/`、`output/` 不单独触发。
- 不安装或静默修改开发者全局 Git hook，不依赖只在某台机器存在的 hook，不由构建脚本自动提交或打 tag。自动化含义是“自动计算、同步并由 Gate 强制”，不是未经授权修改 Git 历史。

## 5. 构建与发布要求

- `scripts/build.ps1` 默认从 `VERSION` 读取产品版本并注入 `stackpilot/internal/buildinfo.Version`；仍可为受控测试提供显式 `-Version` 覆盖，但覆盖值必须满足格式，且构建日志明确标记 override。
- 可交付 `dist/stackpilot.exe` 禁止静默回退到 `dev`。`VERSION` 缺失、空白、含 BOM/额外行、格式非法或同步值漂移时构建立即失败。
- commit 和 UTC build time 继续由现有链路注入；不得把版本自动递增与伪造 commit/build time 混为一体。
- 构建完成后自动执行产物自检：运行新 exe 的 `version` 命令，解析第一行并断言与本次读取的 `VERSION` 完全一致。不得只检查 linker 参数文本。
- GoReleaser 必须使用与 `VERSION` 完全一致的 tag。Release workflow 在发布前校验 `GITHUB_REF_NAME == v<读取的 VERSION>`；不一致时阻断发布。
- snapshot/CI artifact 必须有明确策略：要么注入 `VERSION` 作为系统版本并把 snapshot 信息留在 commit/buildTime，要么使用单独受控的 prerelease 显示字段。不得让 Web、CLI 和 archive filename 对同一 exe 报告三个不同版本。
- 若保留根与 Web package manifest 的 `version` 字段，版本命令必须以结构化 JSON 读写并同步 lockfile，不得用正则替换 JSON。若确认 private workspace 的 package version 无产品含义，可保持固定，但需由检查文档明确说明，不能偶尔手工同步。
- 版本升级不得修改 migration 历史、安装 marker schema 或 Supervisor 协议版本。产品版本、API version、清单 apiVersion、SQLite migration version 和 Supervisor protocol version 是不同概念。

## 6. CLI、API 与安装一致性

- `stackpilot.exe version` 输出的产品版本必须与 `GET /version.version` 完全相同；格式保持便于人工读取，并保留 commit/build time。
- `GET /version` 继续为根路径、只读、无 Secret 的可用性接口，不为 Web 展示另增 `/api/v1/system-version`。
- `VersionResponse.version` 在 OpenAPI 中增加三段数字 pattern 和数字版本示例；`commit`、`buildTime`、`apiVersion` 和 capabilities 语义保持不变。
- 当前安装/升级状态若已经记录候选 exe 版本，必须从候选二进制的受信输出或现有 buildinfo/marker 链路取得，不从用户可编辑路径或前端输入取得。
- 升级时允许版本前进；同版本重装、降级和测试 override 的策略必须先核对 ADR-0003/安装实现后固定，不能仅凭字符串字典序比较。比较必须解析数值三元组。
- 本工作包不要求新增数据库字段；版本是构建身份，不应为了 UI 展示写入 SQLite。

## 7. Web 系统版本展示

- 在 `web/src/api` 新增显式 `VersionResponse` DTO 和只读 `getVersion()`，请求根路径 `/version`。页面不得直接散落 `fetch`。
- 因开发环境前后端端口不同，Vite dev proxy 必须显式代理精确 `/version` 到本地控制面，同时保持现有 `/api` 代理。
- Web 显示值使用标签“系统版本”，值建议显示为 `v0.1.0`；前导 `v` 仅为展示，DTO 和 exe 内部规范值仍为 `0.1.0`。
- 将版本放在侧边栏底部控制面状态附近，形成紧凑、可扫描的运行身份信息，不新增营销式卡片或占用主工作流空间。窄视口下不得与地址、导航或状态文本重叠。
- version 请求应与应用初始化协调，但版本加载失败不得把已认证的系统管理主流程整体置为不可用。失败时显示稳定占位（例如 `--`）或“未知”，并提供 tooltip/可访问文本；不得伪造 `dev` 或缓存旧版本。
- Web 只展示当前响应的产品版本。commit/build time 可以保留在 API DTO 供未来诊断，但本工作包默认不在常驻界面暴露长 commit 或时间，避免拥挤。
- 不使用颜色作为版本含义，不让版本文本成为按钮；如需要完整构建信息，只能通过已有 Element Plus Tooltip/Popover，并确保键盘可访问。

## 8. 自动升级并发与原子性

- 版本命令获取仓库内有界互斥，避免两个终端同时从同一旧版本计算出不同的部分结果。锁文件必须位于被忽略的仓库缓存目录，异常退出后可安全识别并恢复，不能永久阻塞。
- 计算和校验全部完成后再写入；多文件同步采用临时文件加原子替换或具备回滚的明确流程。任一写入失败必须返回非零并报告受影响文件。
- 版本比较按任意精度非负整数或明确上限解析，禁止字符串比较导致 `0.9.9 > 0.10.0` 的错误。
- 不允许通过环境变量把非法或低于 `VERSION` 的值静默注入正式制品。测试 override 必须显式启用，并与正式发布 Gate 隔离。
- 自动化脚本不得扫描或改写 `dist/`、用户数据目录、安装目录、工作区清单或 Git 历史。

## 9. 测试要求

### 9.1 版本脚本

- 覆盖合法 patch/minor/major：`0.1.0 -> 0.1.1`、`0.1.9 -> 0.2.0`、`0.9.9 -> 1.0.0`。
- 覆盖缺失、空文件、BOM、额外行、前导零、负数、预发布后缀、溢出/超大数字和同步文件漂移。
- 覆盖并发调用只允许一个成功更新，失败调用不产生部分写入。
- 使用隔离临时副本测试，不修改真实仓库版本，不依赖用户全局 Git 配置。
- 覆盖变更路径策略：生产文件变化要求 bump，只有明确排除路径变化不要求 bump，版本回退或未变化正确失败。

### 9.2 Go 与 API

- `internal/buildinfo` 覆盖规范版本注入和默认开发值边界。
- CLI 测试断言 `version` 输出；API handler 测试断言相同 `BuildInfo.Version` 被原样返回。
- OpenAPI 契约测试断言 `/version` 路由、schema required 字段、version pattern 和示例。
- 构建集成测试生成隔离 exe，分别运行 CLI 和临时 Server `/version`，断言两处值与 `VERSION` 完全一致。

### 9.3 Web

- API 测试覆盖根 `/version` 路径、成功解码、非 2xx、非法/缺失字段和网络失败。
- 组件测试覆盖加载中、成功显示 `系统版本 v0.1.0`、失败占位、长值防溢出和不影响主页面可用性。
- Vite 开发代理使用真实本地目标验证 `/version`，不能只测试 `/api/v1`。
- Playwright 在桌面和窄视口启动真实构建后的 exe，确认页面展示值等于同一 exe CLI 输出和 `/version` 响应，且布局不重叠。

## 10. 文档与同步要求

同一实现变更至少核对并按实际变化同步：

- `api/openapi.yaml`
- `docs/detailed-design.md`
- `docs/development.md`
- `docs/phased-development-plan.md` 的 P0-01/P0-04/P0-07 或新增维护性说明
- 根 `README.md` 中适用的构建/版本说明
- `.goreleaser.yml`、CI/Release workflow 和构建脚本
- Web API DTO、组件/页面、Vite proxy 和测试
- 对应新增 evidence，记录版本、commit、构建时间、命令和产物校验

若修改 `AGENTS.md` 或 `CLAUDE.md`，必须同步另一份并在交付前比较 SHA-256；若无需修改，仍应校验两者现有哈希一致。

## 11. 明确不做

- 不在每次文件保存、热更新、测试或重复 build 时自动 bump。
- 不让构建命令产生未经审查的 Git commit、tag 或 GitHub Release。
- 不安装全局 Git hook，不修改开发者全局 Git 配置。
- 不由前端 package version、静态常量或浏览器缓存决定系统版本。
- 不把产品版本与 API version、manifest apiVersion、migration version 或 Supervisor protocol version 合并。
- 不新增数据库表/字段，不改变认证、监听范围、Operation 或进程监管语义。
- 不把 commit、build time、完整路径或环境变量作为“系统版本”显示。

## 12. 完成定义

只有同时满足以下条件才能声明完成：

1. 根 `VERSION` 建立为唯一产品版本源，初始规范值为 `0.1.0`，格式和职责有文档说明。
2. 版本命令可原子、并发安全地自动计算 patch/minor/major 并同步受控文件；默认产品变更集使用 patch。
3. Gate 能阻止需要升级但未升级、版本回退、格式非法、同步漂移和 tag/`VERSION` 不一致的发布。
4. 普通 build/check/test 可重复执行且不修改版本；同一源码与同一元数据输入可复现相同版本身份。
5. `npm run build` 生成的 `dist/stackpilot.exe` 不再报告 `dev`，CLI 与 `/version` 都精确报告 `VERSION`。
6. Web 通过 `web/src/api` 读取当前 exe 的 `/version`，在侧边栏显示“系统版本 v<version>”，失败不伪造值也不阻断主流程。
7. OpenAPI、buildinfo、PowerShell、GoReleaser、Release workflow、Web DTO/代理/UI 和文档语义一致。
8. 版本脚本、Go/API 契约、Web 单元、生产构建和真实 Windows/浏览器端到端测试通过，实际命令与未执行项已记录。
9. `AGENTS.md` 与 `CLAUDE.md` 最终 SHA-256 一致，且没有覆盖用户已有修改或泄露敏感信息。
