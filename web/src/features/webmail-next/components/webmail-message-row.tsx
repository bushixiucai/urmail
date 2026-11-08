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
import { formatFullDate, formatListDate } from "../lib/format-list-date";

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
  /** P1-1: open the row context menu at viewport coordinates. */
  onContextMenu?: (x: number, y: number) => void;
  /** P1-2: begin a drag carrying the dragged (or selected) UIDs. */
  onDragStartUids?: (uids: number[]) => number[];
}

/** Per-density metrics (UIUX v0.6.10 visual spec §1). */
const DENSITY = {
  comfortable: {
    row: "h-[52px] px-3 py-2",
    line1: "text-[13px] leading-5",
    line2: "text-xs leading-4",
    senderMax: "max-w-[160px]",
  },
  compact: {
    row: "h-[40px] px-2 py-1",
    line1: "text-[13px] leading-4",
    line2: "text-xs leading-4",
    senderMax: "max-w-[128px]",
  },
  list_only: {
    row: "h-8 px-2 py-0",
    line1: "text-xs leading-4",
    line2: "text-xs leading-4",
    senderMax: "max-w-[96px]",
  },
} as const satisfies Record<DensityMode, Record<string, string>>;

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
  onContextMenu,
  onDragStartUids,
}: WebmailMessageRowProps) {
  const { t, i18n } = useTranslation();
  const metrics = DENSITY[density] ?? DENSITY.comfortable;
  const isChild = Boolean(item.isThreadChild);
  // Spec §4: child first-line shrinks 13→12 except list_only which is already 12.
  const childLine1 =
    density === "list_only" ? metrics.line1 : "text-xs leading-4";
  const starLabel = item.isStarred
    ? t("webmail_next.starred")
    : t("webmail_next.not_starred");
  const readLabel = item.isUnread
    ? t("webmail_next.mark_read")
    : t("webmail_next.mark_unread");
  const deleteLabel = t("webmail_next.delete");
  // Spark-style gradient: today → HH:mm, yesterday → label, older → date.
  // i18n may be absent in unit-test doubles — fall back to the browser
  // locale rather than crashing the row.
  const locale = i18n?.language;
  const dateLabel = formatListDate(item.date, locale, {
    yesterday: t("webmail_next.date_yesterday"),
  });
  const dateTitle = formatFullDate(item.date, locale);
  const threadTitle = t("webmail_next.thread_messages_count", {
    count: item.threadCount ?? 0,
  });

  const senderColor = item.isUnread
    ? "font-[650] text-[var(--vellum-ink)]"
    : "font-medium text-[var(--vellum-ink-read)]";
  const subjectColor = item.isUnread
    ? "font-[650] text-[var(--vellum-ink)]"
    : "font-normal text-[var(--vellum-ink-secondary)]";

  return (
    <div
      data-testid={`mail-row-${item.uid}`}
      data-density={density}
      data-thread-child={isChild ? "true" : "false"}
      data-selected={isSelected ? "true" : "false"}
      data-active={isActive ? "true" : "false"}
      data-cursor={isCursor ? "true" : "false"}
      aria-current={isCursor ? "true" : undefined}
      tabIndex={0}
      onClick={onSelect}
      onContextMenu={(event) => {
        event.preventDefault();
        onContextMenu?.(event.clientX, event.clientY);
      }}
      draggable={Boolean(onDragStartUids)}
      onDragStart={
        onDragStartUids
          ? (event) => {
              const uids = onDragStartUids([item.uid]);
              event.dataTransfer.setData(
                "application/x-urmail-mailbox-uids",
                JSON.stringify(uids),
              );
              event.dataTransfer.effectAllowed = "move";
            }
          : undefined
      }
      className={`mail-row-item group relative flex items-center border-b border-[var(--vellum-border)] cursor-pointer select-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--brand-primary)] focus-visible:-outline-offset-2 ${
        metrics.row
      } ${
        isActive
          ? "bg-[var(--vellum-row-active)]"
          : isSelected
            ? "bg-[var(--vellum-row-selected)]"
            : isChild
              ? "bg-[var(--vellum-surface-subtle)]"
              : "bg-[var(--vellum-surface)]"
      } hover:bg-[var(--vellum-row-hover)] transition-[background-color] duration-[120ms] ease-out`}
    >
      {/* Active Left Indicator Bar */}
      {isActive && (
        <div className="absolute left-0 top-0 bottom-0 w-[3px] bg-[var(--brand-primary)]" />
      )}

      {/* Thread connector (VISUAL §4): vertical line on the left-slot midline
          (w-16 → 32px) from the parent edge down to the last child mid-row,
          plus an 8px stub into the Reply label. Decorative only. */}
      {isChild && (
        <>
          <span
            aria-hidden
            className={`pointer-events-none absolute left-8 w-px bg-[var(--vellum-border)] ${
              item.isLastThreadChild ? "top-0 h-1/2" : "-top-2 bottom-0"
            }`}
          />
          <span
            aria-hidden
            className="pointer-events-none absolute left-8 top-1/2 h-px w-2 -translate-y-1/2 bg-[var(--vellum-border)]"
          />
        </>
      )}

      {/* Row Left: fixed 64px slot (unread dot + checkbox + star) so the
          text column never shifts when the density changes. */}
      <div className="w-16 shrink-0 flex items-center gap-1">
        <div className="w-2 flex justify-center">
          {item.isUnread ? (
            <div
              data-testid={`unread-dot-${item.uid}`}
              className="w-1.5 h-1.5 rounded-full bg-[var(--brand-primary)]"
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
          className="p-0.5 rounded text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)]"
        >
          {isSelected ? (
            <CheckSquare className="w-3.5 h-3.5 text-[var(--brand-primary)]" />
          ) : (
            <Square className="w-3.5 h-3.5" />
          )}
        </button>

        <button
          type="button"
          onClick={onToggleStar}
          className={`p-1 rounded hover:text-[var(--vellum-star)] transition-colors ${
            item.isStarred
              ? "text-[var(--vellum-star)]"
              : "text-[var(--vellum-ink-secondary)]"
          }`}
          aria-label={starLabel}
          title={starLabel}
        >
          <Star
            className={`w-4 h-4 ${item.isStarred ? "fill-[var(--vellum-star)]" : ""}`}
          />
        </button>
      </div>

      {/* Row Center — identical DOM in every density:
          line 1 = sender + subject + attachment + time,
          line 2 = preview snippet (always present, keeps the row height).
          Children get a 20px indent, connector lines, and a Reply label. */}
      <div
        className={`relative flex-1 min-w-0 flex flex-col overflow-hidden ${
          isChild ? "pl-5" : ""
        }`}
      >
        <div className="flex items-center gap-1 min-w-0">
          {isChild && (
            <span className="shrink-0 text-xs text-[var(--vellum-ink-secondary)] mr-1">
              {t("webmail_next.reply")}
            </span>
          )}
          <span
            className={`truncate shrink-0 ${metrics.senderMax} ${
              isChild ? childLine1 : metrics.line1
            } ${senderColor}`}
          >
            {item.sender}
          </span>

          {/* Thread count badge pill */}
          {item.threadCount && item.threadCount > 1 && (
            <button
              data-testid={`thread-toggle-${item.threadId || item.uid}`}
              type="button"
              onClick={onToggleThread}
              aria-expanded={item.isThreadExpanded ? "true" : "false"}
              className="h-4 px-1 rounded-full text-[11px] font-[650] bg-[var(--vellum-border)] text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-border-subtle)] flex items-center gap-0.5 shrink-0"
              title={threadTitle}
            >
              <span>{item.threadCount}</span>
              {item.isThreadExpanded ? (
                <ChevronDown className="w-2.5 h-2.5" />
              ) : (
                <ChevronRight className="w-2.5 h-2.5" />
              )}
            </button>
          )}

          <span
            className={`truncate flex-1 min-w-0 ${
              isChild ? childLine1 : metrics.line1
            } ${subjectColor}`}
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

          {/* Timestamp / Quick Action Hover Bar */}
          <span className="shrink-0 ml-2 text-xs text-[var(--vellum-ink-secondary)] flex items-center">
            <span className="group-hover:hidden" title={dateTitle}>
              {dateLabel}
            </span>
            <span className="hidden group-hover:flex items-center gap-1">
              {onToggleRead && (
                <button
                  type="button"
                  onClick={onToggleRead}
                  className="p-1 hover:text-[var(--vellum-ink)] rounded hover:bg-[var(--vellum-border)]"
                  aria-label={readLabel}
                  title={readLabel}
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
                  className="p-1 hover:text-[#DC2626] rounded hover:bg-[var(--vellum-border)]"
                  aria-label={deleteLabel}
                  title={deleteLabel}
                >
                  <Trash2 className="w-3 h-3" />
                </button>
              )}
            </span>
          </span>
        </div>

        {/* Line 2: preview snippet — always rendered so the row height
            never collapses; an empty preview keeps a non-breaking space. */}
        <p
          data-testid={`mail-snippet-${item.uid}`}
          className={`truncate font-normal text-[var(--vellum-ink-secondary)] ${metrics.line2}`}
        >
          {item.snippet ? item.snippet : " "}
        </p>
      </div>
    </div>
  );
}

export function webmailMessageRowPropsAreEqual(
  prev: WebmailMessageRowProps,
  next: WebmailMessageRowProps,
): boolean {
  const a = prev.item;
  const b = next.item;
  return (
    a.uid === b.uid &&
    a.subject === b.subject &&
    a.sender === b.sender &&
    a.date === b.date &&
    a.snippet === b.snippet &&
    a.isUnread === b.isUnread &&
    a.isStarred === b.isStarred &&
    a.hasAttachments === b.hasAttachments &&
    a.threadId === b.threadId &&
    a.threadCount === b.threadCount &&
    a.isThreadExpanded === b.isThreadExpanded &&
    a.isThreadChild === b.isThreadChild &&
    a.isLastThreadChild === b.isLastThreadChild &&
    a.folder === b.folder &&
    prev.isSelected === next.isSelected &&
    prev.isActive === next.isActive &&
    Boolean(prev.isCursor) === Boolean(next.isCursor) &&
    prev.density === next.density &&
    prev.onContextMenu === next.onContextMenu &&
    prev.onDragStartUids === next.onDragStartUids
  );
}

export const WebmailMessageRow = React.memo(
  WebmailMessageRowView,
  webmailMessageRowPropsAreEqual,
);
