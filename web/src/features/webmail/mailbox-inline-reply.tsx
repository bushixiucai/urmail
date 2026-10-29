import { useState, useRef } from "react";
import { useTranslation } from "react-i18next";
import { Reply, ReplyAll, Send, Maximize2, Paperclip, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { MailboxEditor } from "./mailbox-editor";
import {
  MessageDetail,
  OutgoingAttachment,
  saveDraft,
  sendMessage,
} from "./mailbox-api";
import {
  draftFromMessage,
  chooseMailboxIdentity,
} from "./mailbox-compose-dialog";
import { MailboxSettingsView, MailboxIdentity } from "./mailbox-api";

interface MailboxInlineReplyProps {
  message: MessageDetail;
  folderEnc: string;
  settings: MailboxSettingsView | null;
  identities: MailboxIdentity[];
  onOpenFullCompose: (mode: "reply" | "reply-all") => void;
  onSent: () => void;
  disabled?: boolean;
}

export function MailboxInlineReply({
  message,
  folderEnc,
  settings,
  identities,
  onOpenFullCompose,
  onSent,
  disabled = false,
}: MailboxInlineReplyProps) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [replyMode, setReplyMode] = useState<"reply" | "reply-all">("reply");
  const [text, setText] = useState("");
  const [html, setHtml] = useState("");
  const [attachments, setAttachments] = useState<OutgoingAttachment[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const initialDraft = draftFromMessage(replyMode, message);
  const recipientSummary = initialDraft.to || initialDraft.cc || "";

  const handleSend = async () => {
    if (!text.trim() && !html.trim() && attachments.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const draft = draftFromMessage(replyMode, message);
      const fromAddr = settings
        ? chooseMailboxIdentity(settings, identities, draft)
        : identities[0]?.address || message.to?.[0]?.email || "";
      const toAddrs = (draft.to ?? "").split(/[,;\s]+/).filter(Boolean);
      const ccAddrs = (draft.cc ?? "").split(/[,;\s]+/).filter(Boolean);
      const replySubject =
        draft.subject ?? (message.subject ? `Re: ${message.subject}` : "Re:");

      // Save draft first
      const saved = await saveDraft({
        from: fromAddr,
        to: toAddrs,
        cc: ccAddrs,
        subject: replySubject,
        in_reply_to: message.message_id,
        reply_source: {
          folder_enc: folderEnc,
          uid: message.uid,
          uid_validity: message.uid_validity ?? 0,
        },
        text: text,
        html: html || undefined,
        attachments: attachments.length ? attachments : undefined,
      });

      if (!saved.uid || !saved.uid_validity) {
        throw new Error("DRAFT_SAVE_FAILED");
      }

      // Send
      await sendMessage({
        from: fromAddr,
        to: toAddrs,
        cc: ccAddrs,
        subject: replySubject,
        in_reply_to: message.message_id,
        text: text,
        html: html || undefined,
        attachments: attachments.length ? attachments : undefined,
      });

      setText("");
      setHtml("");
      setAttachments([]);
      setExpanded(false);
      onSent();
    } catch {
      setError(
        t("page.webmail.compose_send_failed", {
          defaultValue: "发送失败，请重试",
        }),
      );
    } finally {
      setBusy(false);
    }
  };

  if (!expanded) {
    return (
      <div className="border border-border/80 rounded-xl bg-card p-3 shadow-2xs hover:border-primary/40 transition-colors">
        <div
          className="flex items-center justify-between gap-3 cursor-pointer py-1 text-muted-foreground hover:text-foreground"
          onClick={() => setExpanded(true)}
        >
          <div className="flex items-center gap-2 text-sm">
            <Reply className="h-4 w-4 text-primary" />
            <span className="font-medium">
              快捷回复给{" "}
              <span className="text-foreground">{recipientSummary}</span>...
            </span>
          </div>
          <div
            className="flex items-center gap-1.5"
            onClick={(e) => e.stopPropagation()}
          >
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 text-xs"
              onClick={() => {
                setReplyMode("reply");
                setExpanded(true);
              }}
            >
              回复
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 text-xs"
              onClick={() => {
                setReplyMode("reply-all");
                setExpanded(true);
              }}
            >
              全部回复
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="h-7 text-xs gap-1"
              onClick={() => onOpenFullCompose("reply")}
            >
              <Maximize2 className="h-3 w-3" />
              独立窗口
            </Button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="border border-border rounded-xl bg-card p-4 shadow-sm space-y-3">
      {/* Top Header */}
      <div className="flex items-center justify-between pb-2 border-b text-xs text-muted-foreground">
        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant={replyMode === "reply" ? "secondary" : "ghost"}
            className="h-6 px-2 text-xs"
            onClick={() => setReplyMode("reply")}
          >
            <Reply className="h-3 w-3 mr-1" />
            回复
          </Button>
          <Button
            type="button"
            size="sm"
            variant={replyMode === "reply-all" ? "secondary" : "ghost"}
            className="h-6 px-2 text-xs"
            onClick={() => setReplyMode("reply-all")}
          >
            <ReplyAll className="h-3 w-3 mr-1" />
            全部回复
          </Button>
          <span className="truncate max-w-xs text-foreground/80 font-medium">
            → {recipientSummary}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-6 px-1.5 text-xs text-muted-foreground hover:text-foreground"
            title="放大到独立写信窗口"
            onClick={() => onOpenFullCompose(replyMode)}
          >
            <Maximize2 className="h-3.5 w-3.5" />
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-6 px-1.5 text-xs text-muted-foreground hover:text-destructive"
            onClick={() => setExpanded(false)}
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>

      {error && (
        <p className="text-xs text-destructive bg-destructive/10 px-2.5 py-1.5 rounded-md">
          {error}
        </p>
      )}

      {/* Editor */}
      <MailboxEditor
        initialContent={html || text}
        placeholder="输入回复内容 (支持富文本、快捷键与 Markdown)..."
        disabled={disabled || busy}
        onChange={({ html: newHtml, text: newText }) => {
          setHtml(newHtml);
          setText(newText);
        }}
      />

      {/* Attachments preview */}
      {attachments.length > 0 && (
        <div className="flex flex-wrap gap-1.5 pt-1">
          {attachments.map((att, idx) => (
            <span
              key={idx}
              className="inline-flex items-center gap-1.5 text-xs bg-muted px-2 py-1 rounded border text-muted-foreground"
            >
              <Paperclip className="h-3 w-3 text-primary" />
              <span className="max-w-[140px] truncate">{att.filename}</span>
              <button
                type="button"
                className="hover:text-destructive"
                onClick={() =>
                  setAttachments((prev) => prev.filter((_, i) => i !== idx))
                }
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      )}

      {/* Bottom Actions */}
      <div className="flex items-center justify-between pt-1">
        <div className="flex items-center gap-2">
          <input
            ref={fileInputRef}
            type="file"
            multiple
            className="hidden"
            onChange={(e) => {
              const files = Array.from(e.target.files || []);
              for (const file of files) {
                const reader = new FileReader();
                reader.onload = () => {
                  const b64 = (reader.result as string).split(",")[1];
                  setAttachments((prev) => [
                    ...prev,
                    {
                      filename: file.name,
                      content_type: file.type || "application/octet-stream",
                      content_base64: b64,
                    },
                  ]);
                };
                reader.readAsDataURL(file);
              }
            }}
          />
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-8 text-xs gap-1"
            disabled={disabled || busy}
            onClick={() => fileInputRef.current?.click()}
          >
            <Paperclip className="h-3.5 w-3.5" />
            添加附件
          </Button>
        </div>

        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-8 text-xs"
            onClick={() => setExpanded(false)}
            disabled={busy}
          >
            取消
          </Button>
          <Button
            type="button"
            size="sm"
            variant="default"
            className="h-8 text-xs gap-1 font-medium shadow-xs"
            disabled={
              disabled ||
              busy ||
              (!text.trim() && !html.trim() && attachments.length === 0)
            }
            onClick={handleSend}
          >
            <Send className="h-3.5 w-3.5" />
            {busy ? "发送中..." : "发送"}
          </Button>
        </div>
      </div>
    </div>
  );
}
