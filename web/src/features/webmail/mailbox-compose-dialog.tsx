import { FormEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import type { MailboxAISource } from "./mailbox-api";
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
import {
  attachmentTotalBytes,
  deleteDraft,
  fileToOutgoingAttachment,
  formatFrom,
  MAX_SEND_ATTACHMENT_BYTES,
  MAX_SEND_TOTAL_ATTACHMENT_BYTES,
  MAX_SEND_ATTACHMENTS,
  MessageDetail,
  OutgoingAttachment,
  getMailboxAddress,
  getMailboxSettings,
  listMailboxIdentities,
  MailboxSettingsView,
  MailboxIdentity,
  saveDraft,
  scheduleSend,
  ScheduleSendRequest,
  sendMessage,
} from "./mailbox-api";

export type ComposeMode = "new" | "reply" | "reply-all" | "forward" | "draft";

export interface ComposeDraft {
  from?: string;
  replyRecipients?: string[];
  quoteText?: string;
  aiSource?: MailboxAISource;
  inReplyTo?: string;
  mode: ComposeMode;
  /** IMAP UID when editing a saved draft (replace on next save). */
  uid?: number;
  uidValidity?: number;
  to?: string;
  cc?: string;
  bcc?: string;
  html?: string;
  subject?: string;
  text?: string;
  attachments?: OutgoingAttachment[];
}

interface MailboxComposeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  draft: ComposeDraft;
  onSent: () => void;
  /** Called after a successful draft save (uid may change after replace). */
  onDraftSaved?: (uid: number) => void;
}

function splitAddrs(raw: string): string[] {
  return raw
    .split(/[,;\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function formatAddrList(
  addrs: { name?: string; email: string }[] | undefined,
): string {
  if (!addrs?.length) return "";
  return addrs
    .map((a) => a.email)
    .filter(Boolean)
    .join(", ");
}

function ownAddresses(): Set<string> {
  const own = new Set<string>();
  const address = getMailboxAddress();
  if (address) own.add(address.toLowerCase());
  return own;
}

function collectRecipients(
  groups: ({ email: string }[] | undefined)[],
  own: Set<string>,
  already: Set<string>,
): string[] {
  const out: string[] = [];
  for (const group of groups) {
    for (const addr of group ?? []) {
      const email = addr.email?.trim();
      if (!email) continue;
      const key = email.toLowerCase();
      if (own.has(key) || already.has(key)) continue;
      already.add(key);
      out.push(email);
    }
  }
  return out;
}

function originalQuoteBody(msg: MessageDetail): string {
  if (msg.text) return msg.text;
  const template = document.createElement("template");
  template.innerHTML = msg.html ?? "";
  template.content
    .querySelectorAll("script,style,noscript,template")
    .forEach((node) => node.remove());
  template.content
    .querySelectorAll("br")
    .forEach((node) => node.replaceWith("\n"));
  template.content
    .querySelectorAll("p,div,li")
    .forEach((node) => node.append(document.createTextNode("\n")));
  return template.content.textContent?.trim() ?? "";
}

export function draftFromMessage(
  mode: "reply" | "reply-all" | "forward",
  msg: MessageDetail,
): ComposeDraft {
  const from = formatFrom(msg.from);
  const own = ownAddresses();
  if (mode === "reply" || mode === "reply-all") {
    const authors = msg.reply_to?.length ? msg.reply_to : msg.from;
    const seen = new Set<string>();
    const authorList = collectRecipients([authors], own, seen);
    let toList = authorList;
    if (mode === "reply-all" && authorList.length === 0) {
      toList = collectRecipients([msg.to], own, seen);
    }
    const ccList =
      mode === "reply-all"
        ? collectRecipients([msg.to, msg.cc], own, seen)
        : [];
    const to = toList.join(", ");
    const cc = ccList.join(", ");
    const subj = msg.subject?.startsWith("Re:")
      ? msg.subject
      : `Re: ${msg.subject || ""}`;
    const quoted = `\n\n---\n${from}\n\n${originalQuoteBody(msg)}`;
    return {
      mode,
      to,
      cc,
      subject: subj,
      text: quoted,
      quoteText: quoted,
      inReplyTo: msg.message_id,
      replyRecipients: [
        ...(msg.to ?? []),
        ...(msg.cc ?? []),
        ...(msg.from ?? []),
      ].map((value) => value.email),
    };
  }
  const subj = msg.subject?.startsWith("Fwd:")
    ? msg.subject
    : `Fwd: ${msg.subject || ""}`;
  const body = `\n\n---------- Forwarded message ----------\nFrom: ${from}\nSubject: ${msg.subject || ""}\n\n${originalQuoteBody(msg)}`;
  return { mode, to: "", subject: subj, text: body, quoteText: body };
}

/** Resume an IMAP draft into the compose dialog. */
export function composeFromSavedDraft(
  msg: MessageDetail,
  attachments: OutgoingAttachment[] = [],
): ComposeDraft {
  return {
    mode: "draft",
    from: msg.from?.[0]?.email,
    inReplyTo: msg.in_reply_to,
    uid: msg.uid,
    uidValidity: msg.uid_validity,
    to: formatAddrList(msg.to),
    cc: formatAddrList(msg.cc),
    bcc: formatAddrList(msg.bcc),
    html: msg.html ?? "",
    subject: msg.subject ?? "",
    text: msg.text ?? "",
    attachments,
  };
}

export function chooseMailboxIdentity(
  settings: MailboxSettingsView,
  identities: MailboxIdentity[],
  draft: ComposeDraft,
): string {
  const allowed = new Set(
    identities.map((value) => value.address.toLowerCase()),
  );
  if (draft.from) return draft.from;
  const fallback =
    typeof settings.preferences.default_from === "string" &&
    allowed.has(settings.preferences.default_from)
      ? settings.preferences.default_from
      : settings.address;
  if (
    (draft.mode === "reply" || draft.mode === "reply-all") &&
    settings.preferences.reply_identity !== "default"
  ) {
    const match = draft.replyRecipients?.find((value) =>
      allowed.has(value.toLowerCase()),
    );
    if (match) return match.toLowerCase();
  }
  return fallback;
}

export function MailboxComposeDialog({
  open,
  onOpenChange,
  draft,
  onSent,
  onDraftSaved,
}: MailboxComposeDialogProps) {
  const { t } = useTranslation();
  const [from, setFrom] = useState("");
  const [quoteOriginal, setQuoteOriginal] = useState(true);
  const [identityChoices, setIdentityChoices] = useState<MailboxIdentity[]>([]);
  const [identitySettings, setIdentitySettings] =
    useState<MailboxSettingsView | null>(null);
  const [identitiesLoaded, setIdentitiesLoaded] = useState(false);
  const bodyRevision = useRef(0);
  const recipientRevision = useRef(0);
  const insertedSignature = useRef({ text: "", html: "" });
  const automaticHTMLSignature = useRef("");
  const [to, setTo] = useState("");
  const [cc, setCc] = useState("");
  const [bcc, setBcc] = useState("");
  const [html, setHtml] = useState("");
  const [subject, setSubject] = useState("");
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<OutgoingAttachment[]>([]);
  const [draftUid, setDraftUid] = useState<number | undefined>();
  const [draftUidValidity, setDraftUidValidity] = useState<
    number | undefined
  >();
  const [draftUncertain, setDraftUncertain] = useState(false);
  const [sendUncertain, setSendUncertain] = useState(false);
  const [sentCopyPending, setSentCopyPending] = useState(false);
  const [cleanupPending, setCleanupPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [savedHint, setSavedHint] = useState(false);
  const [sendLaterAt, setSendLaterAt] = useState("");
  const [scheduledHint, setScheduledHint] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const scheduleAttempt = useRef<{
    key: string;
    body: ScheduleSendRequest;
  } | null>(null);
  const [scheduleUncertain, setScheduleUncertain] = useState(false);
  const editingLocked =
    busy || scheduleUncertain || draftUncertain || sendUncertain;

  useEffect(() => {
    if (!open) return;
    setTo(draft.to ?? "");
    setQuoteOriginal(!!draft.quoteText);
    setCc(draft.cc ?? "");
    setBcc(draft.bcc ?? "");
    setHtml(draft.html ?? "");
    setSubject(draft.subject ?? "");
    setText(draft.text ?? "");
    setAttachments(draft.attachments ?? []);
    setDraftUid(draft.uid);
    setDraftUidValidity(draft.uidValidity);
    setDraftUncertain(false);
    setSendUncertain(false);
    setCleanupPending(false);
    setError(null);
    setSavedHint(false);
    setSendLaterAt("");
    setScheduledHint(false);
    setScheduleUncertain(false);
    scheduleAttempt.current = null;
    if (fileInputRef.current) fileInputRef.current.value = "";
  }, [open, draft]);

  useEffect(() => {
    if (!open) return;
    let active = true;
    const revision = bodyRevision.current;
    const recipientSnapshot = recipientRevision.current;
    setIdentitiesLoaded(false);
    setFrom(draft.from ?? getMailboxAddress() ?? "");
    insertedSignature.current = { text: "", html: "" };
    automaticHTMLSignature.current = "";
    void Promise.all([getMailboxSettings(), listMailboxIdentities()])
      .then(([settings, list]) => {
        if (!active) return;
        setIdentitySettings(settings);
        setIdentityChoices(list.identities);
        setIdentitiesLoaded(true);
        const selected = chooseMailboxIdentity(
          settings,
          list.identities,
          draft,
        );
        setFrom(selected);
        if (
          draft.mode !== "draft" &&
          draft.quoteText &&
          bodyRevision.current === revision
        ) {
          const include = settings.preferences.quote_original !== false;
          setQuoteOriginal(include);
          if (!include) setText((value) => value.replace(draft.quoteText!, ""));
        }

        if (
          (draft.mode === "reply" || draft.mode === "reply-all") &&
          recipientSnapshot === recipientRevision.current
        ) {
          const owned = new Set(
            list.identities.map((identity) => identity.address.toLowerCase()),
          );
          let recipients = splitAddrs(draft.to ?? "").filter(
            (value) => !owned.has(value.toLowerCase()),
          );
          if (!recipients.length) {
            const fallback = (draft.replyRecipients ?? []).filter(
              (value) => !owned.has(value.toLowerCase()),
            );
            recipients =
              draft.mode === "reply" ? fallback.slice(0, 1) : fallback;
          }
          const toSet = new Set(recipients.map((value) => value.toLowerCase()));
          setTo([...new Set(recipients)].join(", "));
          setCc(
            splitAddrs(draft.cc ?? "")
              .filter(
                (value) =>
                  !owned.has(value.toLowerCase()) &&
                  !toSet.has(value.toLowerCase()),
              )
              .join(", "),
          );
        }

        if (draft.mode !== "draft" && bodyRevision.current === revision) {
          const profile =
            settings.identity_profiles?.[selected] ??
            (selected === settings.address
              ? settings
              : { signature_text: "", signature_html: "" });
          const textPart = profile.signature_text
            ? "\n\n-- \n" + profile.signature_text
            : "";
          const htmlPart = profile.signature_html
            ? "<br><br>" + profile.signature_html
            : "";
          setText((value) => value + textPart);
          automaticHTMLSignature.current = htmlPart;
          if (draft.html && htmlPart) setHtml((value) => value + htmlPart);
          insertedSignature.current = { text: textPart, html: htmlPart };
        }
      })
      .catch(() => {
        if (active) {
          const primary = getMailboxAddress() ?? "";
          setIdentityChoices(
            primary ? [{ address: primary, primary: true }] : [],
          );
          setError(t("page.webmail.settings_load_failed"));
        }
      });
    return () => {
      active = false;
    };
  }, [open, draft, t]);
  const identityInvalid =
    identitiesLoaded &&
    !!from &&
    !identityChoices.some((value) => value.address === from);
  const changeIdentity = (selected: string) => {
    bodyRevision.current++;
    setFrom(selected);
    const profile =
      identitySettings?.identity_profiles?.[selected] ??
      (selected === identitySettings?.address ? identitySettings : null);
    const nextText = profile?.signature_text
      ? "\n\n-- \n" + profile.signature_text
      : "";
    const nextHtml = profile?.signature_html
      ? "<br><br>" + profile.signature_html
      : "";
    const old = insertedSignature.current;
    setText(
      (value) => (old.text ? value.replace(old.text, "") : value) + nextText,
    );
    automaticHTMLSignature.current = nextHtml;
    setHtml((value) =>
      value
        ? (old.html ? value.replace(old.html, "") : value) + nextHtml
        : value,
    );
    insertedSignature.current = { text: nextText, html: nextHtml };
  };

  const toggleQuote = (include: boolean) => {
    bodyRevision.current++;
    setQuoteOriginal(include);
    if (!draft.quoteText) return;
    setText((value) =>
      include
        ? value.includes(draft.quoteText!)
          ? value
          : value + draft.quoteText!
        : value.replace(draft.quoteText!, ""),
    );
  };
  const titleKey =
    draft.mode === "reply"
      ? "page.webmail.compose_reply"
      : draft.mode === "forward"
        ? "page.webmail.compose_forward"
        : draft.mode === "draft"
          ? "page.webmail.compose_draft"
          : "page.webmail.compose_new";

  const onPickFiles = async (files: FileList | null) => {
    if (!files?.length) return;
    setError(null);
    const next = [...attachments];
    for (const file of Array.from(files)) {
      if (next.length >= MAX_SEND_ATTACHMENTS) {
        setError(
          t("page.webmail.compose_attach_limit", {
            max: MAX_SEND_ATTACHMENTS,
            size: Math.floor(MAX_SEND_ATTACHMENT_BYTES / (1024 * 1024)),
          }),
        );
        break;
      }
      if (file.size > MAX_SEND_ATTACHMENT_BYTES) {
        setError(
          t("page.webmail.compose_attach_too_large", {
            name: file.name,
            size: Math.floor(MAX_SEND_ATTACHMENT_BYTES / (1024 * 1024)),
          }),
        );
        continue;
      }
      if (
        attachmentTotalBytes(next) + file.size >
        MAX_SEND_TOTAL_ATTACHMENT_BYTES
      ) {
        setError(
          t("page.webmail.compose_attach_total_limit", {
            size: MAX_SEND_TOTAL_ATTACHMENT_BYTES / (1024 * 1024),
          }),
        );
        continue;
      }
      try {
        next.push(await fileToOutgoingAttachment(file));
      } catch {
        setError(t("page.webmail.compose_attach_failed"));
      }
    }
    setAttachments(next);
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const removeAttachment = (index: number) => {
    setAttachments((prev) => prev.filter((_, i) => i !== index));
  };

  const outgoingHTML = (bodyText: string, rawHTML: string) => {
    if (rawHTML || !automaticHTMLSignature.current) return rawHTML;
    const plain = insertedSignature.current.text
      ? bodyText.replace(insertedSignature.current.text, "")
      : bodyText;
    return (
      plain
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/\n/g, "<br>") + automaticHTMLSignature.current
    );
  };
  const onSaveDraft = async (proposal?: {
    subject: string;
    text: string;
    html: string;
  }) => {
    if (editingLocked || identityInvalid || !identitiesLoaded) return;
    setError(null);
    setSavedHint(false);
    setBusy(true);
    try {
      const res = await saveDraft({
        from,
        in_reply_to: draft.inReplyTo,
        reply_source: draft.aiSource,
        uid: draftUid,
        uid_validity: draftUidValidity,
        to: splitAddrs(to),
        cc: splitAddrs(cc),
        bcc: splitAddrs(bcc),
        html: outgoingHTML(proposal?.text ?? text, proposal?.html ?? html),
        subject: proposal?.subject ?? subject,
        text: proposal?.text ?? text,
        attachments: attachments.length ? attachments : undefined,
      });
      if (!res.uid || !res.uid_validity)
        throw new Error("DRAFT_REFERENCE_MISSING");
      if (res.uid) {
        setDraftUid(res.uid);
        setDraftUidValidity(res.uid_validity);
        setCleanupPending(Boolean(res.cleanup_pending));
        onDraftSaved?.(res.uid);
      }
      setSavedHint(true);
      return true;
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        if (err.code === "DRAFT_SAVE_UNCERTAIN") setDraftUncertain(true);
        if (err.code === "MAILBOX_SEND_UNCERTAIN") setSendUncertain(true);
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.compose_draft_failed"),
          }),
        );
      } else {
        setDraftUncertain(true);
        setError(t("error.DRAFT_SAVE_UNCERTAIN"));
      }
    } finally {
      setBusy(false);
    }
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (editingLocked || identityInvalid || !identitiesLoaded) return;
    setError(null);
    const recipients = splitAddrs(to);
    if (
      recipients.length + splitAddrs(cc).length + splitAddrs(bcc).length ===
      0
    ) {
      setError(t("page.webmail.compose_need_to"));
      return;
    }
    setBusy(true);
    try {
      const res = await sendMessage({
        from,
        in_reply_to: draft.inReplyTo,
        to: recipients,
        cc: splitAddrs(cc),
        bcc: splitAddrs(bcc),
        html: outgoingHTML(text, html),
        subject,
        text,
        attachments: attachments.length ? attachments : undefined,
      });
      setSentCopyPending(res.sent_copy === "pending");
      // Best-effort: remove the IMAP draft after a successful send.
      if (draftUid && draftUidValidity) {
        try {
          await deleteDraft(draftUid, draftUidValidity);
        } catch {
          /* non-fatal */
        }
      }
      if (res.sent_copy !== "pending") {
        onOpenChange(false);
      }
      onSent();
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        if (err.code === "DRAFT_SAVE_UNCERTAIN") setDraftUncertain(true);
        if (err.code === "MAILBOX_SEND_UNCERTAIN") setSendUncertain(true);
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.compose_failed"),
          }),
        );
      } else {
        setSendUncertain(true);
        setError(t("error.MAILBOX_SEND_UNCERTAIN"));
      }
    } finally {
      setBusy(false);
    }
  };

  const onSchedule = async () => {
    if (busy || draftUncertain || sendUncertain || !identitiesLoaded) return;
    setError(null);
    setScheduledHint(false);
    if (!scheduleAttempt.current) {
      if (
        splitAddrs(to).length +
          splitAddrs(cc).length +
          splitAddrs(bcc).length ===
        0
      ) {
        setError(t("page.webmail.compose_need_to"));
        return;
      }
      const when = new Date(sendLaterAt);
      if (
        !sendLaterAt ||
        Number.isNaN(when.getTime()) ||
        when.getTime() <= Date.now() + 30_000
      ) {
        setError(t("page.webmail.compose_need_send_at"));
        return;
      }
    }
    setBusy(true);
    try {
      if (!scheduleAttempt.current) {
        const saved = await saveDraft({
          from,
          in_reply_to: draft.inReplyTo,
          reply_source: draft.aiSource,
          uid: draftUid,
          uid_validity: draftUidValidity,
          to: splitAddrs(to),
          cc: splitAddrs(cc),
          bcc: splitAddrs(bcc),
          subject,
          text,
          html: outgoingHTML(text, html),
          attachments: attachments.length ? attachments : undefined,
        });
        if (!saved.uid || !saved.uid_validity)
          throw new Error("DRAFT_REFERENCE_MISSING");
        setDraftUid(saved.uid);
        setDraftUidValidity(saved.uid_validity);
        setCleanupPending(Boolean(saved.cleanup_pending));
        onDraftSaved?.(saved.uid);
        scheduleAttempt.current = {
          key: crypto.randomUUID(),
          body: {
            draft_uid: saved.uid,
            draft_uidvalidity: saved.uid_validity,
            send_at: new Date(sendLaterAt).toISOString(),
          },
        };
      }
      await scheduleSend(
        scheduleAttempt.current.body,
        scheduleAttempt.current.key,
      );
      scheduleAttempt.current = null;
      setScheduleUncertain(false);
      setScheduledHint(true);
      onOpenChange(false);
      onSent();
    } catch (err: unknown) {
      const uncertain =
        scheduleAttempt.current !== null &&
        !(err instanceof ApiRequestError && err.status < 500);
      setScheduleUncertain(uncertain);
      if (!uncertain) scheduleAttempt.current = null;
      if (err instanceof ApiRequestError) {
        if (err.code === "DRAFT_SAVE_UNCERTAIN") setDraftUncertain(true);
        if (err.code === "MAILBOX_SEND_UNCERTAIN") setSendUncertain(true);
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.compose_schedule_failed"),
          }),
        );
      } else {
        if (!scheduleAttempt.current) setDraftUncertain(true);
        setError(
          t(
            scheduleAttempt.current
              ? "page.webmail.compose_schedule_failed"
              : "error.DRAFT_SAVE_UNCERTAIN",
          ),
        );
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!scheduleUncertain && !busy) onOpenChange(next);
      }}
    >
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
          {scheduleUncertain && (
            <p role="status" className="text-sm text-muted-foreground">
              {t("page.webmail.compose_schedule_uncertain")}
            </p>
          )}
          {cleanupPending && (
            <p role="status" className="text-sm text-muted-foreground">
              {t("page.webmail.compose_cleanup_pending")}
            </p>
          )}
          {sentCopyPending && (
            <p role="alert" className="text-sm text-destructive">
              {t("page.webmail.compose_sent_copy_pending")}
            </p>
          )}
          {savedHint && !error && (
            <p className="text-sm text-muted-foreground" role="status">
              {t("page.webmail.compose_draft_saved")}
            </p>
          )}
          {scheduledHint && !error && (
            <p className="text-sm text-muted-foreground" role="status">
              {t("page.webmail.compose_scheduled")}
            </p>
          )}
          {identitiesLoaded && draft.mode !== "draft" && draft.quoteText && (
            <label className="flex min-h-11 items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={quoteOriginal}
                disabled={editingLocked}
                onChange={(e) => toggleQuote(e.target.checked)}
              />
              {t("page.webmail.compose_quote_original")}
            </label>
          )}
          <MailboxAIPanel
            mode="compose"
            source={draft.aiSource}
            subject={subject}
            body={text || html}
            disabled={editingLocked}
            onApply={async (proposal) => {
              setSubject(proposal.subject);
              bodyRevision.current++;
              const marker = insertedSignature.current.text;
              const adopted =
                marker && !proposal.body.includes(marker.trim())
                  ? proposal.body + marker
                  : proposal.body;
              setText(adopted);
              if (draft.quoteText)
                setQuoteOriginal(adopted.includes(draft.quoteText));
              setHtml("");
              const saved = await onSaveDraft({
                subject: proposal.subject,
                text: adopted,
                html: "",
              });
              if (!saved) throw new Error("AI_DRAFT_SAVE_FAILED");
            }}
          />
          <div className="space-y-1.5">
            <label htmlFor="compose-from" className="text-sm font-medium">
              {t("page.webmail.compose_from")}
            </label>
            <select
              id="compose-from"
              value={from}
              disabled={editingLocked || !identitiesLoaded}
              onChange={(e) => changeIdentity(e.target.value)}
              className="h-11 w-full rounded-md border bg-background px-3 text-sm"
            >
              {identityChoices.map((identity) => (
                <option key={identity.address} value={identity.address}>
                  {identitySettings?.identity_profiles?.[identity.address]
                    ?.display_name ??
                    (identity.primary
                      ? identitySettings?.display_name
                      : "")}{" "}
                  &lt;{identity.address}&gt;
                </option>
              ))}
              {identityInvalid && (
                <option value={from} disabled>
                  {from} — {t("page.webmail.identity_revoked")}
                </option>
              )}
            </select>
            {identityInvalid && (
              <p role="alert" className="text-sm text-destructive">
                {t("page.webmail.identity_revoked")}
              </p>
            )}
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-to" className="text-sm font-medium">
              {t("page.webmail.compose_to")}
            </label>
            <Input
              id="compose-to"
              disabled={editingLocked}
              value={to}
              onChange={(e) => {
                recipientRevision.current++;
                setTo(e.target.value);
              }}
              placeholder="a@example.com"
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-cc" className="text-sm font-medium">
              {t("page.webmail.compose_cc")}
            </label>
            <Input
              id="compose-cc"
              disabled={editingLocked}
              value={cc}
              onChange={(e) => {
                recipientRevision.current++;
                setCc(e.target.value);
              }}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-bcc" className="text-sm font-medium">
              {t("page.webmail.compose_bcc")}
            </label>
            <Input
              id="compose-bcc"
              value={bcc}
              disabled={editingLocked}
              onChange={(e) => {
                recipientRevision.current++;
                setBcc(e.target.value);
              }}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-subject" className="text-sm font-medium">
              {t("page.webmail.compose_subject")}
            </label>
            <Input
              id="compose-subject"
              disabled={editingLocked}
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-body" className="text-sm font-medium">
              {t(
                html
                  ? "page.webmail.compose_plain_body"
                  : "page.webmail.compose_body",
              )}
            </label>
            <textarea
              id="compose-body"
              rows={12}
              className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-xs focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              disabled={editingLocked}
              value={text}
              onChange={(e) => {
                bodyRevision.current++;
                if (draft.quoteText)
                  setQuoteOriginal(e.target.value.includes(draft.quoteText));
                setText(e.target.value);
              }}
            />
          </div>
          {html !== "" && (
            <div className="space-y-1.5">
              <label htmlFor="compose-html" className="text-sm font-medium">
                {t("page.webmail.compose_html")}
              </label>
              <p className="text-xs text-muted-foreground">
                {t("page.webmail.compose_html_hint")}
              </p>
              <textarea
                id="compose-html"
                rows={8}
                value={html}
                disabled={editingLocked}
                onChange={(e) => {
                  bodyRevision.current++;
                  setHtml(e.target.value);
                }}
                className="w-full rounded-md border border-input bg-background p-2 font-mono text-sm"
              />
            </div>
          )}
          <div className="space-y-1.5">
            <label htmlFor="compose-attach" className="text-sm font-medium">
              {t("page.webmail.compose_attach")}
            </label>
            <input
              id="compose-attach"
              ref={fileInputRef}
              type="file"
              multiple
              className="block w-full text-sm file:mr-2 file:rounded-md file:border file:border-input file:bg-background file:px-2 file:py-1"
              onChange={(e) => void onPickFiles(e.target.files)}
              disabled={
                editingLocked || attachments.length >= MAX_SEND_ATTACHMENTS
              }
            />
            <p className="text-xs text-muted-foreground">
              {t("page.webmail.compose_attach_hint", {
                max: MAX_SEND_ATTACHMENTS,
                size: Math.floor(MAX_SEND_ATTACHMENT_BYTES / (1024 * 1024)),
                total: MAX_SEND_TOTAL_ATTACHMENT_BYTES / (1024 * 1024),
              })}
            </p>
            {attachments.length > 0 && (
              <ul className="text-sm space-y-1">
                {attachments.map((a, i) => (
                  <li
                    key={`${a.filename}-${i}`}
                    className="flex items-center justify-between gap-2"
                  >
                    <span className="truncate">{a.filename}</span>
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      aria-label={t(
                        "page.webmail.compose_attach_remove_named",
                        {
                          name: a.filename,
                        },
                      )}
                      onClick={() => removeAttachment(i)}
                      disabled={editingLocked}
                    >
                      {t("page.webmail.compose_attach_remove")}
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div className="space-y-1.5">
            <label htmlFor="compose-send-later" className="text-sm font-medium">
              {t("page.webmail.compose_send_later")}
            </label>
            <Input
              id="compose-send-later"
              type="datetime-local"
              disabled={editingLocked}
              value={sendLaterAt}
              onChange={(e) => setSendLaterAt(e.target.value)}
            />
          </div>
          <DialogFooter className="gap-2 flex-wrap sm:gap-0">
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={busy || scheduleUncertain}
            >
              {t("action.cancel")}
            </Button>
            <Button
              type="button"
              variant="secondary"
              onClick={() => void onSaveDraft()}
              disabled={editingLocked || !identitiesLoaded}
            >
              {busy ? t("state.loading") : t("page.webmail.compose_save_draft")}
            </Button>
            <Button
              type="button"
              variant="secondary"
              onClick={() => void onSchedule()}
              disabled={
                !identitiesLoaded ||
                busy ||
                draftUncertain ||
                sendUncertain ||
                (!scheduleUncertain && !sendLaterAt)
              }
            >
              {busy ? t("state.loading") : t("page.webmail.compose_schedule")}
            </Button>
            <Button type="submit" disabled={editingLocked || !identitiesLoaded}>
              {busy ? t("state.loading") : t("page.webmail.compose_send")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
