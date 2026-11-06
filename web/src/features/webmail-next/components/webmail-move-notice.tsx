import { useEffect } from "react";
import { useTranslation } from "react-i18next";

const NOTICE_MS = 8000;

/** Destination notice. Undo is offered only when the caller has an exact UID map. */
export function WebmailMoveNotice({
  message,
  token,
  onDone,
  onUndo,
}: {
  message: string | null;
  token: number;
  onDone: () => void;
  onUndo?: () => void;
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
      className="fixed bottom-6 left-1/2 z-40 flex -translate-x-1/2 items-center gap-3 bg-[#18181B] text-white text-sm px-4 py-2.5 rounded-md shadow-lg"
    >
      <span>{message}</span>
      {onUndo && (
        <button
          type="button"
          data-testid="webmail-move-undo"
          className="text-xs font-semibold underline"
          onClick={onUndo}
        >
          {t("webmail_next.undo_move")}
        </button>
      )}
    </div>
  );
}
