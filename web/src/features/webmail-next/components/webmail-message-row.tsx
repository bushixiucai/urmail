import React from "react";
import {
  Star,
  Paperclip,
  ChevronRight,
  ChevronDown,
  Trash2,
  MailOpen,
  Mail,
} from "lucide-react";
import type { WebmailItem, DensityMode } from "../types";

interface WebmailMessageRowProps {
  item: WebmailItem;
  isSelected: boolean;
  isActive: boolean;
  density: DensityMode;
  onSelect: (e: React.MouseEvent) => void;
  onToggleStar: (e: React.MouseEvent) => void;
  onToggleThread?: (e: React.MouseEvent) => void;
  onDelete?: (e: React.MouseEvent) => void;
  onToggleRead?: (e: React.MouseEvent) => void;
}

export function WebmailMessageRow({
  item,
  isSelected,
  isActive,
  density,
  onSelect,
  onToggleStar,
  onToggleThread,
  onDelete,
  onToggleRead,
}: WebmailMessageRowProps) {
  const isListOnly = density === "list_only";
  const isCompact = density === "compact";

  return (
    <div
      data-testid={`mail-row-${item.uid}`}
      data-selected={isSelected ? "true" : "false"}
      data-active={isActive ? "true" : "false"}
      onClick={onSelect}
      className={`group relative flex items-center border-b border-[#EBE8E0] transition-colors cursor-pointer select-none ${
        isListOnly ? "h-8 px-2" : isCompact ? "h-10 px-3" : "h-13 px-4"
      } ${
        isActive
          ? "bg-[#EFF6FF]"
          : isSelected
            ? "bg-[#F8F7F2]"
            : item.isUnread
              ? "bg-[#FFFFFF]"
              : "bg-[#FAF9F5]/40"
      } hover:bg-[#F8F7F2]`}
    >
      {/* Active Left Indicator Bar */}
      {isActive && (
        <div className="absolute left-0 top-0 bottom-0 w-[3px] bg-[var(--brand-primary,#2563eb)]" />
      )}

      {/* Row Left: Unread dot / Checkbox / Star */}
      <div className="flex items-center gap-2 shrink-0 mr-2.5">
        <div className="w-2 flex justify-center">
          {item.isUnread ? (
            <div
              data-testid={`unread-dot-${item.uid}`}
              className="w-1.5 h-1.5 rounded-full bg-[var(--brand-primary,#2563eb)]"
            />
          ) : (
            <div className="w-1.5 h-1.5" />
          )}
        </div>

        <button
          type="button"
          onClick={onToggleStar}
          className={`p-0.5 rounded hover:text-[#F59E0B] transition-colors ${
            item.isStarred
              ? "text-[#F59E0B]"
              : "text-[#D4D4D8] group-hover:text-[#A1A1AA]"
          }`}
          title={item.isStarred ? "Starred" : "Not starred"}
        >
          <Star
            className={`w-3.5 h-3.5 ${item.isStarred ? "fill-[#F59E0B]" : ""}`}
          />
        </button>
      </div>

      {/* Row Center: Sender, Subject, Snippet */}
      {isListOnly ? (
        <div className="flex-1 min-w-0 flex items-center justify-between gap-2 overflow-hidden">
          <div className="flex items-center gap-1.5 min-w-0 flex-1">
            <span
              className={`truncate text-xs shrink-0 max-w-[110px] ${
                item.isUnread
                  ? "font-bold text-[#18181B]"
                  : "font-medium text-[#3F3F46]"
              }`}
            >
              {item.sender}
            </span>

            {/* Thread count badge pill */}
            {item.threadCount && item.threadCount > 1 && (
              <button
                data-testid={`thread-toggle-${item.threadId || item.uid}`}
                type="button"
                onClick={onToggleThread}
                className="px-1 py-0.2 rounded-full text-[9px] bg-[#EBE8E0] text-[#71717A] hover:bg-[#D4D4D8] font-bold flex items-center gap-0.5 shrink-0"
                title={`${item.threadCount} messages in thread`}
              >
                <span>{item.threadCount}</span>
                {item.isThreadExpanded ? (
                  <ChevronDown className="w-2 h-2" />
                ) : (
                  <ChevronRight className="w-2 h-2" />
                )}
              </button>
            )}

            <span
              className={`truncate text-xs flex-1 ${
                item.isUnread
                  ? "font-semibold text-[#18181B]"
                  : "text-[#71717A]"
              }`}
            >
              {item.subject}
            </span>

            {item.hasAttachments && (
              <Paperclip className="w-3 h-3 text-[#52525B] shrink-0" />
            )}
          </div>

          {/* Timestamp / Quick Action Hover Bar */}
          <div className="shrink-0 flex items-center text-[11px] text-[#52525B]">
            <span className="group-hover:hidden">
              {item.date
                ? new Date(item.date).toLocaleDateString(undefined, {
                    month: "numeric",
                    day: "numeric",
                  })
                : ""}
            </span>
            <div className="hidden group-hover:flex items-center gap-1">
              {onToggleRead && (
                <button
                  type="button"
                  onClick={onToggleRead}
                  className="p-0.5 hover:text-[#18181B] rounded hover:bg-[#EBE8E0]"
                  title={item.isUnread ? "Mark as read" : "Mark as unread"}
                >
                  {item.isUnread ? (
                    <MailOpen className="w-3 h-3" />
                  ) : (
                    <Mail className="w-3 h-3" />
                  )}
                </button>
              )}
              {onDelete && (
                <button
                  type="button"
                  onClick={onDelete}
                  className="p-0.5 hover:text-[#DC2626] rounded hover:bg-[#EBE8E0]"
                  title="Delete"
                >
                  <Trash2 className="w-3 h-3" />
                </button>
              )}
            </div>
          </div>
        </div>
      ) : (
        <div className="flex-1 min-w-0 flex flex-col justify-center overflow-hidden">
          <div className="flex items-center justify-between gap-1.5">
            <div className="flex items-center gap-1.5 min-w-0">
              <span
                className={`truncate text-xs ${
                  item.isUnread
                    ? "font-bold text-[#18181B]"
                    : "font-medium text-[#3F3F46]"
                }`}
              >
                {item.sender}
              </span>

              {/* Thread count badge pill */}
              {item.threadCount && item.threadCount > 1 && (
                <button
                  data-testid={`thread-toggle-${item.threadId || item.uid}`}
                  type="button"
                  onClick={onToggleThread}
                  className="px-1.5 py-0.2 rounded-full text-[10px] bg-[#EBE8E0] text-[#71717A] hover:bg-[#D4D4D8] font-bold flex items-center gap-0.5"
                  title={`${item.threadCount} messages in thread`}
                >
                  <span>{item.threadCount}</span>
                  {item.isThreadExpanded ? (
                    <ChevronDown className="w-2.5 h-2.5" />
                  ) : (
                    <ChevronRight className="w-2.5 h-2.5" />
                  )}
                </button>
              )}
            </div>

            {/* Timestamp / Quick Action Hover Bar */}
            <div className="shrink-0 flex items-center text-[11px] text-[#52525B]">
              {/* Standard date display (hidden on hover if actions available) */}
              <span className="group-hover:hidden">
                {item.date
                  ? new Date(item.date).toLocaleDateString(undefined, {
                      month: "numeric",
                      day: "numeric",
                    })
                  : ""}
              </span>

              {/* Hover quick action buttons */}
              <div className="hidden group-hover:flex items-center gap-1">
                {onToggleRead && (
                  <button
                    type="button"
                    onClick={onToggleRead}
                    className="p-1 hover:text-[#18181B] rounded hover:bg-[#EBE8E0]"
                    title={item.isUnread ? "Mark as read" : "Mark as unread"}
                  >
                    {item.isUnread ? (
                      <MailOpen className="w-3.5 h-3.5" />
                    ) : (
                      <Mail className="w-3.5 h-3.5" />
                    )}
                  </button>
                )}
                {onDelete && (
                  <button
                    type="button"
                    onClick={onDelete}
                    className="p-1 hover:text-[#DC2626] rounded hover:bg-[#EBE8E0]"
                    title="Delete"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                )}
              </div>
            </div>
          </div>

          <div className="flex items-center gap-1.5 mt-0.5 text-xs">
            <span
              className={`truncate ${
                item.isUnread
                  ? "font-semibold text-[#18181B]"
                  : "text-[#71717A]"
              }`}
            >
              {item.subject}
            </span>
            {item.hasAttachments && (
              <Paperclip className="w-3 h-3 text-[#52525B] shrink-0" />
            )}
          </div>
        </div>
      )}
    </div>
  );
}
