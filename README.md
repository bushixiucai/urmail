# urMail.cn

**urMail.cn 是一个多租户邮件 SaaS 平台**：运营方部署一套系统，向最终用户销售域名邮箱与邮箱空间。

---

## 它是什么

urMail.cn 把「域名邮箱」这件事做成一个**可售卖、可自助开通、可计量计费**的平台产品：

| 能力 | 说明 |
|---|---|
| 多租户 | 域名即租户。用户、别名、配额、账单、审计全部按租户强隔离（PostgreSQL Row-Level Security + 应用层双保险） |
| 唯一事实源 | PostgreSQL。Dovecot 的 userdb 与配额、Postfix 的别名与策略、Roundcube 的认证，全部直接读同一份数据 |
| 自助开通 | 加域名 → 自动 DNS → 自动证书 → 自动开邮箱，零人工介入 |
| 双 DNS 模式 | **全托管**（把 NS 指向我们）或**委派**（通过 Cloudflare / 阿里云 / 腾讯云 / DNSPod API 自动写入记录） |
| 商业闭环 | 套餐 → 下单 → 支付（微信 / 支付宝 / 聚合 / Stripe / PayPal）→ 自动开通 → 计量 → 到期续费 / 退款 / 发票 |
| 可观测 | 投递成功率、退信分布、队列深度、发信信誉、配额使用率 |
| 合规 | 归档、追回、审计留痕、合法保留（legal hold） |
| 多语言 | 后端与前端**共享同一份消息目录**，不会出现前后端文案不一致 |

## 技术栈

- **后端**：Go 1.27.3 + Gin + pgx v5 + sqlc + golang-migrate
- **前端**：React 19 + Vite + TypeScript + Tailwind CSS v4 + shadcn/ui
- **数据库**：PostgreSQL（Row-Level Security 多租户隔离）
- **部署**：裸金属 + apt + systemd，单个 deb 包
- **许可证**：AGPL-3.0

## 快速开始（开发环境）

```bash
make dev        # 起 PG + 全栈本地环境
make build      # 前端构建 + Go 构建（含前端 embed）
make test       # 全部测试
make lint       # golangci-lint + eslint + prettier
```

生产安装：

```bash
curl -fsSL https://urmail.cn/install.sh | sudo -E bash
```

## 架构

架构设计、领域模型、里程碑与风险登记册见 `docs/`（**本地文档，不入仓库**）。

## 许可证

AGPL-3.0。见 [LICENSE](LICENSE)。

> 本项目与 [Mail-in-a-Box](https://github.com/mail-in-a-box/mailinabox) 无隶属关系。
>
> 仓库早期历史来自一个 Mail-in-a-Box fork，其版本脉络为：
> - **v77** — 上游 Mail-in-a-Box 的 release（见其
>   [v77 release](https://github.com/mail-in-a-box/mailinabox/releases/tag/v77)），
>   对应本仓库历史中的 `df7f245`；
> - **v80** — 该 fork 自己的版本（v77 之上加 8 个提交），即 `9a3e9ca`，
>   冻结保留在 [`v80` 分支](../../tree/v80)。
>
> 该 fork 的代码以 CC0 1.0 释置于公有领域。
