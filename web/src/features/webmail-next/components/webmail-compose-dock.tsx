import React, { useState, useRef, useEffect, useLayoutEffect } from "react";
import { useTranslation } from "react-i18next";
import {
  Minus,
  Maximize2,
  Minimize2,
  X,
  Send,
  Paperclip,
  Trash2,
  AlertCircle,
  RotateCcw,
  Sparkles,
} from "lucide-react";
import { WebmailRecipientChips } from "./webmail-recipient-chips";
import { WebmailEditorTiptap } from "./webmail-editor-tiptap";
import { WebmailComposeAICompanion } from "./ai/webmail-compose-ai-companion";
import { useDebouncedDraft } from "../hooks/use-debounced-draft";
import {
  sendMessage,
  MAX_SEND_ATTACHMENT_BYTES,
  MAX_SEND_TOTAL_ATTACHMENT_BYTES,
  type OutgoingAttachment,
} from "@/features/webmail/mailbox-api";

interface WebmailComposeDockProps {
  isOpen: boolean;
  onClose: () => void;
  initialTo?: string[];
  initialCc?: string[];
  initialSubject?: string;
  initialHtml?: string;
  initialText?: string;
  /** Original Message-ID for reply/forward; sent as in_reply_to. */
  inReplyTo?: string;
  sendNonce?: number;
  minimizeNonce?: number;
  onOpenAISettings?: () => void;
}

export function WebmailComposeDock({
  isOpen,
  onClose,
  initialTo = [],
  initialCc = [],
  initialSubject = "",
  initialHtml = "",
  initialText = "",
  inReplyTo,
  sendNonce = 0,
  minimizeNonce = 0,
  onOpenAISettings,
}: WebmailComposeDockProps) {
  const { t } = useTranslation();
  const [isMinimized, setIsMinimized] = useState(false);
  const [isMaximized, setIsMaximized] = useState(false);
  const [showCc, setShowCc] = useState(false);
  const [showBcc, setShowBcc] = useState(false);
  const [isPlainText, setIsPlainText] = useState(false);
  const [attachmentError, setAttachmentError] = useState<string | null>(null);
  const [isAICompanionOpen, setIsAICompanionOpen] = useState(false);

  // Undo Toast State
  type SendPhase = "sending" | "sent" | "failed";
  const [undoToast, setUndoToast] = useState<{
    visible: boolean;
    countdown: number;
    phase: SendPhase;
    requestKey?: string;
  }>({ visible: false, countdown: 10, phase: "sending" });

  const undoTimerRef = useRef<NodeJS.Timeout | null>(null);

  const initialDraftProp =
    initialTo.length > 0 ||
    initialCc.length > 0 ||
    Boolean(initialSubject) ||
    Boolean(initialHtml) ||
    Boolean(initialText)
      ? {
          to: initialTo,
          cc: initialCc,
          subject: initialSubject,
          html: initialHtml,
          text: initialText,
          in_reply_to: inReplyTo,
        }
      : undefined;

  const {
    draft,
    updateDraft,
    clearDraft,
    isSaving,
    lastSavedAt,
    lastSaveFailed,
    retrySave,
    restoredFromStash,
  } = useDebouncedDraft(initialDraftProp);
  const sendAction = useRef<() => void>(() => {});
  const seenSend = useRef(0);
  const seenMinimize = useRef(0);

  // REVIEW-UX-002: a composer restored from the local stash represents the
  // user's only copy — push one save attempt up immediately so the draft
  // reaches the server as soon as the endpoint answers, without waiting for
  // the next keystroke.
  const didRestoreFlush = useRef(false);
  useEffect(() => {
    if (!isOpen || didRestoreFlush.current) return;
    if (initialDraftProp || !restoredFromStash) return;
    didRestoreFlush.current = true;
    void retrySave();
    // Runs once per dock instance (remounted per open via composeKey).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen || !sendNonce || sendNonce === seenSend.current) return;
    seenSend.current = sendNonce;
    sendAction.current();
  }, [sendNonce, isOpen]);

  useEffect(() => {
    if (!isOpen || !minimizeNonce || minimizeNonce === seenMinimize.current) {
      return;
    }
    seenMinimize.current = minimizeNonce;
    setIsMinimized(true);
  }, [minimizeNonce, isOpen]);

  const openerRef = useRef<HTMLElement | null>(null);
  const openerRecordedRef = useRef(false);
  useLayoutEffect(() => {
    if (isOpen) {
      if (!openerRecordedRef.current) {
        const opener = document.activeElement;
        openerRef.current =
          opener instanceof HTMLElement && opener !== document.body
            ? opener
            : null;
        openerRecordedRef.current = true;
      }
      return;
    }
    if (!openerRecordedRef.current) return;
    openerRecordedRef.current = false;
    const opener = openerRef.current;
    openerRef.current = null;
    if (opener && document.contains(opener)) {
      opener.focus();
      return;
    }
    document
      .querySelector<HTMLElement>('[data-testid="webmail-compose-button"]')
      ?.focus();
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [isOpen, onClose]);

  useEffect(() => {
    if (!isOpen || isMinimized) return;
    const frame = requestAnimationFrame(() => {
      document
        // REVIEW-UIUX-016 ①: target the dock's actual To field test id — the
        // generic "recipient-input" id was renamed per-field, so focus never
        // moved into the panel on open.
        .querySelector<HTMLInputElement>('[data-testid="compose-to-input"]')
        ?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [isOpen, isMinimized]);

  if (!isOpen) return null;

  const handleFileUpload = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files || files.length === 0) return;

    setAttachmentError(null);
    const newAttachments: OutgoingAttachment[] = [...(draft.attachments || [])];

    for (let i = 0; i < files.length; i++) {
      const file = files[i];

      // Single file check <= 5MB
      if (file.size > MAX_SEND_ATTACHMENT_BYTES) {
        setAttachmentError(t("webmail_next.attachment_too_large"));
        return;
      }

      // Read file as base64
      const reader = new FileReader();
      reader.onload = () => {
        const base64 = (reader.result as string).split(",")[1];
        newAttachments.push({
          filename: file.name,
          content_type: file.type || "application/octet-stream",
          content_base64: base64,
        });

        // Total size check <= 10MB
        const totalSize = newAttachments.reduce(
          (sum, a) => sum + (a.content_base64.length * 3) / 4,
          0,
        );
        if (totalSize > MAX_SEND_TOTAL_ATTACHMENT_BYTES) {
          setAttachmentError(t("webmail_next.total_attachments_too_large"));
          return;
        }

        updateDraft({ attachments: newAttachments });
      };
      reader.readAsDataURL(file);
    }
  };

  const handleRemoveAttachment = (idx: number) => {
    const updated = (draft.attachments || []).filter((_, i) => i !== idx);
    updateDraft({ attachments: updated });
    setAttachmentError(null);
  };

  const handleSend = async () => {
    if (draft.to.length === 0) return;

    const requestKey = crypto.randomUUID();

    // Strict terminal-state toast (global-review 追加①): the "sent" surface
    // only appears after POST /mailbox/send resolves successfully. The
    // countdown window below is a cancel window BEFORE the send fires; undo
    // during it cancels the send entirely (Undo semantics preserved).
    setUndoToast({
      visible: true,
      countdown: 10,
      phase: "sending",
      requestKey,
    });

    let remaining = 10;
    const finishSend = async () => {
      if (undoTimerRef.current) {
        clearInterval(undoTimerRef.current);
        undoTimerRef.current = null;
      }
      setUndoToast((prev) => ({ ...prev, countdown: 0, phase: "sending" }));
      try {
        await sendMessage(
          {
            to: draft.to,
            cc: draft.cc,
            bcc: draft.bcc,
            subject: draft.subject,
            // Reply/forward flows pass the original Message-ID so the SMTP
            // layer writes In-Reply-To/References and the reply threads.
            // Resumed drafts carry their own original Message-ID; fall back
            // to the prop for a dock that was opened for this reply.
            in_reply_to: inReplyTo || draft.in_reply_to || undefined,
            text: draft.text,
            html: draft.html || undefined,
            attachments: draft.attachments,
          },
          requestKey,
        );
        setUndoToast({ visible: true, countdown: 0, phase: "sent" });
        setTimeout(() => {
          setUndoToast((prev) =>
            prev.phase === "sent" ? { ...prev, visible: false } : prev,
          );
        }, 3000);
        clearDraft();
        onClose();
      } catch {
        // Send failed: the toast must not claim success. Keep the draft and
        // the dock open, surface the failure with an explicit retry entry.
        setUndoToast({ visible: true, countdown: 0, phase: "failed" });
      }
    };

    undoTimerRef.current = setInterval(() => {
      remaining -= 1;
      if (remaining <= 0) {
        void finishSend();
      } else {
        setUndoToast((prev) => ({ ...prev, countdown: remaining }));
      }
    }, 1000);
  };
  sendAction.current = () => {
    void handleSend();
  };

  const handleUndo = () => {
    if (undoTimerRef.current) {
      clearInterval(undoTimerRef.current);
    }
    setUndoToast({ visible: false, countdown: 10, phase: "sending" });
    // Keep draft intact for modification
  };

  return (
    <>
      {/* 1. Floating Docked Compose Window */}
      <div
        data-testid="webmail-compose-dock"
        data-compose-dock="true"
        data-minimized={isMinimized ? "true" : "false"}
        className={`fixed z-50 transition-all duration-200 bg-white border border-[var(--vellum-border)] shadow-2xl flex flex-col ${
          isMaximized
            ? "inset-4 rounded-xl"
            : isMinimized
              ? "bottom-0 right-8 w-72 h-10 rounded-t-lg overflow-hidden"
              : `bottom-0 right-8 ${isAICompanionOpen ? "w-[880px]" : "w-[580px]"} h-[640px] rounded-t-xl`
        }`}
      >
        {/* Dock Header */}
        <div className="h-10 px-4 bg-[var(--vellum-ink)] text-white flex items-center justify-between shrink-0 select-none rounded-t-lg">
          <span className="text-xs font-semibold truncate">
            {draft.subject || t("webmail_next.compose")}
          </span>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => setIsMinimized((p) => !p)}
              className="p-1 hover:bg-white/20 rounded text-white"
              aria-label={
                isMinimized
                  ? t("webmail_next.expand")
                  : t("webmail_next.minimize")
              }
              title={
                isMinimized
                  ? t("webmail_next.expand")
                  : t("webmail_next.minimize")
              }
            >
              <Minus className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              onClick={() => setIsMaximized((p) => !p)}
              className="p-1 hover:bg-white/20 rounded text-white"
              aria-label={
                isMaximized
                  ? t("webmail_next.restore")
                  : t("webmail_next.maximize")
              }
              title={
                isMaximized
                  ? t("webmail_next.restore")
                  : t("webmail_next.maximize")
              }
            >
              {isMaximized ? (
                <Minimize2 className="w-3.5 h-3.5" />
              ) : (
                <Maximize2 className="w-3.5 h-3.5" />
              )}
            </button>
            <button
              data-testid="compose-close-button"
              type="button"
              onClick={onClose}
              className="p-1 hover:bg-white/20 rounded text-white"
              aria-label={t("webmail_next.close")}
              title={t("webmail_next.close")}
            >
              <X className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        {/* Dock Body (Hidden when minimized) */}
        {!isMinimized && (
          <div className="flex-1 flex overflow-hidden bg-white">
            {/* Left Column: Editor & Form Controls */}
            <div className="flex-1 flex flex-col min-w-0 overflow-hidden bg-white">
              {/* Header Form: Recipient chips & Cc/Bcc */}
              <div className="px-4 pt-2 border-b border-[var(--vellum-border)] space-y-0.5 bg-white shrink-0">
                <div className="relative">
                  <WebmailRecipientChips
                    label="To"
                    recipients={draft.to}
                    onChange={(to) => updateDraft({ to })}
                    testId="compose-to-input"
                  />
                  <div className="absolute right-0 top-1.5 flex items-center gap-2 text-xs text-[var(--vellum-ink-secondary)]">
                    {!showCc && (
                      <button
                        type="button"
                        onClick={() => setShowCc(true)}
                        className="hover:text-[var(--vellum-ink)]"
                      >
                        Cc
                      </button>
                    )}
                    {!showBcc && (
                      <button
                        type="button"
                        onClick={() => setShowBcc(true)}
                        className="hover:text-[var(--vellum-ink)]"
                      >
                        Bcc
                      </button>
                    )}
                  </div>
                </div>

                {showCc && (
                  <WebmailRecipientChips
                    label="Cc"
                    recipients={draft.cc || []}
                    onChange={(cc) => updateDraft({ cc })}
                    testId="compose-cc-input"
                  />
                )}

                {showBcc && (
                  <WebmailRecipientChips
                    label="Bcc"
                    recipients={draft.bcc || []}
                    onChange={(bcc) => updateDraft({ bcc })}
                    testId="compose-bcc-input"
                  />
                )}

                <div className="flex items-center py-1">
                  <input
                    data-testid="compose-subject-input"
                    type="text"
                    value={draft.subject}
                    onChange={(e) => updateDraft({ subject: e.target.value })}
                    placeholder={t("compose.subject_placeholder")}
                    className="w-full text-xs text-[var(--vellum-ink)] font-medium outline-none py-1"
                  />
                </div>
              </div>

              {/* Attachment Error Notice */}
              {attachmentError && (
                <div
                  data-testid="attachment-error-notice"
                  className="px-4 py-1.5 bg-[var(--vellum-danger-fill)] border-b border-[var(--vellum-danger-border)] flex items-center gap-2 text-xs text-[var(--vellum-danger-strong)]"
                >
                  <AlertCircle className="w-3.5 h-3.5 shrink-0" />
                  <span>{attachmentError}</span>
                </div>
              )}

              {/* Attachments Pill List */}
              {draft.attachments && draft.attachments.length > 0 && (
                <div className="px-4 py-1.5 border-b border-[var(--vellum-border)] flex flex-wrap gap-1.5 max-h-24 overflow-y-auto bg-[var(--vellum-ground)]">
                  {draft.attachments.map((att, idx) => (
                    <div
                      key={att.filename + idx}
                      className="flex items-center gap-1 px-2 py-0.5 bg-white border border-[var(--vellum-border)] rounded text-xs text-[var(--vellum-ink)]"
                    >
                      <Paperclip className="w-3 h-3 text-[var(--vellum-ink-secondary)]" />
                      <span className="truncate max-w-[140px]">
                        {att.filename}
                      </span>
                      <button
                        type="button"
                        onClick={() => handleRemoveAttachment(idx)}
                        className="p-0.5 hover:text-[var(--vellum-danger-strong)]"
                      >
                        <X className="w-3 h-3" />
                      </button>
                    </div>
                  ))}
                </div>
              )}

              {/* TipTap Rich Text Editor */}
              <WebmailEditorTiptap
                content={draft.html || draft.text}
                isPlainText={isPlainText}
                onChange={(html, text) => updateDraft({ html, text })}
                onTogglePlainText={() => setIsPlainText((p) => !p)}
                onOpenAISettings={onOpenAISettings}
              />

              {/* Dock Bottom Footer Bar */}
              <div className="h-12 px-4 border-t border-[var(--vellum-border)] bg-[var(--vellum-ground)] flex items-center justify-between shrink-0 select-none">
                <div className="flex items-center gap-2">
                  <button
                    data-testid="compose-send-button"
                    type="button"
                    onClick={handleSend}
                    disabled={
                      draft.to.length === 0 ||
                      Boolean(attachmentError) ||
                      (undoToast.visible && undoToast.phase === "sending")
                    }
                    className="flex items-center gap-2 px-4 py-1.5 bg-[var(--vellum-cta)] text-[var(--vellum-cta-foreground)] text-xs font-semibold rounded-lg shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 disabled:pointer-events-none transition-all"
                  >
                    <Send className="w-3.5 h-3.5" />
                    <span>{t("webmail_next.send")}</span>
                  </button>

                  <label
                    className="p-1.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] hover:bg-[var(--vellum-border)] rounded cursor-pointer transition-colors"
                    title="Attach file (max 5MB single, 10MB total)"
                  >
                    <Paperclip className="w-4 h-4" />
                    <input
                      data-testid="compose-file-input"
                      type="file"
                      multiple
                      onChange={handleFileUpload}
                      className="hidden"
                    />
                  </label>

                  {/* AI Writing Assistant Toggle Button */}
                  <button
                    data-testid="compose-ai-button"
                    type="button"
                    onClick={() => setIsAICompanionOpen((p) => !p)}
                    className={`flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs font-medium transition-colors ${
                      isAICompanionOpen
                        ? "bg-[var(--vellum-ai-accent-soft)] text-[var(--vellum-ai-accent-strong)]"
                        : "text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] hover:bg-[var(--vellum-border)]"
                    }`}
                    title={t("webmail_next.ai_companion_toggle")}
                  >
                    <Sparkles className="w-3.5 h-3.5 text-[var(--vellum-ai-accent)]" />
                    <span>{t("webmail_next.ai_companion_btn")}</span>
                  </button>
                </div>

                <div className="flex items-center gap-3 text-xs text-[var(--vellum-ink-secondary)]">
                  {isSaving ? (
                    <span data-testid="draft-status-indicator">
                      Saving draft...
                    </span>
                  ) : lastSaveFailed ? (
                    <span
                      data-testid="draft-save-error-notice"
                      role="status"
                      className="flex items-center gap-2 text-[var(--vellum-danger-strong)]"
                    >
                      {t("webmail_next.draft_save_failed")}
                      <button
                        type="button"
                        data-testid="draft-save-retry"
                        onClick={() => void retrySave()}
                        className="underline underline-offset-2 font-medium hover:opacity-80"
                      >
                        {t("webmail_next.draft_retry_save")}
                      </button>
                    </span>
                  ) : (
                    <span data-testid="draft-status-indicator">
                      {lastSavedAt
                        ? `${t("webmail_next.draft_saved")} ${lastSavedAt}`
                        : !draft.uid &&
                            (draft.subject.trim() ||
                              draft.text.trim() ||
                              draft.to.length > 0)
                          ? t("webmail_next.draft_local_only")
                          : ""}
                    </span>
                  )}

                  <button
                    type="button"
                    onClick={() => {
                      clearDraft();
                      onClose();
                    }}
                    className="p-1.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-danger-strong)] hover:bg-[var(--vellum-border)] rounded transition-colors"
                    title="Discard Draft"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              </div>
            </div>

            {/* Right Column: Side-by-Side AI Companion Panel */}
            {isAICompanionOpen && (
              <WebmailComposeAICompanion
                currentSubject={draft.subject}
                currentBody={draft.text || draft.html || ""}
                isReply={draft.subject.startsWith("Re:")}
                onRollback={(ver) => {
                  updateDraft({
                    subject: ver.subject,
                    text: ver.body,
                    html: ver.body,
                  });
                }}
                onApply={(ver) => {
                  updateDraft({
                    subject: ver.subject,
                    text: ver.body,
                    html: ver.body,
                  });
                }}
                onClose={() => setIsAICompanionOpen(false)}
                onOpenAISettings={onOpenAISettings}
              />
            )}
          </div>
        )}
      </div>

      {/* 2. Send Toast — strictly reflects the send API terminal state:
          countdown/cancel window while "sending", success only after the
          POST resolves, failure keeps the draft with an explicit retry. */}
      {undoToast.visible && (
        <div
          data-testid="undo-send-toast"
          className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[100] px-4 py-2 bg-[var(--vellum-ink)] text-white rounded-lg shadow-2xl flex items-center gap-4 text-xs font-medium animate-in fade-in slide-in-from-bottom-2"
        >
          {undoToast.phase === "failed" ? (
            <span
              data-testid="send-failed-toast"
              role="status"
              className="text-[var(--vellum-danger-strong)]"
            >
              {t("webmail_next.send_failed")}
            </span>
          ) : undoToast.phase === "sent" ? (
            <span data-testid="send-sent-toast">
              {t("webmail_next.mail_sent")}
            </span>
          ) : (
            <span data-testid="send-pending-toast">
              {t("webmail_next.send_pending")} ({undoToast.countdown}s)
            </span>
          )}
          {undoToast.phase === "sending" && (
            <button
              data-testid="undo-send-button"
              type="button"
              onClick={handleUndo}
              className="flex items-center gap-1 px-2 py-0.5 bg-white/20 hover:bg-white/30 text-white rounded font-bold transition-colors"
            >
              <RotateCcw className="w-3 h-3" />
              <span>{t("webmail_next.undo_send")}</span>
            </button>
          )}
          {undoToast.phase === "failed" && (
            <button
              data-testid="send-retry-button"
              type="button"
              onClick={() => void handleSend()}
              className="flex items-center gap-1 px-2 py-0.5 bg-white/20 hover:bg-white/30 text-white rounded font-bold transition-colors"
            >
              <RotateCcw className="w-3 h-3" />
              <span>{t("webmail_next.send_retry")}</span>
            </button>
          )}
        </div>
      )}
    </>
  );
}
