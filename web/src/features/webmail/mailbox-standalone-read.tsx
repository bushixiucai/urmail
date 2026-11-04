import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Printer,
  Reply,
  ReplyAll,
  Forward,
  Download,
  AlertCircle,
  X,
  ShieldCheck,
  ShieldAlert,
  Image as ImageIcon,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  getMessage,
  downloadAttachment,
  hasMailboxSession,
  setMailboxSession,
  getMailboxToken,
  getMailboxAddress,
  getMailboxExpiresAt,
  type MessageDetail,
} from "./mailbox-api";
import {
  MailboxComposeDialog,
  draftFromMessage,
  type ComposeDraft,
} from "./mailbox-compose-dialog";
import { sanitizeHtmlMail } from "./sanitize-html-mail";

const SESSION_CHANNEL = "urmail.mailbox-session-sync";

/**
 * Custom hook to securely handle session handover without tokens in URL parameters.
 * Listens on BroadcastChannel and postMessage from window.opener.
 */
export function useStandaloneSessionHandover() {
  const [sessionReady, setSessionReady] = useState(hasMailboxSession());
  const [handoverTimedOut, setHandoverTimedOut] = useState(false);

  useEffect(() => {
    if (hasMailboxSession()) {
      setSessionReady(true);
      return;
    }

    let channel: BroadcastChannel | null = null;
    try {
      channel = new BroadcastChannel(SESSION_CHANNEL);
      channel.onmessage = (event) => {
        if (
          event.data?.type === "SESSION_RESPONSE" &&
          event.data.session?.token
        ) {
          setMailboxSession(event.data.session, false);
          setSessionReady(true);
        }
      };
      channel.postMessage({ type: "REQUEST_SESSION" });
    } catch {
      // BroadcastChannel unsupported
    }

    const handleMessage = (event: MessageEvent) => {
      if (
        event.origin === window.location.origin &&
        event.data?.type === "SESSION_RESPONSE" &&
        event.data.session?.token
      ) {
        setMailboxSession(event.data.session, false);
        setSessionReady(true);
      }
    };
    window.addEventListener("message", handleMessage);

    if (window.opener) {
      window.opener.postMessage(
        { type: "REQUEST_SESSION" },
        window.location.origin,
      );
    }

    const timer = setTimeout(() => {
      if (!hasMailboxSession()) {
        setHandoverTimedOut(true);
      }
    }, 2500);

    return () => {
      clearTimeout(timer);
      channel?.close();
      window.removeEventListener("message", handleMessage);
    };
  }, []);

  return { sessionReady, handoverTimedOut };
}

/** Open message in standalone tab without tokens in URL */
export function openStandaloneRead(folderEnc: string, uid: number) {
  const url = `/webmail/read?folder=${encodeURIComponent(folderEnc)}&uid=${uid}`;
  const win = window.open(url, "_blank");
  if (win && hasMailboxSession()) {
    setTimeout(() => {
      const token = getMailboxToken();
      const address = getMailboxAddress();
      const expires_at = getMailboxExpiresAt();
      if (token && address) {
        win.postMessage(
          {
            type: "SESSION_RESPONSE",
            session: { token, address, expires_at: expires_at ?? "" },
          },
          window.location.origin,
        );
      }
    }, 100);
  }
  return win;
}

export function MailboxStandaloneRead() {
  const { t } = useTranslation();
  const { sessionReady, handoverTimedOut } = useStandaloneSessionHandover();

  const [message, setMessage] = useState<MessageDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [loadImages, setLoadImages] = useState(false);

  const [composeOpen, setComposeOpen] = useState(false);
  const [composeDraft, setComposeDraft] = useState<ComposeDraft | null>(null);

  // Parse folder and uid from URL query parameters (no sensitive credentials)
  const searchParams = new URLSearchParams(
    typeof window !== "undefined" ? window.location.search : "",
  );
  const folderEnc = searchParams.get("folder") || "INBOX";
  const uidStr = searchParams.get("uid");
  const uid = uidStr ? parseInt(uidStr, 10) : 0;

  useEffect(() => {
    if (!sessionReady) return;
    if (!uid) {
      setError(
        t("page.webmail.invalid_message_params", "Invalid message parameters"),
      );
      setLoading(false);
      return;
    }

    let active = true;
    setLoading(true);
    setError(null);

    getMessage(folderEnc, uid, { loadImages })
      .then((msg) => {
        if (!active) return;
        setMessage(msg);
        setLoading(false);
      })
      .catch((err) => {
        if (!active) return;
        setError(
          err instanceof Error
            ? err.message
            : t("page.webmail.message_load_failed", "Failed to load message"),
        );
        setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [sessionReady, folderEnc, uid, loadImages, t]);

  const handlePrint = () => {
    window.print();
  };

  const handleReply = (mode: "reply" | "reply-all" | "forward") => {
    if (!message) return;
    const draft = draftFromMessage(mode, message);
    setComposeDraft(draft);
    setComposeOpen(true);
  };

  const handleClose = () => {
    window.close();
  };

  if (!sessionReady) {
    if (handoverTimedOut) {
      return (
        <div className="min-h-screen flex items-center justify-center bg-background p-6">
          <div className="max-w-md w-full bg-card border rounded-lg p-6 text-center space-y-4">
            <AlertCircle className="h-10 w-10 text-destructive mx-auto" />
            <h1 className="text-lg font-semibold">
              {t("page.webmail.session_required", "Session Handover Required")}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t(
                "page.webmail.session_handover_failed",
                "Could not inherit webmail session. Please open webmail from the main dashboard.",
              )}
            </p>
            <Button onClick={() => (window.location.href = "/webmail/login")}>
              {t("page.webmail.login_title", "Go to Login")}
            </Button>
          </div>
        </div>
      );
    }
    return (
      <div className="min-h-screen flex items-center justify-center bg-background text-sm text-muted-foreground">
        {t("state.loading", "Synchronizing session...")}
      </div>
    );
  }

  if (loading) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background text-sm text-muted-foreground">
        {t("state.loading", "Loading message...")}
      </div>
    );
  }

  if (error || !message) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background p-6">
        <div className="max-w-md w-full bg-card border rounded-lg p-6 text-center space-y-4">
          <AlertCircle className="h-10 w-10 text-destructive mx-auto" />
          <h1 className="text-lg font-semibold">
            {t("page.webmail.message_error", "Message Unavailable")}
          </h1>
          <p className="text-sm text-muted-foreground">{error}</p>
          <Button variant="outline" onClick={handleClose}>
            {t("action.close", "Close")}
          </Button>
        </div>
      </div>
    );
  }

  const sanitizedHtml = message.html
    ? sanitizeHtmlMail(message.html, { allowRemoteImages: loadImages })
    : "";

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col">
      {/* Top Header Toolbar */}
      <header className="border-b bg-card/60 px-4 py-3 flex items-center justify-between gap-3 sticky top-0 z-10 backdrop-blur-xs">
        <div className="flex items-center gap-2 min-w-0">
          <span className="font-semibold text-sm truncate max-w-md">
            {message.subject || t("page.webmail.no_subject", "(No Subject)")}
          </span>
          <span className="text-xs px-2 py-0.5 rounded-full bg-muted text-muted-foreground">
            {folderEnc}
          </span>
        </div>

        <div className="flex items-center gap-1.5 shrink-0">
          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1 text-xs cursor-pointer"
            onClick={() => handleReply("reply")}
          >
            <Reply className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">
              {t("page.webmail.reply", "Reply")}
            </span>
          </Button>

          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1 text-xs cursor-pointer"
            onClick={() => handleReply("reply-all")}
          >
            <ReplyAll className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">
              {t("page.webmail.reply_all", "Reply All")}
            </span>
          </Button>

          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1 text-xs cursor-pointer"
            onClick={() => handleReply("forward")}
          >
            <Forward className="h-3.5 w-3.5" />
            <span className="hidden sm:inline">
              {t("page.webmail.forward", "Forward")}
            </span>
          </Button>

          <Button
            size="sm"
            variant="ghost"
            className="h-8 w-8 p-0 cursor-pointer"
            title={t("page.webmail.print", "Print")}
            onClick={handlePrint}
          >
            <Printer className="h-4 w-4" />
          </Button>

          <Button
            size="sm"
            variant="ghost"
            className="h-8 w-8 p-0 cursor-pointer"
            title={t("action.close", "Close")}
            onClick={handleClose}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </header>

      {/* Message Metadata Header */}
      <div className="px-6 py-4 border-b bg-card space-y-3">
        <h1 className="text-xl font-bold">
          {message.subject || t("page.webmail.no_subject", "(No Subject)")}
        </h1>

        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
          <div className="flex items-center gap-2">
            <span className="font-semibold text-foreground text-sm">
              {message.from?.[0]?.name
                ? `${message.from[0].name} <${message.from[0].email}>`
                : message.from?.[0]?.email}
            </span>
            {message.authentication && (
              <span
                className="flex items-center gap-1 text-[11px] px-1.5 py-0.5 rounded"
                title={message.authentication.reason}
              >
                {message.authentication.status === "pass" ? (
                  <span className="text-green-600 flex items-center gap-0.5">
                    <ShieldCheck className="h-3.5 w-3.5" /> DKIM
                  </span>
                ) : message.authentication.status === "fail" ? (
                  <span className="text-destructive flex items-center gap-0.5">
                    <ShieldAlert className="h-3.5 w-3.5" /> DKIM Fail
                  </span>
                ) : null}
              </span>
            )}
          </div>
          <div>
            {message.date ? new Date(message.date).toLocaleString() : ""}
          </div>
        </div>

        <div className="text-xs text-muted-foreground space-y-0.5">
          <div>
            <span className="font-medium text-foreground">
              {t("page.webmail.to", "To")}
              {": "}
            </span>
            {message.to?.map((a) => a.email).join(", ") || "-"}
          </div>
          {message.cc && message.cc.length > 0 && (
            <div>
              <span className="font-medium text-foreground">
                {t("page.webmail.cc", "Cc")}
                {": "}
              </span>
              {message.cc.map((a) => a.email).join(", ")}
            </div>
          )}
          {message.reply_to && message.reply_to.length > 0 && (
            <div>
              <span className="font-medium text-foreground">
                {t("page.webmail.reply_to", "Reply-To")}
                {": "}
              </span>
              {message.reply_to.map((a) => a.email).join(", ")}
            </div>
          )}
        </div>

        {/* Remote image banner */}
        {!loadImages && (
          <div className="flex items-center justify-between bg-muted/40 p-2 rounded-md border text-xs">
            <div className="flex items-center gap-2">
              <ImageIcon className="h-4 w-4 text-muted-foreground" />
              <span>
                {t(
                  "page.webmail.remote_images_blocked",
                  "Remote images are blocked to protect your privacy.",
                )}
              </span>
            </div>
            <Button
              size="sm"
              variant="outline"
              className="h-7 text-xs cursor-pointer"
              onClick={() => setLoadImages(true)}
            >
              {t("page.webmail.load_images", "Load Images")}
            </Button>
          </div>
        )}

        {/* Attachments */}
        {message.attachments && message.attachments.length > 0 && (
          <div className="pt-2 border-t flex flex-wrap gap-2">
            {message.attachments.map((att) => (
              <Button
                key={att.part_id}
                size="sm"
                variant="outline"
                className="h-7 text-xs gap-1.5 cursor-pointer"
                onClick={() =>
                  downloadAttachment(
                    folderEnc,
                    message.uid,
                    att.part_id,
                    att.filename,
                    message.uid_validity,
                  )
                }
              >
                <Download className="h-3 w-3" />
                <span className="truncate max-w-[150px]">{att.filename}</span>
                <span className="text-[10px] text-muted-foreground">
                  ({Math.round(att.size / 1024)} KB)
                </span>
              </Button>
            ))}
          </div>
        )}
      </div>

      {/* Message Body Content */}
      <main className="flex-1 p-6 overflow-y-auto max-w-4xl w-full mx-auto">
        {sanitizedHtml ? (
          <div
            className="prose prose-sm max-w-none dark:prose-invert font-sans"
            dangerouslySetInnerHTML={{ __html: sanitizedHtml }}
          />
        ) : (
          <pre className="whitespace-pre-wrap font-sans text-sm leading-relaxed">
            {message.text || ""}
          </pre>
        )}
      </main>

      {/* Integrated Compose Dialog for Reply/Forward */}
      {composeDraft && (
        <MailboxComposeDialog
          open={composeOpen}
          onOpenChange={setComposeOpen}
          draft={composeDraft}
          onSent={() => {
            setComposeOpen(false);
          }}
        />
      )}
    </div>
  );
}

export default MailboxStandaloneRead;
