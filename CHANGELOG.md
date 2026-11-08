# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号语义化。完整对比见 [Releases](https://github.com/bushixiucai/urmail/releases)。

## [0.6.10] - 2026-11-08

会话视图重构 + 邮件隐私层 + 列表行重排（用户深度访谈拍板的三议题合成一个版本交付）。无数据库迁移（schema 158 不变）。

### 新增（隐私层：服务端渲染增强）
- **图片代理**（#40）：用户允许远程图片后，浏览器不再直连第三方——`img src` 重写为服务端代理 `/api/v1/mailbox/images/proxy`（HMAC 签名 + TTL + 绑定邮箱会话），剥离 Referer 与第三方 cookie 关联；SSRF 防护（私网/环回/链路本地/TEST-NET 拒绝、DNS 解析后钉死已校验地址、30x 逐跳复检）、类型白名单（拒 SVG）与大小上限；远程图片默认仍阻止（v0.6.4 安全基线不变）
- **外链确认页**（#42）：全部 `http(s)` 外链改走 `/api/v1/mailbox/links/confirm` 确认页——真实域名**逐字符等宽渲染**（防同形字钓鱼）、展示完整目标 URL 与来源邮件/发件人，点击才 302；`mailto:`/`tel:` 不经确认
- **UA/视口适配**（#43）：客户端请求带 `X-UA-Class`/`X-Viewport-Width`（前端统一附加，resize 防抖快照），服务端按宽度分桶（≤640 / 641–1024 / >1024）重写邮件内固定宽度（`width=600` → `max-width:100%`），营销邮件在窄栏不再横向裁切
- **CSP 注入上移服务端**（#44）：消毒后 HTML 统一包裹 CSP meta（`default-src 'none'; script-src 'none'` 等），前端检测到服务端 meta 即不自注入（自注入兜底保留作纵深，#48）；渲染管线串成 sanitize → viewport 适配 → CSP 包裹
- **渲染缓存**（#45）：GetMessage 结果进程内缓存，键含 7 维（mailbox/folder/uid/uid_validity/load_images/ua_class/viewport_bucket），`uid_validity` 变更即失效，TTL 5min、上限 256 条、不跨邮箱；缓存的是 CSP 包裹后的最终 HTML，命中不绕过注入；HTTP 响应保持 no-store
- **隐私前端配合**（#48）：净化器 img 策略收紧——仅放行代理前缀，根相对非代理（tracker 探针）、相对路径、协议相对全部删除（CI E2E 抓到的 tracker 回归已根修）；前端回退 CSP 仅在存在放行代理图时授予 `'self'`，否则维持 `'none'` 并显式 `script-src 'none'`

### 新增（会话/线程视图）
- **列表内联展开层级**（#41）：展开线程后子邮件行缩进 + 竖线连接线 + 层级背景档 + 「回复」语义标签，与普通邮件行清晰区分
- **阅读区堆叠卡片**（#47）：打开线程堆叠全部成员卡片（卡头=发件人/时间/摘要，卡体=正文），默认**仅最新一封展开**，点卡头切换；线程条整组操作（已读/归档/删除/成员计数）+ 上一封/下一封导航；回复/转发作用于当前展开那封
- **线程合并收紧 + 全量计数**（#38）：主题兜底合并增加约束（参与者重叠**或** ≤7 天时间窗口），同主题不同参与者不再误合；线程计数覆盖全量（不再只算已加载页），并以 `thread_scope` 标注计数口径（server/folder/bounded）

### 变更（列表行重排）
- **三档密度统一重排**（#39）：Gmail 式两行结构（第一行发件人+主题同行，第二行预览摘要），comfortable 52px 撕裂根修（去掉用 justify-center 塞两行 32px 文本的布局），三档 52/40/32px 同构 DOM，仅行高字号差异，不再有结构跳变
- **时间梯度**（#39）：今天 `HH:mm` / 昨天「昨天」/ 更早「M月D日」/ 跨年带年（对标 Spark），悬停显示完整时间且不撑行；徽章 title 英文硬编码补 i18n
- **预览摘要服务端提供**（#46）：`MessageSummary` 新增 `preview` 字段（正文纯文本前 1–2 行，去引用头、限长、空白折叠），列表显示真实正文摘要；前端修正 snippet 误填 subject。**实测延迟影响**（假 IMAP、20 封/页、30 轮、每命令 5ms RTT）：列表 17.4ms → 23.3ms（+5.9ms，批量预取摊薄；`TestPreviewListLatencyMeasurement` 留档）

### 文档更正
- v0.6.4 条目「远程图片偏好 AES-256-GCM 加密存储」描述不准确（实际为服务端明文存储偏好键，无加密实现），已更正为实际行为

## [0.6.9] - 2026-11-08

阅读区 iframe 高度自适应（QA 生产目检发现的长邮件裁断缺陷）。无数据库迁移（schema 158 不变）。

### 修复（Webmail）
- **正文 iframe 高度自适应**：`SanitizedHtmlFrame` 高度由固定 420px 改为测量 srcdoc 内容实际高度（clamp 420–10000px），load 后与图片 load/error 后重测 + ResizeObserver 防抖，切换邮件重置高度防残留——长邮件（如深色 MJML 营销邮件）正文不再被 420px 裁断（QA 生产目检：Infura 邮件「Hello Developer」段以下不可见）
- sandbox 改 `allow-same-origin` **单独使用**（绝不与 allow-scripts 同开）：父页面读 contentDocument 测高，净化器注入的 CSP 保持文档脚本惰性，XSS 防线不变；测量不可达时回退 420px
- E2E 主路径：24 段深色 MJML 长内容断言 iframe 高度 ≥ 内容 scrollHeight、尾部块完整可见（`contentHeight > 420` 防恒真）；高度单测 5 条（起点/增长/上限/切信重置/不可达回退）

## [0.6.8] - 2026-11-08

深色邮件简写颜色恢复（真实邮件验证的 #31 漏修点）+ 构建链防旧产物嵌入。无数据库迁移（schema 158 不变）。

### 修复（Webmail）
- **`background:` 简写颜色恢复**：MJML 模板常用 `background:#121212` 简写画深底（枚举名不在白名单仍被剥）——净化器删除前从 CSSOM 读出颜色成分恢复为 `background-color` 长写（`url()/var()` 值过滤照旧），真实 Infura 邮件颜色声明存活 16/18 → 18/18（经生产原始 .eml 验证）
- MSO 条件注释（含 `<style>` 残留）有意保留：删除会误吞注释围栏内可见内容（实测），sandbox+CSP 下惰性
- 深色邮件回归 fixture 3 条（简写恢复/MJML 共存+hostile 邻居/条件注释）

### 加固（构建链）
- **防旧前端静默嵌入**：v0.6.7 部署事故根修——直接 `go build` 会把残留旧 `internal/web/dist` 嵌入二进制（首个 v0.6.7 deb 的前端修复因此未生效）。新校验器三重防线（bundle 存在性/源码新鲜度/git stamp 祖先判定），`make build-go` 与 deb 打包双重接线，旧 dist + 新源码确定性构建失败并指引 `make frontend`；fixture 测试 6 项含事故形态复现

## [0.6.7] - 2026-11-08

生产实拍反馈修复（深色邮件不可读 + 阅读区布局）+ postinst 快照恢复链二次根修。无数据库迁移（schema 158 不变）。

### 修复（Webmail）
- **深色主题邮件白底白字**：HTML 邮件净化白名单补 `background-color`（含 table `bgcolor` 属性）——深色营销邮件（白字深底）净化后不再塌成不可见；值过滤继续拦截 `url()/expression()` 等资源载荷，XSS 防护不放松（净化器与 Web Worker 单点同步）
- **阅读区满宽布局**：正文容器去掉居中 `max-w-4xl` 纸面卡片，对齐 Gmail/Outlook 常规逻辑贴满窗格；纯文本保留 65ch 可读行宽；顶部动作条不变

### 修复（部署链路，v0.6.6 根修的遗漏点）
- **preinst/postinst 快照恢复链**：v0.6.6 的 `ensure_dir` 幂等化生效，但 preinst 给 migration 快照目录硬编码 `chmod 0700`，postinst `cp -a` 恢复时把该属性拷回 `/etc/urmail/observability`，权限仍被重置致 alertmanager 起不来（v0.6.6 生产实测复现）。现改为：快照用 `cp -a` 镜像线上目录自身权限、postinst 恢复后按记录的 `mode/user/group` 显式还原；新增端到端 round-trip 测试（0750 → 模拟解包破坏 → 恢复后保持 0750）

## [0.6.6] - 2026-11-07

Webmail P1 波1（右键/拖拽/文件夹管理）+ 部署链路根修。无数据库迁移（schema 158 不变）。

### 新增（Webmail P1 波1）
- **右键菜单**：列表行右键（或 Shift+F10/菜单键）打开：打开、回复、转发、归档、删除、已读/未读、移动到（子菜单）。动作全部复用既有处理函数；Esc/点击外部关闭并归还焦点
- **拖拽移动**：拖单行或一组已选行到左栏文件夹移动；落点夹高亮；与批量「移动到」同一 MOVE + COPYUID 撤销链路，失败保持原状
- **文件夹管理**：侧栏底部新建文件夹；自定义夹右键改名/删除；special-use 系统夹不提供删除/改名；删除前确认（当前后端契约仅允许删除空文件夹）

### 加固（安全审计 run-1 遗留项）
- 新邮箱密码拒绝控制字符（`passwordwork.ValidateNew`，与自助改密策略统一）
- MoveMessages 目标夹补 `ValidManagedFolder` 校验 + 同夹拒绝（对齐 transferMessages）
- `MemoryVault.Put` 标记 Deprecated；协议认证对未实现 SessionVault 的 vault fail-closed

### 修复（部署链路）
- **deb postinst 幂等化**：全部 `install -d` 收敛为「已存在目录不动权限」——根修 v0.6.4/v0.6.5 连续两版 observability 目录权限被重置致 alertmanager 起不来的扰动（同类 10+ 处一并收敛）
- E2E 修复：webmail-navigation 等 legacy 契约 spec 显式固定 `?v=legacy`（PR #16 默认翻转后的基线遗漏）

## [0.6.5] - 2026-11-07

生产会话体验修复（401 实拍反馈批次）。无数据库迁移（schema 158 不变）。

### 修复
- **「记住我」真正生效**：登录勾选后邮箱会话 TTL 从 30 分钟延长至 30 天（服务端 `mailbox_sessions` 按 remember 参数化；仍绑定 password_version，改密码即失效）；未勾选保持 30 分钟
- **会话过期统一拦截**：mailbox API 层收到 401/REAUTH → 清理本地会话 + 单飞跳转登录页（并发请求只跳一次，附件请求同样接入），不再出现满屏 401 与「没有邮件」假空态
- **死 token 预检**：`hasMailboxSession` 检查 expires_at，过期会话进入页面即被识别并清理，不再发出必败请求
- **列表失败态**：邮件列表加载失败显示「加载失败 + 重试」，与空邮箱「没有邮件」区分（中英双语）

## [0.6.4] - 2026-11-07

真实数据穿帮修复 + webmail-next 默认化 + 全量安全深审。无数据库迁移（schema 158 不变）。

### 修复（生产实拍穿帮三连）
- **MIME 字符集解码**：非 UTF-8 编码主题/发件人（如 gb2312）不再乱码——`mime.WordDecoder` 接入 `charset.NewReaderLabel`（golang.org/x/net/html/charset），覆盖 legacy 编码标签
- **日期零值**：无 Date 头/解析失败的邮件不再显示「1/1/1」——`MessageSummary.Date` 改 `*time.Time`（omitempty），nil 时客户端渲染 em dash；排序对 nil 取信封/内投递时间兜底
- **列表行 INBOX 标签**：单文件夹列表不再给每行盖 Folder 章（跨夹搜索结果仍保留来源夹标签）；`uid_validity` 盖章保留（move 守卫依赖）

### 变更
- **webmail-next 成为默认界面**：`/webmail` 直接进入新 Webmail；旧版保留三层逃生门：URL `?v=legacy`、偏好设置 `preferences.webmail_version=legacy`、本地存储 `storage=legacy`；`isWebmailNextEnabled` 服务端渲染恒为 next
- 设置抽屉新增「隐私与安全→远程图片」：默认阻止，可按账户允许全部远程加载（fail-closed，偏好键以明文存于服务端 settings）——旧版与新版均可配置
- 旧版专属 E2E（浏览器身份/跨标签草稿冲突等 4 处）显式固定 `?v=legacy`

### 安全（v0.6.4 发版门：cloudflare security-audit-skill 六阶段深审）
- 45 覆盖单元、3 波独立猎捕、9 候选全部经独立证伪代理验证：**0 confirmed 漏洞**（8 rejected 附反证、1 needs_validation 为生命周期设计问题非漏洞）
- 侦察两大头部假设被证伪：/admin 双挂载 21 个 handler 全部先鉴权；文件夹名 IMAP 注入被 mUTF-7 折叠消除
- hardening 备注（不阻塞发版，排 v0.6.5）：legacy Basic-auth CSRF 守卫改白名单式、密码策略统一拒绝控制字符、MoveMessages 目标夹校验统一、MemoryVault.Put 标记 Deprecated

### 其他
- CI 修复：go.mod tidy、3 处 golangci-lint 遗留（gofmt/unreachable/exported 注释）、public 仓库后 GitHub Actions 全量恢复

## [0.6.3] - 2026-11-07

Webmail 商业化打磨（阶段 1-3）。无数据库迁移（schema 158 不变）。

### 交互
- 快捷键全套（对标 Gmail 单键）：`j/k` 移动、`o`/Enter 打开、`x` 勾选、`e` 归档、`#` 废纸篓、`r/a/f` 回复转发、`c` 写信、`s` 星标、`Shift+i/u` 已读未读、`/` 搜索、`?` 帮助、`g` 然后 `i` 回收件箱；`⌘/Ctrl+Enter` 发送；输入框/写信/设置内不劫持字符键
- 归档/废纸篓语义：归档进 Archive（缺夹自动创建）、删除进 Trash；仅废纸篓内提供永久删除+二次确认；move 全程走 IMAP MOVE，不标记 `\Deleted`
- 精确撤销：COPYUID `destination_uids` 与请求 1:1 对齐才显示撤销按钮
- 批量操作栏：归档/废纸篓/已读/未读/星标/移动；复选框与打开信分离；计数如实显示「已选已加载的 N 封」
- 打开即已读（600ms 防抖，j/k 划过不打标）；回复/全部回复/转发带引用，Reply All 去重排除自己
- 无障碍：图标按钮可访问名（i18n）、写信停靠与设置抽屉 Esc 关闭并归还焦点、focus-visible 焦点环、正文可选中

### 视觉
- Vellum & Ink 视觉体系落地：密度三档行高精确 52/40/32px，次要文字对比度 ≥ 4.5:1，图标 stroke-width 1.75，行五态，阴影收敛至三处，阅读区纸面化，`prefers-reduced-motion` 生效
- 38 项视觉验收检查全过

### 性能（生产构建实测）
- 虚拟列表（@tanstack/react-virtual）：1000 封全部加载后 DOM 恒 29 行，行数与邮箱总量解耦
- 滚动 10 屏 59.8 FPS、单帧最大 16.8ms、0 掉帧
- 2MB HTML 邮件打开 687ms → 53ms（净化移入 Web Worker，Worker 不可用回退同步）
- 设置抽屉 1131ms → 90ms（7 个子页 React.lazy 按需挂载）
- 搜索回车后 100ms 内进入搜索态，结果绘制 277ms（防抖 + AbortController 竞态保护）

### 其他
- 文件夹语义：special-use（`\Inbox/\Drafts/\Sent/\Junk/\Trash/\Archive`）映射，移除假星标夹/假配额/假未读
- 中英双语全覆盖，i18n 键覆盖测试纳入门禁

## [0.6.2] - 2026-11-07

商业化完全移除。迁移 0156-0158，schema 升至 158。

### 移除
- 计费/支付/订阅/发票/优惠券/自助开通售卖链（0047）前后端代码删除，34 张商业化相关表 DROP
- 自助注册接口下线

### 变更
- AI 功能解耦保留：配额改管理员本地 settings 键（`mailbox_domain_limit`/`mailbox_limit`/`mailbox_quota_bytes`/`ai_default_call_limit`/`mailbox_daily_send_limit`），租户可覆盖平台默认，缺省 -1 不限、0 拒绝；未配置 AI 时失败关闭
- 发信限额、开通、租户激活等查询改读本地 settings

## [0.6.1] - 2026-11-06

- 异地备份（BE-04）：WebDAV / S3 / SFTP 三协议，表单校验、连通性探测、加密凭证掩码
- 平台管理台改进

## [0.6.0] - 2026-10

两阶段升级发布；此前历史见 git log 与 Releases。
