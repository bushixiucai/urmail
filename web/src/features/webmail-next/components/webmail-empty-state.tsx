// 批次 2 B2-2（WEBMAIL-BATCH2-SPEC.md §3.3）：空态组件。
// 结构：垂直居中 max-w 320px ─ 48px 圆底（surface-muted）内 24px 图标
// （ink-secondary）─ 标题 14px/600 ink mt-3 ─ 说明 12px/16 ink-secondary
// max-w 32ch 居中 mt-1 ─ 主按钮 h-8 px-3 rounded-md（有动作时）mt-4。
import type { LucideIcon } from "lucide-react";

export function WebmailEmptyState({
  icon: Icon,
  title,
  description,
  actionLabel,
  onAction,
  testId,
}: {
  icon: LucideIcon;
  title: string;
  description: string;
  actionLabel?: string;
  onAction?: () => void;
  testId?: string;
}) {
  return (
    <div
      data-testid={testId}
      className="h-full flex flex-col items-center justify-center text-center px-6 mx-auto max-w-[320px]"
    >
      <div className="w-12 h-12 rounded-full bg-[var(--vellum-surface-muted)] flex items-center justify-center">
        <Icon className="w-6 h-6 text-[var(--vellum-ink-secondary)]" />
      </div>
      <p className="mt-3 text-sm font-semibold leading-[22px] text-[var(--vellum-ink)]">
        {title}
      </p>
      <p className="mt-1 text-xs leading-4 text-[var(--vellum-ink-secondary)] max-w-[32ch]">
        {description}
      </p>
      {actionLabel && onAction && (
        <button
          type="button"
          onClick={onAction}
          className="mt-4 h-8 px-3 rounded-md bg-[var(--brand-primary)] text-white text-xs font-medium hover:bg-[var(--vellum-brand-strong)] transition-colors"
        >
          {actionLabel}
        </button>
      )}
    </div>
  );
}
