# urMail.cn

**urMail 是一个开源自托管邮件服务器套件**（基于 Mail-in-a-Box 二次开发）：部署一套系统，即拥有完整的域名邮箱服务与商业产品级 Webmail。

---

## 它是什么

urMail 把「域名邮箱」做成开箱即用、可自托管、免费开源的产品：

| 能力 | 说明 |
|---|---|
| 多租户 | 一个租户可管理多个域名。用户、别名、配额、审计按租户强隔离（PostgreSQL Row-Level Security + 应用层双保险） |
| 唯一事实源 | PostgreSQL。Dovecot 的 userdb 与配额、Postfix 的别名与策略、原生 Webmail 的邮箱身份和任务状态读取同一份数据 |
| 自助开通 | 加域名 → 自动 DNS → 自动证书 → 自动开邮箱，零人工介入 |
| 双 DNS 模式 | **全托管**（把 NS 指向服务器）或**委派**（通过 Cloudflare / 阿里云 / 腾讯云 / DNSPod API 自动写入记录） |
| 原生 Webmail | 商业产品级三栏 Webmail（`/webmail`）：Gmail 式单键快捷键、虚拟列表、会话线程、批量操作、中英双语、Vellum & Ink 视觉体系 |
| 邮件隐私层 | 服务端完成邮件消毒与渲染：远程图片走服务端代理（SSRF 防护，浏览器不直连第三方）、外链统一走确认页（真实域名等宽渲染，防同形字钓鱼）、按客户端 UA/视口宽度适配渲染、CSP 由服务端注入、渲染结果进程内缓存 |
| AI 辅助（可选） | BYO API Key 接入 AI 摘要与写作辅助，配额由管理员本地配置，未配置则关闭 |
| 可观测 | 投递成功率、退信分布、队列深度、发信信誉、配额使用率 |
| 多语言 | 后端与前端**共享同一份消息目录**，不会出现前后端文案不一致 |

> **定位变更（v0.6.2，2026-11）**：商业化（计费/支付/订阅/发票/优惠券/自助开通售卖链）已完全移除，项目转为开源自托管、免费，永不提供付费托管。历史商业化设计产物已归档。

## 版本记录

- **v0.6.20（草稿，待验收）**：会话栈与跨箱成员查询、邮件正文渲染参数和设置界面颜色令牌更新；会话栈补充主题、计数与单封操作。本期拟对超过 300 封的文件夹逐封展示并标记会话未核验，跨箱成员查询每个文件夹最多扫描最新 1000 封，结果可能不完整。备份跨秒时异地清单时间不匹配的问题已修复并通过受控复现和回归验证；此前 CI 空清单名的具体原因尚未证实。跨箱归组及间歇性 502 等问题仍待最终版本复测，具体变更以合并和验收结果为准（#104、#110–#112、#114–#120）。
- **v0.6.19（2026-11-09）**：移动端顶栏溢出修复（D2）——<md 通讯录/偏好设置收进「更多」菜单（44px 触达），375 视口偏好设置完全不可达修复，320 残余溢出 flex min-width:auto 根修；≥768 完全回归。PR #100。无数据库迁移。
- **v0.6.18（2026-11-09）**：批次 2 终验收口——顶栏 4 个 icon 按钮触控热区扩至 44×44（375/1600 布局无溢出）+ 钓鱼检查入口注释与实现对齐（常显设计经 PM 拍板保留）。PR #98。无数据库迁移。
- **v0.6.17（2026-11-09）**：UIUX 批次 2——语义令牌体系（新增 22 令牌，硬编码色 951→1 仅剩白名单，dark: 恒 0，CI 守门接入 lint）+ 列表/阅读区骨架屏（密度联动/300ms 防闪/无脉冲）+ 空态三场景组件（i18n 双语）+ 移动端适配（写信 FAB/密度钮 icon-only/默认舒适密度/fixed 批量条，375px 验收 11/11）。PR #92-#96。无数据库迁移。
- **v0.6.16（2026-11-09）**：UIUX 批次 1 回票修复——theme-provider 缺省模式锁 light（决策①b：应用级暗色 v1 不支持；未持久化/非法/system 一律 clamp，仅显式 dark 激活，OS 监听休眠保留供 v2）+ 钓鱼红条 dark: 死变体清理+测试同步。PR #90。无数据库迁移。
- **v0.6.15（2026-11-09）**：UIUX 评审批次 1（品牌主色令牌补全修复未读点/指示条不可见、正文可复制、i18n 泄漏、正文与 AI 字号、AI 区暗色类移除）+ 钓鱼红条接线（分析结果持久化新表+消息 API phishing 对象、读信区触发入口、score≥0.6 红条+证据枚举、polish 选区约束模板 v3）。PR #84-#87。含数据库迁移 0159。
- **v0.6.14（2026-11-09）**：v0.6.13 复测两缺陷快速修复——摘要类操作不再误传 selection（HTML-only 邮件 AI 读信 409 AI_SOURCE_CHANGED 消除）、选区润色补 body/draft 契约字段+按错误码具体呈现；安全徽章判读定案（红条=按需钓鱼分析 score≥0.6 映射，琥珀=认证证明设计行为）；translate 502 复核为 provider 外部抖动（90s 预算已生效）。PR #82。无数据库迁移。
- **v0.6.13（2026-11-09）**：v0.6.12 生产验收缺陷修复批次——AI 结果缓存键补 per-op 选项（更简短 chips 不再吃缓存）、provider 错误走降级契约（translate 超时预算 30s→90s、评估集 28 用例、降级 200 可观测）、写信两步 consent 契约（起草/diff/回退全链恢复）、连通测试模型下拉、todos 卡标题随操作走、线程「已加载 N / 共 M」文案、配额零扣减三重实证+CacheHitsTotal；CI 加固（offsite bootstrap 锁根修+DSN 防呆根除本地绿 CI 红）。PR #76-#80。无数据库迁移。
- **v0.6.12（2026-11-09）**：AI 邮件处理全链路工程化（重点功能）+ 渲染管线系统性收口——内置 Prompt 模板注册表（nonce 注入隔离、版本化）、7 操作结构化输出 schema、校验→低温重试→纯文本降级、配额幂等+结果缓存、输出侧注入加固、双层评估集；webmail-next 全量 AI 界面（BYOK 设置分区、读信摘要卡+追问 chips、写信对话面板+选区润色+AI 建议转 Sieve 规则）；渲染管线：真实邮件语料回归框架、窄桶 inset clamp、钝器收敛、暗色 color-scheme、背景图声明级代理、style 选择器内联（go-premailer）、详情 ETag/304+X-Render-Cache。17 个 PR（#58-#74）。无数据库迁移。
- **v0.6.11（2026-11-08）**：v0.6.10 生产缺陷修复批次——渲染管线系统性根修（净化器递归子树删除失效导致任意嵌套 script/style/iframe 残留破图残壳 93→0、多列表格视口适配按行比例百分比化修复窄栏塌陷竖排 55→0、CSS 类定宽列不强制改写）；QA 生产验收 6 缺陷全修（列表正文预览拆分 fetch 恢复填充、渲染缓存六维松匹配真正命中二次打开更快、b64 编码文件夹名显示明文、线程徽章用服务端全量 thread_count、窄视口隐私横幅竖排、回复引用当前展开封）；CI 基建（paths 过滤 + Integration 分片并行 35→9.5 分钟、Go/Node 工具链下载源改造解 CDN 故障）。无数据库迁移。
- **v0.6.10（2026-11-08）**：会话视图重构 + 邮件隐私层 + 列表行重排（用户深度访谈拍板的三议题合成一个版本）——服务端渲染增强（远程图片走服务端代理、外链确认页、UA/视口适配、CSP 上移、渲染缓存）；会话视图（列表内联层级展开、阅读区堆叠卡片、线程合并收紧与全量计数）；列表行重排（Gmail 式两行、三档密度同构、Spark 时间梯度、真实正文预览摘要）。无数据库迁移。
- **v0.6.9（2026-11-08）**：阅读区正文 iframe 高度自适应（长邮件不再被固定 420px 裁断，QA 生产目检发现）；sandbox allow-same-origin 单独使用 + CSP 惰性，XSS 防线不变。无数据库迁移。
- **v0.6.8（2026-11-08）**：深色邮件 `background:` 简写颜色恢复（真实生产邮件验证，MJML 深底不再丢失）；构建链三重校验防旧前端 dist 静默嵌入（v0.6.7 部署事故根修）。无数据库迁移。
- **v0.6.7（2026-11-08）**：深色主题邮件白底白字修复（净化白名单补 background-color，XSS 防护不变）、阅读区满宽布局（对齐 Gmail/Outlook 常规逻辑）；preinst/postinst 快照恢复链二次根修（快照镜像线上权限 + 恢复后显式还原，修掉 v0.6.6 遗留的 observability 权限重置）。无数据库迁移。
- **v0.6.6（2026-11-07）**：Webmail P1 波1——右键菜单（键盘可达）、拖拽移动到文件夹（复用 MOVE+COPYUID 撤销链）、文件夹新建/改名/删除（special-use 保护）；密码控制字符拒绝等三加固；deb postinst 幂等化根修（已存在目录不再重置权限）。无数据库迁移。
- **v0.6.5（2026-11-07）**：生产会话体验修复——「记住我」真正生效（勾选后会话 30 天，未勾选 30 分钟）、会话过期统一拦截（401 单飞跳登录，不再满屏 401/假空态）、死 token 预检清理、列表失败态与空态区分。无数据库迁移。
- **v0.6.4（2026-11-07）**：真实数据穿帮修复——gb2312 等 legacy 字符集主题乱码、无日期邮件显示 1/1/1、列表行误标 INBOX 三连修；**webmail-next 成为默认界面**（`?v=legacy` 三层逃生门保留）；远程图片默认阻止、可按账户允许；发版前完成 cloudflare security-audit-skill 六阶段安全深审（45 单元、0 confirmed 漏洞）。无数据库迁移。
- **v0.6.3（2026-11-07）**：Webmail 商业化打磨（阶段 1-3）——快捷键全套、归档/废纸篓语义与精确撤销、批量操作栏、虚拟列表（1000 封 DOM 恒 29 行）、2MB 邮件净化进 Web Worker（687ms→53ms）、设置抽屉懒加载（1131ms→90ms）、Vellum & Ink 视觉体系 38 项验收全过、无障碍（aria-label i18n、焦点管理）。无数据库迁移。
- **v0.6.2（2026-11-07）**：商业化完全移除——34 张计费相关表 DROP（迁移 0156-0158，schema 158），AI 配额本地化 settings 键，自助注册接口下线。
- **v0.6.1**：异地备份（BE-04）、平台管理台改进。

## 实施与验收状态

原生网页邮箱入口为 `/webmail`，邮箱身份独立登录，通过 Go API 使用 IMAP/SMTP。当前默认 Webmail 为重写版 `webmail-next`（旧版经 `?v=legacy` 仍可进入）：三栏工作台、Gmail 式快捷键、归档/废纸篓语义、批量操作、虚拟列表、中英双语、远程图片默认阻止（可在设置中按账户允许）；v0.6.10 起补齐会话视图（列表内联层级展开 + 阅读区堆叠卡片）与服务端隐私层（图片代理、外链确认页、UA/视口适配、服务端 CSP 注入、渲染缓存）。阶段 3 性能与视觉验收记录见仓库 `web/src/features/webmail-next/` 配套测试与本机 `Shared/webmail-redesign/qa/`。PHP、Roundcube 和 Nextcloud 已退出当前产品栈。历史修复记录（schema 77 定时任务幂等等）见下方历史段落。

第三轮修复覆盖权限、认证、事务、配置应用、配额、管理面和交付工具。当前验证记录见本机 `docs/acceptance/m1-m4-v3.md`；正式发布版本须在验收后确定，源码实现和本地测试不等于生产上线。新增的 v1 接口子集契约在 `api/v1-remediation.openapi.yaml`。

以下是第二轮（test23 / schema 11）的历史记录，不作为第三轮结果：

- 已在 Ubuntu 22.04 / 24.04 真机安装验证（test23）：PostgreSQL bootstrap、RLS、Dovecot/Postfix/NSD/OpenDKIM、nginx tenant vhost、归档管道、审计、平台/租户 API。
- 邮件链路已实测：SMTP AUTH、LMTP、DKIM 签名、日发信配额（554）、邮箱存储配额（LMTP 552）、archive-pipe 落库。
- 客户端验收：IMAP 993 / POP3 995 / SMTP 587 smoke，Playwright 5/5（API + 浏览器登录/导航）。
- 证书：域名自签占位证书 + `urmailctl cert issue/renew` + `urmail-cert-renew.timer`；真实公网 ACME 未在测试环境签发（已接受限制）。
- 管理面：平台/租户/品牌/套餐/渠道/优惠券/审计/域名/邮箱/别名/账单/PDF/Stripe/manual settlement。
- DNS provider：Cloudflare、Aliyun、DNSPod；managed 模式走 NSD。
- 兼容层：`/admin` status/version、users/domains/aliases、DNS zones/zonefile、SSL status、system/backup/web/MFA；未知路径 404。
- 已接受限制：Thunderbird/iOS GUI 矩阵用协议级 smoke 替代；公网 ACME 未实测。
- 发布：tag `v0.4.0-m1m4-remediated-v2`；验收记录 `docs/acceptance/m1-m4-v2.md`。

## 技术栈

- **后端**：Go 1.27.3 + Gin + pgx v5 + sqlc + golang-migrate
- **前端**：React 19 + Vite + TypeScript + Tailwind CSS v4 + shadcn/ui
- **数据库**：PostgreSQL（Row-Level Security 多租户隔离）
- **部署**：裸金属 + apt + systemd，单个 deb 包
- **许可证**：AGPL-3.0

## 快速开始（开发环境）

```bash
make dev        # 前台运行 PG、API 与 Vite；退出时清理自身进程
make dev-down   # 停止本地 API/Vite，保留开发数据库
make build      # 前端构建 + Go 构建（含前端 embed）
make test       # 全部测试
make lint       # golangci-lint + eslint + prettier
```

开发要求 Go 1.27.3、Node.js 22+、Docker Compose、OpenSSL 和 curl。固定端口为 PG 55432、API 8080、Vite 5173；冲突会明确失败。首次初始化专用 `urmail_development` 数据库，账号为 `admin@example.test`，随机密码保存在 `tmp/dev/admin-password`（0600）。开发模式不应用系统邮件配置。

`make lint` 要求 golangci-lint v2.15.0；可使用 `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0` 安装。

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

## 本地运维验收

原生 Webmail 的协议、HTTP 和 PostgreSQL 组合验收可在独占 Docker 容器中运行：

```bash
python3 scripts/native-webmail-protocol-acceptance.py
```

脚本要求本地存在 `urmail-v3-runtime:22.04` 镜像（可用 `--image` 指定同等运行镜像），仅使用无外网、无宿主端口的临时容器与测试数据，退出时清理容器。它验证草稿、定时发送、真实 SMTP/LMTP、Bcc 和大附件哈希；不验证新装、生产配置或公网证书。浏览器远程资源屏蔽回归会自动启动本地 Vite：

```bash
cd web
URMAIL_E2E_NATIVE_FIXTURE=1 npx playwright test e2e/native-webmail.spec.ts
```

安装器最多等待 240 秒，覆盖三分钟的配置应用预算与启动准备；失败仍以非零退出。`urmailctl config status --wait 90s` 只有在首次配置应用完成、必要服务探针成功且 desired/applied generation 一致时才成功。`/api/v1/healthz` 用于进程与数据库存活检查，两者用途不同。

`urmailctl backup` 默认是 metadata-only；完整本机恢复需要显式包含邮件并暂停写入：

```bash
sudo urmailctl backup --include-mail --maintenance
```

默认备份目录为 root 私有的 `/var/backups/urmail`。完整备份包含数据库、配置、主密钥、消费者证书及本机 Maildir/归档原文，生成版本化 manifest 与校验值；不包含远端收件人的副本，不提供加密或签名。

恢复目标先通过同版本安装器建立四个数据库角色，确认目标环境文件中的四个连接有效。停止写入后执行恢复，使用实际备份文件名：

```bash
sudo systemctl stop urmail-cert-renew.timer urmail-usage-sync.timer
sudo systemctl stop urmaild postfix dovecot urmaild-agent
sudo urmailctl restore --dump DATABASE.dump --config CONFIG.tar.gz \
  --mail MAIL.tar.gz --manifest BACKUP.manifest.json --force
sudo systemctl start urmaild-agent dovecot postfix urmaild
sudo urmailctl config status --wait 90s
sudo systemctl start urmail-cert-renew.timer urmail-usage-sync.timer
```

恢复会验证 manifest、归档路径与运行角色权限，保留目标数据库连接凭据，按目标用户名解析邮件文件的 UID/GID。恢复后的实际登录、原邮件附件读取和新邮件收发仍需验证。不要把 metadata-only 备份称为邮件内容恢复。

`urmailctl data-check` 只列出需审核的记录标识，不自动修复历史异常。邮箱存储身份不可改写；已删除地址的保留记录会阻止直接重建，避免旧 Maildir 被新邮箱继承。历史目录及地址释放必须经过运维数据审核，不由迁移自动删除或搬移。

本地追回接口只处理请求租户内、指定归档记录对应邮箱中的精确邮件副本；保留归档原件，legal hold 阻止删除。`local_recalled` 不代表远端 SMTP 收件人已撤回；未确认或不支持的结果保持 pending/failed。

NSD 默认绑定启动时本机启用网卡的具体单播地址和 `127.0.0.1`，保留系统 DNS stub；公网 DNS 记录仍使用配置的 public IP。复杂网络可由 root 在 `agent.env` 设置逗号分隔的 `URMAIL_DNS_LISTEN_ADDRESSES`，只接受明确 IP。网卡地址变化后重启服务以重新采集；安装升级保留 root 本级覆盖。
