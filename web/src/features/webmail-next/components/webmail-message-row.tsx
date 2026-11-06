import React from "react";
import { useTranslation } from "react-i18next";
import {
  Star,
  Paperclip,
  ChevronRight,
  ChevronDown,
  Trash2,
  MailOpen,
  Mail,
  CheckSquare,
  Square,
} from "lucide-react";
import type { WebmailItem, DensityMode } from "../types";

interface WebmailMessageRowProps {
  item: WebmailItem;
  isSelected: boolean;
  isActive: boolean;
  density: DensityMode;
  isCursor?: boolean;
  onSelect: (e: React.MouseEvent) => void;
  onToggleCheck: (e: React.MouseEvent) => void;
  onToggleStar: (e: React.MouseEvent) => void;
  onToggleThread?: (e: React.MouseEvent) => void;
  onDelete?: (e: React.MouseEvent) => void;
  onToggleRead?: (e: React.MouseEvent) => void;
}

function WebmailMessageRowView({
  item,
  isSelected,
  isActive,
  density,
  isCursor = false,
  onSelect,
  onToggleCheck,
  onToggleStar,
  onToggleThread,
  onDelete,
  onToggleRead,
}: WebmailMessageRowProps) {
  const { t } = useTranslation();
  const isListOnly = density === "list_only";

  return (
    <div
      data-testid={`mail-row-${item.uid}`}
      data-density={density}
      data-selected={isSelected ? "true" : "false"}
      data-active={isActive ? "true" : "false"}
      data-cursor={isCursor ? "true" : "false"}
      aria-current={isCursor ? "true" : undefined}
      tabIndex={0}
      onClick={onSelect}
      className="mail-row-item group relative flex items-center border-b border-[#EBE8E0] cursor-pointer select-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-[#2563EB] focus-visible:-outline-offset-2"
    >
      {/* Active Left Indicator Bar */}
      {isActive && (
        <div className="absolute left-0 top-0 bottom-0 w-[3px] bg-[var(--brand-primary,#2563eb)]" />
      )}

      {/* Row Left: Unread dot / Checkbox / Star */}
      <div className="flex items-center gap-2 shrink-0 mr-2">
        <div className="w-2 flex justify-center">
          {item.isUnread ? (
            <div
              data-testid={`unread-dot-${item.uid}`}
              className="w-1.5 h-1.5 rounded-full bg-[var(--brand-primary,#2563eb)]"
              aria-label={t("webmail_next.row_unread")}
            />
          ) : (
            <div className="w-1.5 h-1.5" />
          )}
        </div>

        <button
          type="button"
          data-testid={`mail-check-${item.uid}`}
          aria-pressed={isSelected}
          aria-label={t("webmail_next.row_check")}
          onClick={(event) => {
            event.stopPropagation();
            onToggleCheck(event);
          }}
          className="p-0.5 rounded text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
        >
          {isSelected ? (
            <CheckSquare className="w-3.5 h-3.5 text-[var(--brand-primary,#2563eb)]" />
          ) : (
            <Square className="w-3.5 h-3.5" />
          )}
        </button>

        <button
          type="button"
          onClick={onToggleStar}
          className={`p-0.5 rounded hover:text-[var(--vellum-star)] transition-colors ${
            item.isStarred
              ? "text-[var(--vellum-star)]"
              : "text-[#D4D4D8] group-hover:text-[var(--vellum-ink-secondary)]"
          }`}
          title={item.isStarred ? "Starred" : "Not starred"}
        >
          <Star
            className={`w-4 h-4 ${item.isStarred ? "fill-[var(--vellum-star)]" : ""}`}
          />
        </button>
      </div>

      {/* Row Center: Sender, Subject, Snippet */}
      {isListOnly ? (
        <div className="flex-1 min-w-0 flex items-center justify-between gap-2 overflow-hidden">
          <div className="flex items-center gap-1.5 min-w-0 flex-1">
            <span
              className={`truncate text-xs leading-4 shrink-0 max-w-[110px] ${
                item.isUnread
                  ? "font-[650] text-[#18181B]"
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
                className="px-1 py-0.2 rounded-full text-xs bg-[#EBE8E0] text-[var(--vellum-ink-secondary)] hover:bg-[#D4D4D8] font-bold flex items-center gap-0.5 shrink-0"
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
              className={`truncate text-xs leading-4 flex-1 ${
                item.isUnread
                  ? "font-[650] text-[#18181B]"
                  : "font-normal text-[#52525B]"
              }`}
            >
              {item.subject}
            </span>
            {item.folder ? (
              <span
                data-testid={`mail-folder-${item.uid}`}
                className="shrink-0 text-xs text-[var(--vellum-ink-secondary)]"
              >
                {item.folder}
              </span>
            ) : null}

            {item.hasAttachments && (
              <Paperclip className="w-4 h-4 text-[var(--vellum-ink-secondary)] shrink-0" />
            )}
          </div>

          {/* Timestamp / Quick Action Hover Bar */}
          <div className="shrink-0 flex items-center text-xs text-[#52525B]">
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
                className={`truncate text-[13px] leading-5 ${
                  item.isUnread
                    ? "font-[650] text-[#18181B]"
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
                  className="px-1.5 py-0.2 rounded-full text-xs bg-[#EBE8E0] text-[var(--vellum-ink-secondary)] hover:bg-[#D4D4D8] font-bold flex items-center gap-0.5"
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
            <div className="shrink-0 flex items-center text-xs text-[#52525B]">
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

          <div className="flex items-center gap-2 min-w-0 text-[13px] leading-5">
            <span
              className={`truncate ${
                item.isUnread
                  ? "font-[650] text-[#18181B]"
                  : "font-normal text-[#52525B]"
              }`}
            >
              {item.subject}
            </span>
            {item.folder ? (
              <span
                data-testid={`mail-folder-${item.uid}`}
                className="shrink-0 text-xs text-[var(--vellum-ink-secondary)]"
              >
                {item.folder}
              </span>
            ) : null}
            {item.hasAttachments && (
              <Paperclip className="w-4 h-4 text-[var(--vellum-ink-secondary)] shrink-0" />
            )}
          </div>
          <p
            data-testid={`mail-snippet-${item.uid}`}
            className="truncate text-[13px] leading-5 font-normal text-[var(--vellum-ink-secondary)]"
          >
            {item.snippet ?? ""}
          </p>
        </div>
      )}
    </div>
  );
}

export const WebmailMessageRow = React.memo(WebmailMessageRowView);
