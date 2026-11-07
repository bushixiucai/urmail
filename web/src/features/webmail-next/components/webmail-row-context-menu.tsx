import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import {
  ChevronRight,
  FolderInput,
  MailOpen,
  Mail,
  Archive,
  Trash2,
  Reply,
  Forward,
  ExternalLink,
} from "lucide-react";

export interface ContextMenuFolder {
  name: string;
  name_enc: string;
}

export interface RowContextMenuState {
  uid: number;
  x: number;
  y: number;
  isUnread: boolean;
}

export interface RowContextMenuActionHandlers {
  onOpen: (uid: number) => void;
  onReply: (uid: number) => void;
  onForward: (uid: number) => void;
  onArchive: (uids: number[]) => void;
  onDelete: (uids: number[]) => void;
  onToggleRead: (uid: number) => void;
  onMoveTo: (uids: number[], folder: ContextMenuFolder) => void;
}

interface WebmailRowContextMenuProps {
  state: RowContextMenuState | null;
  folders: ContextMenuFolder[];
  handlers: RowContextMenuActionHandlers;
  onClose: () => void;
}

const MENU_WIDTH = 220;
const ITEM_HEIGHT = 32;
const MOVE_SUB_WIDTH = 200;

function clampMenuPosition(x: number, y: number, subOpen: boolean) {
  const width = subOpen ? MENU_WIDTH + MOVE_SUB_WIDTH : MENU_WIDTH;
  const margin = 8;
  const maxX = window.innerWidth - width - margin;
  const maxY = window.innerHeight - 9 * ITEM_HEIGHT - margin;
  return {
    x: Math.max(margin, Math.min(x, Math.max(margin, maxX))),
    y: Math.max(margin, Math.min(y, Math.max(margin, maxY))),
  };
}

export function WebmailRowContextMenu({
  state,
  folders,
  handlers,
  onClose,
}: WebmailRowContextMenuProps) {
  const { t } = useTranslation();
  const [moveOpen, setMoveOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // Focus the first item when the menu opens; remember the opener for restore.
  useEffect(() => {
    if (!state) return;
    setMoveOpen(false);
    const opener = document.activeElement as HTMLElement | null;
    const first = rootRef.current?.querySelector<HTMLButtonElement>("button");
    first?.focus();
    return () => {
      // Return focus to the row that opened the menu.
      const row =
        opener?.closest<HTMLElement>("[data-testid^='mail-row-']") ??
        document.querySelector<HTMLElement>(
          `[data-testid='mail-row-${state.uid}']`,
        );
      row?.focus();
    };
  }, [state?.uid, state?.x, state?.y]); // eslint-disable-line react-hooks/exhaustive-deps

  const runAndClose = useCallback(
    (action: () => void) => {
      onClose();
      action();
    },
    [onClose],
  );

  useEffect(() => {
    if (!state) return;
    const onPointerDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) onClose();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        onClose();
      }
    };
    window.addEventListener("mousedown", onPointerDown, true);
    window.addEventListener("keydown", onKeyDown, true);
    window.addEventListener("resize", onClose);
    window.addEventListener("blur", onClose);
    return () => {
      window.removeEventListener("mousedown", onPointerDown, true);
      window.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("resize", onClose);
      window.removeEventListener("blur", onClose);
    };
  }, [state, onClose]);

  if (!state || typeof document === "undefined") return null;
  const pos = clampMenuPosition(state.x, state.y, moveOpen);
  const uids = [state.uid];

  const itemClass =
    "w-full flex items-center gap-2.5 px-3 h-8 text-left text-xs text-[#18181B] hover:bg-[#F6F4EE] disabled:opacity-40 disabled:pointer-events-none";

  const item = (
    label: string,
    icon: React.ReactNode,
    action: () => void,
    testId?: string,
    extraClass = "",
  ) => (
    <button
      type="button"
      role="menuitem"
      data-testid={testId}
      className={`${itemClass} ${extraClass}`}
      onClick={() => runAndClose(action)}
    >
      {icon}
      <span className="truncate">{label}</span>
    </button>
  );

  return createPortal(
    <div
      ref={rootRef}
      role="menu"
      aria-label={t("webmail_next.context_menu")}
      data-testid="webmail-row-context-menu"
      className="fixed z-50 rounded-lg border border-[var(--vellum-border)] bg-[var(--vellum-surface)] shadow-lg py-1 select-none"
      style={{ left: pos.x, top: pos.y, width: MENU_WIDTH }}
    >
      {item(
        t("webmail_next.open_message"),
        <ExternalLink className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />,
        () => handlers.onOpen(state.uid),
        "ctx-open",
      )}
      {item(
        t("webmail_next.reply"),
        <Reply className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />,
        () => handlers.onReply(state.uid),
        "ctx-reply",
      )}
      {item(
        t("webmail_next.forward"),
        <Forward className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />,
        () => handlers.onForward(state.uid),
        "ctx-forward",
      )}
      <div
        className="my-1 border-t border-[var(--vellum-border-subtle)]"
        role="separator"
      />
      {item(
        t("webmail_next.bulk_archive"),
        <Archive className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />,
        () => handlers.onArchive(uids),
        "ctx-archive",
      )}
      {item(
        t("webmail_next.delete"),
        <Trash2 className="w-3.5 h-3.5 text-[#DC2626]" />,
        () => handlers.onDelete(uids),
        "ctx-delete",
      )}
      {item(
        state.isUnread
          ? t("webmail_next.mark_read")
          : t("webmail_next.mark_unread"),
        state.isUnread ? (
          <MailOpen className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
        ) : (
          <Mail className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
        ),
        () => handlers.onToggleRead(state.uid),
        "ctx-toggle-read",
      )}
      <div
        className="my-1 border-t border-[var(--vellum-border-subtle)]"
        role="separator"
      />
      <div className="relative">
        <button
          type="button"
          role="menuitem"
          aria-haspopup="menu"
          aria-expanded={moveOpen}
          data-testid="ctx-move"
          className={itemClass}
          onClick={() => setMoveOpen((open) => !open)}
          onKeyDown={(event) => {
            if (event.key === "ArrowRight") {
              event.preventDefault();
              setMoveOpen(true);
            }
          }}
        >
          <FolderInput className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
          <span className="truncate flex-1">{t("webmail_next.move_to")}</span>
          <ChevronRight className="w-3 h-3 text-[var(--vellum-ink-secondary)]" />
        </button>
        {moveOpen && (
          <div
            role="menu"
            aria-label={t("webmail_next.move_to")}
            data-testid="ctx-move-submenu"
            className="absolute rounded-lg border border-[var(--vellum-border)] bg-[var(--vellum-surface)] shadow-lg py-1"
            style={{
              left: MENU_WIDTH - 6,
              top: -4,
              width: MOVE_SUB_WIDTH,
              maxHeight: 9 * ITEM_HEIGHT,
              overflowY: "auto",
            }}
          >
            {folders.length === 0 && (
              <div className="px-3 py-1.5 text-xs text-[var(--vellum-ink-secondary)]">
                {t("webmail_next.no_messages")}
              </div>
            )}
            {folders.map((folder) => (
              <button
                key={folder.name_enc}
                type="button"
                role="menuitem"
                data-testid={`ctx-move-${folder.name_enc}`}
                className={itemClass}
                onClick={() =>
                  runAndClose(() => handlers.onMoveTo(uids, folder))
                }
              >
                <span className="truncate">{folder.name}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

/** Test seam: menu positioning reads the viewport through this helper. */
export { clampMenuPosition };
