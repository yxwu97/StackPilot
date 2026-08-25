# StackPilot 服务日志查看体验优化开发计划

> 状态：已完成
> 日期：2026-08-19
> 来源 Prompt：`prompt/prompt-20260819-02-service-log-viewer-enhancements.md`
> 关联后端计划：`plan/plan-20260819-03-service-log-retention-worker.md`
> 阶段归属：P1C-09/P1D-09 既有日志查看能力的质量完善；不进入 P3C-01 全文检索

## 1. 目标结果

将现有服务详情抽屉中的日志区域改造成一个有界、可恢复、便于阅读和摘取的日志查看器，交付自动换行、正文复制、清空当前视图、全屏、分级显示、暂停/继续、TXT 导出和错误定位/摘取。

本计划只改前端查看语义及直接相关测试。服务端 NDJSON 自动保留、SQLite segment 元数据清理和 `LOG_STORAGE_PRESSURE` 由关联的 `03` 工作包独立交付；本计划不新增删除 API，也不声称解决长期磁盘增长。

## 2. 固定决策

1. 保留 REST 最近窗口 + SSE 增量模型，不改变日志 DTO、sequence、cursor 或服务端协议。
2. 客户端可见日志与暂停缓冲继续各自最多 5000 条；筛选、错误索引、复制和导出从同一有界数据派生。
3. “清空当前视图”是浏览器会话状态，不删除服务端日志，不重启 SSE，不修改服务状态。
4. `viewFloorSequence` 与 `lastReceivedSequence` 分离；所有初始、增量和恢复路径都应用 floor，传输 cursor 不从已清空的可见数组推导。
5. 原生拖动选择只保证当前虚拟列表已渲染的连续行；跨屏摘取使用数据驱动的错误摘取或导出，不展开全部 DOM。
6. 动态行高方案必须先通过 Spike。优先验证当前 Element Plus `ElTableV2`，不预先承诺手写测量型虚拟列表。
7. 不引入全文搜索、正则搜索、跨实例查询、模型分析、自动修复或手工删除历史日志。

## 3. 当前基线

- `web/src/App.vue` 同时承载抽屉、日志筛选、固定行高虚拟化和导出，继续堆叠功能会扩大单体组件风险。
- 当前虚拟化以 `26px` 固定行高计算上下 spacer；直接开启换行会破坏位置计算。
- `runtime.ts` 已实现最近 500 行加载、SSE、5000 条上限、暂停缓冲、cursor 过期重拉和去重，但没有视图 floor、响应式暂停计数和纯视图清空。
- Web 只有基于 Node test runner 的 SSE 测试，没有现成 Vue 组件测试框架；仓库也没有独立 lint 脚本。
- 当前 Element Plus lock 版本包含 `ElTableV2` 的 `estimatedRowHeight` 动态高度能力，但仍需验证日志选择、精确定位和全屏 resize 行为。

## 4. 工作包总览

| ID | 工作包 | 依赖 | 主要交付物 | Gate |
| --- | --- | --- | --- | --- |
| LV-00 | 动态行高与测试工具 Spike | 无 | 选型记录、5000 行 fixture、测试策略 | Renderer Gate |
| LV-01 | 状态模型与组件边界 | LV-00 | Log Viewer 组件、纯函数、Store floor/cursor | State Gate |
| LV-02 | 换行与虚拟化渲染 | LV-01 | 动态行高列表、锚点、横向查看 | Layout Gate |
| LV-03 | 暂停、清空与正文复制 | LV-01/02 | 控件、暂停计数、选择/复制反馈 | Interaction Gate |
| LV-04 | 分级、错误导航与摘取 | LV-01/02 | level 样式、错误索引/块/上下文 | Error Workflow Gate |
| LV-05 | 导出、全屏与可访问性 | LV-02..04 | TXT 导出、全屏状态、键盘/焦点 | Accessibility Gate |
| LV-06 | 回归、文档与最终验收 | LV-00..05 | 自动测试、浏览器证据、交付报告 | 专项完成 |

## 5. LV-00：动态行高与测试工具 Spike

### 任务

- 建立只包含脱敏虚构内容的 5000 行日志 fixture，覆盖 `trace/debug/info/unknown/warn/error/fatal`、短行、URL、无空格长串、堆栈和接近行长上限的截断记录。
- 使用最小隔离页面或临时测试组件验证 `ElTableV2`：
  - `estimated-row-height` 下的实际高度测量与缓存；
  - 按稳定 sequence 滚动到指定日志；
  - 换行开关、抽屉宽度/全屏变化后的重新测量；
  - 当前渲染范围内的正文选择、hover/focus 行级动作；
  - `1440x900` 与 `390x844` 下的工具栏和日志布局；
  - DOM 行数保持有界，无重叠、空洞和异常跳动。
- 同时估算专用测量型列表需要的复杂度：ResizeObserver 所有权、高度缓存键、前缀偏移、resize invalidation、scroll anchor 和测试面。
- 将结论写入专项 evidence 或计划执行记录。若 `ElTableV2` 通过 Gate，正式实现必须复用它；未通过时逐项记录失败证据，再选择专用实现或评估依赖。
- 明确测试工具：优先沿用 Node test runner 测试纯状态/格式化函数；Vue 组件行为如需新增 Vitest/`@vue/test-utils`，先记录依赖维护状态、许可证、lockfile 影响和无法由真实浏览器稳定覆盖的理由。

### 退出条件

- 动态行高方案、精确定位 API、resize 策略和组件测试方式均已确定。
- 5000 行 fixture 不含真实业务日志或敏感值。
- 未通过 Renderer Gate 前，不修改正式日志列表主路径。

## 6. LV-01：状态模型与组件边界

### 组件拆分

- 从 `App.vue` 提取职责明确的日志查看组件，建议落在 `web/src/components/logs/ServiceLogViewer.vue`；`App.vue` 只传入当前服务并编排抽屉打开/关闭。
- 将无副作用逻辑放入强类型模块，例如日志窗口合并/floor、错误块、导出格式和安全文件名；不从 DOM 反向读取业务数据。
- REST/SSE 继续由 `web/src/api` 和 Pinia Store 管理，不在组件内创建第二条网络链路。

### Store 状态

- 为当前日志 scope 增加：
  - `lastReceivedSequence`：已从 REST/SSE 得知的最高 sequence，用于传输恢复；
  - `viewFloorSequence`：本次抽屉会话的可见下界；
  - `pausedLogCount`：响应式暂停缓存数量；
  - 明确的“当前视图已清空”状态。
- `loadLogs` 在未过滤的 REST 窗口上先更新 `lastReceivedSequence`，再应用 floor。
- `appendLogEntry` 先验证并推进 `lastReceivedSequence`，再根据 paused/floor 进入有界列表。
- `recoverLogWindow(replace)` 先从原始返回窗口计算恢复 cursor，再过滤 `sequence > viewFloorSequence`；过滤后为空也必须返回不回退的 cursor。
- 清空时将 floor 设置为 `lastReceivedSequence`，同时清空可见和已有暂停缓冲；后续日志继续按暂停状态进入相应有界集合。
- 关闭抽屉、切换服务实例或 scope 失效时停止旧流，并原子重置 floor、cursor、暂停和错误锚点。

### 测试 Gate

- 纯状态测试覆盖：初始 500 行、增量去重、5000 上限、暂停/继续、暂停溢出、暂停时清空、普通 retry、cursor expired replace、空显示窗口 cursor 不回退和 scope 切换。
- 构造旧日志在 clear 后通过每一条恢复路径返回的用例，断言它们不会“复活”。

## 7. LV-02：换行与虚拟化渲染

### 任务

- 按 LV-00 选型实现动态高度虚拟化，以 `sequence` 作为稳定 row key。
- 不换行模式提供可达的完整内容，优先使用日志区内部横向滚动；不得只留下不可访问的省略号。
- 换行模式对普通文本、URL、堆栈和无空格长串使用可靠断行，不撑破抽屉。
- 换行、抽屉 resize、全屏和退出全屏后使高度缓存正确失效，同时保持当前锚点 sequence 可见。
- 精确定位使用虚拟列表实例提供的 scroll API 或经 Spike 验证的偏移算法，不用当前 DOM 数组下标冒充 sequence。
- 保持固定/受控 viewport 尺寸，动态内容不得改变工具栏或抽屉整体布局。

### Gate

- 5000 行混合高度下 DOM 有界、无行重叠/错位/空洞。
- 连续切换换行和全屏后可定位同一 sequence。
- 桌面和移动端均无文档级横向滚动或控件遮挡。

## 8. LV-03：暂停、清空与正文复制

### 暂停与清空

- 保留现有暂停/继续入口，增加明确的“已暂停”和缓存条数反馈。
- 暂停只冻结 UI 追加，不停止 SSE；继续时按 sequence 排序去重合并，溢出时重载最近窗口并保留持续可见提示。
- 增加“清空当前视图”图标按钮；空状态区分“尚无日志”与“视图已清空，正在等待新日志”。
- 清空不调用 API mutation、不重置查询、不改变服务状态；关闭并重新打开服务时重新加载最近持久窗口。

### 选择与复制

- sequence、时间和 level 设置为不可选择辅助元数据，message 保持原生 `user-select: text`。
- 为每行提供键盘、鼠标和触屏可达的“复制正文”，只复制已脱敏 `message`。
- Clipboard API 失败使用安全降级和可见反馈；不得将正文写入 console、错误日志或隐藏字段。
- 当前渲染连续行可原生拖选；滚动触发行回收后不保证选区延续，UI 不宣称支持跨屏拖选。

### Gate

- 暂停、清空、筛选、换行和全屏任意组合不重复、不串服务、不无限积压。
- 单行和当前渲染多行复制均不混入序号、时间、level 或隐藏操作文本。

## 9. LV-04：分级、错误导航与摘取

### 任务

- 建立穷尽的前端 level 映射：`trace/debug` 低强调，`info/unknown` 普通，`warn` 警告，`error/fatal` 错误；未知未来值安全降级为 unknown 样式。
- level 文本始终可见，warning/error/fatal 同时使用图标、标签形状或左边框等非颜色提示；`stderr` 不自动升级为错误。
- 错误集合从当前筛选后的有界日志中按 `error/fatal` 派生，锚点只保存稳定 sequence。
- 实现上一个/下一个错误和 `当前/总数`。首尾行为固定为禁用越界方向，避免循环导航造成位置误判。
- 错误定位滚动到目标 sequence，并以非闪烁、短时且可访问的样式高亮；筛选、clear、继续接收或 scope 切换后重新校正锚点。
- 错误摘取从当前筛选结果构造：相邻 error/fatal 间隔不超过 2 条时合并为错误块，再取块外前后最多各 5 条。输出只含按 sequence 排序的 message。

### 测试 Gate

- 覆盖无错误、首尾错误、连续错误、间隔 1/2/3 条、窗口边界、重复 message、筛选变化和实时追加。
- 自动换行、全屏和虚拟行回收后定位仍指向同一 sequence。

## 10. LV-05：TXT 导出、全屏与可访问性

### 导出

- 导出源为点击时的 `filteredLogs` 数据快照，不读取虚拟 DOM。
- 范围固定为客户端已加载窗口：初始最近 500 行 + SSE 增量，最多 5000 条；文件内按 sequence 升序。
- 行格式固定为完整 UTC 时间、level、stream、message，使用 UTF-8 `.txt`。
- 文件名由清洗后的 system/service、实例摘要和 UTC 时间组成；拒绝路径分隔符、控制字符和设备名风险。
- 空结果禁用；Blob URL 在触发下载后可靠释放。错误片段导出使用独立、明确命名的动作。

### 全屏与可访问性

- 使用 `ElDrawer` header slot 增加全屏/退出全屏图标和 tooltip；普通宽度保持当前约束，全屏覆盖可用视口。
- 切换只改变布局状态，不重新 load、不重建 SSE，不丢失查询、暂停、wrap、scroll anchor 或错误锚点。
- 明确实现 Esc：全屏时先退出全屏，非全屏时再按抽屉既有关闭语义处理，并编写 E2E。
- 记录打开抽屉的触发元素，关闭后恢复焦点；所有图标按钮具备 `aria-label`、可见焦点和稳定点击尺寸。
- 窄屏工具栏允许分组换行，不出现文本溢出、按钮重叠或内容遮挡。

## 11. LV-06：验证、文档与最终 Gate

### 自动验证

- 纯函数/Store 测试覆盖 Prompt 与本计划列出的状态矩阵。
- 组件测试覆盖控件状态、copy、clear、level、错误导航、导出和全屏状态保持；若 LV-00 决定不用组件测试依赖，必须以等价真实浏览器断言覆盖，不能只留截图。
- 真实浏览器 E2E 使用虚构日志 fixture 覆盖 `1440x900`、`390x844`、5000 行压力、长行、动态行高、键盘焦点、Esc、暂停/继续、清空、导出和错误摘取。
- 回归 SSE 断开、retry、cursor expired、去重、慢消费者与 scope 切换。

### 命令基线

```powershell
npm run test:web
npm run type-check
npm run build:web
go test ./internal/logs ./internal/api -count=1
./scripts/check.ps1
```

仓库当前没有独立 frontend lint script，不得编造 lint 已通过。若本工作包新增 lint 能力，应作为明确工具链变更评审并更新 package scripts；否则在交付报告中说明现状。

### 文档与证据

- 更新与服务详情日志交互直接相关的开发说明、progress/evidence。
- 记录 LV-00 选型、桌面/移动端结构化断言、压力结果和未执行项。
- 不改写历史 Gate；截图只能作为布局辅助证据。

### 专项完成条件

- 来源 Prompt 第 12 节全部满足。
- 不产生 API、OpenAPI、migration 或服务端删除行为变化。
- `App.vue` 不再承载新增日志领域细节，Store/组件/纯函数职责清晰。
- 测试产物无真实日志、Secret、Token、Cookie、Authorization 或内部路径泄漏。
- `AGENTS.md` 与 `CLAUDE.md` SHA-256 完全一致。

## 12. 建议执行顺序

1. LV-00 必须首先完成并通过 Renderer Gate。
2. LV-01 完成状态模型与组件边界。
3. LV-02 完成渲染基础后，LV-03 与 LV-04 可并行实现，但共享的 sequence/floor helper 必须由 LV-01 单点维护。
4. LV-05 在布局和错误工作流稳定后接入。
5. LV-06 统一运行回归、真实浏览器验证和文档收口。

后端 `03` 工作包可独立推进，不阻塞 LV-00..06；产品层面只有两个工作包分别完成后，才可声称同时解决“查看体验”和“长期磁盘增长”。

## 13. 执行记录

- 完成日期：2026-08-19。
- Renderer Gate：锁定版本 Element Plus 2.14.4 的 `ElTableV2` 通过动态行高、sequence 定位、resize、桌面/移动布局和有界 DOM 验证；正式实现复用该组件，未新增虚拟列表或 Vue 组件测试依赖。
- State/Interaction Gate：REST/SSE 恢复 cursor 与视图 floor 已分离；18 个 Node 测试覆盖计划状态矩阵；5000 行真实浏览器 fixture 覆盖换行、错误定位、暂停、清空、继续、复制、全屏和 Esc。
- Layout Gate：`1440x900` 与 `390x844` 均无文档级横向溢出或工具按钮越界；多行动态高度断言无重叠。
- 最终验证：`npm run test:web`、`npm run type-check`、`npm run build:web`、`go test ./internal/logs ./internal/api -count=1` 和 `./scripts/check.ps1` 通过。
- 专项证据：`docs/evidence/service-log-viewer-enhancements-20260819.md`。本包仍不包含服务端日志保留 worker 或手工删除能力。
