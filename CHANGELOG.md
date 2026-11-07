# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号语义化。完整对比见 [Releases](https://github.com/bushixiucai/urmail/releases)。

## [0.6.4] - 2026-11-07

真实数据穿帮修复 + webmail-next 默认化 + 全量安全深审。无数据库迁移（schema 158 不变）。

### 修复（生产实拍穿帮三连）
- **MIME 字符集解码**：非 UTF-8 编码主题/发件人（如 gb2312）不再乱码——`mime.WordDecoder` 接入 `charset.NewReaderLabel`（golang.org/x/net/html/charset），覆盖 legacy 编码标签
- **日期零值**：无 Date 头/解析失败的邮件不再显示「1/1/1」——`MessageSummary.Date` 改 `*time.Time`（omitempty），nil 时客户端渲染 em dash；排序对 nil 取信封/内投递时间兜底
- **列表行 INBOX 标签**：单文件夹列表不再给每行盖 Folder 章（跨夹搜索结果仍保留来源夹标签）；`uid_validity` 盖章保留（move 守卫依赖）

### 变更
- **webmail-next 成为默认界面**：`/webmail` 直接进入新 Webmail；旧版保留三层逃生门：URL `?v=legacy`、偏好设置 `preferences.webmail_version=legacy`、本地存储 `storage=legacy`；`isWebmailNextEnabled` 服务端渲染恒为 next
- 设置抽屉新增「隐私与安全→远程图片」：默认阻止，可按账户允许全部远程加载（fail-closed，AES-256-GCM 加密存储）——旧版与新版均可配置
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
