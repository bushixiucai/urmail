import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  CheckSquare,
  Square,
  ArrowDownUp,
  RefreshCw,
  Archive,
  Trash2,
  Mail,
  MailOpen,
  Star,
  FolderInput,
} from "lucide-react";
import { WebmailMessageRow } from "./webmail-message-row";
import {
  WebmailRowContextMenu,
  type RowContextMenuState,
  type RowContextMenuActionHandlers,
  type ContextMenuFolder,
} from "./webmail-row-context-menu";
import { useMessageThreading } from "../hooks/use-message-threading";
import { useRangeSelection } from "../hooks/use-range-selection";
import type {
  MessageSummary,
  MailboxSortKey,
} from "@/features/webmail/mailbox-api";
import type { DensityMode, WebmailItem } from "../types";

/** Payload type for dragging UIDs from the list onto a folder. */
export const MAILBOX_DRAG_TYPE = "application/x-urmail-mailbox-uids";

export interface SelectionApi {
  toggle: (uid: number, shift: boolean, meta: boolean) => void;
  clear: () => void;
  selected: () => number[];
}

interface MoveFolder {
  name: string;
  name_enc: string;
}

interface WebmailMessageListProps {
  folderName: string;
  messages: MessageSummary[];
  activeUid: number | null;
  cursorUid?: number | null;
  onSelectMessage: (uid: number) => void;
  density: DensityMode;
  sort: MailboxSortKey;
  order: "asc" | "desc";
  onSortChange: (sort: MailboxSortKey, order: "asc" | "desc") => void;
  onRefresh?: () => void;
  isRefreshing?: boolean;
  onToggleStar?: (uid: number) => void;
  onDeleteMessage?: (uid: number) => void;
  onToggleRead?: (uid: number) => void;
  moveFolders?: MoveFolder[];
  onBulkArchive?: (uids: number[]) => void;
  onBulkTrash?: (uids: number[]) => void;
  onBulkRead?: (uids: number[]) => void;
  onBulkUnread?: (uids: number[]) => void;
  onBulkStar?: (uids: number[]) => void;
  onBulkMove?: (uids: number[], folderName: string) => void;
  onBindSelection?: (api: SelectionApi) => void;
  /** Row context menu (P1-1): folders for the move submenu. */
  contextFolders?: ContextMenuFolder[];
  /** Row context menu (P1-1): reuse the P0 semantic handlers. */
  contextHandlers?: RowContextMenuActionHandlers;
  totalCount?: number;
  isLoadingMore?: boolean;
  isInitialLoading?: boolean;
  /** Fetch failure for the current view: show an error, never "no messages". */
  loadError?: Error | null;
  onNearEnd?: () => void;
  onLoadedUids?: (uids: number[]) => void;
  /** Test and embedding override. Production measures the scroller. */
  viewportHeight?: number;
}

export function WebmailMessageList({
  folderName,
  messages,
  activeUid,
  cursorUid = null,
  onSelectMessage,
  density,
  sort,
  order,
  onSortChange,
  onRefresh,
  isRefreshing = false,
  onToggleStar,
  onDeleteMessage,
  onToggleRead,
  moveFolders = [],
  onBulkArchive,
  onBulkTrash,
  onBulkRead,
  onBulkUnread,
  onBulkStar,
  onBulkMove,
  onBindSelection,
  contextFolders = [],
  contextHandlers,
  totalCount,
  isLoadingMore = false,
  isInitialLoading = false,
  loadError = null,
  onNearEnd,
  onLoadedUids,
  viewportHeight,
}: WebmailMessageListProps) {
  const { t } = useTranslation();
  const { items, toggleThreadExpand } = useMessageThreading(messages);
  const { selectedUids, toggleSelect, selectAll, clearSelection } =
    useRangeSelection(items);
  const [moveOpen, setMoveOpen] = useState(false);
  // P1-1: one row context menu instance for the whole list.
  const [menuState, setMenuState] = useState<RowContextMenuState | null>(null);
  const itemsRef = useRef(items);
  itemsRef.current = items;

  const openContextMenu = (uid: number, x: number, y: number) => {
    const target = itemsRef.current.find((item) => item.uid === uid);
    if (!target) return;
    setMenuState({ uid, x, y, isUnread: target.isUnread });
  };

  // P1-1 keyboard parity: Shift+F10 or the Menu key opens the menu on the
  // focused (or cursor) row, anchored near that row.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (
        event.key !== "ContextMenu" &&
        !(event.shiftKey && event.key === "F10")
      )
        return;
      const row = document.activeElement?.closest?.(
        "[data-testid^='mail-row-']",
      ) as HTMLElement | null;
      const uid = row
        ? Number(row.dataset.testid?.replace("mail-row-", ""))
        : Number.NaN;
      const resolved = Number.isFinite(uid)
        ? uid
        : (cursorUid ?? itemsRef.current[0]?.uid);
      if (resolved == null || !contextHandlers) return;
      const anchor = document.querySelector<HTMLElement>(
        `[data-testid='mail-row-${resolved}']`,
      );
      const rect = anchor?.getBoundingClientRect();
      event.preventDefault();
      openContextMenu(resolved, rect?.left ?? 80, (rect?.bottom ?? 80) + 4);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [cursorUid, contextHandlers]);

  const isAllSelected = items.length > 0 && selectedUids.size === items.length;
  const selectedList = [...selectedUids];
  const parentRef = useRef<HTMLDivElement>(null);
  const rowHeight =
    density === "comfortable" ? 52 : density === "list_only" ? 32 : 40;
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => rowHeight,
    overscan: 8,
    initialRect: { width: 380, height: viewportHeight ?? 640 },
    ...(viewportHeight
      ? {
          observeElementRect: (
            _instance: unknown,
            cb: (rect: { width: number; height: number }) => void,
          ) => {
            cb({ width: 380, height: viewportHeight });
            return () => {};
          },
        }
      : {}),
  });
  const virtualItems = virtualizer.getVirtualItems();
  const virtualizerRef = useRef(virtualizer);
  virtualizerRef.current = virtualizer;

  useEffect(() => {
    onLoadedUids?.(items.map((item) => item.uid));
  }, [items, onLoadedUids]);

  useEffect(() => {
    if (cursorUid == null) return;
    const index = items.findIndex((item) => item.uid === cursorUid);
    if (index >= 0) {
      virtualizerRef.current.scrollToIndex(index, { align: "auto" });
    }
  }, [cursorUid, items]);

  useEffect(() => {
    const element = parentRef.current;
    if (!element || !onNearEnd) return;
    const onScroll = () => {
      if (element.clientHeight <= 0) return;
      const remaining =
        element.scrollHeight - element.scrollTop - element.clientHeight;
      if (remaining <= 240) onNearEnd();
    };
    element.addEventListener("scroll", onScroll, { passive: true });
    return () => element.removeEventListener("scroll", onScroll);
  }, [onNearEnd, items.length]);

  const loadedLabel =
    messages.length > 0
      ? t("webmail_next.loaded_range", {
          from: 1,
          to: messages.length,
          total: totalCount ?? messages.length,
        })
      : "";

  useEffect(() => {
    onBindSelection?.({
      toggle: toggleSelect,
      clear: clearSelection,
      selected: () => [...selectedUids],
    });
  }, [onBindSelection, toggleSelect, clearSelection, selectedUids]);

  const handleToggleSelectAll = () => {
    if (isAllSelected) clearSelection();
    else selectAll();
  };

  const handleSortClick = () => {
    if (sort === "date" && order === "desc") onSortChange("date", "asc");
    else if (sort === "date" && order === "asc") onSortChange("subject", "asc");
    else if (sort === "subject") onSortChange("from", "asc");
    else if (sort === "from") onSortChange("size", "desc");
    else onSortChange("date", "desc");
  };

  const runBulk = (action?: (uids: number[]) => void) => {
    if (!action || selectedList.length === 0) return;
    action(selectedList);
    clearSelection();
    setMoveOpen(false);
  };

  const rowActionsRef = useRef({
    onSelectMessage,
    toggleSelect,
    onToggleStar,
    toggleThreadExpand,
    onDeleteMessage,
    onToggleRead,
    openContextMenu,
  });
  rowActionsRef.current = {
    onSelectMessage,
    toggleSelect,
    onToggleStar,
    toggleThreadExpand,
    onDeleteMessage,
    onToggleRead,
    openContextMenu,
  };

  const renderMessageRow = (item: WebmailItem) => (
    <WebmailMessageRow
      item={item}
      isSelected={selectedUids.has(item.uid)}
      isActive={activeUid === item.uid}
      isCursor={cursorUid === item.uid}
      density={density}
      onSelect={() => rowActionsRef.current.onSelectMessage(item.uid)}
      onToggleCheck={(event) => {
        rowActionsRef.current.toggleSelect(
          item.uid,
          event.shiftKey,
          event.metaKey || event.ctrlKey,
        );
      }}
      onToggleStar={(event) => {
        event.stopPropagation();
        rowActionsRef.current.onToggleStar?.(item.uid);
      }}
      onToggleThread={(event) => {
        event.stopPropagation();
        if (item.threadId) {
          rowActionsRef.current.toggleThreadExpand(item.threadId);
        }
      }}
      onDelete={(event) => {
        event.stopPropagation();
        rowActionsRef.current.onDeleteMessage?.(item.uid);
      }}
      onToggleRead={(event) => {
        event.stopPropagation();
        rowActionsRef.current.onToggleRead?.(item.uid);
      }}
      onContextMenu={(x, y) =>
        rowActionsRef.current.openContextMenu(item.uid, x, y)
      }
      onDragStartUids={(fallbackUids) => {
        // Dragging a member of the selection carries the whole selection;
        // otherwise it carries just this row.
        const uids = selectedUids.has(item.uid)
          ? [...selectedUids]
          : fallbackUids;
        return uids;
      }}
    />
  );

  return (
    <div
      data-testid="webmail-message-list"
      className="h-full flex flex-col bg-[#FFFFFF] overflow-hidden select-none"
    >
      {selectedUids.size > 0 ? (
        <div
          data-testid="webmail-bulk-bar"
          className="h-10 px-3 border-b border-[#EBE8E0] bg-[#18181B] text-white flex items-center justify-between text-xs shrink-0"
        >
          <div className="flex items-center gap-2">
            <button
              data-testid="select-all-button"
              type="button"
              onClick={handleToggleSelectAll}
              className="p-1 rounded hover:bg-white/10"
              aria-label={
                isAllSelected
                  ? t("webmail_next.deselect_all")
                  : t("webmail_next.select_loaded")
              }
            >
              {isAllSelected ? (
                <CheckSquare className="w-3.5 h-3.5" />
              ) : (
                <Square className="w-3.5 h-3.5" />
              )}
            </button>
            <span data-testid="bulk-selected-label">
              {t("webmail_next.selected_loaded", { count: selectedUids.size })}
            </span>
          </div>
          <span data-testid="loaded-range" className="sr-only">
            {loadedLabel}
          </span>
          <div className="flex items-center gap-1">
            <button
              type="button"
              data-testid="bulk-archive"
              className="p-1 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_archive")}
              onClick={() => runBulk(onBulkArchive)}
            >
              <Archive className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-trash"
              className="p-1 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_trash")}
              onClick={() => runBulk(onBulkTrash)}
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-read"
              className="p-1 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_read")}
              onClick={() => runBulk(onBulkRead)}
            >
              <MailOpen className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-unread"
              className="p-1 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_unread")}
              onClick={() => runBulk(onBulkUnread)}
            >
              <Mail className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-star"
              className="p-1 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_star")}
              onClick={() => runBulk(onBulkStar)}
            >
              <Star className="w-3.5 h-3.5" />
            </button>
            <div className="relative">
              <button
                type="button"
                data-testid="bulk-move"
                className="p-1 rounded hover:bg-white/10"
                aria-label={t("webmail_next.bulk_move")}
                onClick={() => setMoveOpen((open) => !open)}
              >
                <FolderInput className="w-3.5 h-3.5" />
              </button>
              {moveOpen && (
                <div
                  data-testid="bulk-move-menu"
                  className="absolute right-0 top-7 z-20 min-w-36 bg-white text-[#18181B] border border-[#EBE8E0] rounded shadow-lg py-1"
                >
                  {moveFolders.map((folder) => (
                    <button
                      key={folder.name_enc}
                      type="button"
                      className="block w-full text-left px-3 py-1.5 hover:bg-[#F6F4EE]"
                      onClick={() => {
                        onBulkMove?.(selectedList, folder.name);
                        clearSelection();
                        setMoveOpen(false);
                      }}
                    >
                      {folder.name}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      ) : (
        <div className="h-10 px-3 border-b border-[#EBE8E0] bg-[#FAF9F5]/70 flex items-center justify-between text-xs text-[var(--vellum-ink-secondary)] shrink-0">
          <div className="flex items-center gap-2">
            <button
              data-testid="select-all-button"
              type="button"
              onClick={handleToggleSelectAll}
              className="p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] transition-colors"
              aria-label={
                isAllSelected
                  ? t("webmail_next.deselect_all")
                  : t("webmail_next.select_loaded")
              }
            >
              {isAllSelected ? (
                <CheckSquare className="w-3.5 h-3.5 text-[var(--brand-primary,#2563eb)]" />
              ) : (
                <Square className="w-3.5 h-3.5" />
              )}
            </button>
            <span className="font-semibold text-[#18181B]">{folderName}</span>
            <span
              data-testid="loaded-range"
              className="text-xs text-[var(--vellum-ink-secondary)]"
            >
              {loadedLabel}
            </span>
          </div>

          <div className="flex items-center gap-1.5">
            <button
              data-testid="sort-toggle-button"
              type="button"
              onClick={handleSortClick}
              className="flex items-center gap-1 px-2 py-0.5 rounded text-xs hover:bg-[#EBE8E0] hover:text-[#18181B]"
              title={`Sort: ${sort || "date"} (${order})`}
            >
              <ArrowDownUp className="w-3 h-3" />
              <span className="capitalize">
                {sort || t("webmail_next.sort_date")}
              </span>
            </button>

            {onRefresh && (
              <button
                type="button"
                onClick={onRefresh}
                disabled={isRefreshing}
                className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
                  isRefreshing ? "animate-spin" : ""
                }`}
                aria-label={t("webmail_next.refresh")}
              >
                <RefreshCw className="w-3 h-3" />
              </button>
            )}
          </div>
        </div>
      )}

      <div
        ref={parentRef}
        data-testid="message-rows-container"
        className="flex-1 overflow-y-auto contain-strict will-change-scroll"
        style={{ contain: "layout paint" }}
      >
        {items.length === 0 ? (
          isInitialLoading ? (
            <div data-testid="list-initial-skeleton" aria-busy="true">
              {Array.from({ length: 6 }, (_, index) => (
                <div
                  key={index}
                  className="mail-row-item border-b border-[#EBE8E0] bg-[#F6F4EE] animate-pulse"
                />
              ))}
            </div>
          ) : loadError ? (
            <div
              data-testid="list-load-error"
              role="alert"
              className="h-full flex flex-col items-center justify-center gap-2 text-xs text-[var(--vellum-ink-secondary)] px-6 text-center"
            >
              <Mail className="w-6 h-6 opacity-40" />
              <p>
                {t("webmail_next.list_load_failed", {
                  defaultValue: "Couldn't load messages.",
                })}
              </p>
              {onRefresh && (
                <button
                  type="button"
                  onClick={onRefresh}
                  className="px-3 py-1 rounded border border-[#EBE8E0] text-[#18181B] hover:bg-[#F6F4EE] transition-colors"
                >
                  {t("action.retry")}
                </button>
              )}
            </div>
          ) : (
            <div className="h-full flex items-center justify-center text-xs text-[var(--vellum-ink-secondary)]">
              {t("webmail_next.no_messages")}
            </div>
          )
        ) : virtualItems.length > 0 ? (
          <div
            style={{
              height: virtualizer.getTotalSize(),
              position: "relative",
              width: "100%",
            }}
          >
            {virtualItems.map((virtualRow) => {
              const item = items[virtualRow.index];
              if (!item) return null;
              return (
                <div
                  key={item.uid}
                  style={{
                    position: "absolute",
                    top: 0,
                    left: 0,
                    width: "100%",
                    height: rowHeight,
                    transform: `translateY(${virtualRow.start}px)`,
                  }}
                >
                  {renderMessageRow(item)}
                </div>
              );
            })}
          </div>
        ) : (
          items.map((item) => (
            <div key={item.uid} className="contents">
              {renderMessageRow(item)}
            </div>
          ))
        )}
        {isLoadingMore && (
          <div
            data-testid="list-page-skeleton"
            aria-busy="true"
            className="mail-row-item border-b border-[#EBE8E0] bg-[#F6F4EE] animate-pulse"
          />
        )}
      </div>
      {contextHandlers && (
        <WebmailRowContextMenu
          state={menuState}
          folders={contextFolders}
          handlers={contextHandlers}
          onClose={() => setMenuState(null)}
        />
      )}
    </div>
  );
}
