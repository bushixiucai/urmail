// web/src/features/webmail-next/components/webmail-thread-summary-bar.tsx
import { useTranslation } from "react-i18next";
import { Loader2, RotateCcw } from "lucide-react";

export type ThreadSummaryState = "idle" | "loading" | "done" | "error";

export interface ThreadSummaryBarProps {
  /** total - loaded (may overstate until has_more_newer lands). */
  earlierCount: number;
  state: ThreadSummaryState;
  step: number;
  onLoadEarlier: () => void;
  onRetry: () => void;
}

/**
 * Giant-thread summary bar (THREAD-STACK-DESIGN §3). Rendered at the top
 * of the card stack when the thread has unloaded older members.
 */
export function WebmailThreadSummaryBar({
  earlierCount,
  state,
  step,
  onLoadEarlier,
  onRetry,
}: ThreadSummaryBarProps) {
  const { t } = useTranslation();
  if (earlierCount <= 0 && state !== "error") return null;

  return (
    <div
      data-testid="thread-summary-bar"
      data-state={state}
      className="h-10 mx-auto max-w-[720px] mb-2 flex items-center justify-center gap-2 rounded-lg border border-dashed border-[var(--vellum-border)] bg-[var(--vellum-surface-subtle)] px-3"
    >
      <span className="text-xs text-[var(--vellum-ink-secondary)] truncate">
        {state === "error"
          ? t("webmail_next.thread_earlier_error")
          : t("webmail_next.thread_earlier_count", { count: earlierCount })}
      </span>
      {state === "loading" ? (
        <span
          data-testid="thread-summary-loading"
          className="flex items-center gap-1 text-xs text-[var(--vellum-ink-secondary)]"
        >
          <Loader2 className="w-3.5 h-3.5 animate-spin" />
        </span>
      ) : state === "done" ? (
        <span
          data-testid="thread-summary-done"
          className="text-xs text-[var(--vellum-ink-secondary)]"
        >
          {t("webmail_next.thread_earlier_done")}
        </span>
      ) : state === "error" ? (
        <button
          type="button"
          data-testid="thread-summary-retry"
          onClick={onRetry}
          className="h-7 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] border border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center gap-1"
        >
          <RotateCcw className="w-3.5 h-3.5" />
          <span>{t("webmail_next.thread_retry")}</span>
        </button>
      ) : (
        <button
          type="button"
          data-testid="thread-load-earlier"
          onClick={onLoadEarlier}
          className="h-7 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] border border-[var(--vellum-border)] bg-[var(--vellum-surface)]"
        >
          {t("webmail_next.thread_load_earlier", { step })}
        </button>
      )}
    </div>
  );
}
