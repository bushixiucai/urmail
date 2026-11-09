# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号语义化。完整对比见 [Releases](https://github.com/bushixiucai/urmail/releases)。

## [0.6.19] - 2026-11-09

移动端顶栏溢出缺陷 D2 修复（QA v0.6.18 轻复验发现，存量问题被 #98 放大）。无数据库迁移。

- **<md 顶栏「更多」折叠**：个人通讯录/偏好设置收进 MoreHorizontal 更多菜单（44×44 触发、菜单项 h-11、外点关闭、手写菜单）；修复 375 视口 header 溢出（scrollWidth 462>375，偏好设置完全在屏外触摸不可达、通讯录右侧 35px 被裁；320/414 同样溢出）（#100）
- **320 根修**：搜索容器/输入补 min-w-0（flex 默认 min-width:auto 是残余溢出根因）；品牌文字 <md 隐藏（圆点保留）
- ≥768 完全回归：4 按钮常显 44×44、更多隐藏、密度带文字；320/375/414 三档 header/doc scrollWidth 均等于视口宽（含菜单展开态）

## [0.6.18] - 2026-11-09

批次 2 终验收口（QA 有条件放量两项处置）。无数据库迁移。

- **顶栏 icon 按钮触控热区**：高级筛选/刷新/通讯录/偏好设置 4 按钮实测 24–28px <44px——热区扩至 44×44（视觉图标保持 16px），375px/1600px 布局无溢出（#98）
- 钓鱼安全检查入口注释与实现对齐：入口对所有邮件常显（按需分析由用户主动触发，含已验签/已分析；结果区仅在检查运行后渲染）——产品口径 PM 拍板保留常显设计，QA v0.6.15「普通信无按钮」基线经 git 双证证伪（部署滞后）（#98）

## [0.6.17] - 2026-11-09

UIUX 评审批次 2 全量交付（语义令牌体系 / 骨架空态 / 移动端适配）。无数据库迁移。

### 语义令牌体系（#92 #93 #96，UIUX-BATCH2-SPEC 实施）

- **新增 22 个语义令牌**（theme-vellum.css）：品牌系（brand-strong/brand-soft/brand-border）、墨色系（ink-disabled/ink-hover）、表面系（surface-muted）、警告系（warning-fill/border/text/text-strong/strong）、危险系（danger-fill/fill-soft/border/text/strong/strong-hover）、成功系（success-fill/border/text）、AI 琥珀系（ai-accent/ai-accent-strong，与警告同值分令牌，独立演进）；全部对比度实测达标
- **硬编码色 951→1**：机械迁移 419 处 + 语义三组 131 处 + 判读定夺落实，全仓仅剩白名单 #78350F（度假横幅链接 hover）；`dark:` 前缀类保持 0；settings 目录豁免至下批次
- **CI 守门**：check-color-tokens.mjs 接入 lint（hex 阈值 <140 逐次不得回升、dark:=0 断言、测试 fixture 与白名单排除）；靛青→品牌蓝为规格预期视觉变化
- 判读治理三修正：琥珀家族按用途映射禁止按值匹配（#D97706/#B45309 双案）、信息蓝链接统一 brand-primary（Gmail/Outlook 标准）、danger/ink 按钮 hover 补档位

### 骨架屏与空态（#94）

- 列表骨架 8 行双条（行高随三档密度 52/40/32 联动、请求 >300ms 才渲染防闪、整块 200ms fade 无 pulse 闪光）+ 阅读区骨架
- 空态组件 WebmailEmptyState 三场景：空文件夹/搜索无结果/全部已处理（图标+标题+说明+动作，i18n zh-CN/en 双语齐备）

### 移动端适配（#95，375px Playwright 验收 11/11）

- 写信 FAB 56×56（safe-area 内边距、写信 dock 打开时隐藏、批量条激活自动让位）；顶栏密度按钮 <md icon-only 44×44（三档密度统一）；<md 默认锁定 comfortable 密度（触控目标 ≥44px，html 密度 class 同步修正全局覆盖）；批量条 <md fixed 钉底；顶栏 logo 375px 单行（#96）

## [0.6.16] - 2026-11-09

UIUX 批次 1 验证回票修复（暗色决策①b 收尾）。无数据库迁移。

- **theme-provider 缺省模式锁 light**：缺省 `mode` 落 `"system"` 跟随 OS 深色切换 `html.dark`，而应用无主题设置入口，系统深色用户被动进入红条 dark 变体+浅色外壳的半暗割裂态，与决策①b（应用级暗色 v1 不支持）冲突——未持久化/非法值/`"system"` 一律 clamp 为 light，仅显式 `"dark"` 激活（OS 监听保留休眠，供 v2 主题功能复用）（#90）
- 钓鱼红条 3 处 `dark:` 死变体清理（暗色下不可达）
- 测试同步：深色 OS 下缺省 light、非法值回 light

## [0.6.15] - 2026-11-09

UIUX 评审批次 1 + 钓鱼红条接线。含数据库迁移（0159，新表 mailbox_ai_phishing_findings）。

### UIUX 批次 1（运行时评审 2 P0+全 S 项，#84）

- **品牌主色令牌补全**：`--brand-primary` 全局未定义致未读点/正在读 3px 指示条六态不可见（computed-style 实测 rgba(0,0,0,0)）——theme-vellum.css 补 `#2563eb`，焦点环一并恢复
- **正文可复制**：移除工作台根 select-none，阅读区正文容器补 select-text（导航/列表行/控件各自 select-none 保留）
- i18n 泄漏修复：排序按钮 label+tooltip 与附件标题入消息目录（zh/en 各 8 键）
- 纯文本正文 text-xs→text-sm（leading 22px）；AI 四组件（cue strip/compose companion/selection polish/structured summary）10/11px→12px
- **暗色决策①b 落地**：AI 区局部 dark 类全部移除（应用级暗色 v1 明确不支持；vellum dark 令牌补全随批次 2 语义令牌迁移进 backlog，届时按主题令牌自然恢复双态）

### 钓鱼红条接线（#85 #86 #87，QA 实测检测 score=0.99/critical 后的唯一缺口）

- **分析结果持久化与暴露**（#87，migration 0159）：phishing_v2 分析成功后 upsert mailbox_ai_phishing_findings（UNIQUE(mailbox_id,folder,uid_validity,uid)，重复分析覆盖，mailbox 删除级联）；GET message 对分析过的邮件返回可选 phishing 对象 {score, risk_level, reasons, indicators, analyzed_at}（risk_level 恒为服务端按 score 映射，≥0.6=high/≥0.85=critical）；未分析/失败均无键=前端静默；assist 成功后按消息 evict 渲染缓存（新增 Cache.InvalidateMessage），重开即见结果；写入 best-effort+失败计数 urmail_ai_phishing_persist_failures_total
- **读信区触发入口与红条**（#85）：发件人行安全徽标旁新增「钓鱼与安全检查」，两步 consent 契约调用（无 selection、409 静默重试）；score≥0.6 渲染 #FEE2E2 红条（标题正下方、indicators 枚举、评分行），<0.6 静默轻提示，消息无 risk 键完全无感；6 例测试钉住状态机
- **polish 选区约束**（#86）：polish_v2 模板 v3 加「revised_text 仅含被润色 draft 改写」约束（修 QA 发现的模型整篇重写致上下文重复），评估 harness 新增 len_le/not_contains 断言

### 验收

- v0.6.14 定向复测可放量（QA：两项阻断修复确认、translate 200/20.7s、渲染/线程回归全绿）
- 红条最终验收：探针二号（tiger 信箱保留样本，score=0.99）随本版部署后由 QA 执行；UIUX 批次 1 视觉回归同步

## [0.6.14] - 2026-11-09

v0.6.13 定向复测两项前端缺陷快速修复 + 安全徽章判读定案。无数据库迁移（schema 158 不变）。

- **摘要类操作不再误传 selection**（#82）：HTML-only 邮件 AI 读信全链 409 AI_SOURCE_CHANGED——前端把整份渲染文本（Infura 信 15023 字）当作 selection 传入，服务端 strings.Contains(原文, selection) 必败、provider 未被调用。修复：selection 语义收窄为「用户真实选区」，摘要类操作（摘要/待办/关键日期/翻译/更简短 chips）一律不传，仅选区润色携带
- **选区润色请求补契约字段**（#82）：润色 400 INVALID_PARAMS——请求缺 body/draft（服务端空 Body 拒绝），popover 只弹「生成失败」。修复：润色请求带当前草稿纯文本（editor-tiptap 新增 draftBody 透传，空草稿回退选中文本）+ draft=选中文本；popover 按服务端错误码展示具体原因并可重试（409 静默重试仍失败才出 consent 卡）
- **安全徽章判读定案**（无代码变更）：红色高危条来自 Webmail 按需「钓鱼/安全检查」操作（投递链路不跑 AI）；琥珀条=认证证明缺失的设计行为（本地直注无 milter 证明、Sieve 转发副本按策略剥证明，均非回归）；钓鱼判定只采信管线 score（≥0.6=high、≥0.85=critical），模型枚举不采信
- **translate 502 复核定案**：v0.6.13 的 90s 翻译预算已生效（时序证据：结算时刻早于 30s 预算应到期点，ctx 未触发 deadline），502 为 provider 侧 ~29.5s 网关超时+fallback 快败的外部抖动，双失败硬终态契约按设计走完；硬失败台账可见（callErr 先结算再返回）

## [0.6.13] - 2026-11-09

v0.6.12 生产验收（QA 定向复验）缺陷修复批次 + CI 加固。无数据库迁移（schema 158 不变）。

### AI 缺陷修复（QA v0.6.12 验收产出，六项全修）

- **结果缓存键补 per-op 选项**（#76）：「更简短」chip 带 `length_limit=100` 仍命中默认摘要缓存返回旧 5 条要点——归一化操作参数纳入缓存键，同操作不同参数必为不同键
- **provider 错误走降级契约**（#78）：translate 两次 502 AI_UNAVAILABLE 双层根因——provider 结构化调用打满 30s 客户端超时（生产 ai_usage_requests id16/17 status=uncertain 实证，provider 从未返回）+ `CompleteValidated` 违反「provider 错误不重试直接降级」契约硬返 502。修复：provider 错误改走纯文本 fallback resend（成功→200 `degraded=true, degrade_reason=provider_unavailable`），fallback 也失败才是 502 硬终态；translate 调用预算 30s→90s（`ai.OperationCallTimeout` per-op）；评估集 27→28（translate 长文稳定用例）；HTTP e2e 钉住「structured 失败→200 degraded」「降级结果不缓存」
- **写信两步 consent 契约**（#77 #79）：`draft_new` 409 AI_CONTENT_CONSENT_STALE 根因=前端一步直发不带 `content_digest`（契约要求 preview_only=true 拿 digest → confirmed=true+digest，digest 绑定全部输入字段+folder/uid 字节级比对）。实现两步流（preview→run）、输入冻结+静默重走 preview、同 request_key、409 静默恢复（不再弹「生成失败」）——起草/diff/回退/采纳全链恢复可用
- **连通测试填充模型下拉**（#77）：按 UX SPEC [Settings-3]，`/ai/models` 成功后由文本框升级为模型列表
- **todos 卡标题随操作走**（#77）+ **线程文案「已加载 N / 共 M 封」**（#77，v0.6.11 遗留口径项）
- **配额可观测与零扣减实证**：`ai.CacheHitsTotal` 计数器；缓存命中零扣减经三重验证（代码路径命中即 return 先于扣减 / 口径 calls_used=台账行数 / 生产对照 QA≥6 请求 vs 台账 5 行精确吻合）

### CI 加固（#80）

- **offsite flake 根修**：testdb 集群 bootstrap 锁改 `pg_try_advisory_lock` 重试循环（100ms→2s 退避），锁等待专享 3 分钟预算（总 5 分钟）；MigrateTo 无界锁收紧；unlock 独立 15s context 防泄漏。实测复现旧缺陷（持锁 120s→旧代码 90.01s FAIL「lock cluster bootstrap: deadline exceeded」，正是单跑绿/并行挂的形状）→新代码 2m1.8s 正常取得 fixture
- **DSN 防呆**：`testdb.RequireDatabaseURL`——测试域近似名（URMAIL_TEST_DSN 等 6 个）直接 FAIL 并点名改名，根除「本地变量名写错静默 skip、本地绿 CI 红」；泛用名仅 CI 显式 REQUIRE=1 时 FAIL，不误杀合法无 PG 单测
- offsite 旅程 deadline 移到 fixture 之后起算（锁排队不再静默吃预算）；agentd manifest.json 工作区污染兜底入库

## [0.6.12] - 2026-11-09

双主线：**AI 邮件处理（重点功能，全链路工程化）** + **服务端渲染管线系统性收口**。无数据库迁移（schema 158 不变）。

### AI 邮件处理（重点功能）

产品原则：内置 Prompt + 结构化数据处理 workflow，不依赖模型自由发挥。深度设计经用户终审批准后实施（决策主文档 + UX 规格 + 管线技术设计，17 个 PR 分三线交付）。

后端管线（#61 #62 #63 #65 #68 #71 #73）：
- **内置 Prompt 模板注册表**（#61）：每操作 system+user 模板常量化，版本号随变更递增进 metrics 与响应体，回滚=revert；每请求随机 nonce 包裹邮件内容 + 数据声明——模型无法用正文伪造闭合标签逃逸；few-shot 全不用（省一半 token、消除模仿注入面）
- **结构化输出 schema 全覆盖**（#62）：摘要=要点/待办/关键日期/语言；起草=主题/正文/语气说明/模型假设清单；润色=改写文本+修改理由；翻译=译文+源语言检测；分类/钓鱼升级（risk_level 由分数映射兜底，防枚举字段被注入操纵）；DisallowUnknownFields 严格校验
- **契约 v2**（#63）：assist 响应带 degraded/degrade_reason/template_version/cache_hit；legacy 字段别名保一个版本兼容；OpenAPI 全量更新
- **校验→重试→降级**（#65）：schema 失败低温重试 1 次 → 仍失败换纯文本模板重发 + degraded 标注（用户永远拿到带标注的可用结果，ollama 小模型同样可用）；provider 错误不重试直接降级；配额一次操作幂等扣一次（预扣防刷量，重试/降级不重复扣）
- **输出侧注入加固**（#68）：SanitizePayload 递归剥离指令回显（只剥不拒，记 injection_attempts_total）；五用例钉住
- **操作结果缓存**（#71）：键=操作+模板版本+内容摘要（含租户维度），命中不调 provider 不扣配额回 cache_hit=true；降级结果永不定格缓存
- **双层评估集**（#73）：27 fixtures（9 操作×3 例），mock 评估即 go test 零 CI 改动；URMAIL_AI_EVAL=1 走真实 provider（阈值云端遵从率 ≥95% / ollama ≥80%）；钉住 fence 抽取、risk_level 映射防谎报、注入输入输出干净

前端（#66 #69 #72）：
- **设置抽屉第 8 分区「AI 智控」**（#66）：BYOK write-only key、key_configured 徽标、清除、模型连通测试、额度看板
- **读信摘要卡**（#69）：要点/待办/关键日期/语言结构块 + 追问 chips（更简短/只看待办/翻译本文）+ 长信/外文信轻引导条（场景化引导，不自动生成不浪费配额）+ 钓鱼安全徽章（低风险静默、高风险醒目）+ degraded 降级标注
- **写信 AI 伴侣**（#72）：880px 双栏对话面板（chips 调优+自由输入+版本历史回退+模型假设审阅折叠卡）+ 选区就地润色胶囊（diff+修改理由）+ 移动时 AI 建议一键转 Sieve 规则（AI 仅建议不执行）
- UX 范式（用户访谈拍板）：混合双形态（读信=嵌入卡片、写信=对话面板）、入口少而准；Ask 全邮箱问答移至下期（结合搜索体验专门设计）

### 服务端渲染管线（系统性收口）

「修一处破一处」长尾的工程化根治，纯 Go 无新组件，每步带语料回归：
- **语料回归框架**（#58）：.eml 输入 + golden + 不变量断言（无 script/CSP 恰一份/无多格塌陷行）；QA 导出的 3 封真实破版邮件脱敏入语料（发件人域名哈希零泄露）——每个后续渲染改动都有真实邮件防护网
- **窄桶 inset clamp**（#59）：margin/padding-left/right ≥32px clamp 到 16px，修 Slack 营销信 375 宽下 97px 横向富余（真实样本 golden 实证生效）
- **钝器收敛**（#60）：ensureFluidStyle 只作用顶层容器与表格（嵌套 flex/inline-block 不再被强加满宽压成竖排）、min/max-width 识别、calc() 100% 分支保护、包裹层通配收敛为四元素精准作用面
- **暗色最小版**（#64）：color-scheme 声明让原生控件跟随系统深浅，零配色赋值不反色（Gmail 克制路线，测试钉住 dark 块不含配色）
- **背景图声明级代理**（#67）：background-image 与 img 同政策（默认阻止、允许时走服务端代理），style 属性从一票否决改为按声明粒度处理；顺带修复老式邮件 font color/face/size 丢失
- **style 选择器内联**（#70）：go-premailer 在净化前跑（内联→净化→适配），class 布局邮件（Slack/Notion 类）从「净化即失明」恢复完整骨架；@font-face 剥除、@media 按请求视口 bucket 服务端判定应用/丢弃
- **条件请求与可观测**（#74）：详情 200 带强 ETag（payload sha256）+ X-Render-Cache: hit|miss 观测头；If-None-Match 按 RFC 9110 弱比较→纯头 304，缓存命中路径连 IMAP FETCH 都不做——v0.6.11 复验 DEF-3「公网二次打开无体感」的正解

### 其他
- CI 加固（urmailctl offsite flake 诊断、本地 DSN 变量名防呆）随下版
- 无数据库迁移（schema 158 不变）

## [0.6.11] - 2026-11-08

v0.6.10 生产缺陷修复批次（QA 验收 6 缺陷 + 用户实拍 2 缺陷全修）+ CI 基建改造。无数据库迁移（schema 158 不变）。

### 修复（Webmail 渲染管线——「很多邮件排版有问题」的系统性根修）
- **净化器子树删除失效（P0）**（#55）：消毒遍历丢弃递归返回值，只有正文顶层的危险元素被过滤——任意嵌套深度的 script/style/iframe/被阻止图片全部残留为破图残壳（用户截图中的孤立图标+大空洞即此类）。修复为递归删子树保留文本；10 封生产样本图片残壳 **93 → 0**
- **多列表格视口适配塌陷**（#55）：行内 ≥2 格定宽时旧适配把每格都强制 `width:100%`，表格布局算法把窄列挤成竖排文字（Slack 营销邮件实拍：右栏文字逐字竖排）。修复为行级按比例百分比化（420/180→70%/30%）、单格行 100%、本就放得下的小行与 colspan 行不动只加 max-width 上限、`colgroup` 同法处理；普查真塌陷行 **55 → 0**
- **CSS 类定宽列**（#55）：列宽写在 `<style>` 块（如 Slack 的 icon_width）时服务端无法测算，保持标记不强制改写，配合上一条删除失效样式后由 auto 文本列让位；脱敏 Slack 结构 fixture 移动/桌面双断言
- v0.6.7–0.6.9 的深色邮件/MSO 条件注释修复在新管线复验 0 回归

### 修复（Webmail 界面——QA 生产验收缺陷）
- **列表预览摘要为空**（#55）：生产邮件头叠加 DKIM 后达 3.5–11KB，超出 `BODY.PEEK[]<0.2048>` 预算导致正文片段零字节（此前仅本地短头测试邮件可复现填充）。改为单命令拆分取 `HEADER.FIELDS` + `TEXT<0.N>`：一轮往返、预算不变、不置 `\Seen`，真实生产邮件预览恢复填充
- **渲染缓存永不命中**（#55）：命中硬性要求请求携带 `uid_validity`，而列表→详情流不带，同一邮件连开两次 833ms→1320ms。新增除 uid_validity 外六维松匹配命中，payload 回传渲染时 uid_validity 供校验；精确键语义不变，TTL 5min 兜底
- **文件夹名显示为编码值**（#52，用户实拍）：列表标题显示「b64.SU5CT1g」而非「收件箱」——服务端 `b64.` 编码名被直接当显示名渲染。新增 `decodeFolderName`，列表标题/跨夹搜索徽章/移动菜单统一显示明文，API 传参契约不变
- **线程徽章与接口计数不一致**（#52）：徽章显示当前页成员数而非服务端全量 `thread_count`（6 vs 164），修正后徽章=全量 N 封
- **窄视口隐私横幅逐字竖排**（#52）：375px 下「远程图片已阻止」横幅被挤竖排不可读，阅读区容器宽度计算一并修复
- **线程内回复无引用**（#52）：堆叠卡片点回复正文为空，现按当前展开那封生成引用块
- **悬停不显示完整时间**（#52）：时间被快捷操作图标替换，现完整时间与操作并存且不撑行
- 顺带修复 ResizeObserver loop 控制台告警

### 变更（CI 基建）
- **CI 耗时优化**（#50）：paths 过滤（纯文档 PR 不再白跑 35 分钟）、Integration 按 `-p1` 串行包分片并行（实测全量 **30–37 分钟 → 9.5 分钟**）、显式 GOCACHE/node_modules 缓存
- **工具链下载源改造**（#56）：Go/Node 工具链改从 go.dev / nodejs.org 安装（带缓存与重试）——GitHub 资产 CDN 故障期间 setup-go/setup-node 全量 403/504 的根修

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
