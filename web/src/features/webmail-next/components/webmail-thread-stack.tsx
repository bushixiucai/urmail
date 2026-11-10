import { useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Archive,
  ChevronDown,
  ChevronUp,
  Forward,
  MailOpen,
  Reply,
  ReplyAll,
  Star,
  Trash2,
} from "lucide-react";
import type {
  MessageDetail,
  MessageSummary,
} from "@/features/webmail/mailbox-api";
import { formatFullDate, formatListDate } from "../lib/format-list-date";
import { avatarInitial } from "../lib/avatar-initial";
import { threadMemberKey } from "../lib/thread-members-api";

export interface ThreadStackProps {
  members: MessageSummary[];
  /** Contract total (cross-box, all pages); falls back to member data. */
  totalOverride?: number;
  /** Unread count across origin-folder members (0 → badge hidden). */
  unreadCount?: number;
  /** The message currently expanded (body shown). */
  expandedUid: number;
  /** Composite identity (folder_enc:uid) of the expanded member when it
   * is unambiguous (same-thread same-uid across folders); falls back to
   * expandedUid when null. */
  expandedKey?: string | null;
  /** Full detail for the expanded message (null while loading). */
  expandedDetail: MessageDetail | null;
  isDetailLoading?: boolean;
  /** Rendered body for the expanded card (iframe / plain text). */
  bodySlot: React.ReactNode;
  onExpand: (uid: number, folderEnc?: string) => void;
  onReply: (msg: MessageDetail) => void;
  onReplyAll: (msg: MessageDetail) => void;
  onForward: (msg: MessageDetail) => void;
  /** §11-5 single-message actions (expanded card header). */
  onToggleStar?: (uid: number, folderEnc?: string) => void;
  onDelete?: (uid: number, folderEnc?: string) => void;
  /** §11-5 expansion relay: focus the card header with this composite
   * key (folder_enc:uid) when set. */
  focusKey?: string | null;
  /** Name of the Sent folder (\Sent flag) for the §11-4 cross-box badge. */
  sentFolderName?: string;
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
  unreadCount,
  expandedUid,
  expandedKey,
  expandedDetail,
  isDetailLoading,
  bodySlot,
  onExpand,
  onReply,
  onReplyAll,
  onForward,
  onToggleStar,
  onDelete,
  focusKey,
  sentFolderName,
  onMarkThreadRead,
  onArchiveThread,
  onDeleteThread,
}: ThreadStackProps) {
  const { t, i18n } = useTranslation();
  const locale = i18n?.language;
  const cardRefs = useRef<Map<string, HTMLDivElement>>(new Map());
  const headerRefs = useRef<Map<string, HTMLDivElement>>(new Map());

  // Spec: oldest → newest in the stack.
  const ordered = useMemo(
    () =>
      [...members].sort(
        (a, b) =>
          new Date(a.date || 0).getTime() - new Date(b.date || 0).getTime(),
      ),
    [members],
  );

  const isExpandedMember = (m: MessageSummary): boolean =>
    expandedKey != null
      ? threadMemberKey(m) === expandedKey
      : m.uid === expandedUid;
  const expandedIndex = ordered.findIndex(isExpandedMember);

  useEffect(() => {
    const target = ordered.find(isExpandedMember);
    const el = target
      ? cardRefs.current.get(threadMemberKey(target))
      : undefined;
    // jsdom test doubles have no scrollIntoView.
    el?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
    // Deliberately not keyed on `ordered`: "load earlier" prepends must not
    // trigger a smooth scroll that fights the DEF-5 anchoring compensation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expandedUid, expandedKey]);

  // §11-5 focus management: the expansion relay (delete → expand neighbor)
  // moves keyboard focus onto the newly expanded card's header.
  useEffect(() => {
    if (focusKey == null) return;
    headerRefs.current.get(focusKey)?.focus();
  }, [focusKey]);

  // §11-3 scroll anchoring: when older pages are prepended ("加载更早"),
  // the expanded card's offsetTop grows by the inserted height; shifting
  // scrollTop by the same delta keeps the reading position visually still
  // (±3px acceptance). Expanded-card changes are ignored (intended jumps).
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const anchorSnapRef = useRef<{ key: string | null; top: number } | null>(
    null,
  );
  useLayoutEffect(() => {
    const scroller = scrollRef.current;
    const target = ordered.find(isExpandedMember);
    const card = target
      ? cardRefs.current.get(threadMemberKey(target))
      : undefined;
    const key = target ? threadMemberKey(target) : null;
    const snap = anchorSnapRef.current;
    if (scroller && card && snap && key != null && snap.key === key) {
      const delta = card.offsetTop - snap.top;
      if (delta > 0) scroller.scrollTop += delta;
    }
    anchorSnapRef.current =
      card && key != null ? { key, top: card.offsetTop } : null;
  }, [members, expandedUid, expandedKey]);

  const goPrev = () => {
    if (expandedIndex <= 0) return;
    onExpand(
      ordered[expandedIndex - 1].uid,
      ordered[expandedIndex - 1].folder_enc,
    );
  };
  const goNext = () => {
    if (expandedIndex < 0 || expandedIndex >= ordered.length - 1) return;
    onExpand(
      ordered[expandedIndex + 1].uid,
      ordered[expandedIndex + 1].folder_enc,
    );
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

  // §11-1 subject bar: expanded message's subject, falling back to the
  // newest member; unread badge renders only when there is unread mail.
  const subject =
    expandedDetail?.subject ||
    (expandedIndex >= 0
      ? ordered[expandedIndex]?.subject
      : ordered[ordered.length - 1]?.subject) ||
    t("page.webmail.no_subject");
  const unreadBadgeCount = unreadCount ?? 0;

  return (
    <div
      data-testid="thread-stack"
      className="flex-1 flex flex-col min-h-0"
      aria-label={t("webmail_next.thread_stack_label", {
        count: totalThreadCount,
      })}
    >
      {/* §11-1 subject bar — sits above the action bar (header = 96px). */}
      <div
        data-testid="thread-subject-bar"
        className="h-12 px-4 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center gap-2 shrink-0"
      >
        <h2
          className="text-sm font-[650] text-[var(--vellum-ink)] truncate min-w-0 flex-1"
          title={subject}
        >
          {subject}
        </h2>
        <span
          data-testid="thread-total-badge"
          title={countLabel}
          className="shrink-0 h-5 px-2 rounded-full bg-[var(--vellum-surface-muted)] text-[var(--vellum-ink-secondary)] text-[11px] font-semibold leading-5 text-center"
        >
          {totalThreadCount}
        </span>
        {unreadBadgeCount > 0 && (
          <span
            data-testid="thread-unread-badge"
            aria-label={t("webmail_next.thread_unread_badge", {
              count: unreadBadgeCount,
            })}
            title={t("webmail_next.thread_unread_badge", {
              count: unreadBadgeCount,
            })}
            className="shrink-0 h-5 px-2 rounded-full bg-[var(--brand-primary)] text-white text-[11px] font-semibold leading-5 text-center"
          >
            {unreadBadgeCount}
          </span>
        )}
      </div>
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
      <div
        ref={scrollRef}
        data-testid="thread-stack-scroll"
        className="flex-1 overflow-y-auto bg-[var(--vellum-ground)] px-3 sm:px-6 py-3 sm:py-4"
      >
        <div className="relative max-w-[720px] mx-auto space-y-2">
          {/* Timeline (decorative) */}
          <div
            aria-hidden
            className="pointer-events-none absolute left-[26px] top-4 bottom-4 w-px bg-[var(--vellum-border)]"
          />

          {ordered.map((member) => {
            const memberCardKey = threadMemberKey(member);
            const isExpanded = isExpandedMember(member);
            const isSentMember =
              sentFolderName != null &&
              (member.folder ?? "").toLowerCase() ===
                sentFolderName.toLowerCase();
            const unread = !(member.flags || []).includes("\\Seen");
            const isStarred = (member.flags || []).includes("\\Flagged");
            const name = isSentMember
              ? t("webmail_next.sent_self")
              : senderName(member);
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
                key={memberCardKey}
                ref={(el) => {
                  if (el) cardRefs.current.set(memberCardKey, el);
                  else cardRefs.current.delete(memberCardKey);
                }}
                data-testid={`thread-card-${member.uid}`}
                data-expanded={isExpanded ? "true" : "false"}
                className="relative bg-[var(--vellum-surface)] border border-[var(--vellum-border)] rounded-lg"
              >
                {/* Avatar over the timeline (§11-4: Sent members get a
                    brand ring marking "me") */}
                <div
                  aria-hidden
                  data-testid={`thread-card-avatar-${memberCardKey}`}
                  data-sent={isSentMember ? "true" : undefined}
                  className={`absolute left-[12px] top-[10px] w-7 h-7 rounded-full bg-[var(--vellum-border)] text-[var(--vellum-ink)] text-xs font-[650] flex items-center justify-center z-10 ${
                    isSentMember
                      ? "ring-2 ring-[var(--brand-primary)]"
                      : "ring-2 ring-[var(--vellum-surface)]"
                  }`}
                >
                  {senderInitial(member)}
                </div>

                <div
                  role="button"
                  tabIndex={0}
                  ref={(el) => {
                    if (el) headerRefs.current.set(memberCardKey, el);
                    else headerRefs.current.delete(memberCardKey);
                  }}
                  data-testid={`thread-card-header-${member.uid}`}
                  aria-expanded={isExpanded}
                  onClick={() => onExpand(member.uid, member.folder_enc)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onExpand(member.uid, member.folder_enc);
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
                  {isSentMember && (
                    <span
                      data-testid={`thread-card-sent-${memberCardKey}`}
                      className="h-4 px-1 rounded text-[11px] font-medium text-[var(--vellum-ink-secondary)] bg-[var(--vellum-surface-muted)] shrink-0"
                    >
                      {t("webmail_next.sent_badge")}
                    </span>
                  )}
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
                        <span className="hidden sm:inline">
                          {t("webmail_next.reply")}
                        </span>
                      </button>
                      <button
                        type="button"
                        data-testid="thread-card-reply-all"
                        onClick={() => onReplyAll(expandedDetail)}
                        className="h-8 px-2 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1"
                      >
                        <ReplyAll className="w-4 h-4" />
                        <span className="hidden sm:inline">
                          {t("webmail_next.reply_all")}
                        </span>
                      </button>
                      <button
                        type="button"
                        data-testid="thread-card-forward"
                        onClick={() => onForward(expandedDetail)}
                        className="h-8 px-2 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] flex items-center gap-1"
                      >
                        <Forward className="w-4 h-4" />
                        <span className="hidden sm:inline">
                          {t("webmail_next.forward")}
                        </span>
                      </button>
                      {/* §11-5 single-message actions: Star (dual-state)
                          and Delete, icon-only below sm (≥24px hit area). */}
                      {onToggleStar && (
                        <button
                          type="button"
                          data-testid={`thread-card-star-${member.uid}`}
                          aria-pressed={isStarred}
                          aria-label={
                            isStarred
                              ? t("webmail_next.starred")
                              : t("webmail_next.not_starred")
                          }
                          title={
                            isStarred
                              ? t("webmail_next.starred")
                              : t("webmail_next.not_starred")
                          }
                          onClick={() =>
                            onToggleStar(member.uid, member.folder_enc)
                          }
                          className={`h-8 w-8 rounded-md flex items-center justify-center transition-colors ${
                            isStarred
                              ? "text-[var(--vellum-star)] hover:bg-[var(--vellum-row-hover)]"
                              : "text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-row-hover)] hover:text-[var(--vellum-star)]"
                          }`}
                        >
                          <Star
                            className={`w-4 h-4 ${
                              isStarred ? "fill-[var(--vellum-star)]" : ""
                            }`}
                          />
                        </button>
                      )}
                      {onDelete && (
                        <button
                          type="button"
                          data-testid={`thread-card-delete-${member.uid}`}
                          aria-label={t("webmail_next.delete")}
                          title={t("webmail_next.delete")}
                          onClick={() =>
                            onDelete(member.uid, member.folder_enc)
                          }
                          className="h-8 w-8 rounded-md flex items-center justify-center text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-row-hover)] hover:text-[var(--vellum-danger-strong)] transition-colors"
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      )}
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
