import { useEffect } from "react";
import { useTranslation } from "react-i18next";

const NOTICE_MS = 8000;

/** Destination notice. Undo is offered only when the caller has an exact UID map. */
export function WebmailMoveNotice({
  message,
  token,
  onDone,
  onUndo,
  suggestRule,
  onSuggestRule,
}: {
  message: string | null;
  token: number;
  onDone: () => void;
  onUndo?: () => void;
  suggestRule?: { sender: string; folder: string } | null;
  onSuggestRule?: () => void;
}) {
  const { t } = useTranslation();

  useEffect(() => {
    if (!message) return;
    const timer = window.setTimeout(onDone, NOTICE_MS);
    return () => window.clearTimeout(timer);
  }, [message, token, onDone]);

  if (!message) return null;
  return (
    <div
      role="status"
      data-testid="webmail-move-notice"
      className="fixed bottom-6 left-1/2 z-40 flex -translate-x-1/2 items-center gap-3 bg-[var(--vellum-ink)] text-white text-sm px-4 py-2 rounded-md shadow-lg"
    >
      <span>{message}</span>
      {onUndo && (
        <button
          type="button"
          data-testid="webmail-move-undo"
          className="text-xs font-semibold underline hover:text-[var(--brand-primary)]"
          onClick={onUndo}
        >
          {t("webmail_next.undo_move")}
        </button>
      )}
      {suggestRule && onSuggestRule && (
        <div className="flex items-center gap-2 border-l border-white/20 pl-3">
          <span className="text-xs text-white/80">
            {t("webmail_next.suggest_sieve_rule")}
          </span>
          <button
            type="button"
            data-testid="btn-create-sieve-rule"
            className="text-xs font-semibold text-[var(--brand-primary)] hover:text-[var(--brand-primary)] underline"
            onClick={onSuggestRule}
          >
            {t("webmail_next.create_sieve_rule_action")}
          </button>
        </div>
      )}
    </div>
  );
}
