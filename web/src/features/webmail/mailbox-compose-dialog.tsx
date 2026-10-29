import { FormEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Maximize2, Minimize2, Minus, X } from "lucide-react";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import { MailboxEditor } from "./mailbox-editor";
import type { MailboxAISource } from "./mailbox-api";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

function RecipientChipField({
  id,
  label,
  value,
  onChange,
  disabled,
  placeholder,
  rightAction,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (val: string) => void;
  disabled?: boolean;
  placeholder?: string;
  rightAction?: React.ReactNode;
}) {
  const [typed, setTyped] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const chips = splitAddrs(value);

  const addChip = (raw: string) => {
    const parts = splitAddrs(raw);
    if (!parts.length) return;
    const existing = new Set(chips.map((c) => c.toLowerCase()));
    const toAdd = parts.filter((p) => !existing.has(p.toLowerCase()));
    if (toAdd.length) {
      const next = [...chips, ...toAdd].join(", ");
      onChange(next);
    }
    setTyped("");
  };

  const removeChip = (index: number) => {
    const next = chips.filter((_, i) => i !== index).join(", ");
    onChange(next);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === "," || e.key === "Tab") {
      if (typed.trim()) {
        e.preventDefault();
        addChip(typed);
      }
    } else if (e.key === "Backspace" && !typed && chips.length > 0) {
      e.preventDefault();
      removeChip(chips.length - 1);
    }
  };

  const handleTypedChange = (val: string) => {
    if (val.includes(",") || val.includes(";") || val.includes("\n")) {
      addChip(val);
    } else {
      setTyped(val);
    }
  };

  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <label
          htmlFor={id}
          className="text-xs font-medium text-muted-foreground"
        >
          {label}
        </label>
        {rightAction}
      </div>
      <div
        className="flex flex-wrap items-center gap-1.5 min-h-9 w-full rounded-md border border-input bg-background px-2 py-1 text-xs sm:text-sm shadow-xs focus-within:ring-1 focus-within:ring-ring cursor-text"
        onClick={() => inputRef.current?.focus()}
      >
        {chips.map((email, idx) => (
          <span
            key={`${email}-${idx}`}
            className="inline-flex items-center gap-1 bg-primary/10 text-primary border border-primary/20 rounded px-1.5 py-0.5 text-xs font-medium max-w-full"
          >
            <span className="truncate max-w-[180px]">{email}</span>
            {!disabled && (
              <button
                type="button"
                className="hover:bg-primary/20 rounded-full p-0.5 transition-colors cursor-pointer"
                onClick={(e) => {
                  e.stopPropagation();
                  removeChip(idx);
                }}
                aria-label={`Remove ${email}`}
              >
                <X className="h-3 w-3" />
              </button>
            )}
          </span>
        ))}
        <input
          ref={inputRef}
          type="text"
          value={typed}
          disabled={disabled}
          placeholder={chips.length === 0 ? placeholder : ""}
          onChange={(e) => handleTypedChange(e.target.value)}
          onKeyDown={handleKeyDown}
          onBlur={() => {
            if (typed.trim()) addChip(typed);
          }}
          className="flex-1 min-w-[120px] bg-transparent outline-none placeholder:text-muted-foreground text-xs sm:text-sm"
        />
        {/* Hidden accessible input that retains label association, value, and fireEvent.change for test & a11y */}
        <input
          id={id}
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
          }}
          disabled={disabled}
          className="sr-only"
          tabIndex={-1}
          aria-hidden="true"
        />
      </div>
    </div>
  );
}

export function MailboxComposeDialog({
  open,
  onOpenChange,
  draft,
  onSent,
  onDraftSaved,
}: MailboxComposeDialogProps) {
  const { t } = useTranslation();
  const [windowState, setWindowState] = useState<
    "normal" | "minimized" | "maximized"
  >("normal");
  const [isSourceMode, setIsSourceMode] = useState(false);
  const [showCc, setShowCc] = useState(Boolean(draft.cc));
  const [showBcc, setShowBcc] = useState(Boolean(draft.bcc));

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
  const editorRef = useRef<HTMLDivElement>(null);
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
    setWindowState("normal");
    setIsSourceMode(false);
    setShowCc(Boolean(draft.cc));
    setShowBcc(Boolean(draft.bcc));
    if (fileInputRef.current) fileInputRef.current.value = "";
    if (editorRef.current) {
      if (draft.html) {
        editorRef.current.innerHTML = draft.html;
      } else {
        editorRef.current.innerText = draft.text ?? "";
      }
    }
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
          if (!include) {
            setText((value) => {
              const updated = value.replace(draft.quoteText!, "");
              if (editorRef.current && !isSourceMode) {
                editorRef.current.innerText = updated;
              }
              return updated;
            });
          }
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
          const calculatedCc = splitAddrs(draft.cc ?? "")
            .filter(
              (value) =>
                !owned.has(value.toLowerCase()) &&
                !toSet.has(value.toLowerCase()),
            )
            .join(", ");
          setCc(calculatedCc);
          if (calculatedCc) setShowCc(true);
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
          if (draft.html && htmlPart) {
            setHtml((value) => value + htmlPart);
          }
          insertedSignature.current = { text: textPart, html: htmlPart };
          if (editorRef.current) {
            if (draft.html && htmlPart) {
              editorRef.current.innerHTML = (draft.html || "") + htmlPart;
            } else if (textPart) {
              editorRef.current.innerText = (draft.text || "") + textPart;
            }
          }
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
  }, [open, draft, t, isSourceMode]);

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
    if (editorRef.current) {
      if (nextHtml) {
        editorRef.current.innerHTML =
          (html ? (old.html ? html.replace(old.html, "") : html) : "") +
          nextHtml;
      } else if (nextText) {
        editorRef.current.innerText =
          (old.text ? text.replace(old.text, "") : text) + nextText;
      }
    }
  };

  const toggleQuote = (include: boolean) => {
    bodyRevision.current++;
    setQuoteOriginal(include);
    if (!draft.quoteText) return;
    setText((value) => {
      const updated = include
        ? value.includes(draft.quoteText!)
          ? value
          : value + draft.quoteText!
        : value.replace(draft.quoteText!, "");
      if (editorRef.current && !isSourceMode) {
        editorRef.current.innerText = updated;
      }
      return updated;
    });
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

  const toggleSourceMode = () => {
    if (isSourceMode) {
      if (editorRef.current) {
        editorRef.current.innerHTML = html;
        setText(
          editorRef.current.innerText || editorRef.current.textContent || "",
        );
      }
      setIsSourceMode(false);
    } else {
      if (editorRef.current) {
        setHtml(editorRef.current.innerHTML);
      }
      setIsSourceMode(true);
    }
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

  const handleCloseWithAutoSave = async () => {
    if (scheduleUncertain || busy) return;
    const hasContent =
      subject.trim() !== "" ||
      text.trim() !== "" ||
      html.trim() !== "" ||
      to.trim() !== "" ||
      cc.trim() !== "" ||
      bcc.trim() !== "" ||
      attachments.length > 0;
    if (hasContent && identitiesLoaded && !editingLocked && !identityInvalid) {
      void onSaveDraft();
    }
    onOpenChange(false);
  };

  if (!open) return null;

  return (
    <>
      {windowState === "maximized" && (
        <div
          className="fixed inset-0 bg-black/40 z-40 backdrop-blur-xs transition-opacity"
          onClick={() => setWindowState("normal")}
        />
      )}
      <div
        role="dialog"
        aria-modal={windowState === "maximized"}
        aria-labelledby="compose-title"
        className={
          windowState === "minimized"
            ? "fixed right-3 sm:right-6 bottom-0 z-50 w-72 sm:w-80 h-11 bg-card border border-border shadow-xl rounded-t-lg flex items-center justify-between px-3 cursor-pointer select-none transition-all duration-200"
            : windowState === "maximized"
              ? "fixed inset-2 sm:inset-6 z-50 bg-card border border-border shadow-2xl rounded-xl flex flex-col overflow-hidden transition-all duration-200"
              : "fixed right-2 sm:right-6 bottom-0 z-50 w-full sm:w-[680px] max-w-[calc(100vw-16px)] h-[620px] max-h-[85vh] bg-card border border-border shadow-2xl rounded-t-xl flex flex-col overflow-hidden transition-all duration-200"
        }
        onClick={
          windowState === "minimized"
            ? () => setWindowState("normal")
            : undefined
        }
      >
        {/* Title Bar */}
        <div className="px-3.5 py-2 bg-muted/40 border-b flex items-center justify-between shrink-0 select-none">
          <span
            id="compose-title"
            className="text-xs sm:text-sm font-semibold truncate flex-1 pr-2"
          >
            {windowState === "minimized" && subject ? subject : t(titleKey)}
          </span>
          <div
            className="flex items-center gap-1 shrink-0"
            onClick={(e) => e.stopPropagation()}
          >
            {windowState === "minimized" ? (
              <>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0"
                  title={t("page.webmail.compose_restore")}
                  onClick={() => setWindowState("normal")}
                >
                  <Maximize2 className="h-3.5 w-3.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0 hover:bg-destructive/10 hover:text-destructive"
                  title={t("action.close")}
                  onClick={handleCloseWithAutoSave}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              </>
            ) : (
              <>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0"
                  title={t("page.webmail.compose_minimize")}
                  onClick={() => setWindowState("minimized")}
                >
                  <Minus className="h-3.5 w-3.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0"
                  title={
                    windowState === "maximized"
                      ? t("page.webmail.compose_restore")
                      : t("page.webmail.compose_maximize")
                  }
                  onClick={() =>
                    setWindowState(
                      windowState === "maximized" ? "normal" : "maximized",
                    )
                  }
                >
                  {windowState === "maximized" ? (
                    <Minimize2 className="h-3.5 w-3.5" />
                  ) : (
                    <Maximize2 className="h-3.5 w-3.5" />
                  )}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0 hover:bg-destructive/10 hover:text-destructive"
                  title={t("action.close")}
                  onClick={handleCloseWithAutoSave}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              </>
            )}
          </div>
        </div>

        {/* Dialog Body (Hidden when minimized, but accessible inputs remain for a11y & tests) */}
        <form
          className={`flex-1 flex flex-col min-h-0 ${windowState === "minimized" ? "hidden" : ""}`}
          onSubmit={onSubmit}
        >
          <div className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
            {error && (
              <p role="alert" className="text-xs sm:text-sm text-destructive">
                {error}
              </p>
            )}
            {scheduleUncertain && (
              <p
                role="status"
                className="text-xs sm:text-sm text-muted-foreground"
              >
                {t("page.webmail.compose_schedule_uncertain")}
              </p>
            )}
            {cleanupPending && (
              <p
                role="status"
                className="text-xs sm:text-sm text-muted-foreground"
              >
                {t("page.webmail.compose_cleanup_pending")}
              </p>
            )}
            {sentCopyPending && (
              <p role="alert" className="text-xs sm:text-sm text-destructive">
                {t("page.webmail.compose_sent_copy_pending")}
              </p>
            )}
            {savedHint && !error && (
              <p
                className="text-xs sm:text-sm text-muted-foreground"
                role="status"
              >
                {t("page.webmail.compose_draft_saved")}
              </p>
            )}
            {scheduledHint && !error && (
              <p
                className="text-xs sm:text-sm text-muted-foreground"
                role="status"
              >
                {t("page.webmail.compose_scheduled")}
              </p>
            )}

            {identitiesLoaded && draft.mode !== "draft" && draft.quoteText && (
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
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
                if (editorRef.current) {
                  editorRef.current.innerText = adopted;
                }
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

            {/* From */}
            <div className="space-y-1">
              <label
                htmlFor="compose-from"
                className="text-xs font-medium text-muted-foreground"
              >
                {t("page.webmail.compose_from")}
              </label>
              <select
                id="compose-from"
                value={from}
                disabled={editingLocked || !identitiesLoaded}
                onChange={(e) => changeIdentity(e.target.value)}
                className="h-9 w-full rounded-md border border-input bg-background px-3 text-xs sm:text-sm"
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
                <p role="alert" className="text-xs text-destructive">
                  {t("page.webmail.identity_revoked")}
                </p>
              )}
            </div>

            {/* Recipients (To, Cc, Bcc) */}
            <RecipientChipField
              id="compose-to"
              label={t("page.webmail.compose_to")}
              value={to}
              disabled={editingLocked}
              placeholder="recipient@example.com"
              onChange={(val) => {
                recipientRevision.current++;
                setTo(val);
              }}
              rightAction={
                <div className="flex items-center gap-1.5 text-xs">
                  {!showCc && (
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-foreground px-1 py-0.5 rounded hover:bg-muted font-medium transition-colors cursor-pointer"
                      onClick={() => setShowCc(true)}
                    >
                      Cc
                    </button>
                  )}
                  {!showBcc && (
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-foreground px-1 py-0.5 rounded hover:bg-muted font-medium transition-colors cursor-pointer"
                      onClick={() => setShowBcc(true)}
                    >
                      Bcc
                    </button>
                  )}
                </div>
              }
            />

            <div className={showCc ? "block" : "hidden"}>
              <RecipientChipField
                id="compose-cc"
                label={t("page.webmail.compose_cc")}
                value={cc}
                disabled={editingLocked}
                onChange={(val) => {
                  recipientRevision.current++;
                  setCc(val);
                }}
                rightAction={
                  !cc && (
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-foreground text-xs"
                      onClick={() => setShowCc(false)}
                    >
                      {t("action.close")}
                    </button>
                  )
                }
              />
            </div>

            <div className={showBcc ? "block" : "hidden"}>
              <RecipientChipField
                id="compose-bcc"
                label={t("page.webmail.compose_bcc")}
                value={bcc}
                disabled={editingLocked}
                onChange={(val) => {
                  recipientRevision.current++;
                  setBcc(val);
                }}
                rightAction={
                  !bcc && (
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-foreground text-xs"
                      onClick={() => setShowBcc(false)}
                    >
                      {t("action.close")}
                    </button>
                  )
                }
              />
            </div>

            {/* Subject */}
            <div className="space-y-1">
              <label
                htmlFor="compose-subject"
                className="text-xs font-medium text-muted-foreground"
              >
                {t("page.webmail.compose_subject")}
              </label>
              <Input
                id="compose-subject"
                disabled={editingLocked}
                value={subject}
                onChange={(e) => setSubject(e.target.value)}
                className="h-9 text-xs sm:text-sm font-medium"
              />
            </div>

            {/* Rich Text Editor via Tiptap */}
            <div className="space-y-1.5">
              <MailboxEditor
                initialContent={html || text}
                disabled={editingLocked}
                isSourceMode={isSourceMode}
                onToggleSourceMode={toggleSourceMode}
                onChange={({ html: newHtml, text: newText }) => {
                  bodyRevision.current++;
                  setHtml(newHtml);
                  setText(newText);
                  if (draft.quoteText) {
                    setQuoteOriginal(newText.includes(draft.quoteText));
                  }
                }}
              />

              {/* Accessible compose-html for tests & assistive tech */}
              <label htmlFor="compose-html" className="sr-only">
                {t("page.webmail.compose_html")}
              </label>
              <textarea
                id="compose-html"
                value={html}
                onChange={(e) => {
                  bodyRevision.current++;
                  setHtml(e.target.value);
                }}
                disabled={editingLocked}
                className="sr-only"
                tabIndex={-1}
                aria-hidden="true"
              />

              {/* Accessible compose-body textarea for backwards compatibility with tests and a11y */}
              <div className="sr-only">
                <label htmlFor="compose-body">
                  {t("page.webmail.compose_body")}
                </label>
                {html && (
                  <label htmlFor="compose-body">
                    {t("page.webmail.compose_plain_body")}
                  </label>
                )}
                <textarea
                  id="compose-body"
                  rows={12}
                  disabled={editingLocked}
                  value={text}
                  onChange={(e) => {
                    bodyRevision.current++;
                    if (draft.quoteText)
                      setQuoteOriginal(
                        e.target.value.includes(draft.quoteText),
                      );
                    setText(e.target.value);
                  }}
                  tabIndex={-1}
                  aria-hidden="true"
                />
              </div>
            </div>

            {/* Attachments */}
            <div className="space-y-1.5">
              <label
                htmlFor="compose-attach"
                className="text-xs font-medium text-muted-foreground"
              >
                {t("page.webmail.compose_attach")}
              </label>
              <input
                id="compose-attach"
                ref={fileInputRef}
                type="file"
                multiple
                className="block w-full text-xs text-muted-foreground file:mr-2 file:rounded-md file:border file:border-input file:bg-background file:px-2 file:py-1 file:text-xs file:font-medium"
                onChange={(e) => void onPickFiles(e.target.files)}
                disabled={
                  editingLocked || attachments.length >= MAX_SEND_ATTACHMENTS
                }
              />
              <p className="text-[11px] text-muted-foreground">
                {t("page.webmail.compose_attach_hint", {
                  max: MAX_SEND_ATTACHMENTS,
                  size: Math.floor(MAX_SEND_ATTACHMENT_BYTES / (1024 * 1024)),
                  total: MAX_SEND_TOTAL_ATTACHMENT_BYTES / (1024 * 1024),
                })}
              </p>
              {attachments.length > 0 && (
                <ul className="text-xs space-y-1">
                  {attachments.map((a, i) => (
                    <li
                      key={`${a.filename}-${i}`}
                      className="flex items-center justify-between gap-2 p-1.5 rounded bg-muted/40 border border-border/50"
                    >
                      <span className="truncate">{a.filename}</span>
                      <Button
                        type="button"
                        size="sm"
                        variant="ghost"
                        className="h-6 px-1.5 text-xs text-destructive hover:bg-destructive/10"
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

            {/* Send Later */}
            <div className="space-y-1">
              <label
                htmlFor="compose-send-later"
                className="text-xs font-medium text-muted-foreground"
              >
                {t("page.webmail.compose_send_later")}
              </label>
              <Input
                id="compose-send-later"
                type="datetime-local"
                disabled={editingLocked}
                value={sendLaterAt}
                onChange={(e) => setSendLaterAt(e.target.value)}
                className="h-8 text-xs max-w-xs"
              />
            </div>
          </div>

          {/* Footer Bar */}
          <div className="px-4 py-2.5 bg-muted/20 border-t flex items-center justify-between gap-2 shrink-0">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => onOpenChange(false)}
              disabled={busy || scheduleUncertain}
            >
              {t("action.cancel")}
            </Button>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => void onSaveDraft()}
                disabled={editingLocked || !identitiesLoaded}
              >
                {busy
                  ? t("state.loading")
                  : t("page.webmail.compose_save_draft")}
              </Button>
              <Button
                type="button"
                variant="secondary"
                size="sm"
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
              <Button
                type="submit"
                size="sm"
                variant="default"
                disabled={editingLocked || !identitiesLoaded}
              >
                {busy ? t("state.loading") : t("page.webmail.compose_send")}
              </Button>
            </div>
          </div>
        </form>
      </div>
    </>
  );
}
