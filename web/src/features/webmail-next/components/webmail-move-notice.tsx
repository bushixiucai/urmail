import { useEffect } from "react";

const NOTICE_MS = 8000;

/** Destination notice only. Move does not return destination UIDs, so there is no undo. */
export function WebmailMoveNotice({
  message,
  token,
  onDone,
}: {
  message: string | null;
  token: number;
  onDone: () => void;
}) {
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
      className="fixed bottom-6 left-1/2 z-40 -translate-x-1/2 bg-[#18181B] text-white text-sm px-4 py-2.5 rounded-md shadow-lg"
    >
      {message}
    </div>
  );
}
