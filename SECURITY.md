# 安全策略

简体中文 | [English](#security-policy)

## 报告漏洞

请不要在公开的 Issue、讨论或 PR 里报告安全问题。

请用 GitHub 的私密漏洞报告提交：[Security → Report a vulnerability](https://github.com/shiceai/agy2api/security/advisories/new)。写清楚受影响的版本、复现步骤和可能造成的影响。我们会尽快回复；修复发布后会在 Release 说明里致谢，你不希望署名的除外。

报告和复现材料里不要放真实的 refresh token、API Key、管理口令或代理密码，需要时用打码后的值。

## 支持的版本

只有最新的正式版会收到安全修复。

## 范围

- 包括：本仓库的代码、Release 里的二进制包和安装脚本、`ghcr.io/shiceai/agy2api` 镜像。
- 不包括：上游服务本身的问题；部署方式造成的问题，例如管理端不经 HTTPS 直接暴露在公网、使用弱口令。部署建议见 [docs/deploy.md](docs/deploy.md)。

---

# Security policy

## Reporting a vulnerability

Please do not report security issues in public issues, discussions or pull requests.

Use GitHub private vulnerability reporting instead: [Security → Report a vulnerability](https://github.com/shiceai/agy2api/security/advisories/new). Include the affected version, steps to reproduce and the potential impact. We will reply as soon as we can, and credit you in the release notes once a fix ships unless you prefer not to be named.

Never include real refresh tokens, API keys, the admin password or proxy passwords in a report; use redacted values.

## Supported versions

Only the latest release receives security fixes.

## Scope

- In scope: the code in this repository, the binaries and install script attached to releases, and the `ghcr.io/shiceai/agy2api` image.
- Out of scope: issues in the upstream service itself, and issues caused by how it is deployed, such as exposing the admin UI to the internet without HTTPS or using a weak password. See [docs/deploy.md](docs/deploy.md) for deployment advice.
