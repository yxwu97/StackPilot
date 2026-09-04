# 安全策略

## 支持范围

StackPilot 仍在持续开发，尚无稳定 GitHub Release。当前只对默认分支的最新代码接受安全报告，不承诺为历史提交提供补丁。

正式运行支持范围仅包括 Windows amd64、本机单用户和回环监听。Linux/macOS、远程访问、多用户与 RBAC 不在当前安全支持范围内。

## 报告漏洞

请优先使用本仓库 **Security** 页面中的私密漏洞报告或 Security Advisory。不要在公开 Issue、Discussion 或 Pull Request 中披露漏洞细节、凭据、令牌、Secret、真实日志、数据库或本机路径。

报告应尽量包含：

- 受影响版本、commit 或可执行文件版本输出；
- 可复现的最小步骤和预期/实际结果；
- 影响范围与攻击前提；
- 已脱敏的日志、请求或截图；
- 建议的修复方向（可选）。

如果无法使用 GitHub 私密报告，请先通过仓库所有者的 GitHub 主页建立联系，不要在首次公开消息中发送敏感细节。

维护者确认报告后，会先验证影响与受支持范围，再协调修复、测试和披露时间。项目目前不承诺固定响应 SLA。

## 凭据意外提交

如果发现真实凭据已进入工作区或 Git 历史：

1. 立即在原服务端撤销或轮换凭据，不要等待提交删除。
2. 停止继续推送或复制包含该值的日志和构建产物。
3. 通过私密渠道通知维护者，并说明凭据类型和最早出现的 commit；不要发送明文值。
4. 在轮换完成后再评估是否需要清理 Git 历史及所有派生 clone、fork、缓存和制品。

仅从最新提交删除文件不能使已发布的历史凭据失效。

## 安全设计资料

认证、Secret、路径信任边界和日志脱敏的实现基线见：

- [`docs/detailed-design.md`](docs/detailed-design.md)
- [`docs/overall-design.md`](docs/overall-design.md)
- [`api/openapi.yaml`](api/openapi.yaml)
- [`code_rule.md`](code_rule.md)
