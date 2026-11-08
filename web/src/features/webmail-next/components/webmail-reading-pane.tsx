import { useState, useEffect, useRef, useCallback } from "react";
import { useTranslation } from "react-i18next";
import {
  Reply,
  ReplyAll,
  Forward,
  Trash2,
  Star,
  ShieldAlert,
  ArrowLeft,
  UserPlus,
  Check,
  Mail,
} from "lucide-react";
import { WebmailSecurityBadge } from "./webmail-security-badge";
import { WebmailAttachmentCard } from "./webmail-attachment-card";
import { sanitizeHtmlMail } from "@/features/webmail/sanitize-html-mail";
import {
  sanitizeHtmlMailAsync,
  sanitizeWorkerAvailable,
} from "@/features/webmail/sanitize-html-mail-async";
import {
  createContact,
  type MessageDetail,
  type MessageSummary,
} from "@/features/webmail/mailbox-api";
import { WebmailThreadStack } from "./webmail-thread-stack";

interface WebmailReadingPaneProps {
  message: MessageDetail | null;
  folderEnc: string;
  isLoading?: boolean;
  isGlobalRemoteImagesAllowed?: boolean;
  isManualRemoteImagesAllowed?: boolean;
  /** Loaded members of the selected thread (oldest→newest optional). */
  threadMembers?: MessageSummary[];
  onBack?: () => void;
  onReply?: (msg: MessageDetail) => void;
  onReplyAll?: (msg: MessageDetail) => void;
  onForward?: (msg: MessageDetail) => void;
  onDelete?: (uid: number) => void;
  onToggleStar?: (uid: number) => void;
  onLoadRemoteImages?: (uid: number) => void;
  onAlwaysTrustSender?: (senderEmail: string) => void;
  /** Switch the expanded stack card (loads that UID's detail). */
  onExpandThreadUid?: (uid: number) => void;
  onMarkThreadRead?: () => void;
  onArchiveThread?: () => void;
  onDeleteThread?: () => void;
}

const FRAME_MIN_HEIGHT = 420;
// Generous ceiling against runaway measurement on pathological bodies;
// taller mail simply scrolls the pane instead of being clipped.
const FRAME_MAX_HEIGHT = 10000;

/**
 * Height of the frame's srcdoc content, measured from the parent side.
 * Requires sandbox="allow-same-origin" (no allow-scripts — the CSP meta
 * injected by the sanitizer keeps the body inert, so script execution is
 * impossible even though the document is same-origin).
 */
function measureFrameHeight(frame: HTMLIFrameElement): number {
  try {
    const doc = frame.contentDocument;
    if (!doc || !doc.body) return 0;
    const body = doc.body;
    const docEl = doc.documentElement;
    return Math.max(
      body ? body.scrollHeight : 0,
      docEl ? docEl.scrollHeight : 0,
      body ? body.offsetHeight : 0,
    );
  } catch {
    // Cross-origin or not yet loaded — keep the current height.
    return 0;
  }
}

function SanitizedHtmlFrame({
  title,
  html,
}: {
  title: string;
  html: string | null;
}) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const [frameHeight, setFrameHeight] = useState(FRAME_MIN_HEIGHT);
  const resizeDebounce = useRef<number | null>(null);

  const remeasure = useCallback(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const height = measureFrameHeight(frame);
    if (height > 0) {
      setFrameHeight(
        Math.min(
          FRAME_MAX_HEIGHT,
          Math.max(FRAME_MIN_HEIGHT, Math.ceil(height)),
        ),
      );
    }
  }, []);

  // Re-measure once the srcdoc has parsed and again after its images
  // load — an <img> without intrinsic size collapses to ~0px, so the
  // first pass can undershoot.
  const handleLoad = useCallback(() => {
    remeasure();
    const doc = frameRef.current?.contentDocument;
    if (!doc) return;
    const pendingImages = Array.from(doc.images).filter((img) => !img.complete);
    for (const img of pendingImages) {
      img.addEventListener("load", remeasure, { once: true });
      img.addEventListener("error", remeasure, { once: true });
    }
  }, [remeasure]);

  // Body content that resizes without a load event (fonts, reflow after
  // webfont swap) — watch it from the parent while the document persists.
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    let observer: ResizeObserver | null = null;
    const attach = () => {
      const body = frame.contentDocument?.body;
      if (body && typeof ResizeObserver !== "undefined") {
        observer?.disconnect();
        observer = new ResizeObserver(() => {
          if (resizeDebounce.current != null) {
            window.clearTimeout(resizeDebounce.current);
          }
          resizeDebounce.current = window.setTimeout(remeasure, 80);
        });
        observer.observe(body);
      }
    };
    if (frame.contentDocument?.body) {
      attach();
    } else {
      frame.addEventListener("load", attach, { once: true });
    }
    return () => {
      observer?.disconnect();
      if (resizeDebounce.current != null) {
        window.clearTimeout(resizeDebounce.current);
        resizeDebounce.current = null;
      }
    };
  }, [html, remeasure]);

  // A new message must not inherit the previous mail's height.
  useEffect(() => {
    setFrameHeight(FRAME_MIN_HEIGHT);
  }, [html]);

  if (html == null) {
    return (
      <div
        data-testid="mail-html-skeleton"
        aria-busy="true"
        className="animate-pulse space-y-3"
      >
        <div className="h-3 w-2/3 rounded bg-[#EBE8E0]" />
        <div className="h-3 w-1/2 rounded bg-[#EBE8E0]" />
        <div className="h-24 w-full rounded bg-[#EBE8E0]" />
      </div>
    );
  }
  return (
    <iframe
      ref={frameRef}
      data-testid="mail-html-frame"
      title={title}
      // allow-same-origin (alone, never with allow-scripts) so the parent
      // can measure the srcdoc body; the sanitizer-injected CSP meta keeps
      // the document inert, preserving the XSS defense line.
      sandbox="allow-same-origin"
      referrerPolicy="no-referrer"
      className="w-full rounded border-0 bg-transparent"
      style={{
        height: frameHeight,
        minHeight: FRAME_MIN_HEIGHT,
        transition: "height 160ms ease-out",
      }}
      onLoad={handleLoad}
      srcDoc={html}
    />
  );
}

export function WebmailReadingPane({
  message,
  folderEnc,
  isLoading = false,
  isGlobalRemoteImagesAllowed = false,
  isManualRemoteImagesAllowed = false,
  threadMembers,
  onBack,
  onReply,
  onReplyAll,
  onForward,
  onDelete,
  onToggleStar,
  onLoadRemoteImages,
  onAlwaysTrustSender,
  onExpandThreadUid,
  onMarkThreadRead,
  onArchiveThread,
  onDeleteThread,
}: WebmailReadingPaneProps) {
  const { t } = useTranslation();
  const [localManualGranted, setLocalManualGranted] = useState(false);
  const [isAddingContact, setIsAddingContact] = useState(false);
  const [contactAdded, setContactAdded] = useState(false);

  const isImageAllowed = Boolean(
    isGlobalRemoteImagesAllowed ||
    isManualRemoteImagesAllowed ||
    localManualGranted,
  );
  const useWorker = sanitizeWorkerAvailable();
  const [workerHtml, setWorkerHtml] = useState<string | null>(null);

  useEffect(() => {
    setLocalManualGranted(false);
    setContactAdded(false);
  }, [message?.uid]);

  useEffect(() => {
    if (!useWorker || !message?.html) {
      setWorkerHtml(null);
      return;
    }
    let cancelled = false;
    setWorkerHtml(null);
    void sanitizeHtmlMailAsync(message.html, {
      allowRemoteImages: isImageAllowed,
    }).then((html) => {
      if (!cancelled) setWorkerHtml(html);
    });
    return () => {
      cancelled = true;
    };
  }, [useWorker, message?.html, message?.uid, isImageAllowed]);

  if (isLoading) {
    return (
      <div
        data-testid="webmail-reading-pane"
        className="flex-1 h-full bg-[#FAF9F5] p-6"
      >
        <div
          data-testid="reading-skeleton"
          aria-busy="true"
          aria-label={t("webmail_next.loading_message")}
          className="animate-pulse space-y-3"
        >
          <div className="h-4 w-2/3 rounded bg-[#EBE8E0]" />
          <div className="h-3 w-1/3 rounded bg-[#EBE8E0]" />
          <div className="h-24 w-full rounded bg-[#EBE8E0]" />
        </div>
      </div>
    );
  }

  if (!message) {
    return (
      <div
        data-testid="webmail-reading-pane"
        className="flex-1 h-full flex flex-col items-center justify-center bg-[#FAF9F5] text-xs text-[#52525B]"
      >
        <div className="w-12 h-12 rounded-full bg-[#F3F1EC] flex items-center justify-center mb-2">
          <Mail className="w-6 h-6 text-[var(--vellum-ink-secondary)]" />
        </div>
        <span className="text-sm font-semibold leading-[22px] text-[#18181B]">
          {t("webmail_next.empty_reading_title")}
        </span>
        <span className="mt-1 text-xs leading-4 text-[var(--vellum-ink-secondary)]">
          {t("webmail_next.empty_reading_body")}
        </span>
      </div>
    );
  }

  const sender = message.from?.[0];
  const senderEmail = sender?.email || "";
  const senderName = sender?.name || senderEmail;
  const isStarred = Boolean(
    Array.isArray(message.flags) && message.flags.includes("\\Flagged"),
  );

  // Server SanitizeHTML drops the <img> and emits note "remote_images_blocked".
  // It does not leave a remote src or inject data-blocked-src. HTML markers
  // remain only as a fallback if a body still contains them.
  const containsRemoteImages = Boolean(
    message.html &&
    (/<img\b[^>]*\bsrc\s*=\s*["']?https?:\/\//i.test(message.html) ||
      message.html.includes("data-blocked-src")),
  );
  const hasBlockedNote =
    Array.isArray(message.notes) &&
    message.notes.includes("remote_images_blocked");
  const hasBlockedImages =
    !isImageAllowed && (hasBlockedNote || containsRemoteImages);

  const handleShowImages = () => {
    setLocalManualGranted(true);
    if (message.uid) {
      onLoadRemoteImages?.(message.uid);
    }
  };

  const handleAlwaysTrust = () => {
    setLocalManualGranted(true);
    if (message.uid) {
      onLoadRemoteImages?.(message.uid);
    }
    onAlwaysTrustSender?.(senderEmail);
  };

  const handleAddContact = async () => {
    if (!senderEmail || contactAdded || isAddingContact) return;
    try {
      setIsAddingContact(true);
      await createContact(senderName, senderEmail);
      setContactAdded(true);
    } catch {
      // ignore
    } finally {
      setIsAddingContact(false);
    }
  };

  const bodySlot = (
    <>
      {hasBlockedImages && (
        <div
          data-testid="remote-images-banner"
          className="mb-3 p-3 bg-[#FEF3C7] border border-[#FDE68A] rounded-lg flex items-center justify-between gap-3 text-xs text-[#92400E]"
        >
          <div className="flex items-center gap-2">
            <ShieldAlert className="w-4 h-4 text-[#D97706] shrink-0" />
            <span>{t("webmail_next.remote_images_blocked")}</span>
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <button
              data-testid="show-images-button"
              type="button"
              onClick={handleShowImages}
              className="px-2 py-1 bg-white border border-[#D97706]/30 text-[#92400E] font-medium rounded hover:bg-[#FFFBEB] transition-colors"
            >
              {t("webmail_next.show_images")}
            </button>
            <button
              data-testid="always-trust-button"
              type="button"
              onClick={handleAlwaysTrust}
              className="px-2 py-1 text-[#92400E] hover:underline transition-colors"
            >
              {t("webmail_next.always_trust_images")}
            </button>
          </div>
        </div>
      )}
      <div
        data-testid="mail-body-container"
        className="text-sm text-[#18181B] leading-relaxed min-h-[220px]"
      >
        {message.html ? (
          <SanitizedHtmlFrame
            title={message.subject || "Email Content"}
            html={
              useWorker
                ? workerHtml
                : sanitizeHtmlMail(message.html, {
                    allowRemoteImages: isImageAllowed,
                  })
            }
          />
        ) : (
          <pre className="whitespace-pre-wrap font-sans text-xs text-[#18181B] max-w-[65ch]">
            {message.text || "(Empty message)"}
          </pre>
        )}
      </div>
      {message.attachments && message.attachments.length > 0 && (
        <div data-testid="attachments-container" className="pt-2">
          <div className="text-xs font-semibold text-[var(--vellum-ink-secondary)] mb-3">
            Attachments ({message.attachments.length})
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {message.attachments.map((att) => (
              <WebmailAttachmentCard
                key={att.part_id}
                attachment={att}
                folderEnc={folderEnc}
                uid={message.uid}
              />
            ))}
          </div>
        </div>
      )}
    </>
  );

  // Thread stack (VISUAL §3): when the loaded set has 2+ members of the
  // same thread, render the stacked cards instead of the single-message pane.
  if (threadMembers && threadMembers.length > 1 && message) {
    return (
      <div
        data-testid="webmail-reading-pane"
        className="flex-1 h-full flex flex-col bg-[var(--vellum-ground)] overflow-hidden"
      >
        {onBack && (
          <div className="md:hidden h-10 px-3 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center shrink-0">
            <button
              data-testid="reading-back-button"
              type="button"
              onClick={onBack}
              className="p-1.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] rounded"
              aria-label={t("webmail_next.back_to_list")}
            >
              <ArrowLeft className="w-4 h-4" />
            </button>
          </div>
        )}
        <WebmailThreadStack
          members={threadMembers}
          expandedUid={message.uid}
          expandedDetail={message}
          isDetailLoading={isLoading}
          bodySlot={bodySlot}
          onExpand={(uid) => onExpandThreadUid?.(uid)}
          onReply={(msg) => onReply?.(msg)}
          onReplyAll={(msg) => onReplyAll?.(msg)}
          onForward={(msg) => onForward?.(msg)}
          onMarkThreadRead={() => onMarkThreadRead?.()}
          onArchiveThread={() => onArchiveThread?.()}
          onDeleteThread={() => onDeleteThread?.()}
        />
      </div>
    );
  }

  return (
    <div
      data-testid="webmail-reading-pane"
      className="flex-1 h-full flex flex-col bg-[#FAF9F5] overflow-y-auto"
    >
      {/* 1. Header Toolbar */}
      <div className="h-12 px-6 border-b border-[#EBE8E0] bg-[#FFFFFF] flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2">
          {onBack && (
            <button
              data-testid="reading-back-button"
              type="button"
              onClick={onBack}
              className="md:hidden p-1.5 text-[var(--vellum-ink-secondary)] hover:text-[#18181B] rounded hover:bg-[#F3F1EC]"
              aria-label={t("webmail_next.back_to_list")}
              title={t("webmail_next.back_to_list")}
            >
              <ArrowLeft className="w-4 h-4" />
            </button>
          )}
          <button
            data-testid="reading-reply-button"
            type="button"
            onClick={() => onReply?.(message)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-[#18181B] hover:bg-[#F3F1EC] transition-colors"
          >
            <Reply className="w-4 h-4" />
            <span>{t("webmail_next.reply")}</span>
          </button>
          <button
            type="button"
            onClick={() => onReplyAll?.(message)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-[#18181B] hover:bg-[#F3F1EC] transition-colors"
          >
            <ReplyAll className="w-4 h-4" />
            <span>{t("webmail_next.reply_all")}</span>
          </button>
          <button
            type="button"
            onClick={() => onForward?.(message)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-[#18181B] hover:bg-[#F3F1EC] transition-colors"
          >
            <Forward className="w-4 h-4" />
            <span>{t("webmail_next.forward")}</span>
          </button>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => onToggleStar?.(message.uid)}
            className={`p-1.5 rounded hover:bg-[#F3F1EC] transition-colors ${
              isStarred
                ? "text-[var(--vellum-star)]"
                : "text-[var(--vellum-ink-secondary)]"
            }`}
            aria-label={t("webmail_next.star")}
            title={t("webmail_next.star")}
          >
            <Star
              className={`w-4 h-4 ${isStarred ? "fill-[var(--vellum-star)]" : ""}`}
            />
          </button>
          <button
            type="button"
            onClick={() => onDelete?.(message.uid)}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:text-[#DC2626] hover:bg-[#F3F1EC] transition-colors"
            aria-label={t("webmail_next.delete")}
            title={t("webmail_next.delete")}
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* 2. Message Detail Content — full width like Gmail/Outlook; the
          readable-measure cap applies to plain-text paragraphs only, never
          to the body container itself. */}
      <div className="px-6 py-4 w-full space-y-4">
        {/* Subject & Security */}
        <div>
          <h1 className="text-[20px] leading-7 font-[650] text-[#18181B] tracking-tight">
            {message.subject || "(No subject)"}
          </h1>
          <div className="flex flex-wrap items-center justify-between gap-3 mt-3 pt-3 border-t border-[#EBE8E0]">
            <div className="flex items-center gap-3">
              <div className="w-9 h-9 rounded-full bg-[var(--brand-primary,#2563eb)] text-white font-bold text-sm flex items-center justify-center shrink-0">
                {(senderName || "?").charAt(0).toUpperCase()}
              </div>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-semibold text-[#18181B]">
                    {senderName}
                  </span>
                  <span className="text-xs text-[var(--vellum-ink-secondary)]">
                    &lt;{senderEmail}&gt;
                  </span>
                  <WebmailSecurityBadge
                    authentication={message.authentication}
                  />
                  <button
                    data-testid="btn-add-contact"
                    type="button"
                    onClick={handleAddContact}
                    disabled={contactAdded || isAddingContact}
                    className={`ml-1 flex items-center gap-1 px-1.5 py-0.5 rounded text-xs transition-colors ${
                      contactAdded
                        ? "text-[#16A34A] bg-[#DCFCE7]"
                        : "text-[var(--vellum-ink-secondary)] hover:text-[#18181B] hover:bg-[#EBE8E0]"
                    }`}
                    aria-label={
                      contactAdded
                        ? t("webmail_next.contact_added")
                        : t("webmail_next.add_contact")
                    }
                    title={
                      contactAdded
                        ? t("webmail_next.contact_added")
                        : t("webmail_next.add_contact")
                    }
                  >
                    {contactAdded ? (
                      <>
                        <Check className="w-3 h-3" />
                        <span>{t("webmail_next.contact_added", "Added")}</span>
                      </>
                    ) : (
                      <>
                        <UserPlus className="w-3 h-3" />
                        <span>
                          {t("webmail_next.add_contact", "+ Add Contact")}
                        </span>
                      </>
                    )}
                  </button>
                </div>
                <div className="text-xs text-[#52525B] mt-0.5">
                  {message.to && message.to.length > 0
                    ? t("webmail_next.to_recipients", {
                        recipients: message.to
                          .map((t) => t.name || t.email)
                          .join(", "),
                      })
                    : t("webmail_next.to_me")}
                </div>
              </div>
            </div>

            <div className="text-xs text-[#52525B]">
              {message.date ? new Date(message.date).toLocaleString() : ""}
            </div>
          </div>
        </div>

        {/* Remote Images Neutralization Banner */}
        {hasBlockedImages && (
          <div
            data-testid="remote-images-banner"
            className="p-3 bg-[#FEF3C7] border border-[#FDE68A] rounded-lg flex items-center justify-between gap-3 text-xs text-[#92400E]"
          >
            <div className="flex items-center gap-2">
              <ShieldAlert className="w-4 h-4 text-[#D97706] shrink-0" />
              <span>{t("webmail_next.remote_images_blocked")}</span>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              <button
                data-testid="show-images-button"
                type="button"
                onClick={handleShowImages}
                className="px-2 py-1 bg-white border border-[#D97706]/30 text-[#92400E] font-medium rounded hover:bg-[#FFFBEB] transition-colors"
              >
                {t("webmail_next.show_images")}
              </button>
              <button
                data-testid="always-trust-button"
                type="button"
                onClick={handleAlwaysTrust}
                className="px-2 py-1 text-[#92400E] hover:underline transition-colors"
              >
                {t("webmail_next.always_trust_images")}
              </button>
            </div>
          </div>
        )}

        {/* Sandboxed Body Rendering — spans the pane width for HTML; the
            card chrome is gone so the body is not boxed into a strip. */}
        <div
          data-testid="mail-body-container"
          className="text-sm text-[#18181B] leading-relaxed min-h-[220px]"
        >
          {message.html ? (
            <SanitizedHtmlFrame
              title={message.subject || "Email Content"}
              html={
                useWorker
                  ? workerHtml
                  : sanitizeHtmlMail(message.html, {
                      allowRemoteImages: isImageAllowed,
                    })
              }
            />
          ) : (
            <pre className="whitespace-pre-wrap font-sans text-xs text-[#18181B] max-w-[65ch]">
              {message.text || "(Empty message)"}
            </pre>
          )}
        </div>

        {/* Attachments Section */}
        {message.attachments && message.attachments.length > 0 && (
          <div data-testid="attachments-container" className="pt-2">
            <div className="text-xs font-semibold text-[var(--vellum-ink-secondary)] mb-3">
              Attachments ({message.attachments.length})
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {message.attachments.map((att) => (
                <WebmailAttachmentCard
                  key={att.part_id}
                  attachment={att}
                  folderEnc={folderEnc}
                  uid={message.uid}
                />
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
