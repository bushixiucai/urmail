import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Inbox,
  FileText,
  Send,
  Clock,
  AlertOctagon,
  Trash2,
  Archive,
  SquarePen,
  Folder,
  FolderPlus,
  type LucideIcon,
} from "lucide-react";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";
import { resolveSpecialFolder, SCHEDULED_VIEW } from "../lib/special-folders";
import {
  WebmailFolderContextMenu,
  type FolderMenuState,
} from "./webmail-folder-context-menu";
import { MAILBOX_DRAG_TYPE } from "./webmail-message-list";

interface WebmailNavigationRailProps {
  isCollapsed: boolean;
  activeFolder: string;
  onSelectFolder: (folderEnc: string) => void;
  folders: MailboxFolder[];
  onCompose: () => void;
  inboxUnread?: number;
  quota?: { used_bytes: number; quota_bytes: number } | null;
  /** P1-2: move dropped UIDs; resolves false when the move failed. */
  onDropUids?: (
    folderEnc: string,
    uids: number[],
  ) => Promise<boolean> | boolean;
  /** P1-7: create a folder; resolves to null on failure. */
  onCreateFolder?: (name: string) => Promise<MailboxFolder | null>;
  /** P1-7: rename a custom folder; resolves to false on failure. */
  onRenameFolder?: (
    nameEnc: string,
    name: string,
  ) => Promise<boolean> | boolean;
  /** P1-7: delete a custom folder (messages go with it). */
  onDeleteFolder?: (nameEnc: string) => Promise<boolean> | boolean;
  /** P1-7: confirm dialog element selector rendered by the workbench. */
  deleteConfirmSlot?: (props: {
    open: boolean;
    folderName: string;
    onCancel: () => void;
    onConfirm: () => void;
  }) => React.ReactNode;
}

const SYSTEM: Array<{
  flag: string;
  fallback: string;
  labelKey: string;
  icon: LucideIcon;
  testId: string;
}> = [
  {
    flag: "\\Inbox",
    fallback: "INBOX",
    labelKey: "webmail_next.inbox",
    icon: Inbox,
    testId: "inbox",
  },
  {
    flag: "\\Drafts",
    fallback: "Drafts",
    labelKey: "webmail_next.drafts",
    icon: FileText,
    testId: "drafts",
  },
  {
    flag: "\\Sent",
    fallback: "Sent",
    labelKey: "webmail_next.sent",
    icon: Send,
    testId: "sent",
  },
  {
    flag: "\\Junk",
    fallback: "Junk",
    labelKey: "webmail_next.junk",
    icon: AlertOctagon,
    testId: "junk",
  },
  {
    flag: "\\Trash",
    fallback: "Trash",
    labelKey: "webmail_next.trash",
    icon: Trash2,
    testId: "trash",
  },
  {
    flag: "\\Archive",
    fallback: "Archive",
    labelKey: "webmail_next.archive",
    icon: Archive,
    testId: "archive",
  },
];

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : 1;
  return `${size.toFixed(digits)} ${units[unit]}`;
}

export function WebmailNavigationRail({
  isCollapsed,
  activeFolder,
  onSelectFolder,
  folders,
  onCompose,
  inboxUnread = 0,
  quota = null,
  onDropUids,
  onCreateFolder,
  onRenameFolder,
  onDeleteFolder,
  deleteConfirmSlot,
}: WebmailNavigationRailProps) {
  const { t } = useTranslation();
  const resolved = SYSTEM.map((item) => ({
    ...item,
    folder: resolveSpecialFolder(folders, item.flag, item.fallback),
  })).filter((item) => item.folder);
  const systemEnc = new Set(
    resolved.map((item) => item.folder?.name_enc.toLowerCase()),
  );
  const custom = folders.filter(
    (folder) => !systemEnc.has(folder.name_enc.toLowerCase()),
  );
  const showQuota =
    quota != null &&
    Number.isFinite(quota.used_bytes) &&
    Number.isFinite(quota.quota_bytes) &&
    quota.quota_bytes > 0;
  const quotaPercent = showQuota
    ? Math.round((quota.used_bytes / quota.quota_bytes) * 100)
    : 0;
  const quotaText = showQuota
    ? `${formatBytes(quota.used_bytes)} / ${formatBytes(quota.quota_bytes)}`
    : "";

  // P1-2: drag hover state and payload reader.
  const [dropTargetEnc, setDropTargetEnc] = useState<string | null>(null);
  // P1-7: inline create input, rename state, delete confirm, folder menu.
  const [createOpen, setCreateOpen] = useState(false);
  const [createName, setCreateName] = useState("");
  const [renaming, setRenaming] = useState<{
    nameEnc: string;
    name: string;
  } | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [folderMenu, setFolderMenu] = useState<FolderMenuState | null>(null);
  const [deletePending, setDeletePending] = useState<{
    nameEnc: string;
    name: string;
  } | null>(null);
  const createInputRef = useRef<HTMLInputElement>(null);

  const readDraggedUids = (event: React.DragEvent): number[] => {
    try {
      const raw = event.dataTransfer.getData(MAILBOX_DRAG_TYPE);
      if (!raw) return [];
      const parsed: unknown = JSON.parse(raw);
      if (!Array.isArray(parsed)) return [];
      return parsed.filter((item): item is number => typeof item === "number");
    } catch {
      return [];
    }
  };

  const itemClass = (active: boolean) =>
    `w-full flex items-center gap-3 px-3 py-2 rounded-md text-xs transition-colors ${
      active
        ? "bg-[var(--vellum-brand-soft)] text-[var(--brand-primary,var(--brand-primary))] font-semibold"
        : "text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)]"
    }`;

  const dropHandlers = (folderEnc: string) =>
    onDropUids
      ? {
          onDragOver: (event: React.DragEvent) => {
            if (!event.dataTransfer.types.includes(MAILBOX_DRAG_TYPE)) return;
            event.preventDefault();
            event.dataTransfer.dropEffect = "move";
            setDropTargetEnc(folderEnc);
          },
          onDragLeave: () =>
            setDropTargetEnc((current) =>
              current === folderEnc ? null : current,
            ),
          onDrop: async (event: React.DragEvent) => {
            event.preventDefault();
            setDropTargetEnc(null);
            const uids = readDraggedUids(event);
            if (uids.length === 0) return;
            const ok = await onDropUids?.(folderEnc, uids);
            if (ok === false) {
              // Failure keeps the list as-is (source rows never moved).
              return;
            }
          },
        }
      : {};

  const submitCreate = async () => {
    const name = createName.trim();
    if (!name || !onCreateFolder) {
      setCreateOpen(false);
      setCreateName("");
      return;
    }
    const created = await onCreateFolder(name);
    if (created) {
      setCreateOpen(false);
      setCreateName("");
      onSelectFolder(created.name_enc);
    } else {
      createInputRef.current?.focus();
    }
  };

  const submitRename = async () => {
    if (!renaming) return;
    const name = renameValue.trim();
    if (name && name !== renaming.name)
      await onRenameFolder?.(renaming.nameEnc, name);
    setRenaming(null);
  };

  return (
    <aside
      data-testid="webmail-navigation-rail"
      data-collapsed={isCollapsed ? "true" : "false"}
      className={`border-r border-[var(--vellum-border)] bg-[var(--vellum-ground)] flex flex-col justify-between transition-all duration-200 shrink-0 select-none ${
        isCollapsed ? "w-16 items-center" : "w-56"
      }`}
    >
      <div className="p-3 w-full">
        <button
          data-testid="webmail-compose-button"
          type="button"
          onClick={onCompose}
          className={`w-full flex items-center justify-center gap-2 bg-[var(--brand-primary,var(--brand-primary))] text-white font-medium rounded-lg hover:brightness-105 active:brightness-95 transition-all focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--brand-primary)] focus-visible:outline-offset-2 ${
            isCollapsed ? "h-10 px-0" : "h-10 px-4"
          }`}
          aria-label={t("webmail_next.compose")}
        >
          <SquarePen className="w-4 h-4 shrink-0" />
          {!isCollapsed && (
            <span className="text-sm font-semibold">
              {t("webmail_next.compose")}
            </span>
          )}
        </button>

        <nav className="mt-4 space-y-0.5 w-full">
          {resolved.map((item) => {
            const folder = item.folder;
            if (!folder) return null;
            const Icon = item.icon;
            const isActive = activeFolder === folder.name_enc;
            const unread = item.flag === "\\Inbox" ? inboxUnread : 0;
            return (
              <button
                key={item.testId}
                data-testid={`nav-folder-${item.testId}`}
                type="button"
                onClick={() => onSelectFolder(folder.name_enc)}
                className={itemClass(isActive)}
                style={
                  dropTargetEnc === folder.name_enc
                    ? { backgroundColor: "var(--vellum-row-active)" }
                    : undefined
                }
                title={t(item.labelKey)}
                {...dropHandlers(folder.name_enc)}
              >
                <Icon className="w-4 h-4 shrink-0" />
                {!isCollapsed && (
                  <div className="flex-1 flex items-center justify-between overflow-hidden">
                    <span className="truncate">{t(item.labelKey)}</span>
                    {unread > 0 && (
                      <span
                        data-testid="inbox-unread-badge"
                        className="px-1.5 py-0.5 rounded-full text-xs bg-[var(--brand-primary)] text-white font-bold leading-none"
                      >
                        {unread}
                      </span>
                    )}
                  </div>
                )}
              </button>
            );
          })}

          <button
            data-testid="nav-folder-scheduled"
            type="button"
            onClick={() => onSelectFolder(SCHEDULED_VIEW)}
            className={itemClass(activeFolder === SCHEDULED_VIEW)}
            title={t("webmail_next.scheduled")}
          >
            <Clock className="w-4 h-4 shrink-0" />
            {!isCollapsed && (
              <span className="truncate">{t("webmail_next.scheduled")}</span>
            )}
          </button>

          {custom.map((folder) =>
            renaming?.nameEnc === folder.name_enc ? (
              <input
                key={folder.name_enc}
                data-testid="folder-rename-input"
                autoFocus
                value={renameValue}
                onChange={(event) => setRenameValue(event.target.value)}
                onBlur={submitRename}
                onKeyDown={(event) => {
                  if (event.key === "Enter") void submitRename();
                  if (event.key === "Escape") setRenaming(null);
                }}
                className="w-full px-2 py-1.5 rounded-md border border-[var(--brand-primary,var(--brand-primary))] text-xs bg-white"
                aria-label={t("webmail_next.rename_folder")}
              />
            ) : (
              <button
                key={folder.name_enc}
                data-testid={`nav-folder-${folder.name_enc}`}
                type="button"
                onClick={() => onSelectFolder(folder.name_enc)}
                onContextMenu={(event) => {
                  if (isCollapsed) return;
                  event.preventDefault();
                  setFolderMenu({
                    nameEnc: folder.name_enc,
                    name: folder.name,
                    x: event.clientX,
                    y: event.clientY,
                  });
                }}
                className={itemClass(activeFolder === folder.name_enc)}
                style={
                  dropTargetEnc === folder.name_enc
                    ? { backgroundColor: "var(--vellum-row-active)" }
                    : undefined
                }
                title={folder.name}
                {...dropHandlers(folder.name_enc)}
              >
                <Folder className="w-4 h-4 shrink-0" />
                {!isCollapsed && (
                  <span className="truncate flex-1 text-left">
                    {folder.name}
                  </span>
                )}
              </button>
            ),
          )}
        </nav>

        {/* P1-7: create folder at the bottom of the folder list. */}
        {onCreateFolder && !isCollapsed && (
          <div className="mt-2">
            {createOpen ? (
              <input
                ref={createInputRef}
                data-testid="folder-create-input"
                autoFocus
                value={createName}
                placeholder={t("webmail_next.new_folder")}
                aria-label={t("webmail_next.new_folder")}
                onChange={(event) => setCreateName(event.target.value)}
                onBlur={() => void submitCreate()}
                onKeyDown={(event) => {
                  if (event.key === "Enter") void submitCreate();
                  if (event.key === "Escape") {
                    setCreateOpen(false);
                    setCreateName("");
                  }
                }}
                className="w-full px-2 py-1.5 rounded-md border border-[var(--brand-primary,var(--brand-primary))] text-xs bg-white"
              />
            ) : (
              <button
                data-testid="folder-create-button"
                type="button"
                onClick={() => {
                  setCreateName("");
                  setCreateOpen(true);
                }}
                className="w-full flex items-center gap-2 px-3 py-2 rounded-md text-xs text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-row-hover)] hover:text-[var(--vellum-ink)]"
              >
                <FolderPlus className="w-4 h-4 shrink-0" />
                <span className="truncate">{t("webmail_next.new_folder")}</span>
              </button>
            )}
          </div>
        )}
      </div>

      {showQuota && !isCollapsed && (
        <div
          data-testid="webmail-quota-bar"
          className="p-4 border-t border-[var(--vellum-border)] bg-[var(--vellum-surface)]/60"
        >
          <div className="flex justify-between items-center text-xs text-[var(--vellum-ink-secondary)] mb-1.5 font-medium">
            <span>{t("webmail_next.storage")}</span>
            <span>{quotaPercent}%</span>
          </div>
          <div className="w-full h-1.5 bg-[var(--vellum-border)] rounded-full overflow-hidden">
            <div
              className={`h-full transition-all duration-300 ${
                quotaPercent > 95
                  ? "bg-[#DC2626]"
                  : quotaPercent > 80
                    ? "bg-[var(--vellum-color-warning)]"
                    : "bg-[var(--brand-primary,var(--brand-primary))]"
              }`}
              style={{ width: `${Math.min(100, Math.max(0, quotaPercent))}%` }}
            />
          </div>
          <div className="mt-1 text-xs text-[var(--vellum-ink-secondary)] truncate">
            {quotaText}
          </div>
        </div>
      )}
      {showQuota && isCollapsed && (
        <div
          data-testid="webmail-quota-bar-mini"
          className="p-3 border-t border-[var(--vellum-border)] flex justify-center"
          title={quotaText}
        >
          <div className="w-2.5 h-2.5 rounded-full bg-[var(--brand-primary,var(--brand-primary))]" />
        </div>
      )}

      {/* P1-7: custom-folder context menu (rename/delete). System folders
          never receive this menu, so no delete path exists for them. */}
      <WebmailFolderContextMenu
        state={folderMenu}
        onRename={(nameEnc, currentName) => {
          setRenameValue(currentName);
          setRenaming({ nameEnc, name: currentName });
        }}
        onDelete={(nameEnc, name) => {
          setDeletePending({ nameEnc, name });
        }}
        onClose={() => setFolderMenu(null)}
      />
      {deleteConfirmSlot?.({
        open: deletePending !== null,
        folderName: deletePending?.name ?? "",
        onCancel: () => setDeletePending(null),
        onConfirm: () => {
          const pending = deletePending;
          setDeletePending(null);
          if (pending) void onDeleteFolder?.(pending.nameEnc);
        },
      })}
    </aside>
  );
}
