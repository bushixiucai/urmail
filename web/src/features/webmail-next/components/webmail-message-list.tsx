import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
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
import { useMessageThreading } from "../hooks/use-message-threading";
import { useRangeSelection } from "../hooks/use-range-selection";
import type {
  MessageSummary,
  MailboxSortKey,
} from "@/features/webmail/mailbox-api";
import type { DensityMode } from "../types";

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
}: WebmailMessageListProps) {
  const { t } = useTranslation();
  const { items, toggleThreadExpand } = useMessageThreading(messages);
  const { selectedUids, toggleSelect, selectAll, clearSelection } =
    useRangeSelection(items);
  const [moveOpen, setMoveOpen] = useState(false);

  const isAllSelected = items.length > 0 && selectedUids.size === items.length;
  const selectedList = [...selectedUids];

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
          <span data-testid="bulk-selected-label">
            {t("webmail_next.selected_loaded", { count: selectedUids.size })}
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
            <span className="text-[11px] text-[var(--vellum-ink-secondary)]">
              ({messages.length})
            </span>
          </div>

          <div className="flex items-center gap-1.5">
            <button
              data-testid="sort-toggle-button"
              type="button"
              onClick={handleSortClick}
              className="flex items-center gap-1 px-2 py-0.5 rounded text-[11px] hover:bg-[#EBE8E0] hover:text-[#18181B]"
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
        data-testid="message-rows-container"
        className="flex-1 overflow-y-auto contain-strict will-change-scroll"
        style={{ contain: "layout paint" }}
      >
        {items.length === 0 ? (
          <div className="h-full flex items-center justify-center text-xs text-[var(--vellum-ink-secondary)]">
            {t("webmail_next.no_messages")}
          </div>
        ) : (
          items.map((item) => (
            <WebmailMessageRow
              key={item.uid}
              item={item}
              isSelected={selectedUids.has(item.uid)}
              isActive={activeUid === item.uid}
              isCursor={cursorUid === item.uid}
              density={density}
              onSelect={() => onSelectMessage(item.uid)}
              onToggleCheck={(event) => {
                toggleSelect(
                  item.uid,
                  event.shiftKey,
                  event.metaKey || event.ctrlKey,
                );
              }}
              onToggleStar={(event) => {
                event.stopPropagation();
                onToggleStar?.(item.uid);
              }}
              onToggleThread={(event) => {
                event.stopPropagation();
                if (item.threadId) toggleThreadExpand(item.threadId);
              }}
              onDelete={(event) => {
                event.stopPropagation();
                onDeleteMessage?.(item.uid);
              }}
              onToggleRead={(event) => {
                event.stopPropagation();
                onToggleRead?.(item.uid);
              }}
            />
          ))
        )}
      </div>
    </div>
  );
}
