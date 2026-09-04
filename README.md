# StackPilot

StackPilot 是面向本地开发环境的声明式系统编排与控制工具。它把一个由后端、前端、数据库、消息队列和一次性初始化任务组成的开发系统，统一描述在 `.stackpilot/system.yaml` 中，并通过单一 Windows 可执行文件提供 CLI、REST/SSE 控制面和 Web 控制台。

项目当前处于持续开发阶段，正式运行目标为 Windows 单用户本机环境。控制面只监听回环地址，不提供远程访问、多用户或 RBAC 能力。

## 主要能力

- 声明式管理服务依赖、启动顺序、端口、readiness、liveness 和有限自动重启策略。
- 通过持久化 Operation 执行启动、停止、重启和取消，控制面重启后可以恢复状态。
- 使用 Windows Job Object 监管进程树，避免只终止父 PID 后遗留子进程。
- 支持 Maven、npm、Java、Go、Python venv Runner，以及受约束的 Docker Compose Driver。
- 提供 Web 控制台、CLI、REST API、SSE 事件流、日志查看和有界资源指标。
- 使用 SQLite 保存控制面状态，使用当前 Windows 用户保护的本地认证令牌和 Secret。
- 支持工作区修订、运行态与文件态差异计划，以及基于不可变计划的验证式重启。

StackPilot 不从 HTTP 或 Web 接收任意命令、参数或工作目录。生命周期命令只能来自通过 Schema 和语义校验的系统清单及受信任 Runner。

## 当前状态

| 项目 | 状态 |
| --- | --- |
| 支持平台 | Windows amd64 |
| 运行模式 | 本机、单用户、当前用户后台进程 |
| 默认控制面 | `http://127.0.0.1:32100` |
| 开发 Web 端口 | `http://127.0.0.1:32101` |
| 清单版本 | `stackpilot.io/v1alpha1` |
| 发布状态 | 尚无稳定 GitHub Release，请从源码构建 |

Linux/macOS 目前不属于正式运行支持范围。跨平台编译通过不代表对应平台的进程监管、Secret 存储和安装流程已经完成验证。

## 快速开始

### 环境要求

- Windows 10/11 x64
- Go 1.26.6
- Node.js 24.x 与 npm 11.x
- Git
- 被管理项目所需的 Java、Maven、Docker Desktop 等工具；未使用对应 Runner 时无需安装

版本基线以 [`go.mod`](go.mod)、[`package.json`](package.json) 和 [`package-lock.json`](package-lock.json) 为准。

### 从源码构建

```powershell
git clone https://github.com/yxwu97/StackPilot.git
Set-Location StackPilot
npm ci
npm run check
npm run build
.\dist\stackpilot.exe version
```

构建产物位于 `dist/stackpilot.exe`。`npm run check` 会执行格式检查、Go 测试、Go vet、前端类型检查和生产构建。

### 安装并打开控制台

StackPilot 按当前用户安装，不需要管理员权限：

```powershell
.\dist\stackpilot.exe service install
.\dist\stackpilot.exe service status --output json
.\dist\stackpilot.exe open
```

默认安装目录为 `%LOCALAPPDATA%\Programs\StackPilot`，持久数据目录为 `%LOCALAPPDATA%\StackPilot`。`open` 会创建短期一次性浏览器引导信息并打开已认证的本地 Web 控制台。

升级和卸载：

```powershell
.\stackpilot-new.exe service upgrade
.\stackpilot-new.exe service uninstall
```

卸载只移除经过身份核验的安装目录和当前用户启动注册，不删除 SQLite 状态、日志、工作区引用或业务项目文件。

## 注册工作区

工作区的唯一系统定义文件是 `.stackpilot/system.yaml`。最小示例：

```yaml
apiVersion: stackpilot.io/v1alpha1
kind: System
metadata:
  id: example
  name: Example System
spec:
  services:
    app:
      driver: process
      runner: java
      workingDirectory: ./app
      arguments: [-jar, app.jar]
      readiness:
        type: process
        timeout: 30s
        interval: 1s
```

将清单放入项目后注册并启动：

```powershell
stackpilot workspace add C:\Projects\example
Set-Location C:\Projects\example
stackpilot up --wait --open
stackpilot status --output json
stackpilot logs example/app --follow
stackpilot down --wait
```

如果工作区没有清单，Web 导入流程可以只读分析受支持的 BAT 启动文件、生成候选清单并要求用户确认。BAT/PowerShell 脚本不会作为受管服务的生命周期命令执行。

更多清单示例见 [`schemas/examples`](schemas/examples)，字段约束以 [`schemas/system-v1alpha1.schema.json`](schemas/system-v1alpha1.schema.json) 为准。

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `stackpilot open` | 打开已认证的本地 Web 控制台 |
| `stackpilot workspace add <path>` | 注册包含系统清单的工作区 |
| `stackpilot up --wait` | 启动当前或指定系统并等待 Operation 完成 |
| `stackpilot down --wait` | 停止当前或指定系统 |
| `stackpilot status --output json` | 查询运行状态 |
| `stackpilot logs <system/service> --follow` | 读取并持续跟踪服务日志 |
| `stackpilot metrics ...` | 查询有界资源指标 |
| `stackpilot plan ...` | 生成运行态到工作区的只读变更计划 |
| `stackpilot verified-restart ...` | 按不可变计划重启并验证稳定性 |
| `stackpilot secret ...` | 设置、查看元数据或删除受保护 Secret |

运行 `stackpilot <command> --help` 查看参数。API 契约见 [`api/openapi.yaml`](api/openapi.yaml)。

## 安全边界

- Server 默认且仅支持回环监听，不能用作远程控制面。
- 浏览器变更请求需要 HttpOnly 会话、精确 Origin、CSRF header 和 JSON Content-Type。
- CLI 使用保存在 Windows 当前用户安全存储中的 Bearer token；SQLite 只保存摘要。
- Secret 值不进入 SQLite 明文字段、DTO、SSE、Operation 快照或测试证据。
- 相对路径会规范化并校验 junction/符号链接解析后的真实边界。
- Compose 端口绑定到 `127.0.0.1`；高风险 build 字段、任意命令和越界路径会被拒绝。

安全问题请按 [`SECURITY.md`](SECURITY.md) 私下报告，不要在公开 Issue 中附加凭据、日志或本机数据。公开共享前的检查结论和剩余事项见 [`docs/github-sharing-security-review.md`](docs/github-sharing-security-review.md)。

## 文档

- [总体设计](docs/overall-design.md)
- [详细设计](docs/detailed-design.md)
- [分阶段开发计划](docs/phased-development-plan.md)
- [开发、验证与发布](docs/development.md)
- [存储 Schema](docs/storage-schema.md)
- [错误码](docs/error-codes.md)
- [ADR](docs/adr)
- [阶段验收证据](docs/evidence)

## 参与开发

提交改动前请阅读 [`AGENTS.md`](AGENTS.md) 和 [`code_rule.md`](code_rule.md)，并运行与改动范围匹配的验证：

```powershell
npm ci
npm run check
npm run test:web
```

涉及 API、清单、SQLite、状态机或 Windows 进程监管的变更，必须同步对应契约、设计文档和测试证据。

## 许可证

本仓库目前尚未提供开源许可证。源码可供查看和评估，但这不自动授予复制、修改、分发或商用许可；公开发布或接受外部贡献前，应由仓库所有者明确选择并添加许可证。
