import React, { useState, useRef } from "react";
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
} from "lucide-react";
import { WebmailRecipientChips } from "./webmail-recipient-chips";
import { WebmailEditorTiptap } from "./webmail-editor-tiptap";
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
  initialSubject?: string;
}

export function WebmailComposeDock({
  isOpen,
  onClose,
  initialTo = [],
  initialSubject = "",
}: WebmailComposeDockProps) {
  const { t } = useTranslation();
  const [isMinimized, setIsMinimized] = useState(false);
  const [isMaximized, setIsMaximized] = useState(false);
  const [showCc, setShowCc] = useState(false);
  const [showBcc, setShowBcc] = useState(false);
  const [isPlainText, setIsPlainText] = useState(false);
  const [attachmentError, setAttachmentError] = useState<string | null>(null);

  // Undo Toast State
  const [undoToast, setUndoToast] = useState<{
    visible: boolean;
    countdown: number;
    requestKey?: string;
  }>({ visible: false, countdown: 10 });

  const undoTimerRef = useRef<NodeJS.Timeout | null>(null);

  const { draft, updateDraft, clearDraft, isSaving, lastSavedAt } =
    useDebouncedDraft({
      to: initialTo,
      subject: initialSubject,
    });

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

    // Start 10-second client-side buffer undo toast
    setUndoToast({ visible: true, countdown: 10, requestKey });

    let remaining = 10;
    undoTimerRef.current = setInterval(async () => {
      remaining -= 1;
      if (remaining <= 0) {
        if (undoTimerRef.current) clearInterval(undoTimerRef.current);
        setUndoToast({ visible: false, countdown: 0 });

        // Trigger real send API call
        try {
          await sendMessage(
            {
              to: draft.to,
              cc: draft.cc,
              bcc: draft.bcc,
              subject: draft.subject,
              text: draft.text,
              html: draft.html || undefined,
              attachments: draft.attachments,
            },
            requestKey,
          );
          clearDraft();
          onClose();
        } catch {
          // Send failure handling
        }
      } else {
        setUndoToast((prev) => ({ ...prev, countdown: remaining }));
      }
    }, 1000);
  };

  const handleUndo = () => {
    if (undoTimerRef.current) {
      clearInterval(undoTimerRef.current);
    }
    setUndoToast({ visible: false, countdown: 10 });
    // Keep draft intact for modification
  };

  return (
    <>
      {/* 1. Floating Docked Compose Window */}
      <div
        data-testid="webmail-compose-dock"
        className={`fixed z-50 transition-all duration-200 bg-white border border-[#EBE8E0] shadow-2xl flex flex-col ${
          isMaximized
            ? "inset-4 rounded-xl"
            : isMinimized
              ? "bottom-0 right-8 w-72 h-10 rounded-t-lg overflow-hidden"
              : "bottom-0 right-8 w-[580px] h-[640px] rounded-t-xl"
        }`}
      >
        {/* Dock Header */}
        <div className="h-10 px-4 bg-[#18181B] text-white flex items-center justify-between shrink-0 select-none rounded-t-lg">
          <span className="text-xs font-semibold truncate">
            {draft.subject || t("webmail_next.compose")}
          </span>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => setIsMinimized((p) => !p)}
              className="p-1 hover:bg-white/20 rounded text-white"
              title={isMinimized ? "Expand" : "Minimize"}
            >
              <Minus className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              onClick={() => setIsMaximized((p) => !p)}
              className="p-1 hover:bg-white/20 rounded text-white"
              title={isMaximized ? "Restore" : "Maximize"}
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
              title="Close"
            >
              <X className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        {/* Dock Body (Hidden when minimized) */}
        {!isMinimized && (
          <div className="flex-1 flex flex-col overflow-hidden bg-white">
            {/* Header Form: Recipient chips & Cc/Bcc */}
            <div className="px-4 pt-2 border-b border-[#EBE8E0] space-y-0.5 bg-white shrink-0">
              <div className="relative">
                <WebmailRecipientChips
                  label="To"
                  recipients={draft.to}
                  onChange={(to) => updateDraft({ to })}
                  testId="compose-to-input"
                />
                <div className="absolute right-0 top-1.5 flex items-center gap-2 text-xs text-[#71717A]">
                  {!showCc && (
                    <button
                      type="button"
                      onClick={() => setShowCc(true)}
                      className="hover:text-[#18181B]"
                    >
                      Cc
                    </button>
                  )}
                  {!showBcc && (
                    <button
                      type="button"
                      onClick={() => setShowBcc(true)}
                      className="hover:text-[#18181B]"
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
                  placeholder="Subject"
                  className="w-full text-xs text-[#18181B] font-medium outline-none py-1"
                />
              </div>
            </div>

            {/* Attachment Error Notice */}
            {attachmentError && (
              <div
                data-testid="attachment-error-notice"
                className="px-4 py-1.5 bg-[#FEE2E2] border-b border-[#FECACA] flex items-center gap-2 text-xs text-[#DC2626]"
              >
                <AlertCircle className="w-3.5 h-3.5 shrink-0" />
                <span>{attachmentError}</span>
              </div>
            )}

            {/* Attachments Pill List */}
            {draft.attachments && draft.attachments.length > 0 && (
              <div className="px-4 py-1.5 border-b border-[#EBE8E0] flex flex-wrap gap-1.5 max-h-24 overflow-y-auto bg-[#FAF9F5]">
                {draft.attachments.map((att, idx) => (
                  <div
                    key={att.filename + idx}
                    className="flex items-center gap-1 px-2 py-0.5 bg-white border border-[#EBE8E0] rounded text-xs text-[#18181B]"
                  >
                    <Paperclip className="w-3 h-3 text-[#71717A]" />
                    <span className="truncate max-w-[140px]">
                      {att.filename}
                    </span>
                    <button
                      type="button"
                      onClick={() => handleRemoveAttachment(idx)}
                      className="p-0.5 hover:text-[#DC2626]"
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
            />

            {/* Dock Bottom Footer Bar */}
            <div className="h-12 px-4 border-t border-[#EBE8E0] bg-[#FAF9F5] flex items-center justify-between shrink-0 select-none">
              <div className="flex items-center gap-3">
                <button
                  data-testid="compose-send-button"
                  type="button"
                  onClick={handleSend}
                  disabled={draft.to.length === 0 || Boolean(attachmentError)}
                  className="flex items-center gap-2 px-4 py-1.5 bg-[var(--brand-primary,#2563eb)] text-white text-xs font-semibold rounded-lg shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 disabled:pointer-events-none transition-all"
                >
                  <Send className="w-3.5 h-3.5" />
                  <span>{t("webmail_next.send")}</span>
                </button>

                <label
                  className="p-1.5 text-[#71717A] hover:text-[#18181B] hover:bg-[#EBE8E0] rounded cursor-pointer transition-colors"
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
              </div>

              <div className="flex items-center gap-3 text-xs text-[#A1A1AA]">
                <span data-testid="draft-status-indicator">
                  {isSaving
                    ? "Saving draft..."
                    : lastSavedAt
                      ? `${t("webmail_next.draft_saved")} ${lastSavedAt}`
                      : ""}
                </span>

                <button
                  type="button"
                  onClick={() => {
                    clearDraft();
                    onClose();
                  }}
                  className="p-1.5 text-[#71717A] hover:text-[#DC2626] hover:bg-[#EBE8E0] rounded transition-colors"
                  title="Discard Draft"
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
            </div>
          </div>
        )}
      </div>

      {/* 2. 10-Second Undo Toast */}
      {undoToast.visible && (
        <div
          data-testid="undo-send-toast"
          className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[100] px-4 py-2.5 bg-[#18181B] text-white rounded-lg shadow-2xl flex items-center gap-4 text-xs font-medium animate-in fade-in slide-in-from-bottom-2"
        >
          <span>
            {t("webmail_next.mail_sent")} ({undoToast.countdown}s)
          </span>
          <button
            data-testid="undo-send-button"
            type="button"
            onClick={handleUndo}
            className="flex items-center gap-1 px-2 py-0.5 bg-white/20 hover:bg-white/30 text-white rounded font-bold transition-colors"
          >
            <RotateCcw className="w-3 h-3" />
            <span>{t("webmail_next.undo_send")}</span>
          </button>
        </div>
      )}
    </>
  );
}
