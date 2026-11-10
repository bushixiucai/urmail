// web/src/features/webmail-next/components/webmail-thread-newer-bar.tsx
import { useTranslation } from "react-i18next";
import { ArrowDown } from "lucide-react";

export interface ThreadNewerBarProps {
  /** Unloaded newer members (deep-anchored window only, >0 to render). */
  newerCount: number;
  onLoadNewer: () => void;
}

/**
 * "更新的 N 封" entry below the card stack (§12): deep-anchored windows
 * leave newer members unloaded; jumping re-fetches the latest window.
 */
export function WebmailThreadNewerBar({
  newerCount,
  onLoadNewer,
}: ThreadNewerBarProps) {
  const { t } = useTranslation();
  if (newerCount <= 0) return null;
  return (
    <div
      data-testid="thread-newer-bar"
      className="h-10 mx-auto max-w-[720px] mt-2 flex items-center justify-center gap-2 rounded-lg border border-dashed border-[var(--vellum-border)] bg-[var(--vellum-surface-subtle)] px-3"
    >
      <span className="text-xs text-[var(--vellum-ink-secondary)] truncate">
        {t("webmail_next.thread_newer_count", { count: newerCount })}
      </span>
      <button
        type="button"
        data-testid="thread-load-newer"
        onClick={onLoadNewer}
        className="h-7 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] border border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center gap-1"
      >
        <ArrowDown className="w-3.5 h-3.5" />
        <span>{t("webmail_next.thread_load_newer")}</span>
      </button>
    </div>
  );
}
