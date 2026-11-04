import {
  FormEvent,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
  type Ref,
} from "react";
import { useDraftPersistence } from "./use-draft-persistence";
import { useTranslation } from "react-i18next";
import {
  Maximize2,
  Minimize2,
  Minus,
  X,
  FileText,
  PenLine,
} from "lucide-react";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import { MailboxEditor } from "./mailbox-editor";
import type { MailboxAISource } from "./mailbox-api";
import type { Editor } from "@tiptap/react";
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
  listMailboxSnippets,
  type MailboxSnippet,
  MailboxSettingsView,
  MailboxIdentity,
  saveDraft,
  scheduleSend,
  ScheduleSendRequest,
  sendMessage,
  getSendReceiptByRequestKey,
  getSendReceiptForDraft,
  type SendReceipt,
} from "./mailbox-api";

export type ComposeMode = "new" | "reply" | "reply-all" | "forward" | "draft";

export interface ComposeDraft {
  from?: string;
  replyTo?: string;
  replyRecipients?: string[];
  quoteText?: string;
  aiSource?: MailboxAISource;
  inReplyTo?: string;
  mode: ComposeMode;
  /** IMAP UID when editing a saved draft (replace on next save). */
  uid?: number;
  uidValidity?: number;
  folderEnc?: string;
  to?: string;
  cc?: string;
  bcc?: string;
  html?: string;
  subject?: string;
  text?: string;
  attachments?: OutgoingAttachment[];
  requestMdn?: boolean;
  requestDsn?: boolean;
}

export interface ComposeControllerHandle {
  flush: () => Promise<boolean>;
}

interface MailboxComposeDialogProps {
  ref?: Ref<ComposeControllerHandle>;
  inline?: boolean;
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
    const quotedHtml = msg.html
      ? `<br><br><div class="urmail-quoted-message"><p>On ${msg.date ? new Date(msg.date).toLocaleString() : ""}, ${from} wrote:</p><blockquote style="border-left: 2px solid #ccc; padding-left: 8px; margin-left: 8px;">${msg.html}</blockquote></div>`
      : undefined;
    return {
      mode,
      to,
      cc,
      subject: subj,
      text: quoted,
      quoteText: quoted,
      html: quotedHtml,
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
  const forwardedHtml = msg.html
    ? `<br><br><div class="urmail-forwarded-message"><p>---------- Forwarded message ----------<br>From: ${from}<br>Subject: ${msg.subject || ""}</p><div>${msg.html}</div></div>`
    : undefined;
  return {
    mode,
    to: "",
    subject: subj,
    text: body,
    quoteText: body,
    html: forwardedHtml,
  };
}

/** Resume an IMAP draft into the compose dialog. */
export function composeFromSavedDraft(
  msg: MessageDetail,
  attachments: OutgoingAttachment[] = [],
): ComposeDraft {
  return {
    mode: "draft",
    from: msg.from?.[0]?.email,
    replyTo: msg.reply_to?.[0]?.email,
    inReplyTo: msg.in_reply_to,
    uid: msg.uid,
    uidValidity: msg.uid_validity,
    folderEnc: msg.folder_enc,
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

function plainToHTML(value: string) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/\n/g, "<br>");
}

/** Remove known quoted/signature text while retaining surrounding formatting. */
function removeHTMLText(value: string, text: string, trailing = false) {
  if (!value || !text.trim()) return value;
  const template = document.createElement("template");
  template.innerHTML = value;
  const positions: { node: Text; offset: number }[] = [];
  let compact = "";
  const walker = document.createTreeWalker(
    template.content,
    NodeFilter.SHOW_TEXT,
  );
  while (walker.nextNode()) {
    const node = walker.currentNode as Text;
    for (let offset = 0; offset < node.data.length; offset++)
      if (!/\s/.test(node.data[offset])) {
        compact += node.data[offset];
        positions.push({ node, offset });
      }
  }
  const target = text.replace(/\s/g, "");
  const index = trailing
    ? compact.endsWith(target)
      ? compact.length - target.length
      : -1
    : compact.indexOf(target);
  if (index < 0 || !target) return value;
  const first = positions[index],
    last = positions[index + target.length - 1];
  const range = document.createRange();
  range.setStart(first.node, first.offset);
  range.setEnd(last.node, last.offset + 1);
  range.deleteContents();
  return template.innerHTML;
}

function RecipientField({
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
      <Input
        id={id}
        value={value}
        disabled={disabled}
        placeholder={placeholder}
        autoComplete="off"
        onChange={(event) => onChange(event.target.value)}
        className="h-9 text-xs sm:text-sm"
      />
    </div>
  );
}

let nextComposeIdentity = 0;
const composeIdentities = new WeakMap<ComposeDraft, number>();
export function MailboxComposeDialog(props: MailboxComposeDialogProps) {
  if (!props.open) return null;
  let identity = composeIdentities.get(props.draft);
  if (identity === undefined) {
    identity = ++nextComposeIdentity;
    composeIdentities.set(props.draft, identity);
  }
  return <MailboxComposeEditor key={identity} {...props} />;
}

function MailboxComposeEditor({
  open,
  onOpenChange,
  draft,
  onSent,
  onDraftSaved,
  ref,
  inline = false,
}: MailboxComposeDialogProps) {
  const { t } = useTranslation();
  const translation = useRef(t);
  translation.current = t;
  const [windowState, setWindowState] = useState<
    "normal" | "minimized" | "maximized"
  >("normal");
  const [isSourceMode, setIsSourceMode] = useState(false);
  const [showCc, setShowCc] = useState(Boolean(draft.cc));
  const [showBcc, setShowBcc] = useState(Boolean(draft.bcc));
  const [replyTo, setReplyTo] = useState(draft.replyTo ?? "");
  const [showReplyTo, setShowReplyTo] = useState(Boolean(draft.replyTo));
  const [requestMdn, setRequestMdn] = useState(Boolean(draft.requestMdn));
  const [requestDsn, setRequestDsn] = useState(Boolean(draft.requestDsn));
  const [snippets, setSnippets] = useState<MailboxSnippet[]>([]);
  const [showSnippets, setShowSnippets] = useState(false);
  const editorInstanceRef = useRef<Editor | null>(null);

  const [from, setFrom] = useState(draft.from ?? getMailboxAddress() ?? "");
  const [quoteOriginal, setQuoteOriginal] = useState(true);
  const [identityChoices, setIdentityChoices] = useState<MailboxIdentity[]>([]);
  const [identitySettings, setIdentitySettings] =
    useState<MailboxSettingsView | null>(null);
  const [identitiesLoaded, setIdentitiesLoaded] = useState(false);
  const bodyRevision = useRef(0);
  const recipientRevision = useRef(0);
  const insertedSignature = useRef({ text: "", html: "" });
  const automaticHTMLSignature = useRef("");
  const [to, setTo] = useState(draft.to ?? "");
  const [cc, setCc] = useState(draft.cc ?? "");
  const [bcc, setBcc] = useState(draft.bcc ?? "");
  const [html, setHtml] = useState(draft.html ?? "");
  const [subject, setSubject] = useState(draft.subject ?? "");
  const [text, setText] = useState(draft.text ?? "");
  const [attachments, setAttachments] = useState<OutgoingAttachment[]>(
    draft.attachments ?? [],
  );
  const [draftUncertain, setDraftUncertain] = useState(false);
  const [sendUncertain, setSendUncertain] = useState(false);
  const [sentCopyPending, setSentCopyPending] = useState(false);
  const [cleanupPending, setCleanupPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const [preparingSubmission, setPreparingSubmission] = useState(false);
  const [composing, setComposing] = useState(false);
  const [readingAttachments, setReadingAttachments] = useState(false);
  const readingAttachmentRef = useRef(false);
  const [sendReceipt, setSendReceipt] = useState<SendReceipt | null>(null);
  const [newSendConfirmed, setNewSendConfirmed] = useState(false);
  const [receiptLoaded, setReceiptLoaded] = useState(
    !(draft.uid && draft.uidValidity && draft.folderEnc),
  );
  const [receiptLookupRevision, setReceiptLookupRevision] = useState(0);
  const sendAttempt = useRef<{
    key: string;
    body: Parameters<typeof sendMessage>[0];
  } | null>(null);
  const [savedHint, setSavedHint] = useState(false);
  const hasSavedDraft = useRef(Boolean(draft.uid));
  const [sendLaterAt, setSendLaterAt] = useState("");
  const [scheduledHint, setScheduledHint] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    containerRef.current
      ?.querySelector<HTMLInputElement>("#compose-to")
      ?.focus();
    return () => {
      if (previous?.isConnected) previous.focus();
    };
  }, []);
  const scheduleAttempt = useRef<{
    key: string;
    body: ScheduleSendRequest;
  } | null>(null);
  const [scheduleUncertain, setScheduleUncertain] = useState(false);
  const editingLocked =
    busy ||
    scheduleUncertain ||
    draftUncertain ||
    sendUncertain ||
    sentCopyPending ||
    sendReceipt?.status === "accepted";

  useEffect(() => {
    if (!open) return;
    setTo(draft.to ?? "");
    setQuoteOriginal(!!draft.quoteText);
    setCc(draft.cc ?? "");
    setBcc(draft.bcc ?? "");
    setReplyTo(draft.replyTo ?? "");
    setShowReplyTo(Boolean(draft.replyTo));
    setRequestMdn(Boolean(draft.requestMdn));
    setRequestDsn(Boolean(draft.requestDsn));
    setShowSnippets(false);
    setHtml(draft.html ?? "");
    setSubject(draft.subject ?? "");
    setText(draft.text ?? "");
    setAttachments(draft.attachments ?? []);
    setDraftUncertain(false);
    setSendUncertain(false);
    setSentCopyPending(false);
    setSendReceipt(null);
    sendAttempt.current = null;
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
        if (settings.preferences?.reply_to_address && !draft.replyTo) {
          const prefReplyTo = String(
            settings.preferences.reply_to_address,
          ).trim();
          if (prefReplyTo) {
            setReplyTo(prefReplyTo);
          }
        }

        if (
          draft.mode !== "draft" &&
          settings.preferences?.auto_bcc_enabled &&
          settings.preferences?.auto_bcc_address
        ) {
          const autoBcc = String(settings.preferences.auto_bcc_address).trim();
          if (autoBcc) {
            setBcc((curr) => {
              if (!curr) return autoBcc;
              const list = splitAddrs(curr).map((a) => a.toLowerCase());
              return list.includes(autoBcc.toLowerCase())
                ? curr
                : curr + ", " + autoBcc;
            });
            setShowBcc(true);
          }
        }

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
          if (htmlPart) {
            const original =
              settings.preferences.quote_original === false && draft.quoteText
                ? (draft.text ?? "").replace(draft.quoteText, "")
                : (draft.text ?? "");
            setHtml((value) => (value || plainToHTML(original)) + htmlPart);
          } else if (draft.html && textPart) {
            setHtml((value) => value + plainToHTML(textPart));
          }
          insertedSignature.current = { text: textPart, html: htmlPart };
        }
      })
      .catch(() => {
        if (active) {
          const primary = getMailboxAddress() ?? "";
          setIdentityChoices(
            primary ? [{ address: primary, primary: true }] : [],
          );
          setError(translation.current("page.webmail.settings_load_failed"));
        }
      });
    const snippetPromise = listMailboxSnippets?.();
    if (snippetPromise && typeof snippetPromise.then === "function") {
      void snippetPromise
        .then((res) => {
          if (active && res) setSnippets(res.snippets ?? []);
        })
        .catch(() => {});
    }
    return () => {
      active = false;
    };
  }, [open, draft]);

  const handleInsertSnippet = (content: string) => {
    if (editorInstanceRef.current && !isSourceMode) {
      try {
        editorInstanceRef.current.chain().insertContent(content).run();
      } catch {
        editorInstanceRef.current.commands.insertContent(content);
      }
    } else {
      const textarea =
        containerRef.current?.querySelector<HTMLTextAreaElement>(
          "textarea[aria-label]",
        ) || (document.getElementById("compose-body") as HTMLTextAreaElement);
      if (textarea) {
        const start = textarea.selectionStart || 0;
        const end = textarea.selectionEnd || 0;
        const val = textarea.value;
        const next = val.slice(0, start) + content + val.slice(end);
        setText(next);
      } else {
        setText((val) => val + "\n" + content);
      }
    }
    setShowSnippets(false);
  };

  const handleInsertSignature = () => {
    const profile =
      identitySettings?.identity_profiles?.[from] ??
      (from === identitySettings?.address ? identitySettings : null);
    const sigText =
      profile?.signature_text || identitySettings?.signature_text || "";
    const sigHtml =
      profile?.signature_html ||
      identitySettings?.signature_html ||
      (sigText ? plainToHTML(sigText) : "");
    if (editorInstanceRef.current && !isSourceMode) {
      const toInsert = sigHtml
        ? `<br><br>-- <br>${sigHtml}`
        : sigText
          ? `<br><br>-- <br>${plainToHTML(sigText)}`
          : "";
      if (toInsert) {
        try {
          editorInstanceRef.current.chain().insertContent(toInsert).run();
        } catch {
          editorInstanceRef.current.commands.insertContent(toInsert);
        }
      }
    } else {
      const toInsert = sigText ? `\n\n-- \n${sigText}` : "";
      if (toInsert) {
        setText((val) => val + toInsert);
      }
    }
  };

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
    setHtml((value) => {
      if (!value)
        return nextHtml
          ? plainToHTML(old.text ? text.replace(old.text, "") : text) + nextHtml
          : value;
      const withoutOld =
        old.html && value.includes(old.html)
          ? value.replace(old.html, "")
          : removeHTMLText(value, old.text, true);
      return withoutOld + (nextHtml || plainToHTML(nextText));
    });
    insertedSignature.current = { text: nextText, html: nextHtml };
  };

  const toggleQuote = (include: boolean) => {
    bodyRevision.current++;
    setQuoteOriginal(include);
    if (!draft.quoteText) return;
    setHtml((value) =>
      !value
        ? value
        : include
          ? value + plainToHTML(draft.quoteText!)
          : removeHTMLText(value, draft.quoteText!),
    );
    setText((value) => {
      const updated = include
        ? value.includes(draft.quoteText!)
          ? value
          : value + draft.quoteText!
        : value.replace(draft.quoteText!, "");

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
    if (!files?.length || readingAttachmentRef.current) return;
    readingAttachmentRef.current = true;
    setError(null);
    setReadingAttachments(true);
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
    setReadingAttachments(false);
    readingAttachmentRef.current = false;
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

  const toggleSourceMode = () => setIsSourceMode((value) => !value);

  const outgoing = {
    from,
    in_reply_to: draft.inReplyTo,
    reply_source: draft.aiSource,
    to: splitAddrs(to),
    cc: splitAddrs(cc),
    bcc: splitAddrs(bcc),
    html: outgoingHTML(text, html),
    subject,
    text,
    attachments: attachments.length ? attachments : undefined,
    ...(replyTo.trim() ? { reply_to: replyTo.trim() } : {}),
    ...(requestMdn ? { request_mdn: true } : {}),
    ...(requestDsn ? { request_dsn: true } : {}),
  };
  const initialOutgoing = useRef(outgoing);
  const hasContent = Boolean(
    to.trim() ||
    cc.trim() ||
    bcc.trim() ||
    replyTo.trim() ||
    subject.trim() ||
    text.trim() ||
    html.trim() ||
    attachments.length,
  );
  const persistence = useDraftPersistence({
    enabled:
      open &&
      identitiesLoaded &&
      receiptLoaded &&
      (hasContent || hasSavedDraft.current) &&
      !identityInvalid,
    value: outgoing,
    initialValue: initialOutgoing.current,
    initialResult:
      draft.uid && draft.uidValidity
        ? ({
            uid: draft.uid,
            uid_validity: draft.uidValidity,
            name_enc: draft.folderEnc ?? "",
            folder: "Drafts",
            saved: true,
          } as Awaited<ReturnType<typeof saveDraft>>)
        : undefined,
    blocked: Boolean(editingLocked || composing || readingAttachments),
    save: async (value, previous, key) => {
      const result = await saveDraft(
        { ...value, uid: previous?.uid, uid_validity: previous?.uid_validity },
        key,
      );
      if (!result.uid || !result.uid_validity)
        throw new Error("DRAFT_REFERENCE_MISSING");
      setCleanupPending(Boolean(result.cleanup_pending));
      if (result.cleanup_pending)
        throw new ApiRequestError(409, {
          error: {
            code: "DRAFT_SAVE_UNCERTAIN",
            message: "error.DRAFT_SAVE_UNCERTAIN",
          },
        });
      hasSavedDraft.current = true;
      onDraftSaved?.(result.uid);
      setError(null);
      setSavedHint(true);
      return result;
    },
    remove: (previous) => deleteDraft(previous.uid!, previous.uid_validity!),
    uncertain: (cause) =>
      !(cause instanceof ApiRequestError) ||
      cause.code === "DRAFT_SAVE_UNCERTAIN" ||
      cause.status >= 500,
    onError: (cause) =>
      setError(
        cause instanceof ApiRequestError
          ? t(`error.${cause.code}`, {
              defaultValue: t("page.webmail.compose_draft_failed"),
            })
          : t("error.DRAFT_SAVE_UNCERTAIN"),
      ),
  });
  const onSaveDraft = async (proposal?: {
    subject: string;
    text: string;
    html: string;
  }) => {
    if (proposal) {
      // Save the adopted revision immediately, through the same serialized writer.
      setSubject(proposal.subject);
      setText(proposal.text);
      setHtml(proposal.html);
      return persistence.flushValue({
        ...outgoing,
        ...proposal,
        html: outgoingHTML(proposal.text, proposal.html),
      });
    }
    setError(null);
    if (!hasContent && !hasSavedDraft.current) return true;
    return persistence.flush();
  };
  useImperativeHandle(ref, () => ({
    flush: async () => {
      if (!open || (!hasContent && !hasSavedDraft.current)) return true;
      if (!receiptLoaded) return false;
      if (submitting.current) return false;
      if (sendReceipt && sendReceipt.status !== "rejected") return true;
      return Boolean(await persistence.flush());
    },
  }));

  const applyReceipt = (receipt: SendReceipt) => {
    setSendReceipt(receipt);
    if (receipt.status === "accepted") {
      setSendUncertain(false);
      setSentCopyPending(receipt.sent_copy_state !== "complete");
      if (receipt.sent_copy_state === "complete") onOpenChange(false);
      onSent();
    } else if (receipt.status === "rejected") {
      setSendUncertain(false);
      sendAttempt.current = null;
      setError(t("page.webmail.compose_failed"));
    } else {
      setSendUncertain(true);
      setError(t("error.MAILBOX_SEND_UNCERTAIN"));
    }
  };
  useEffect(() => {
    if (!draft.uid || !draft.uidValidity || !draft.folderEnc) return;
    const controller = new AbortController();
    setReceiptLoaded(false);
    const source = {
      folder_enc: draft.folderEnc,
      uid: draft.uid,
      uid_validity: draft.uidValidity,
    };
    void getSendReceiptForDraft(source, controller.signal)
      .then((receipt) => {
        if (controller.signal.aborted) return;
        if (receipt && receipt.status !== "rejected") {
          setSendReceipt(receipt);
          setSentCopyPending(
            receipt.status === "accepted" &&
              receipt.sent_copy_state !== "complete",
          );
          setSendUncertain(receipt.status !== "accepted");
          sendAttempt.current = {
            key: receipt.request_key,
            body: {
              from: draft.from,
              to: splitAddrs(draft.to ?? ""),
              cc: splitAddrs(draft.cc ?? ""),
              bcc: splitAddrs(draft.bcc ?? ""),
              subject: draft.subject ?? "",
              text: draft.text ?? "",
              html: draft.html,
              in_reply_to: draft.inReplyTo,
              attachments: draft.attachments,
              draft_source: source,
            },
          };
        }
        setReceiptLoaded(true);
      })
      .catch(() => {
        if (!controller.signal.aborted)
          setError(
            translation.current("page.webmail.compose_receipt_load_failed"),
          );
      });
    return () => controller.abort();
  }, [draft, receiptLookupRevision]);
  const reconcileSend = async () => {
    if (!sendAttempt.current || busy) return;
    setBusy(true);
    try {
      const receipt = await getSendReceiptByRequestKey(sendAttempt.current.key);
      if (receipt) applyReceipt(receipt);
      else setError(t("error.MAILBOX_SEND_UNCERTAIN"));
    } catch {
      setError(t("error.MAILBOX_SEND_UNCERTAIN"));
    } finally {
      setBusy(false);
    }
  };
  const startNewSend = async () => {
    if (
      !identitiesLoaded ||
      identityInvalid ||
      !receiptLoaded ||
      !newSendConfirmed ||
      sendReceipt?.status !== "uncertain" ||
      !sendAttempt.current ||
      submitting.current
    )
      return;
    submitting.current = true;
    setBusy(true);
    setNewSendConfirmed(false);
    const attempt = {
      key: crypto.randomUUID(),
      body: {
        ...sendAttempt.current.body,
        acknowledged_uncertain_receipt_id: sendReceipt.id,
      },
    };
    sendAttempt.current = attempt;
    try {
      const result = await sendMessage(attempt.body, attempt.key);
      if (result.receipt) applyReceipt(result.receipt);
      else {
        setSendUncertain(true);
        setError(t("error.MAILBOX_SEND_UNCERTAIN"));
      }
    } catch {
      setSendUncertain(true);
      setError(t("error.MAILBOX_SEND_UNCERTAIN"));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };
  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (
      editingLocked ||
      identityInvalid ||
      !identitiesLoaded ||
      composing ||
      readingAttachments ||
      persistence.status === "save_uncertain"
    )
      return;
    setError(null);
    if (
      splitAddrs(to).length + splitAddrs(cc).length + splitAddrs(bcc).length ===
      0
    ) {
      setError(t("page.webmail.compose_need_to"));
      return;
    }
    if (replyTo.trim()) {
      const parts = splitAddrs(replyTo);
      const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
      const invalid = parts.find((a) => !emailRegex.test(a));
      if (invalid) {
        setError(
          t("page.webmail.invalid_reply_to", {
            defaultValue: "Invalid Reply-To address",
          }),
        );
        return;
      }
    }
    if (submitting.current) return;
    submitting.current = true;
    setPreparingSubmission(true);
    if (!(await persistence.flush())) {
      submitting.current = false;
      setPreparingSubmission(false);
      return;
    }
    const saved = persistence.currentResult();
    if (!saved?.uid || !saved.uid_validity || !saved.name_enc) {
      setError(t("page.webmail.compose_draft_failed"));
      submitting.current = false;
      setPreparingSubmission(false);
      return;
    }
    const attempt = {
      key: crypto.randomUUID(),
      body: {
        ...persistence.persistedValue(),
        draft_source: {
          folder_enc: saved.name_enc,
          uid: saved.uid,
          uid_validity: saved.uid_validity,
        },
        ...(sendReceipt?.status === "uncertain" && newSendConfirmed
          ? { acknowledged_uncertain_receipt_id: sendReceipt.id }
          : {}),
      },
    };
    sendAttempt.current = attempt;
    setBusy(true);
    try {
      const res = await sendMessage(attempt.body, attempt.key);
      if (res.receipt) applyReceipt(res.receipt);
      else {
        // Compatibility with the previous success response; pending still locks editing.
        setSentCopyPending(res.sent_copy === "pending");
        if (res.sent_copy !== "pending") onOpenChange(false);
        onSent();
      }
    } catch (err) {
      // Once submission starts, recover its durable receipt before offering a new attempt.
      setSendUncertain(true);
      setError(
        err instanceof ApiRequestError
          ? t(`error.${err.code}`, {
              defaultValue: t("error.MAILBOX_SEND_UNCERTAIN"),
            })
          : t("error.MAILBOX_SEND_UNCERTAIN"),
      );
      try {
        const receipt = await getSendReceiptByRequestKey(attempt.key);
        if (receipt) applyReceipt(receipt);
      } catch {
        /* Preserve the original attempt and recovery controls. */
      }
    } finally {
      setBusy(false);
      submitting.current = false;
      setPreparingSubmission(false);
    }
  };

  const onSchedule = async () => {
    if (
      busy ||
      submitting.current ||
      draftUncertain ||
      sendUncertain ||
      !identitiesLoaded
    )
      return;
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
    submitting.current = true;
    setPreparingSubmission(true);
    try {
      if (!scheduleAttempt.current) {
        if (!(await persistence.flush())) return;
        const saved = persistence.currentResult();
        if (!saved?.uid || !saved.uid_validity)
          throw new Error("DRAFT_REFERENCE_MISSING");
        scheduleAttempt.current = {
          key: crypto.randomUUID(),
          body: {
            draft_uid: saved.uid,
            draft_uidvalidity: saved.uid_validity,
            send_at: new Date(sendLaterAt).toISOString(),
          },
        };
      }
      setBusy(true);
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
      submitting.current = false;
      setPreparingSubmission(false);
    }
  };

  const handleCloseWithAutoSave = async () => {
    if (sendReceipt && sendReceipt.status !== "rejected") {
      onOpenChange(false);
      return;
    }
    if (
      scheduleUncertain ||
      busy ||
      submitting.current ||
      readingAttachments ||
      composing
    )
      return;
    if ((hasContent || hasSavedDraft.current) && !(await persistence.flush()))
      return;
    onOpenChange(false);
  };
  const handleDiscard = async () => {
    if (
      sendReceipt?.status === "accepted" &&
      sendReceipt.sent_copy_state === "complete"
    ) {
      const saved = persistence.currentResult();
      if (!saved?.uid || !saved.uid_validity) return;
      try {
        await deleteDraft(saved.uid, saved.uid_validity);
        onOpenChange(false);
      } catch {
        setError(t("page.webmail.compose_draft_failed"));
      }
      return;
    }
    if (editingLocked || submitting.current || composing || readingAttachments)
      return;
    if (await persistence.discard()) onOpenChange(false);
  };

  const fieldsLocked =
    editingLocked ||
    !receiptLoaded ||
    preparingSubmission ||
    readingAttachments ||
    persistence.status === "save_uncertain" ||
    persistence.status === "discarding";

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
        ref={containerRef}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            void handleCloseWithAutoSave();
          }
          if (event.key !== "Tab" || windowState !== "maximized") return;
          const controls = [
            ...(containerRef.current?.querySelectorAll<HTMLElement>(
              'button:not([disabled]),input:not([disabled]),textarea:not([disabled]),select:not([disabled]),[contenteditable="true"],[tabindex="0"]',
            ) ?? []),
          ].filter(
            (element) =>
              element.getClientRects().length &&
              element.getAttribute("aria-hidden") !== "true",
          );
          const first = controls[0],
            last = controls.at(-1);
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last?.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first?.focus();
          }
        }}
        role="dialog"
        aria-modal={windowState === "maximized"}
        aria-labelledby="compose-title"
        className={
          inline && windowState === "normal"
            ? "relative w-full bg-card border border-border rounded-xl flex flex-col overflow-hidden"
            : windowState === "minimized"
              ? "fixed right-3 sm:right-6 bottom-0 z-50 w-72 sm:w-80 h-11 bg-card border border-border shadow-xl rounded-t-lg flex items-center justify-between px-3 cursor-pointer select-none transition-all duration-200"
              : windowState === "maximized"
                ? "fixed inset-2 sm:inset-6 z-50 bg-card border border-border shadow-2xl rounded-xl flex flex-col overflow-hidden transition-all duration-200"
                : "fixed right-2 sm:right-6 bottom-0 z-50 w-full sm:w-[680px] max-w-[calc(100vw-16px)] h-[620px] max-h-[85dvh] bg-card border border-border shadow-2xl rounded-t-xl flex flex-col overflow-hidden transition-all duration-200"
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
            {!receiptLoaded && (
              <Button
                type="button"
                variant="outline"
                onClick={() => setReceiptLookupRevision((value) => value + 1)}
              >
                {t("page.webmail.compose_check_receipt")}
              </Button>
            )}
            {sendReceipt?.status === "accepted" &&
              sendReceipt.sent_copy_state === "complete" && (
                <p role="status">{t("page.webmail.compose_already_sent")}</p>
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
            {(sendUncertain || sentCopyPending) && (
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => void reconcileSend()}
              >
                {t("page.webmail.compose_check_receipt")}
              </Button>
            )}
            {sendReceipt?.status === "uncertain" && (
              <div className="space-y-2">
                <label className="flex gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={newSendConfirmed}
                    onChange={(e) => setNewSendConfirmed(e.target.checked)}
                  />
                  {t("page.webmail.compose_confirm_new_send")}
                </label>
                <Button
                  type="button"
                  disabled={!newSendConfirmed || busy}
                  onClick={() => void startNewSend()}
                >
                  {t("page.webmail.compose_prepare_new_send")}
                </Button>
              </div>
            )}
            {sentCopyPending && (
              <p role="alert" className="text-xs sm:text-sm text-destructive">
                {t("page.webmail.compose_sent_copy_pending")}
              </p>
            )}
            {(persistence.status === "saving" ||
              persistence.status === "dirty") && (
              <p role="status" className="text-xs text-muted-foreground">
                {t(`page.webmail.draft_${persistence.status}`)}
              </p>
            )}
            {persistence.status === "save_uncertain" && (
              <Button
                type="button"
                variant="outline"
                onClick={() => void persistence.flush(true)}
              >
                {t("page.webmail.compose_reconcile_draft")}
              </Button>
            )}
            {readingAttachments && (
              <p role="status">
                {t("page.webmail.compose_reading_attachments")}
              </p>
            )}
            {savedHint &&
              !error &&
              (persistence.status === "saved" ||
                persistence.status === "clean") && (
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
                  disabled={fieldsLocked}
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
              disabled={fieldsLocked}
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
                disabled={fieldsLocked || !identitiesLoaded}
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
            <RecipientField
              id="compose-to"
              label={t("page.webmail.compose_to")}
              value={to}
              disabled={fieldsLocked}
              placeholder="recipient@example.com"
              onChange={(val) => {
                recipientRevision.current++;
                setTo(val);
              }}
              rightAction={
                <div className="flex items-center gap-1.5 text-xs">
                  {!showReplyTo && (
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-foreground px-1 py-0.5 rounded hover:bg-muted font-medium transition-colors cursor-pointer"
                      onClick={() => setShowReplyTo(true)}
                    >
                      Reply-To
                    </button>
                  )}
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

            <div className={showReplyTo ? "block" : "hidden"}>
              <RecipientField
                id="compose-reply-to"
                label={t("page.webmail.compose_reply_to", {
                  defaultValue: "Reply-To",
                })}
                value={replyTo}
                disabled={fieldsLocked}
                placeholder="reply-to@example.com"
                onChange={(val) => {
                  recipientRevision.current++;
                  setReplyTo(val);
                }}
                rightAction={
                  !replyTo && (
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-foreground text-xs cursor-pointer"
                      onClick={() => setShowReplyTo(false)}
                    >
                      {t("action.close")}
                    </button>
                  )
                }
              />
            </div>

            <div className={showCc ? "block" : "hidden"}>
              <RecipientField
                id="compose-cc"
                label={t("page.webmail.compose_cc")}
                value={cc}
                disabled={fieldsLocked}
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
              <RecipientField
                id="compose-bcc"
                label={t("page.webmail.compose_bcc")}
                value={bcc}
                disabled={fieldsLocked}
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
                disabled={fieldsLocked}
                value={subject}
                onChange={(e) => setSubject(e.target.value)}
                className="h-9 text-xs sm:text-sm font-medium"
              />
            </div>

            {/* Rich Text Editor Options & Actions Bar */}
            <div className="flex flex-wrap items-center justify-between gap-2 px-1 py-1 text-xs text-muted-foreground border-y border-border/40">
              <div className="flex items-center gap-1.5">
                {/* Personal Canned Snippets */}
                <div className="relative">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    id="compose-snippets-btn"
                    className="h-7 px-2 text-xs gap-1.5 cursor-pointer hover:bg-muted"
                    disabled={fieldsLocked}
                    title={t("page.webmail.compose_snippets", {
                      defaultValue: "Personal snippets",
                    })}
                    aria-label={t("page.webmail.compose_snippets", {
                      defaultValue: "Personal snippets",
                    })}
                    onClick={() => setShowSnippets(!showSnippets)}
                  >
                    <FileText className="h-3.5 w-3.5" />
                    <span>
                      {t("page.webmail.compose_snippets", {
                        defaultValue: "Snippets",
                      })}
                    </span>
                  </Button>

                  {showSnippets && (
                    <div className="absolute left-0 top-8 z-50 w-64 max-h-56 overflow-y-auto rounded-md border border-border bg-popover p-1.5 shadow-lg text-xs">
                      <div className="px-2 py-1 font-semibold text-muted-foreground border-b border-border/50 flex justify-between items-center">
                        <span>
                          {t("page.webmail.compose_snippets_title", {
                            defaultValue: "Canned Snippets",
                          })}
                        </span>
                        <button
                          type="button"
                          className="text-muted-foreground hover:text-foreground cursor-pointer"
                          onClick={() => setShowSnippets(false)}
                        >
                          ✕
                        </button>
                      </div>
                      {snippets.length === 0 ? (
                        <div className="p-3 text-center text-muted-foreground">
                          {t("page.webmail.no_snippets", {
                            defaultValue: "No snippets found",
                          })}
                        </div>
                      ) : (
                        snippets.map((snip) => (
                          <button
                            key={snip.id}
                            type="button"
                            className="w-full text-left px-2 py-1.5 rounded hover:bg-muted cursor-pointer transition-colors block"
                            onClick={() => handleInsertSnippet(snip.content)}
                          >
                            <div className="font-medium text-foreground truncate">
                              {snip.title}
                            </div>
                            <div className="text-[11px] text-muted-foreground line-clamp-1 truncate">
                              {snip.content}
                            </div>
                          </button>
                        ))
                      )}
                    </div>
                  )}
                </div>

                {/* Visual signature insertion */}
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  id="compose-signature-btn"
                  className="h-7 px-2 text-xs gap-1.5 cursor-pointer hover:bg-muted"
                  disabled={fieldsLocked}
                  title={t("page.webmail.compose_insert_signature", {
                    defaultValue: "Insert signature",
                  })}
                  aria-label={t("page.webmail.compose_insert_signature", {
                    defaultValue: "Insert signature",
                  })}
                  onClick={handleInsertSignature}
                >
                  <PenLine className="h-3.5 w-3.5" />
                  <span>
                    {t("page.webmail.compose_signature", {
                      defaultValue: "Signature",
                    })}
                  </span>
                </Button>
              </div>

              {/* MDN / DSN Request Checkboxes */}
              <div className="flex items-center gap-3">
                <label className="flex items-center gap-1.5 cursor-pointer hover:text-foreground select-none">
                  <input
                    type="checkbox"
                    id="compose-request-mdn"
                    checked={requestMdn}
                    disabled={fieldsLocked}
                    onChange={(e) => setRequestMdn(e.target.checked)}
                    className="rounded border-border text-primary focus:ring-primary h-3.5 w-3.5 cursor-pointer"
                  />
                  <span>
                    {t("page.webmail.compose_request_mdn", {
                      defaultValue: "Read receipt (MDN)",
                    })}
                  </span>
                </label>
                <label className="flex items-center gap-1.5 cursor-pointer hover:text-foreground select-none">
                  <input
                    type="checkbox"
                    id="compose-request-dsn"
                    checked={requestDsn}
                    disabled={fieldsLocked}
                    onChange={(e) => setRequestDsn(e.target.checked)}
                    className="rounded border-border text-primary focus:ring-primary h-3.5 w-3.5 cursor-pointer"
                  />
                  <span>
                    {t("page.webmail.compose_request_dsn", {
                      defaultValue: "Delivery status (DSN)",
                    })}
                  </span>
                </label>
              </div>
            </div>

            {/* Rich Text Editor via Tiptap */}
            <div className="space-y-1.5">
              <MailboxEditor
                initialContent={html || text}
                isHtml={Boolean(html)}
                onCompositionChange={setComposing}
                disabled={fieldsLocked}
                isSourceMode={isSourceMode}
                onToggleSourceMode={toggleSourceMode}
                onEditorReady={(ed) => {
                  editorInstanceRef.current = ed;
                }}
                onPasteAttachment={(att) => {
                  setAttachments((prev) => [...prev, att]);
                }}
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
                disabled={fieldsLocked}
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
                  disabled={fieldsLocked}
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
                  fieldsLocked || attachments.length >= MAX_SEND_ATTACHMENTS
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
                        disabled={fieldsLocked}
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
                disabled={fieldsLocked}
                value={sendLaterAt}
                onChange={(e) => setSendLaterAt(e.target.value)}
                className="h-8 text-xs max-w-xs"
              />
            </div>
          </div>

          {/* Footer Bar */}
          <div className="px-4 pt-2.5 pb-[max(0.625rem,env(safe-area-inset-bottom))] bg-muted/20 border-t flex items-center justify-between gap-2 shrink-0">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void handleCloseWithAutoSave()}
              disabled={busy || scheduleUncertain}
            >
              {t("action.cancel")}
            </Button>
            <div className="flex items-center gap-2 flex-wrap">
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => void handleDiscard()}
                disabled={
                  (fieldsLocked &&
                    !(
                      sendReceipt?.status === "accepted" &&
                      sendReceipt.sent_copy_state === "complete"
                    )) ||
                  composing ||
                  readingAttachments ||
                  persistence.status === "save_uncertain"
                }
              >
                {t("page.webmail.compose_discard")}
              </Button>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => void onSaveDraft()}
                disabled={fieldsLocked || !identitiesLoaded}
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
                disabled={fieldsLocked || !identitiesLoaded}
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
