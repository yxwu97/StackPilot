# StackPilot 可执行文件与系统版本管理开发计划

> 状态：已完成（证据：`docs/evidence/executable-system-version-20260819.md`）
> 日期：2026-08-19
> 来源 Prompt：`prompt/prompt-20260819-05-executable-system-version.md`
> 阶段归属：Phase 0 构建、API 与发布基线的维护性增强

## 1. 目标结果

建立一条从仓库产品版本到运行中 `stackpilot.exe` 再到 Web 控制台的单向可信链路：

```text
VERSION -> 版本升级/一致性脚本 -> build/GoReleaser ldflags
        -> stackpilot.exe buildinfo -> CLI version + GET /version -> Web“系统版本”
```

一次可交付产品变更集默认自动计算 PATCH +1；构建、测试和重复 CI 只消费版本，不修改版本。发布 tag、archive、CLI、API 和 Web 对同一 exe 报告同一规范版本。

## 2. 固定决策

1. 产品版本采用标准三段数字 `MAJOR.MINOR.PATCH`，不补零；首个基线为 `0.1.0`。
2. 根 `VERSION` 是源码树唯一产品版本事实来源，内容只有 `0.1.0` 和结尾 LF。
3. “每次修改”按一次准备交付/合并的产品变更集计数，默认 PATCH +1；不按文件保存、测试或构建次数计数。
4. 自动化负责读取、计算、同步和验证，但不自动 commit、tag、push 或发布。
5. 普通 `npm run build` 从 `VERSION` 注入版本，缺失或非法直接失败，不回退 `dev`。
6. Web 从当前 Server 根路径 `/version` 读取 `version`，不读取 package manifest 或前端静态变量。
7. UI 在侧边栏控制面状态附近显示 `系统版本 v0.1.0`；加载失败显示未知占位，不阻断系统管理。
8. API version、清单 apiVersion、migration version、Supervisor protocol version 与产品版本保持独立。

## 3. 变更范围

预计修改范围以实现前复核为准：

- 新增根 `VERSION`。
- 新增版本读取/升级/一致性检查 PowerShell 脚本及隔离测试。
- 调整 `scripts/build.ps1`、`scripts/check.ps1`、根 npm scripts。
- 调整 `.goreleaser.yml`、`.github/workflows/ci.yml`、`.github/workflows/release.yml`。
- 必要时同步 root/web `package.json` 与 `package-lock.json`；是否同步由 VP-00 决策固定。
- 强化 `internal/buildinfo`、CLI/API/OpenAPI 契约和测试。
- 在 `web/src/api` 增加 Version DTO/client，在 `App.vue` 或聚焦组件显示，并调整 Vite proxy、样式和测试。
- 同步设计、开发、发布说明和专项 evidence。

不修改 SQLite schema、manifest schema、认证状态机、安装 marker schema、Supervisor 协议或业务系统编排逻辑。

## 4. VP-00：设计核对与版本规则冻结

### 工作项

- 完整读取 Prompt 指定文件，绘制当前版本来源和消费者清单，确认 `buildinfo.Current()` 是 CLI/API 的共同运行时入口。
- 核对安装/升级代码是否解析或比较产品版本，查明同版本重装和降级的现有行为；如果变更行为会影响 ADR-0003，先单独形成设计裁决，本工作包默认不改变。
- 核对 GoReleaser snapshot、tag release 和手工 `scripts/build.ps1` 三条路径的实际版本值与 archive 名称。
- 固定产品变更路径策略：生产源码、API、构建发布、Web 生产代码和用户文档触发 bump；`prompt/`、`plan/`、历史 evidence、生成物和缓存不单独触发。
- 决定 private npm package version：推荐由版本脚本同步 root/web package 和 lockfile，使工具输出一致；若会造成无意义 lockfile churn，则保持 package version 为 `0.0.0` 并在检查中明确排除，禁止模糊状态。
- 固定 CI 比较基线来源，例如 PR base SHA / workflow event base；本地允许 `-BaseRef`，无 base 时降级为只做格式与同步检查并明确提示。
- 固定 snapshot 策略。推荐 snapshot exe 的 `version` 仍为 `VERSION`，唯一性由 `commit`/`buildTime` 表达；release tag 只允许 `v<VERSION>`。

### 交付物

- 在实现 evidence 开头记录上述决策和现状命令输出。
- 如果发现现有设计与固定决策冲突，先更新对应设计或形成 ADR；没有冲突则不新建无意义 ADR。

### Design Gate

- 唯一版本源、触发路径、npm 同步策略、snapshot 策略和 CI base 均有确定结论。
- 没有把“自动升级”实现成每次 build 改写源码或未经授权的 Git 操作。
- 确认本工作包不改变安装/降级、API version、migration 或 Supervisor protocol 语义。

## 5. VP-01：建立版本源与严格解析器

### 实现

- 新增 UTF-8 无 BOM、LF 的根 `VERSION`，初始内容 `0.1.0`。
- 在 `scripts/lib` 或职责清晰的脚本模块中实现共享版本函数：
  - 以字节/文本规则拒绝 BOM、空文件、多行、首尾空格和非 LF 规范内容。
  - 使用 `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$` 解析。
  - 返回数值三元组和规范字符串，比较时按数值而非字典序。
  - 设置合理上限或使用不会溢出的数值类型；错误包含文件和原因但不输出环境敏感信息。
- 若 Go 生产代码需要验证，只在 `internal/buildinfo` 提供最小 `ValidateVersion` 或测试 helper；不要让运行时读取 `VERSION`。
- 将默认 `buildinfo.Version = "dev"` 保留为 `go run`/未注入诊断边界，但可交付构建必须覆盖它。

### 测试

- 为 PowerShell parser 建立隔离 fixture，覆盖合法、BOM、CRLF、额外行、空白、前导零、负数、后缀和超大数字。
- 扩展 `internal/buildinfo/buildinfo_test.go`，覆盖注入值完整传递；如果新增校验函数，覆盖边界表。
- 检查新增文本编码和换行，不让测试改写真实 `VERSION`。

### Version Source Gate

- `VERSION` 可被 PowerShell 和 Go build 链路稳定读取为 `0.1.0`。
- 所有非法输入确定性失败，版本比较正确处理 `0.9.9 < 0.10.0`。
- 运行中二进制仍不依赖源码文件存在。

## 6. VP-02：自动升级命令与变更 Gate

### 实现

- 新增 `scripts/version.ps1`，建议子命令或参数：
  - 默认/`bump -Part patch`：PATCH +1。
  - `bump -Part minor`：MINOR +1，PATCH 归零。
  - `bump -Part major`：MAJOR +1，MINOR/PATCH 归零。
  - `check [-BaseRef <ref>]`：格式、同步、前进性和变更集 bump 要求。
  - `show`：只读输出规范版本，便于 CI 复用。
- 根 npm scripts 增加稳定入口，例如 `version:show`、`version:bump`、`version:check`；PowerShell 是 Windows 首发平台的实现事实，npm 只封装命令。
- bump 开始时获取 `.cache/version.lock` 排他锁，锁目录已被忽略；通过持有文件句柄而不是脆弱的“文件存在即锁定”判断并发。
- bump 以 `-BaseRef` 或未提交场景的 `HEAD` 中版本为幂等基线，并检查同一基线后的产品文件变化：当前版本已高于基线时校验同步后返回 `already-bumped`，不得再次加一；没有产品变化时拒绝 bump。CI 必须传入事件提供的准确 base SHA，不能把当前 `HEAD` 错当成 PR 基线。
- 先计算所有目标内容并验证，再写临时文件并原子替换。若 VP-00 决定同步 package manifest，使用 PowerShell JSON API 或 `npm version --no-git-tag-version` 的受控行为，并校验 lockfile；不得正则改 JSON。
- 输出一行机器可解析结果，例如 `oldVersion=0.1.0 newVersion=0.1.1 part=patch`，退出码区分成功和校验失败。
- `check -BaseRef` 使用 `git diff --name-only <base>...HEAD` 加工作区变化做集中路径分类；版本未前进、回退或跳过规则不符时失败。不要扫描 `.git` 内容或修改历史。
- 不在当前 Prompt/Plan 创建动作中执行第一次 bump；`0.1.0` 是功能实现时建立的基线，后续产品变更集从其递增。

### 测试

- 在临时目录复制最小 fixture，表驱动验证 patch/minor/major 和多位数进位。
- 两个并发进程对同一 fixture 和同一 base bump，断言只发生一个完整更新，另一个返回 `already-bumped` 或明确并发结果，绝不产生第二次版本递增；最终文件始终合法。
- 模拟目标文件只读/替换失败，断言退出非零且无部分同步。
- 构造路径清单验证触发/排除策略；验证 base 不存在时明确失败或降级，不声称完成前进性比较。

### Automation Gate

- 一条命令自动计算并完整同步下一个版本，失败保持原状态。
- 同一变更集的检查能阻止漏 bump 和回退。
- `build`、`check`、测试和读取命令运行前后 `VERSION` 哈希不变。
- 无 Git commit/tag/push、全局 hook 或用户目录写入。

## 7. VP-03：构建、产物自检与发布一致性

### 手工/本地构建

- 修改 `scripts/build.ps1`：未传 `-Version` 时调用共享 parser 读取 `VERSION`，不再默认 `dev`。
- `-Version` 仅作为显式测试/受控流水线 override，严格匹配规范格式并在日志显示 `versionSource=override`；正式 release 禁止 override 与 `VERSION` 不同。
- 保留 commit/build time 的现有安全字符校验和 UTC 行为，避免扩大 linker 参数注入面。
- 构建后运行 `$resolvedOutput version`，精确解析 `StackPilot <version>` 并断言等于期望；失败删除或隔离无效候选，不能把它留作成功制品。
- `npm run build` 输出版本、commit/build time 来源和最终 exe 路径，但不显示敏感环境。

### CI 与 GoReleaser

- CI quality 先运行 `version check`；Windows artifact 构建后解压/执行 exe 并校验 CLI version。
- `.goreleaser.yml` 保留 `{{ .Version }}` 注入，但 release workflow 在 GoReleaser 前读取 `VERSION` 并校验当前 tag 为 `v<VERSION>`。
- snapshot 构建按 VP-00 决策显式传递 `VERSION`，并断言 archive 中 exe 的 CLI `/version` 身份；不要只验证 ZIP 存在和 checksum。
- 发布制品文件名中的版本、checksums 清单和二进制内部版本必须一致。
- Release job 不负责 bump 或创建 tag；错误 tag 直接阻断并给出期望值。

### 测试

- 使用临时输出路径构建两次，断言两次 `VERSION` 不变且 exe 版本相同。
- `VERSION` 缺失/非法/与 tag 不一致时，构建或 release preflight 确定性失败。
- 显式合法 override 可用于测试，非法/降级 override 在正式模式失败。
- 保留现有 GoReleaser config check、archive 内容和 SHA-256 验证。

### Build Gate

- 默认 `npm run build` 产物报告 `0.1.0`，不再报告 `dev`。
- `stackpilot.exe version`、GoReleaser archive 名和发布 tag 完全一致。
- 重复构建不 bump、不改写 tracked 文件，release 不一致被提前阻断。

## 8. VP-04：CLI、API 与 OpenAPI 契约强化

### 实现

- 保持 `buildinfo.Current()` 是 CLI 和 API 共同来源；不在 handler 或 CLI 重新读取/拼接版本。
- 保持 CLI 现有三行结构，第一行为 `StackPilot 0.1.0`；只有已有消费者允许时才增加机器格式，避免无关兼容变化。
- 保持根 `GET /version` 和 `security: []`，不新增重复 API。
- 更新 `VersionResponse.version`：增加规范 pattern、示例 `0.1.0` 和说明“running executable product version”。
- OpenAPI 顶部 `info.version` 继续表达 OpenAPI 文档/API 契约版本；VP-00 若决定同步，必须先记录理由，默认不跟随每次产品 patch。
- 检查 API response cache 语义。构建身份可缓存但前端必须得到当前 Server 的响应；若现有全局 no-store 已覆盖则不重复增加中间层。

### 测试

- CLI 单元测试注入 `0.1.0` 并断言 stdout，错误信息不进入 stdout。
- API router 测试注入同值，解码响应并断言 `version`、commit、buildTime、apiVersion、capabilities。
- 契约测试检查 OpenAPI pattern、example、required 字段和实际 handler 字段一致。
- 构建集成 Gate 启动隔离端口/数据目录的真实 exe，请求 `/version`，与同一 exe 的 CLI 输出比较。

### Contract Gate

- CLI、API 和 `VERSION` 对构建候选三方完全一致。
- `/version` 未泄露路径、环境、token 或其他新增字段。
- OpenAPI 可解析，API version 和产品 version 没有混淆。

## 9. VP-05：Web 系统版本展示

### API 层

- 在 `web/src/api/types.ts` 增加 `VersionResponse`，字段与 OpenAPI 一致：`version`、`commit`、`buildTime`、`apiVersion`、`capabilities`。
- 在 `web/src/api/client.ts` 或独立聚焦模块增加 `getVersion()`。现有 request helper 固定拼 `/api/v1`，需最小重构为可显式选择 root endpoint，避免传入 `../version` 或页面直接 fetch。
- 继续使用统一的非 2xx/error 处理，但 `/version` 无标准 error envelope 的网络/代理失败要安全处理，不触发错误的认证失效。
- Vite server proxy 增加精确 `/version`，目标仍为 `http://127.0.0.1:32100`；不能用过宽代理吞掉 SPA 路径。

### 状态与 UI

- 版本只需应用级只读状态。若仅 `App.vue` 消费，可使用局部 `ref` 和聚焦加载函数；若多个组件消费再放入现有合适 Store，不为单字段创建空壳 Store。
- 会话 ready 后与首批只读快照并行加载版本，或在认证初始化前读取公开接口。选择必须保证版本失败不会使 auth/catalog 失败；推荐独立错误边界。
- 在 `.sidebar-status` 附近增加紧凑行：文字标签“系统版本”，值 `v${version}`。版本值使用 `title`/tooltip 提供完整可访问名称，非按钮。
- 加载中使用稳定宽度占位，失败显示 `--` 并提供“系统版本暂不可用”的可访问提示；不回退 `dev`、package version 或上次缓存值。
- 调整现有 CSS，让桌面侧边栏、窄窗口和移动布局不发生溢出、遮挡或高度跳动；不新增卡片套卡片。

### 测试

- API 单元测试验证实际请求 `/version` 而不是 `/api/v1/version`，并覆盖合法响应、网络失败、非 JSON、字段缺失/非法版本。
- 组件测试覆盖 loading/success/failure；断言 `系统版本 v0.1.0` 可见且业务导航仍可操作。
- Vite dev 集成测试或真实开发服务 smoke 验证 `/version` 被正确代理。
- Playwright 使用真实构建 exe：分别在桌面和移动 viewport 比较 Web 文本、CLI 和 HTTP JSON；截图和 DOM bounding box 检查无重叠。

### Web Gate

- Web 展示来自当前运行 exe，不是构建 Web 时写死的值。
- 版本接口故障只影响版本占位，不阻断认证、系统列表或操作页面。
- 开发代理与嵌入式生产页面均可取得 `/version`，布局通过桌面/窄视口检查。

## 10. VP-06：文档、全量验证与证据

### 文档同步

- `docs/detailed-design.md`：补充唯一版本源、运行时注入、自动 bump 边界和 Web 展示链路。
- `docs/development.md`：写明 `version:show`、`version:bump`、build、release tag 操作顺序和“build 不 bump”。
- `docs/phased-development-plan.md`：在 P0-01/P0-04/P0-07 的维护性说明或验收中补充版本 Gate，不重写历史完成事实。
- `README.md`：只加入用户/开发者真正需要的版本查询和构建说明。
- 新增专项 evidence，记录初始/最终版本、commit、UTC build time、脚本输出摘要、CLI/API/Web 三方一致性、测试命令与限制。

### 自动验证命令基线

按最终 npm script 和脚本参数校准后至少执行：

```powershell
npm run version:show
npm run version:check
npm run test:web
npm run type-check
go test ./internal/buildinfo ./internal/api ./cmd/stackpilot -count=1
go test ./... -count=1
go vet ./...
npm run build
.\dist\stackpilot.exe version
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

另执行：

- 版本 PowerShell fixture 测试和并发/原子失败测试。
- GoReleaser config/snapshot 和 archive/checksum/exe identity 校验。
- 隔离数据目录、非默认端口的真实 exe `/version` smoke。
- Playwright 桌面/移动 UI 一致性与布局检查。
- `git diff --exit-code -- VERSION <同步文件>` 或等价前后哈希断言，证明 build/check/test 不会 bump。
- `Get-FileHash AGENTS.md,CLAUDE.md`，确认哈希一致。

不得把计划中的命令直接写成已通过；evidence 只记录实际执行结果。真实 release 发布和 Git tag 创建不属于本工作包的验证授权，可用无发布 preflight/snapshot 代替并记录限制。

### Final Gate

- VP-00 至 VP-05 全部 Gate 通过。
- `VERSION`、CLI、API、Web、tag preflight 和制品命名一致。
- 漏 bump/回退/非法格式/同步漂移/错误 tag 均有负向测试。
- build/check/test 可重复且不改版本，bump 命令原子、并发安全。
- 全量 Go/Web/PowerShell/GoReleaser/浏览器验证通过，无敏感信息和测试遗留。
- 文档、契约、实现和 evidence 一致，`AGENTS.md` 与 `CLAUDE.md` 哈希相同。

## 11. 相对排期、依赖与风险

当前未授权实名负责人和日历截止日期，责任统一记为“实现 Agent”，不编造承诺日期。

| 工作包 | 参考工作量 | 前置条件 | 可并行项 |
| --- | --- | --- | --- |
| VP-00 | 0.5 人日 | 无 | 现有 Web UI 只读核对 |
| VP-01 | 0.5-1 人日 | VP-00 | 无 |
| VP-02 | 1-1.5 人日 | VP-01 | Web DTO 设计 |
| VP-03 | 1-1.5 人日 | VP-01/VP-02 | VP-04 测试准备 |
| VP-04 | 0.5-1 人日 | VP-03 注入策略 | VP-05 样式准备 |
| VP-05 | 1-1.5 人日 | VP-04 契约稳定 | 文档草稿 |
| VP-06 | 1-1.5 人日 | 前述 Gate | 无 |

参考总量约 5.5-8.5 人日。关键路径为 `VP-00 -> VP-01 -> VP-02 -> VP-03 -> VP-04 -> VP-05 -> VP-06`。

主要风险：

- 把自动 bump 放进 build 导致工作区每构建一次就变脏、制品不可复现。
- GoReleaser tag version 与 `VERSION` 形成双事实来源。
- private npm package version 同步策略未冻结，造成 lockfile 长期漂移。
- Vite 只代理 `/api`，开发时 Web 的根 `/version` 被错误落到前端服务器。
- 前端把版本请求并入认证/目录的单一失败链，导致非关键版本故障阻断主流程。
- 使用字符串比较版本，错误处理 `0.9.9` 与 `0.10.0`。
- 并发 bump 或多文件同步失败留下半更新状态。

## 12. 建议执行顺序

1. 完成 VP-00，冻结 npm/snapshot/base-ref 策略。
2. 完成 VP-01，先建立严格、唯一的 `VERSION` 读取契约。
3. 完成 VP-02，实现自动 bump 和漏 bump Gate，并证明不触碰 Git 历史。
4. 完成 VP-03，让所有可交付构建消费 `VERSION` 并执行真实 exe 自检。
5. 完成 VP-04，强化 CLI/API/OpenAPI 的同源契约。
6. 完成 VP-05，通过统一 Web API 层展示当前 exe 系统版本并完成响应式验证。
7. 完成 VP-06，全量回归、snapshot、真实 Windows/浏览器 Gate 和文档证据收口。

任一 Gate 失败时，不得用硬编码 Web 版本、保留 `dev`、构建时隐式 bump、忽略 tag 漂移或自动提交来伪装完成。
