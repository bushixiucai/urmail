import { useEffect, useMemo, useRef, useState } from "react";
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
  Inbox,
  SearchX,
  CheckCircle2,
} from "lucide-react";
import { WebmailMessageRow } from "./webmail-message-row";
import {
  WebmailRowContextMenu,
  type RowContextMenuState,
  type RowContextMenuActionHandlers,
  type ContextMenuFolder,
} from "./webmail-row-context-menu";
import { useMessageThreading } from "../hooks/use-message-threading";
import { useDelayedFlag } from "../hooks/use-delayed-flag";
import { useDismissable } from "../hooks/use-dismissable";
import { MailListSkeleton, SkeletonRow } from "./webmail-skeleton";
import { WebmailEmptyState } from "./webmail-empty-state";
import { useRangeSelection } from "../hooks/use-range-selection";
import type {
  MessageSummary,
  MailboxSortKey,
} from "@/features/webmail/mailbox-api";
import type { DensityMode, WebmailItem } from "../types";
import { decodeFolderName } from "../lib/folder-names";

/** Cross-folder result rows from other boxes are excluded from selection
 * and folder-scoped quick actions so bulk ops can never touch a same-
 * numeric-uid copy in the browsing folder (QA/UIUX review). */
function isForeignRow(
  item: { folderEnc?: string },
  activeFolderEnc?: string,
): boolean {
  return (
    activeFolderEnc != null &&
    item.folderEnc != null &&
    item.folderEnc !== activeFolderEnc
  );
}

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
  /** Encoded browsing folder — disambiguates same-uid rows in cross-folder
   * result sets (selection is folder-scoped). */
  activeFolderEnc?: string;
  /** Folder currently being read (search results may differ) — drives the
   * active-row highlight. */
  readingFolderEnc?: string;
  /** Composite cursor identity (folder of the cursor entry). */
  cursorFolderEnc?: string | null;
  messages: MessageSummary[];
  activeUid: number | null;
  cursorUid?: number | null;
  /** folderEnc disambiguates same-uid rows in cross-folder result sets. */
  onSelectMessage: (uid: number, folderEnc?: string) => void;
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
  /** B2-2 空态三场景：当前视图为搜索（无结果时展示 SearchX 空态）。 */
  isSearchView?: boolean;
  /** B2-2 空态动作：清除搜索条件。 */
  onClearSearch?: () => void;
  /** B2-2 空态动作：发起写信。 */
  onCompose?: () => void;
  /** B2-3：批量选择数量变化上报（移动端写信 FAB 让位批量条）。 */
  onSelectionCountChange?: (count: number) => void;
}

export function WebmailMessageList({
  folderName,
  activeFolderEnc,
  readingFolderEnc,
  cursorFolderEnc,
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
  isSearchView = false,
  onClearSearch,
  onCompose,
  onSelectionCountChange,
}: WebmailMessageListProps) {
  const { t } = useTranslation();
  const { items, toggleThreadExpand } = useMessageThreading(
    messages,
    activeFolderEnc,
  );
  // Cross-folder result sets can carry the same numeric uid from two
  // boxes. Folder-scoped selection/bulk actions must only ever see rows
  // of the browsing folder (UIUX ①).
  const selectableItems = items.filter(
    (item) => !isForeignRow(item, activeFolderEnc),
  );
  const { selectedUids, toggleSelect, selectAll, clearSelection } =
    useRangeSelection(selectableItems);
  const [moveOpen, setMoveOpen] = useState(false);
  const bulkMoveRef = useRef<HTMLDivElement>(null);
  const bulkMoveButtonRef = useRef<HTMLButtonElement>(null);
  useDismissable(
    moveOpen,
    bulkMoveRef,
    () => setMoveOpen(false),
    bulkMoveButtonRef,
  );
  // C1 D-D: loaded member count per thread_id, for the pill's
  // "inline-expand vs open-stack" decision.
  const loadedThreadCounts = useMemo(() => {
    const counts = new Map<number, number>();
    for (const message of messages) {
      if (message.thread_id != null) {
        counts.set(message.thread_id, (counts.get(message.thread_id) ?? 0) + 1);
      }
    }
    return counts;
  }, [messages]);
  // P1-1: one row context menu instance for the whole list.
  const [menuState, setMenuState] = useState<RowContextMenuState | null>(null);
  const itemsRef = useRef(items);
  itemsRef.current = items;

  const openContextMenu = (
    uid: number,
    x: number,
    y: number,
    folderEnc?: string,
  ) => {
    // Composite identity: with same-uid rows from two boxes present, the
    // right-clicked ROW decides the target copy (QA/UIUX).
    const candidates = itemsRef.current.filter((item) => item.uid === uid);
    const target = folderEnc
      ? candidates.find(
          (item) => (item.folderEnc ?? activeFolderEnc) === folderEnc,
        )
      : // No captured folder: accept only a SINGLE folder-less row (legacy
        // single-box). Ambiguous/foreign provenance must NOT open the menu
        // — a cross-folder result set cannot prove the row's box.
        candidates.length === 1 && candidates[0].folderEnc == null
        ? candidates[0]
        : undefined;
    if (!target) return;
    setMenuState({
      uid,
      // Bind the menu to the row's known folder at OPEN time; a legacy
      // row without folder_enc belongs to the browsing box — capturing it
      // here lets a later refresh refuse a stale same-uid target instead
      // of silently falling back to another box's copy (QA stale probe:
      // refusal requires a bound folder; an unbound menu would instead
      // open the other box's mail).
      folder_enc: target.folderEnc ?? activeFolderEnc,
      x,
      y,
      isUnread: target.isUnread,
    });
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
      // Keyboard parity with the pointer foreign-row guard: the menu is a
      // folder-scoped surface, so it may only open for a UNIQUE row of the
      // browsing folder (a same-uid foreign copy must never be targeted).
      const matches = itemsRef.current.filter((it) => it.uid === resolved);
      if (matches.length !== 1 || isForeignRow(matches[0], activeFolderEnc)) {
        event.preventDefault();
        return;
      }
      const anchor = document.querySelector<HTMLElement>(
        `[data-testid='mail-row-${resolved}']`,
      );
      const rect = anchor?.getBoundingClientRect();
      event.preventDefault();
      const keyboardFolderEnc = matches[0].folderEnc ?? activeFolderEnc;
      openContextMenu(
        resolved,
        rect?.left ?? 80,
        (rect?.bottom ?? 80) + 4,
        keyboardFolderEnc,
      );
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [cursorUid, contextHandlers]);

  // Keep the denominator identical to the selectable set so a mixed
  // cross-folder list can still be fully deselected (QA sequence ④).
  const isAllSelected =
    selectableItems.length > 0 && selectedUids.size === selectableItems.length;
  const selectedList = [...selectedUids];
  const parentRef = useRef<HTMLDivElement>(null);
  const rowHeight =
    density === "comfortable" ? 52 : density === "list_only" ? 32 : 40;
  // B2-2 防闪：in-flight >300ms 才渲染骨架（<300ms 直接空→内容）。
  const showInitialSkeleton = useDelayedFlag(isInitialLoading, 300);
  // B2-2 场景三「全部已处理」：列表非空、全部已读、非搜索视图时在列表末尾提示。
  const allCaughtUp =
    !isSearchView && items.length > 0 && items.every((item) => !item.isUnread);
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
  // md 档列宽有限，隐藏「已加载」前缀只留数字区间（B3-2 防列头折行）。
  const loadedLabelShort =
    messages.length > 0
      ? t("webmail_next.loaded_range_short", {
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

  // B2-3：批量条激活状态上报（<md 批量条固定底部时 FAB 上移让位）。
  useEffect(() => {
    onSelectionCountChange?.(selectedUids.size);
  }, [onSelectionCountChange, selectedUids]);

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

  const rowKey = (item: WebmailItem) => `${item.folderEnc ?? ""}:${item.uid}`;
  // Highlight follows the full identity: the row's own folder (or the
  // browsing folder when it carries none) must equal the folder being
  // read (readingFolderEnc falls back to the browsing folder).
  const highlightEnc = readingFolderEnc ?? activeFolderEnc;
  const isRowActive = (item: WebmailItem) => {
    if (activeUid !== item.uid) return false;
    if (highlightEnc == null) return true;
    const rowEnc = item.folderEnc ?? activeFolderEnc;
    return rowEnc == null || rowEnc === highlightEnc;
  };
  const renderMessageRow = (item: WebmailItem) => (
    <WebmailMessageRow
      item={item}
      isSelected={selectedUids.has(item.uid) && !isForeignRow(item)}
      isActive={isRowActive(item)}
      isCursor={
        cursorUid === item.uid &&
        (cursorFolderEnc == null ||
          (item.folderEnc ?? activeFolderEnc) === cursorFolderEnc)
      }
      density={density}
      onSelect={() =>
        rowActionsRef.current.onSelectMessage(item.uid, item.folderEnc)
      }
      onToggleCheck={(event) => {
        if (isForeignRow(item, activeFolderEnc)) return; // folder-scoped
        rowActionsRef.current.toggleSelect(
          item.uid,
          event.shiftKey,
          event.metaKey || event.ctrlKey,
        );
      }}
      onToggleStar={(event) => {
        event.stopPropagation();
        if (isForeignRow(item, activeFolderEnc)) return; // folder-scoped op
        rowActionsRef.current.onToggleStar?.(item.uid);
      }}
      onToggleThread={(event) => {
        event.stopPropagation();
        if (!item.threadId) return;
        // D-D (C1): inline expansion only when every member of the thread
        // is already loaded in the list; otherwise the pill opens the
        // reading stack, which progressively loads the missing members.
        const loaded = loadedThreadCounts.get(item.threadId) ?? 0;
        if ((item.threadCount ?? 0) > loaded) {
          rowActionsRef.current.onSelectMessage(item.uid, item.folderEnc);
          return;
        }
        rowActionsRef.current.toggleThreadExpand(
          item.threadGroupKey ?? String(item.threadId),
        );
      }}
      onDelete={(event) => {
        event.stopPropagation();
        if (isForeignRow(item, activeFolderEnc)) return; // folder-scoped op
        rowActionsRef.current.onDeleteMessage?.(item.uid);
      }}
      onToggleRead={(event) => {
        event.stopPropagation();
        if (isForeignRow(item, activeFolderEnc)) return; // folder-scoped op
        rowActionsRef.current.onToggleRead?.(item.uid);
      }}
      onContextMenu={(x, y) => {
        if (isForeignRow(item, activeFolderEnc)) return; // menu actions are folder-scoped
        rowActionsRef.current.openContextMenu(item.uid, x, y, item.folderEnc);
      }}
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
      className="h-full flex flex-col bg-[var(--vellum-surface)] overflow-hidden select-none"
    >
      {selectedUids.size > 0 ? (
        <div
          data-testid="webmail-bulk-bar"
          className="h-10 px-3 border-b border-[var(--vellum-border)] bg-[var(--vellum-ink)] text-white flex items-center justify-between text-xs shrink-0 max-md:fixed max-md:inset-x-0 max-md:bottom-0 max-md:z-20 max-md:border-b-0 max-md:border-t"
        >
          <div className="flex items-center gap-2">
            <button
              data-testid="select-all-button"
              type="button"
              onClick={handleToggleSelectAll}
              className="p-1.5 rounded hover:bg-white/10"
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
              className="p-1.5 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_archive")}
              onClick={() => runBulk(onBulkArchive)}
            >
              <Archive className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-trash"
              className="p-1.5 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_trash")}
              onClick={() => runBulk(onBulkTrash)}
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-read"
              className="p-1.5 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_read")}
              onClick={() => runBulk(onBulkRead)}
            >
              <MailOpen className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-unread"
              className="p-1.5 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_unread")}
              onClick={() => runBulk(onBulkUnread)}
            >
              <Mail className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              data-testid="bulk-star"
              className="p-1.5 rounded hover:bg-white/10"
              aria-label={t("webmail_next.bulk_star")}
              onClick={() => runBulk(onBulkStar)}
            >
              <Star className="w-3.5 h-3.5" />
            </button>
            <div className="relative" ref={bulkMoveRef}>
              <button
                ref={bulkMoveButtonRef}
                type="button"
                data-testid="bulk-move"
                className="p-1.5 rounded hover:bg-white/10"
                aria-label={t("webmail_next.bulk_move")}
                onClick={() => setMoveOpen((open) => !open)}
              >
                <FolderInput className="w-3.5 h-3.5" />
              </button>
              {moveOpen && (
                <div
                  data-testid="bulk-move-menu"
                  className="absolute right-0 top-7 z-20 min-w-36 bg-white text-[var(--vellum-ink)] border border-[var(--vellum-border)] rounded shadow-lg py-1"
                >
                  {moveFolders.map((folder) => (
                    <button
                      key={folder.name_enc}
                      type="button"
                      className="block w-full text-left px-3 py-1.5 hover:bg-[var(--vellum-surface-muted)]"
                      onClick={() => {
                        onBulkMove?.(selectedList, folder.name);
                        clearSelection();
                        setMoveOpen(false);
                      }}
                    >
                      {decodeFolderName(folder.name)}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      ) : (
        <div className="h-10 px-3 border-b border-[var(--vellum-border)] bg-[var(--vellum-ground)]/70 flex items-center justify-between text-xs text-[var(--vellum-ink-secondary)] shrink-0">
          <div className="flex items-center gap-2 min-w-0">
            <button
              data-testid="select-all-button"
              type="button"
              onClick={handleToggleSelectAll}
              className="p-1.5 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] transition-colors"
              aria-label={
                isAllSelected
                  ? t("webmail_next.deselect_all")
                  : t("webmail_next.select_loaded")
              }
            >
              {isAllSelected ? (
                <CheckSquare className="w-3.5 h-3.5 text-[var(--brand-primary,var(--brand-primary))]" />
              ) : (
                <Square className="w-3.5 h-3.5" />
              )}
            </button>
            <span
              data-testid="folder-name-header"
              className="font-semibold text-[var(--vellum-ink)] truncate whitespace-nowrap shrink-0"
            >
              {decodeFolderName(folderName)}
            </span>
            <span
              data-testid="loaded-range"
              className="min-w-0 truncate whitespace-nowrap text-xs text-[var(--vellum-ink-secondary)]"
            >
              <span className="hidden lg:inline">{loadedLabel}</span>
              <span className="lg:hidden">{loadedLabelShort}</span>
            </span>
          </div>

          <div className="flex items-center gap-1.5 shrink-0">
            <button
              data-testid="sort-toggle-button"
              type="button"
              onClick={handleSortClick}
              className="flex items-center gap-1 px-2 py-0.5 rounded text-xs hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] select-none shrink-0 whitespace-nowrap"
              title={t("webmail_next.sort_tooltip", {
                field: t(`webmail_next.sort_field_${sort || "date"}`),
                order: t(
                  order === "asc"
                    ? "webmail_next.sort_order_asc"
                    : "webmail_next.sort_order_desc",
                ),
              })}
            >
              <ArrowDownUp className="w-3 h-3" />
              <span>{t(`webmail_next.sort_field_${sort || "date"}`)}</span>
            </button>

            {onRefresh && (
              <button
                type="button"
                onClick={onRefresh}
                disabled={isRefreshing}
                className={`p-1.5 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
                  isRefreshing ? "animate-spin" : ""
                }`}
                aria-label={t("webmail_next.refresh")}
              >
                <RefreshCw className="w-3.5 h-3.5" />
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
          showInitialSkeleton ? (
            <MailListSkeleton
              testId="list-initial-skeleton"
              rows={8}
              rowHeight={rowHeight}
            />
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
                  className="px-3 py-1 rounded border border-[var(--vellum-border)] text-[var(--vellum-ink)] hover:bg-[var(--vellum-surface-muted)] transition-colors"
                >
                  {t("action.retry")}
                </button>
              )}
            </div>
          ) : isSearchView ? (
            <WebmailEmptyState
              testId="empty-search"
              icon={SearchX}
              title={t("webmail_next.empty_search_title")}
              description={t("webmail_next.empty_search_body")}
              actionLabel={t("webmail_next.empty_search_action")}
              onAction={onClearSearch}
            />
          ) : (
            <WebmailEmptyState
              testId="empty-folder"
              icon={Inbox}
              title={t("webmail_next.empty_folder_title")}
              description={t("webmail_next.empty_folder_body")}
              actionLabel={t("webmail_next.empty_folder_action")}
              onAction={onCompose}
            />
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
                  key={rowKey(item)}
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
            <div key={rowKey(item)} className="contents">
              {renderMessageRow(item)}
            </div>
          ))
        )}
        {allCaughtUp && (
          <div
            data-testid="list-all-caught-up"
            className="py-8 border-t border-[var(--vellum-border)]"
          >
            <WebmailEmptyState
              icon={CheckCircle2}
              title={t("webmail_next.empty_all_done_title")}
              description={t("webmail_next.empty_all_done_body")}
            />
          </div>
        )}
        {isLoadingMore && (
          <div data-testid="list-page-skeleton" aria-busy="true">
            <SkeletonRow height={rowHeight} />
          </div>
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
