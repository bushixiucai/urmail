# Contributing to urMail.cn

感谢参与。本项目采用 **AGPL-3.0** 许可证。

## 开发环境

需要 Go **1.27.3**、Node.js 22+、PostgreSQL 16+、Docker（用于本地依赖与集成测试）。

```bash
make dev        # 起本地全栈
make test       # 跑全部测试
make lint       # 静态检查
make build      # 构建（含前端 embed）
```

## 代码规范

### Go

- `golangci-lint` 必须零告警。启用的规则集见 `.golangci.yml`。
- 业务逻辑放在 `internal/domain/`，**不得**依赖数据库或 HTTP。
- 所有面向租户的数据访问**必须**带 `tenant_id` 谓词。例外只允许出现在
  `internal/store/queries/platform_*.sql`（仅平台级 role 使用）。
  CI 有一条检查专门盯这条规则。
- 不允许在日志中输出密码、邮件正文、TOTP 密钥、API token。日志字段有白名单，见
  `internal/logging/`。

### SQL

- 用 sqlc，SQL 写在 `internal/store/queries/*.sql`，**不要**手写字符串拼接查询。
- 迁移文件放在 `migrations/`，**必须同时写 `up` 和 `down`**。

### 前端

- TypeScript strict 模式。
- 组件基于 shadcn/ui，源码直接放在 `web/src/components/ui/`（可改）。
- **所有用户可见文案必须走 i18n**（`web/src/locales/*.json`）。
  后端返回的是 i18n key + params，不是已本地化字符串。
- 新增 i18n key 时**必须同时补齐 `zh-CN` 和 `en`**，CI 会检查。

### 变更系统配置

Postfix / Dovecot / nsd / nginx 的配置一律由 `internal/mail/` 下的生成器产出，
**不要**手工编辑 `/etc/` 下的文件，也不要在脚本里零散地 `sed` 系统配置。
生成器是纯函数，必须配套 golden file 快照测试。

特权操作（写系统目录、重载服务、签发证书）统一走 `urmaild-agent` 的结构化接口，
新增操作必须在 `internal/agentd/ops.go` 的白名单里显式注册。

## 提交与分支

- 主线 `main`；大规模改造走 `feat/*` 分支。
- 提交信息用英文祈使句，首行 ≤ 72 字符。
- 一个提交只做一件事。

## 安全问题

**不要**开公开 issue 报告安全漏洞。请按 [SECURITY.md](SECURITY.md) 的指引私下报告。

## 行为准则

参与即同意 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
