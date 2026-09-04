# GitHub 共享安全检查

检查日期：2026-09-04

检查基线：`main` / `f44c2b8`，并包含检查时工作区中尚未提交的文件

GitHub 状态：仓库为 Private，默认分支为 `main`，共 3 个提交，无 tag 和 GitHub Release

## 结论

未发现高置信度的真实私钥、云访问密钥、GitHub/OpenAI/Slack token、带凭据 URL 或已跟踪的本地数据库。现有提交者邮箱使用 Apple 隐私中继地址。

当前仓库适合继续在 GitHub 私有共享。在改为 Public 前仍应完成以下决策或设置：

1. 选择并添加明确许可证；当前“无许可证”不授予外部用户复制、修改、分发或商用权利。
2. 启用 GitHub Dependency graph、Dependabot alerts，并根据维护策略决定是否启用自动安全更新。
3. 决定是否保留验收证据中的项目名、本机盘符路径、工作区 ID 和内容摘要。这些不是认证 Secret，但会披露开发环境结构。
4. 使用可联网的独立环境再运行一次固定版本 Gitleaks，并保存脱敏结果。

因此，本次不建议自动把仓库可见性从 Private 改为 Public；可见性变更应由仓库所有者在确认上述事项后单独执行。

## 检查范围与方法

- 枚举当前受 Git 跟踪、未跟踪和忽略的文件类型。
- 对当前工作区和全部 3 个提交执行常见供应商凭据、私钥头、带凭据 URL 的规则扫描。
- 检查 `.env`、私钥、证书、密码库、SQLite、日志和构建/测试输出是否被跟踪。
- 检查提交作者信息、历史文件名、历史大对象和远端地址。
- 人工复核 `token`、`secret`、`password`、`authorization`、`cookie`、`csrf` 等命中上下文。
- 人工检查 GitHub 仓库可见性、About、Release 和 Advanced Security 页面。
- 人工检查 GitHub Actions 发布工作流的触发条件与 token 权限。

Gitleaks 8.28.0 的下载因检查环境无法连接 `proxy.golang.org` 而失败。本报告没有把该步骤记为通过；仓库内 Git 规则扫描作为补充，但不能完全替代成熟的凭据扫描器。

## 已处理项

### 1. 生成的 Compose Gate 文件被跟踪

`test/result/p2c-03/compose.override.yml` 是测试生成文件，包含明确标注为 Gate-only 的固定 Keycloak 测试口令。没有证据表明它是生产或长期凭据，但生成产物不应进入共享源码。

处理：从当前树移除该文件，忽略整个 `test/result/`，并把 Gate 脚本改为每次运行时生成 256-bit 随机口令。旧字符串仍存在于历史提交中；如果它曾在非隔离环境中复用，必须在对应 Keycloak 实例中轮换，仅删除文件不能撤销凭据。

### 2. 敏感本地文件忽略规则不足

原 `.gitignore` 没有显式覆盖 `.env`、私钥、个人证书包、密码库和 SQLite 数据库。

处理：增加常见 Secret/数据库模式，并保留 `.env.example` 等无敏感值模板的跟踪能力。

### 3. GitHub 入口说明不足

原 README 偏向内部开发摘要，且仓库 About 为空，访问者难以判断平台范围、安装前提、当前发布状态和安全边界。

处理：README 已补充功能、限制、源码构建、安装、工作区接入、常用命令、安全边界、文档导航和许可证状态；新增根目录 `SECURITY.md`。

## 剩余风险

### GitHub 安全能力未启用

检查时 Dependency graph、Dependabot alerts、Dependabot security updates 和 grouped security updates 均为关闭状态。公开前至少应启用依赖图和漏洞告警。是否允许 Dependabot 自动创建 PR 属于维护策略选择。

### 发布工作流供应链边界

`.github/workflows/release.yml` 只在 `v*` tag 上运行，发布 job 需要 `contents: write`。使用的 GitHub Actions 目前按可变 major tag 引用，而不是固定 commit SHA。建议在建立正式 Release 流程前固定第三方 Action SHA，并把写权限限制在唯一发布 job。

### 环境元数据

设计、脚本和验收证据中存在 `E:\...` 项目路径、BTC/AIWS/PMS/AgentHub 名称、工作区 ID、PID 和摘要。这些值不具备认证能力，也没有发现 Windows 用户目录、真实 Secret 或业务数据库内容。若这些项目名称或目录拓扑本身属于非公开信息，应在公开前按一份明确的脱敏清单统一替换，并重新扫描 Git 历史。

### 忽略目录的本地内容

`.cache/`、`.local/`、`dist/`、`output/` 和依赖目录均未被 Git 跟踪。本次 Git 共享检查不读取或发布这些目录中的运行时内容；通过 Git 推送不会共享它们，但手工上传压缩包前仍需单独检查。

## 公开前 Gate

- [ ] 已选择许可证并由所有者确认。
- [ ] 已决定环境元数据是否允许公开。
- [ ] 已在可联网隔离环境运行 Gitleaks 全历史扫描。
- [ ] 已启用 Dependency graph 与 Dependabot alerts。
- [ ] 已复核 Actions 权限并固定发布 Action 版本。
- [ ] 已确认 GitHub Release 只包含预期 ZIP 与 `checksums.txt`。
- [ ] 已确认 `AGENTS.md` 与 `CLAUDE.md` 哈希一致。
