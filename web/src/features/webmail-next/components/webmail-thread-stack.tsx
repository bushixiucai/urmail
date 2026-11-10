import { useEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Archive,
  ChevronDown,
  ChevronUp,
  Forward,
  MailOpen,
  Reply,
  ReplyAll,
  Trash2,
} from "lucide-react";
import type {
  MessageDetail,
  MessageSummary,
} from "@/features/webmail/mailbox-api";
import { formatFullDate, formatListDate } from "../lib/format-list-date";
import { avatarInitial } from "../lib/avatar-initial";

export interface ThreadStackProps {
  members: MessageSummary[];
  /** Contract total (cross-box, all pages); falls back to member data. */
  totalOverride?: number;
  /** The message currently expanded (body shown). */
  expandedUid: number;
  /** Full detail for the expanded message (null while loading). */
  expandedDetail: MessageDetail | null;
  isDetailLoading?: boolean;
  /** Rendered body for the expanded card (iframe / plain text). */
  bodySlot: React.ReactNode;
  onExpand: (uid: number) => void;
  onReply: (msg: MessageDetail) => void;
  onReplyAll: (msg: MessageDetail) => void;
  onForward: (msg: MessageDetail) => void;
  onMarkThreadRead: () => void;
  onArchiveThread: () => void;
  onDeleteThread: () => void;
}

function senderName(m: MessageSummary): string {
  const from = m.from?.[0];
  return from?.name || from?.email?.split("@")[0] || "?";
}

function senderInitial(m: MessageSummary): string {
  const from = m.from?.[0];
  // F4: encoded-word junk in the display name must not render as tofu.
  return avatarInitial(senderName(m), from?.email?.split("@")[0]);
}

/**
 * Thread reading stack (WEBMAIL-V0610-VISUAL §3).
 * Oldest cards on top, newest at the bottom. Exactly one card expanded.
 */
export function WebmailThreadStack({
  members,
  totalOverride,
  expandedUid,
  expandedDetail,
  isDetailLoading,
  bodySlot,
  onExpand,
  onReply,
  onReplyAll,
  onForward,
  onMarkThreadRead,
  onArchiveThread,
  onDeleteThread,
}: ThreadStackProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n?.language;
  const cardRefs = useRef<Map<number, HTMLDivElement>>(new Map());

  // Spec: oldest → newest in the stack.
  const ordered = useMemo(
    () =>
      [...members].sort(
        (a, b) =>
          new Date(a.date || 0).getTime() - new Date(b.date || 0).getTime(),
      ),
    [members],
  );

  const expandedIndex = ordered.findIndex((m) => m.uid === expandedUid);

  useEffect(() => {
    const el = cardRefs.current.get(expandedUid);
    // jsdom test doubles have no scrollIntoView.
    el?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [expandedUid]);

  const goPrev = () => {
    if (expandedIndex <= 0) return;
    onExpand(ordered[expandedIndex - 1].uid);
  };
  const goNext = () => {
    if (expandedIndex < 0 || expandedIndex >= ordered.length - 1) return;
    onExpand(ordered[expandedIndex + 1].uid);
  };

  const totalThreadCount = useMemo(() => {
    if (totalOverride != null) return totalOverride;
    const maxServerCount = Math.max(
      ...members.map((m) => m.thread_count ?? 0),
      0,
    );
    return maxServerCount > ordered.length ? maxServerCount : ordered.length;
  }, [totalOverride, members, ordered.length]);

  const countLabel = t("webmail_next.thread_loaded_count", {
    loaded: ordered.length,
    total: totalThreadCount,
  });

  return (
    <div
      data-testid="thread-stack"
      className="flex-1 flex flex-col min-h-0"
      aria-label={t("webmail_next.thread_stack_label", {
        count: totalThreadCount,
      })}
    >
      {/* Thread action bar — sticky top */}
      <div
        data-testid="thread-action-bar"
        className="h-12 px-4 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center justify-between shrink-0"
      >
        <div className="flex items-center gap-1">
          <button
            type="button"
            data-testid="thread-mark-read"
            onClick={onMarkThreadRead}
            className="h-8 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1.5"
          >
            <MailOpen className="w-4 h-4" />
            <span>{t("webmail_next.mark_read")}</span>
          </button>
          <button
            type="button"
            data-testid="thread-archive"
            onClick={onArchiveThread}
            className="h-8 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1.5"
          >
            <Archive className="w-4 h-4" />
            <span>{t("webmail_next.archive")}</span>
          </button>
          <button
            type="button"
            data-testid="thread-delete"
            onClick={onDeleteThread}
            className="h-8 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] hover:text-[var(--vellum-danger-strong)] flex items-center gap-1.5"
          >
            <Trash2 className="w-4 h-4" />
            <span>{t("webmail_next.delete")}</span>
          </button>
        </div>
        <div className="flex items-center gap-2">
          <span
            data-testid="thread-count-label"
            className="text-xs text-[var(--vellum-ink-secondary)]"
          >
            {countLabel}
          </span>
          <button
            type="button"
            data-testid="thread-prev"
            onClick={goPrev}
            disabled={expandedIndex <= 0}
            aria-disabled={expandedIndex <= 0}
            className="h-8 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1 disabled:opacity-40"
          >
            <ChevronUp className="w-4 h-4" />
            <span>{t("webmail_next.thread_prev")}</span>
          </button>
          <button
            type="button"
            data-testid="thread-next"
            onClick={goNext}
            disabled={expandedIndex < 0 || expandedIndex >= ordered.length - 1}
            aria-disabled={
              expandedIndex < 0 || expandedIndex >= ordered.length - 1
            }
            className="h-8 px-2.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1 disabled:opacity-40"
          >
            <span>{t("webmail_next.thread_next")}</span>
            <ChevronDown className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Card stack */}
      <div className="flex-1 overflow-y-auto bg-[var(--vellum-ground)] px-3 sm:px-6 py-3 sm:py-4">
        <div className="relative max-w-[720px] mx-auto space-y-2">
          {/* Timeline (decorative) */}
          <div
            aria-hidden
            className="pointer-events-none absolute left-[26px] top-4 bottom-4 w-px bg-[var(--vellum-border)]"
          />

          {ordered.map((member) => {
            const isExpanded = member.uid === expandedUid;
            const unread = !(member.flags || []).includes("\\Seen");
            const name = senderName(member);
            const shortDate = formatListDate(member.date, locale, {
              yesterday: t("webmail_next.date_yesterday"),
            });
            const fullDate = formatFullDate(member.date, locale);
            const snippet =
              member.preview && member.preview !== member.subject
                ? member.preview
                : member.subject || "";

            return (
              <div
                key={member.uid}
                ref={(el) => {
                  if (el) cardRefs.current.set(member.uid, el);
                  else cardRefs.current.delete(member.uid);
                }}
                data-testid={`thread-card-${member.uid}`}
                data-expanded={isExpanded ? "true" : "false"}
                className="relative bg-[var(--vellum-surface)] border border-[var(--vellum-border)] rounded-lg"
              >
                {/* Avatar over the timeline */}
                <div
                  aria-hidden
                  className="absolute left-[12px] top-[10px] w-7 h-7 rounded-full bg-[var(--vellum-border)] text-[var(--vellum-ink)] text-xs font-[650] flex items-center justify-center ring-2 ring-[var(--vellum-surface)] z-10"
                >
                  {senderInitial(member)}
                </div>

                <div
                  role="button"
                  tabIndex={0}
                  data-testid={`thread-card-header-${member.uid}`}
                  aria-expanded={isExpanded}
                  onClick={() => onExpand(member.uid)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onExpand(member.uid);
                    }
                  }}
                  className="w-full h-12 pl-4 sm:pl-12 pr-3 flex items-center gap-2 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--brand-primary)] focus-visible:outline-offset-[-2px] rounded-lg cursor-pointer select-none"
                >
                  <span
                    className={`truncate max-w-[40%] text-[13px] leading-4 ${
                      unread
                        ? "font-[650] text-[var(--vellum-ink)]"
                        : "font-medium text-[var(--vellum-ink-read)]"
                    }`}
                  >
                    {name}
                  </span>
                  {!isExpanded && (
                    <span className="truncate flex-1 text-xs text-[var(--vellum-ink-secondary)]">
                      {snippet}
                    </span>
                  )}
                  {isExpanded && <span className="flex-1" />}

                  {isExpanded && expandedDetail && (
                    <span
                      className="shrink-0 flex items-center gap-1"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <button
                        type="button"
                        data-testid="thread-card-reply"
                        onClick={() => onReply(expandedDetail)}
                        className="h-8 px-2 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1"
                      >
                        <Reply className="w-4 h-4" />
                        <span>{t("webmail_next.reply")}</span>
                      </button>
                      <button
                        type="button"
                        data-testid="thread-card-reply-all"
                        onClick={() => onReplyAll(expandedDetail)}
                        className="h-8 px-2 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1"
                      >
                        <ReplyAll className="w-4 h-4" />
                        <span>{t("webmail_next.reply_all")}</span>
                      </button>
                      <button
                        type="button"
                        data-testid="thread-card-forward"
                        onClick={() => onForward(expandedDetail)}
                        className="h-8 px-2 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1"
                      >
                        <Forward className="w-4 h-4" />
                        <span>{t("webmail_next.forward")}</span>
                      </button>
                    </span>
                  )}

                  <span
                    className="shrink-0 text-xs text-[var(--vellum-ink-secondary)]"
                    title={fullDate}
                  >
                    {shortDate}
                  </span>
                  <ChevronDown
                    className={`w-4 h-4 shrink-0 text-[var(--vellum-ink-secondary)] transition-transform duration-[120ms] ease-out ${
                      isExpanded ? "" : "-rotate-90"
                    }`}
                  />
                </div>

                {isExpanded && (
                  <div
                    role="region"
                    aria-label={name}
                    data-testid={`thread-card-body-${member.uid}`}
                    className="border-t border-[var(--vellum-border-subtle)] pl-4 sm:pl-12 pr-3 sm:pr-4 py-3 sm:py-4"
                  >
                    {isDetailLoading && !expandedDetail ? (
                      <div className="animate-pulse space-y-2">
                        <div className="h-3 w-2/3 rounded bg-[var(--vellum-border)]" />
                        <div className="h-3 w-1/2 rounded bg-[var(--vellum-border)]" />
                        <div className="h-24 w-full rounded bg-[var(--vellum-border)]" />
                      </div>
                    ) : (
                      bodySlot
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
