# THREAD-UX 决策落地记录（2026-11-26，方向①②访谈四项）

依据：`Shared/design/THREAD-UX-REVIEW-V2-20261126.md` §5、`Shared/design/SSR-RENDER-PLAN-V2-20261126.md` §3/§5、`THREAD-MEMBERS-API-CONTRACT.md` §7-6。用户已拍板，均采建议项。基线 `b04e7953`，分支 `feat/thread-ux-decisions-20261126`。

## Q1 · 系统邮件真链语义：归组保留 + 子行默认收起 + 退信「回复」弱化前缀

- **归组保留**：服务端 References/In-Reply-To 强链归组不变，本方向无后端改动。
- **子行默认收起**：内联子行仅在 pill 显式点击后展开（`expandedThreadIds` 初始为空集），系统真链线程同样默认收起 —— `hooks/use-message-threading.ts`（终态追认注释）。
- **退信行「回复」弱化前缀（样式定稿）**：子行发件人命中系统发件人黑名单（`lib/system-sender.ts`，逐字镜像 `internal/webmail/threading.go` `systemSenderMarkers`，email+显示名双匹配）时，「回复」前缀以 **11px + `--vellum-ink-muted` 灰字**渲染（普通子行 12px + `--vellum-ink-secondary`）—— `components/webmail-message-row.tsx`。

## Q2 · weak_aggregation「相关 N 封」弱化措辞（契约 §7-6 前端欠账）

- `MessageSummary.weak_aggregation` / `WebmailItem.weakAggregation` 透传 —— `features/webmail/mailbox-api.ts`、`types.ts`、`hooks/use-message-threading.ts`。
- **列表 pill**：weak 时徽标文本与 title 切换 `webmail_next.thread_related_count`（zh「相关 {{count}} 封」/ en "{{count}} related messages"）—— `components/webmail-message-row.tsx`。
- **栈主题条徽标**：任一成员带服务端 `weak_aggregation` 盖章即切换 —— `components/webmail-thread-stack.tsx`。
- 阈值常量 `THREAD_WEAK_AGGREGATION_THRESHOLD = 20`（= 服务端 `WeakAggregationThreshold`）—— `lib/thread-members-api.ts`。

## Q3 · 内联展开终态追认（文档债，无行为改动）

- D-D 条件版（成员全载 → 内联展开；缺载 → 进栈）为**终态**；已标注于 `components/webmail-message-list.tsx`（`onToggleThread`）与 `hooks/use-message-threading.ts`。**勿再按 V0620 §7 原稿「移除内联、pill 纯进栈」返工。**

## Q4 · 巨线程豁免（真链线程子行 >20 条不自动展开）

- `weak_aggregation`（>20 成员）线程 pill 点击一律进栈（`onSelectMessage`），不再内联展开；子行默认收起，pill/栈入口保留 —— `components/webmail-message-list.tsx` `onToggleThread` Q4 gate。
- 边界注记：≥1024px 首封自动打开（`webmail-next-workbench.tsx`）不在本次决策文本内，保持现状；评审 V2 Q4 选项 B 的「首行不自动打开」未被采纳，如需另开单。

## O1 · 隐私纵深保持现状（无行为改动）

- 双层消毒（服务端渲染时 + 客户端 worker）+ same-origin sandboxed iframe（无 `allow-scripts`，CSP `script-src 'none'` 兜底）为**确认终态** —— 标注于 `components/webmail-reading-pane.tsx`（`mail-html-frame`）。勿单独移除任一层或改 sandbox 模式。
