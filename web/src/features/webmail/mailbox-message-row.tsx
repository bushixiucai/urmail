import React from "react";
import { useTranslation } from "react-i18next";
import { Flag, Layers, Mail, MailOpen, Paperclip, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  formatFrom,
  hasFlag,
  type MessageSummary,
  type MailboxAddress,
} from "./mailbox-api";

export type RowDensity = "comfortable" | "compact" | "list";

export interface RowColumnsConfig {
  fromTo?: boolean;
  subject?: boolean;
  date?: boolean;
  size?: boolean;
  flags?: boolean;
}

export interface MailboxMessageRowProps {
  message: MessageSummary;
  isSelected: boolean;
  isChecked: boolean;
  threadCount: number;
  onSelect: () => void;
  onToggleCheck: (e: React.MouseEvent) => void;
  onToggleFlag: (e: React.MouseEvent) => void;
  onToggleRead: (e: React.MouseEvent) => void;
  onDelete: (e: React.MouseEvent) => void;
  onThreadFilter?: () => void;
  crossResults?: boolean;
  dateText: string;
  isSelectionMode?: boolean;
  /** WP03 Workspace density: 'comfortable' (default), 'compact', or 'list' (pure single-line list). */
  density?: RowDensity;
  /** WP03 Sent / Drafts display recipient (To/Cc) instead of From. */
  isSentOrDrafts?: boolean;
  /** WP03 Optional columns visibility. */
  visibleColumns?: RowColumnsConfig;
}

export function formatSize(bytes?: number): string {
  if (bytes == null || bytes <= 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatRecipient(to?: unknown): string {
  if (!to) return "";
  if (typeof to === "string") return to;
  if (Array.isArray(to)) {
    return to
      .map((a: unknown) => {
        if (!a) return "";
        if (typeof a === "string") return a;
        const addr = a as MailboxAddress;
        return addr.name || addr.email || "";
      })
      .filter(Boolean)
      .join(", ");
  }
  return "";
}

export const MailboxMessageRow = React.memo(function MailboxMessageRow({
  message,
  isSelected,
  isChecked,
  threadCount,
  onSelect,
  onToggleCheck,
  onToggleFlag,
  onToggleRead,
  onDelete,
  onThreadFilter,
  crossResults = false,
  dateText,
  isSelectionMode = false,
  density = "comfortable",
  isSentOrDrafts = false,
  visibleColumns,
}: MailboxMessageRowProps) {
  const { t } = useTranslation();

  const unread = !hasFlag(message.flags, "\\Seen");
  const flagged = hasFlag(message.flags, "\\Flagged");
  const hasAttachment = message.has_attachments === true;

  // Sent / Drafts recipient formatting (WP03 / I02)
  const recipientTarget =
    (message as { to?: unknown }).to ||
    (message as { recipient?: string }).recipient ||
    (message as { cc?: unknown }).cc;
  const formattedRecipient = formatRecipient(recipientTarget);

  const senderName =
    isSentOrDrafts && formattedRecipient
      ? `${t("page.webmail.to_prefix", { defaultValue: "To: " })}${formattedRecipient}`
      : formatFrom(message.from) || t("page.webmail.unknown_sender");

  const subjectName = message.subject || t("page.webmail.no_subject");
  const sizeText = formatSize(message.size);

  const showFromTo = visibleColumns?.fromTo !== false;
  const showSubject = visibleColumns?.subject !== false;
  const showDate = visibleColumns?.date !== false;
  const showSize = visibleColumns?.size === true;
  const showFlags = visibleColumns?.flags !== false;

  const isPureList = density === "list";
  const isCompact = density === "compact";

  return (
    <li
      className={cn(
        "group relative flex items-center min-w-0 border-b border-border/40 transition-colors duration-150 border-l-2 select-none",
        isSelected
          ? "bg-muted border-l-primary"
          : "hover:bg-muted/50 border-l-transparent",
        unread && !isSelected && "bg-background",
        isPureList && "h-10 text-xs",
      )}
    >
      {/* Unread solid blue dot indicator */}
      <div className="flex items-center justify-center pl-2.5 pr-0.5 shrink-0">
        <span
          data-testid="unread-indicator"
          data-unread={unread ? "true" : "false"}
          className={cn(
            "h-2 w-2 rounded-full shrink-0 transition-colors duration-150",
            unread
              ? "bg-primary shadow-xs shadow-primary/40 ring-2 ring-primary/20"
              : "bg-transparent",
          )}
          aria-hidden="true"
        />
      </div>

      {/* Native checkbox */}
      <label
        className={cn(
          "flex items-center px-1.5 shrink-0 cursor-pointer",
          isPureList ? "min-h-0" : "min-h-12 md:min-h-0",
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <input
          type="checkbox"
          className="h-4 w-4 rounded border-input text-primary focus:ring-1 focus:ring-primary accent-primary cursor-pointer shrink-0 transition-all"
          checked={isChecked}
          aria-label={t("page.webmail.select_message_named", {
            name: senderName || message.subject || t("page.webmail.no_subject"),
          })}
          onChange={(e) => e.stopPropagation()}
          onClick={(e) => {
            e.stopPropagation();
            onToggleCheck(e);
          }}
        />
      </label>

      {/* Main interactive message content */}
      <button
        type="button"
        onClick={onSelect}
        className={cn(
          "flex-1 min-w-0 text-left focus:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-sm cursor-pointer",
          isPureList
            ? "flex items-center gap-3 py-1 px-2 h-full"
            : isCompact
              ? "py-1.5 px-2"
              : "py-2.5 px-2",
        )}
      >
        {isPureList ? (
          /* Pure list single-line tabular view */
          <div className="flex items-center gap-3 flex-1 min-w-0">
            {showFlags && flagged && (
              <Flag
                className="h-3 w-3 text-amber-500 fill-amber-500 shrink-0"
                aria-label={t("page.webmail.flag")}
              />
            )}
            {crossResults && message.folder && (
              <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground border border-border/40">
                {message.folder}
              </span>
            )}
            {showFromTo && (
              <span
                className={cn(
                  "w-36 md:w-44 shrink-0 truncate",
                  unread
                    ? "font-semibold text-foreground"
                    : "font-normal text-muted-foreground",
                )}
              >
                {senderName}
              </span>
            )}
            {showSubject && (
              <div className="flex-1 min-w-0 flex items-center gap-1.5">
                <span
                  className={cn(
                    "truncate flex-1 min-w-0",
                    unread
                      ? "font-semibold text-foreground"
                      : "text-muted-foreground",
                  )}
                >
                  {subjectName}
                </span>
                {hasAttachment && (
                  <span
                    title={t("page.webmail.search_attachment", {
                      defaultValue: "Attachment",
                    })}
                    className="inline-flex items-center shrink-0"
                  >
                    <Paperclip
                      className="h-3.5 w-3.5 text-muted-foreground shrink-0"
                      data-testid="attachment-icon"
                      aria-label={t(
                        "page.webmail.search_attachment",
                        "Attachment",
                      )}
                    />
                  </span>
                )}
              </div>
            )}
            {showSize && (
              <span className="w-14 shrink-0 text-right tabular-nums text-xs text-muted-foreground">
                {sizeText}
              </span>
            )}
            {showDate && (
              <span className="w-20 md:w-24 shrink-0 text-right tabular-nums text-xs text-muted-foreground">
                {dateText}
              </span>
            )}
          </div>
        ) : (
          /* Comfortable / Compact 2-line view */
          <>
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground leading-tight mb-1">
              {showFlags && flagged && (
                <Flag
                  className="h-3 w-3 text-amber-500 fill-amber-500 shrink-0"
                  aria-label={t("page.webmail.flag")}
                />
              )}
              {crossResults && message.folder && (
                <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground border border-border/40">
                  {message.folder}
                </span>
              )}
              {showFromTo && (
                <span
                  className={cn(
                    "truncate flex-1 min-w-0",
                    unread
                      ? "font-semibold text-foreground"
                      : "font-normal text-muted-foreground",
                  )}
                >
                  {senderName}
                </span>
              )}
              <div className="flex items-center gap-1.5 shrink-0 ml-1">
                {hasAttachment && (
                  <span
                    title={t("page.webmail.search_attachment", {
                      defaultValue: "Attachment",
                    })}
                    className="inline-flex items-center"
                  >
                    <Paperclip
                      className="h-3.5 w-3.5 text-muted-foreground shrink-0"
                      data-testid="attachment-icon"
                      aria-label={t(
                        "page.webmail.search_attachment",
                        "Attachment",
                      )}
                    />
                  </span>
                )}
                {showSize && (
                  <span className="tabular-nums text-xs text-muted-foreground">
                    {sizeText}
                  </span>
                )}
                {showDate && (
                  <span className="tabular-nums text-xs text-muted-foreground">
                    {dateText}
                  </span>
                )}
              </div>
            </div>
            {showSubject && (
              <p
                className={cn(
                  "truncate leading-snug",
                  isCompact ? "text-xs" : "text-sm",
                  unread
                    ? "font-semibold text-foreground"
                    : "text-muted-foreground",
                )}
              >
                {subjectName}
              </p>
            )}
          </>
        )}
      </button>

      {/* Thread filter badge */}
      {onThreadFilter && !crossResults && threadCount > 1 && (
        <div className="flex items-center px-1.5 shrink-0">
          <button
            type="button"
            className="flex items-center gap-1 px-1.5 py-0.5 rounded text-xs text-muted-foreground hover:text-foreground hover:bg-muted shrink-0 transition-colors cursor-pointer border border-transparent hover:border-border/50"
            aria-label={t("page.webmail.thread_filter", {
              count: threadCount,
            })}
            title={t("page.webmail.thread_filter", {
              count: threadCount,
            })}
            onClick={(e) => {
              e.stopPropagation();
              onThreadFilter();
            }}
          >
            <Layers className="h-3 w-3" />
            <span className="tabular-nums">{threadCount}</span>
          </button>
        </div>
      )}

      {/* Hover action capsule */}
      {!isSelectionMode && (
        <div
          className="absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-0.5 bg-background/95 dark:bg-card/95 backdrop-blur-xs border border-border/80 shadow-xs rounded-lg px-1 py-0.5 opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus-within:opacity-100 focus-within:pointer-events-auto transition-all duration-150 z-10"
          aria-label={t("page.webmail.message_quick_actions", {
            defaultValue: "Quick actions",
          })}
        >
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            aria-label={
              flagged ? t("page.webmail.unflag") : t("page.webmail.flag")
            }
            title={flagged ? t("page.webmail.unflag") : t("page.webmail.flag")}
            onClick={(e) => {
              e.stopPropagation();
              onToggleFlag(e);
            }}
          >
            <Flag
              className={cn(
                "h-3.5 w-3.5 transition-colors",
                flagged
                  ? "text-amber-500 fill-amber-500"
                  : "text-muted-foreground hover:text-amber-500",
              )}
            />
          </button>
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            aria-label={
              unread
                ? t("page.webmail.mark_read")
                : t("page.webmail.mark_unread")
            }
            title={
              unread
                ? t("page.webmail.mark_read")
                : t("page.webmail.mark_unread")
            }
            onClick={(e) => {
              e.stopPropagation();
              onToggleRead(e);
            }}
          >
            {unread ? (
              <MailOpen className="h-3.5 w-3.5" />
            ) : (
              <Mail className="h-3.5 w-3.5" />
            )}
          </button>
          <button
            type="button"
            className="p-1.5 rounded-md hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors cursor-pointer"
            aria-label={t("page.webmail.delete")}
            title={t("page.webmail.delete")}
            onClick={(e) => {
              e.stopPropagation();
              onDelete(e);
            }}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      )}
    </li>
  );
});
