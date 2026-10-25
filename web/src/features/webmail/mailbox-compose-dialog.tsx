import { FormEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { formatFrom, MessageDetail, sendMessage } from "./mailbox-api";

export type ComposeMode = "new" | "reply" | "forward";

export interface ComposeDraft {
  mode: ComposeMode;
  to?: string;
  cc?: string;
  subject?: string;
  text?: string;
}

interface MailboxComposeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  draft: ComposeDraft;
  onSent: () => void;
}

function splitAddrs(raw: string): string[] {
  return raw
    .split(/[,;\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export function draftFromMessage(
  mode: "reply" | "forward",
  msg: MessageDetail,
): ComposeDraft {
  const from = formatFrom(msg.from);
  if (mode === "reply") {
    const replyTo = msg.from?.[0]?.email ?? "";
    const subj = msg.subject?.startsWith("Re:")
      ? msg.subject
      : `Re: ${msg.subject || ""}`;
    const quoted = `\n\n---\n${from}\n\n${msg.text || ""}`;
    return { mode, to: replyTo, subject: subj, text: quoted };
  }
  const subj = msg.subject?.startsWith("Fwd:")
    ? msg.subject
    : `Fwd: ${msg.subject || ""}`;
  const body = `\n\n---------- Forwarded message ----------\nFrom: ${from}\nSubject: ${msg.subject || ""}\n\n${msg.text || ""}`;
  return { mode, to: "", subject: subj, text: body };
}

export function MailboxComposeDialog({
  open,
  onOpenChange,
  draft,
  onSent,
}: MailboxComposeDialogProps) {
  const { t } = useTranslation();
  const [to, setTo] = useState("");
  const [cc, setCc] = useState("");
  const [subject, setSubject] = useState("");
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    setTo(draft.to ?? "");
    setCc(draft.cc ?? "");
    setSubject(draft.subject ?? "");
    setText(draft.text ?? "");
    setError(null);
  }, [open, draft]);

  const titleKey =
    draft.mode === "reply"
      ? "page.webmail.compose_reply"
      : draft.mode === "forward"
        ? "page.webmail.compose_forward"
        : "page.webmail.compose_new";

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    const recipients = splitAddrs(to);
    if (recipients.length === 0) {
      setError(t("page.webmail.compose_need_to"));
      return;
    }
    setBusy(true);
    try {
      await sendMessage({
        to: recipients,
        cc: splitAddrs(cc),
        subject,
        text,
      });
      onOpenChange(false);
      onSent();
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.compose_failed"),
          }),
        );
      } else {
        setError(t("page.webmail.compose_failed"));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t(titleKey)}</DialogTitle>
        </DialogHeader>
        <form className="space-y-3" onSubmit={onSubmit}>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <div className="space-y-1.5">
            <label htmlFor="compose-to" className="text-sm font-medium">
              {t("page.webmail.compose_to")}
            </label>
            <Input
              id="compose-to"
              required
              value={to}
              onChange={(e) => setTo(e.target.value)}
              placeholder="a@example.com"
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-cc" className="text-sm font-medium">
              {t("page.webmail.compose_cc")}
            </label>
            <Input
              id="compose-cc"
              value={cc}
              onChange={(e) => setCc(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-subject" className="text-sm font-medium">
              {t("page.webmail.compose_subject")}
            </label>
            <Input
              id="compose-subject"
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-body" className="text-sm font-medium">
              {t("page.webmail.compose_body")}
            </label>
            <textarea
              id="compose-body"
              rows={12}
              className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={busy}
            >
              {t("action.cancel")}
            </Button>
            <Button type="submit" disabled={busy}>
              {busy ? t("state.loading") : t("page.webmail.compose_send")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
