import { useState, useEffect } from "react";
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
} from "@/features/webmail/mailbox-api";

interface WebmailReadingPaneProps {
  message: MessageDetail | null;
  folderEnc: string;
  isLoading?: boolean;
  isGlobalRemoteImagesAllowed?: boolean;
  isManualRemoteImagesAllowed?: boolean;
  onBack?: () => void;
  onReply?: (msg: MessageDetail) => void;
  onReplyAll?: (msg: MessageDetail) => void;
  onForward?: (msg: MessageDetail) => void;
  onDelete?: (uid: number) => void;
  onToggleStar?: (uid: number) => void;
  onLoadRemoteImages?: (uid: number) => void;
  onAlwaysTrustSender?: (senderEmail: string) => void;
}

function SanitizedHtmlFrame({
  title,
  html,
}: {
  title: string;
  html: string | null;
}) {
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
      data-testid="mail-html-frame"
      title={title}
      sandbox=""
      referrerPolicy="no-referrer"
      className="w-full min-h-[420px] rounded border-0 bg-transparent"
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
  onBack,
  onReply,
  onReplyAll,
  onForward,
  onDelete,
  onToggleStar,
  onLoadRemoteImages,
  onAlwaysTrustSender,
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
              title="Back to list"
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
            title="Star"
          >
            <Star
              className={`w-4 h-4 ${isStarred ? "fill-[var(--vellum-star)]" : ""}`}
            />
          </button>
          <button
            type="button"
            onClick={() => onDelete?.(message.uid)}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:text-[#DC2626] hover:bg-[#F3F1EC] transition-colors"
            title="Delete"
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* 2. Message Detail Content */}
      <div className="p-6 max-w-4xl w-full mx-auto space-y-5">
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
                    title={contactAdded ? "Contact added" : "Add to contacts"}
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

        {/* Sandboxed Body Rendering */}
        <div
          data-testid="mail-body-container"
          className="bg-white p-6 rounded-xl border border-[#EBE8E0] shadow-sm text-sm text-[#18181B] leading-relaxed min-h-[220px]"
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
            <pre className="whitespace-pre-wrap font-sans text-xs text-[#18181B]">
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
