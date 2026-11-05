import React from "react";
import { useTranslation } from "react-i18next";
import { CheckSquare, Square, ArrowDownUp, RefreshCw } from "lucide-react";
import { WebmailMessageRow } from "./webmail-message-row";
import { useMessageThreading } from "../hooks/use-message-threading";
import { useRangeSelection } from "../hooks/use-range-selection";
import type { MessageSummary, MailboxSortKey } from "@/features/webmail/mailbox-api";
import type { DensityMode } from "../types";

interface WebmailMessageListProps {
  folderName: string;
  messages: MessageSummary[];
  activeUid: number | null;
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
}

export function WebmailMessageList({
  folderName,
  messages,
  activeUid,
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
}: WebmailMessageListProps) {
  const { t } = useTranslation();
  const { items, toggleThreadExpand } = useMessageThreading(messages);
  const { selectedUids, toggleSelect, selectAll, clearSelection } =
    useRangeSelection(items);

  const isAllSelected = items.length > 0 && selectedUids.size === items.length;

  const handleToggleSelectAll = () => {
    if (isAllSelected) {
      clearSelection();
    } else {
      selectAll();
    }
  };

  const handleSortClick = () => {
    // Cycle: date:desc -> date:asc -> subject:asc -> from:asc -> size:desc
    if (sort === "date" && order === "desc") onSortChange("date", "asc");
    else if (sort === "date" && order === "asc") onSortChange("subject", "asc");
    else if (sort === "subject") onSortChange("from", "asc");
    else if (sort === "from") onSortChange("size", "desc");
    else onSortChange("date", "desc");
  };

  return (
    <div
      data-testid="webmail-message-list"
      className="h-full flex flex-col bg-[#FFFFFF] overflow-hidden select-none"
    >
      {/* List Toolbar (Top of Middle Pane) */}
      <div className="h-10 px-3 border-b border-[#EBE8E0] bg-[#FAF9F5]/70 flex items-center justify-between text-xs text-[#71717A] shrink-0">
        <div className="flex items-center gap-2">
          <button
            data-testid="select-all-button"
            type="button"
            onClick={handleToggleSelectAll}
            className="p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] transition-colors"
            title={isAllSelected ? "Deselect All" : "Select All"}
          >
            {isAllSelected ? (
              <CheckSquare className="w-3.5 h-3.5 text-[var(--brand-primary,#2563eb)]" />
            ) : (
              <Square className="w-3.5 h-3.5" />
            )}
          </button>
          <span className="font-semibold text-[#18181B]">{folderName}</span>
          <span className="text-[11px] text-[#A1A1AA]">
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
            <span className="capitalize">{sort || t("webmail_next.sort_date")}</span>
          </button>

          {onRefresh && (
            <button
              type="button"
              onClick={onRefresh}
              disabled={isRefreshing}
              className={`p-1 rounded hover:bg-[#EBE8E0] hover:text-[#18181B] ${
                isRefreshing ? "animate-spin" : ""
              }`}
              title="Refresh Folder"
            >
              <RefreshCw className="w-3 h-3" />
            </button>
          )}
        </div>
      </div>

      {/* Message Rows Scroll Container (Optimized for 60FPS) */}
      <div
        data-testid="message-rows-container"
        className="flex-1 overflow-y-auto contain-strict will-change-scroll"
        style={{ contain: "layout paint" }}
      >
        {items.length === 0 ? (
          <div className="h-full flex items-center justify-center text-xs text-[#A1A1AA]">
            No messages
          </div>
        ) : (
          items.map((item) => (
            <WebmailMessageRow
              key={item.uid}
              item={item}
              isSelected={selectedUids.has(item.uid)}
              isActive={activeUid === item.uid}
              density={density}
              onSelect={(e) => {
                toggleSelect(item.uid, e.shiftKey, e.metaKey || e.ctrlKey);
                onSelectMessage(item.uid);
              }}
              onToggleStar={(e) => {
                e.stopPropagation();
                onToggleStar?.(item.uid);
              }}
              onToggleThread={(e) => {
                e.stopPropagation();
                if (item.threadId) toggleThreadExpand(item.threadId);
              }}
              onDelete={(e) => {
                e.stopPropagation();
                onDeleteMessage?.(item.uid);
              }}
              onToggleRead={(e) => {
                e.stopPropagation();
                onToggleRead?.(item.uid);
              }}
            />
          ))
        )}
      </div>
    </div>
  );
}
