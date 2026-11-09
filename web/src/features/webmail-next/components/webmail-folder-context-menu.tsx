import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

export interface FolderMenuState {
  nameEnc: string;
  name: string;
  x: number;
  y: number;
}

interface WebmailFolderContextMenuProps {
  state: FolderMenuState | null;
  onRename: (nameEnc: string, currentName: string) => void;
  onDelete: (nameEnc: string, name: string) => void;
  onClose: () => void;
}

/**
 * P1-7: context menu for custom folders only. The rail never attaches it to
 * special-use folders, so system folders have no rename/delete path at all.
 */
export function WebmailFolderContextMenu({
  state,
  onRename,
  onDelete,
  onClose,
}: WebmailFolderContextMenuProps) {
  const { t } = useTranslation();
  const rootRef = useRef<HTMLDivElement>(null);

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

  useEffect(() => {
    if (!state) return;
    const opener = document.activeElement as HTMLElement | null;
    const first = rootRef.current?.querySelector<HTMLButtonElement>("button");
    first?.focus();
    return () => {
      opener?.focus();
    };
  }, [state?.nameEnc, state?.x, state?.y]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!state || typeof document === "undefined") return null;
  const margin = 8;
  const x = Math.max(
    margin,
    Math.min(state.x, window.innerWidth - 180 - margin),
  );
  const y = Math.max(
    margin,
    Math.min(state.y, window.innerHeight - 2 * 32 - margin),
  );

  const runAndClose = (action: () => void) => {
    onClose();
    action();
  };

  return createPortal(
    <div
      ref={rootRef}
      role="menu"
      aria-label={t("webmail_next.folder_actions")}
      data-testid="webmail-folder-context-menu"
      className="fixed z-50 rounded-lg border border-[var(--vellum-border)] bg-[var(--vellum-surface)] shadow-lg py-1 select-none"
      style={{ left: x, top: y, width: 180 }}
    >
      <button
        type="button"
        role="menuitem"
        data-testid="folder-menu-rename"
        className="w-full flex items-center px-3 h-8 text-left text-xs text-[var(--vellum-ink)] hover:bg-[var(--vellum-surface-muted)]"
        onClick={() => runAndClose(() => onRename(state.nameEnc, state.name))}
      >
        {t("webmail_next.rename_folder")}
      </button>
      <button
        type="button"
        role="menuitem"
        data-testid="folder-menu-delete"
        className="w-full flex items-center px-3 h-8 text-left text-xs text-[#DC2626] hover:bg-[var(--vellum-surface-muted)]"
        onClick={() => runAndClose(() => onDelete(state.nameEnc, state.name))}
      >
        {t("webmail_next.delete_folder")}
      </button>
    </div>,
    document.body,
  );
}
