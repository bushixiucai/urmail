import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  hasMailboxSession,
  getMailboxToken,
  getMailboxAddress,
  getMailboxExpiresAt,
  getMessage,
  type MessageDetail,
} from "./mailbox-api";
import {
  MailboxComposeDialog,
  composeFromSavedDraft,
  draftFromMessage,
  type ComposeDraft,
  type ComposeMode,
  type ComposeControllerHandle,
} from "./mailbox-compose-dialog";
import { useStandaloneSessionHandover } from "./mailbox-standalone-read";

/**
 * Open a new standalone compose window without sensitive tokens in the URL.
 * Securely delivers session via postMessage and BroadcastChannel.
 */
export function openStandaloneCompose(options?: {
  to?: string;
  subject?: string;
  body?: string;
  mode?: ComposeMode;
  folder?: string;
  uid?: number;
}) {
  const params = new URLSearchParams();
  if (options?.to) params.set("to", options.to);
  if (options?.subject) params.set("subject", options.subject);
  if (options?.body) params.set("body", options.body);
  if (options?.mode) params.set("mode", options.mode);
  if (options?.folder) params.set("folder", options.folder);
  if (options?.uid) params.set("uid", String(options.uid));

  const qs = params.toString();
  const url = `/webmail/compose${qs ? `?${qs}` : ""}`;
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

export function MailboxStandaloneCompose() {
  const { t } = useTranslation();
  const [searchParams] = useSearchParams();
  const { sessionReady, handoverTimedOut } = useStandaloneSessionHandover();

  const [initialDraft, setInitialDraft] = useState<ComposeDraft | null>(null);
  const [loadingDraft, setLoadingDraft] = useState(true);
  const [isDirty, setIsDirty] = useState(false);
  const [open, setOpen] = useState(true);
  const controllerRef = useRef<ComposeControllerHandle>(null);

  // Unload warning when draft has unsaved changes or active edits
  useEffect(() => {
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      if (isDirty) {
        e.preventDefault();
        e.returnValue = t(
          "page.webmail.close_window_warning",
          "You have unsaved changes.",
        );
      }
    };
    window.addEventListener("beforeunload", handleBeforeUnload);
    return () => {
      window.removeEventListener("beforeunload", handleBeforeUnload);
    };
  }, [isDirty, t]);

  // Parse draft configuration from URL query params (without any sensitive tokens)
  useEffect(() => {
    if (!sessionReady) return;

    const mode = (searchParams.get("mode") as ComposeMode) || "new";
    const to = searchParams.get("to") || "";
    const subject = searchParams.get("subject") || "";
    const text = searchParams.get("body") || "";
    const folderEnc = searchParams.get("folder");
    const uidStr = searchParams.get("uid");
    const uid = uidStr ? parseInt(uidStr, 10) : undefined;

    let active = true;

    if (uid && folderEnc) {
      setLoadingDraft(true);
      getMessage(folderEnc, uid)
        .then((msg: MessageDetail) => {
          if (!active) return;
          if (mode === "draft") {
            setInitialDraft(composeFromSavedDraft(msg));
          } else if (
            mode === "reply" ||
            mode === "reply-all" ||
            mode === "forward"
          ) {
            setInitialDraft(draftFromMessage(mode, msg));
          } else {
            setInitialDraft({ mode: "new", to, subject, text });
          }
          setLoadingDraft(false);
        })
        .catch(() => {
          if (!active) return;
          setInitialDraft({ mode: "new", to, subject, text });
          setLoadingDraft(false);
        });
    } else {
      setInitialDraft({
        mode,
        to,
        subject,
        text,
      });
      setLoadingDraft(false);
    }

    return () => {
      active = false;
    };
  }, [sessionReady, searchParams]);

  const handleSent = () => {
    setIsDirty(false);
    setOpen(false);
    window.close();
  };

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setIsDirty(false);
      setOpen(false);
      window.close();
    }
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

  if (loadingDraft || !initialDraft) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background text-sm text-muted-foreground">
        {t("state.loading", "Preparing message...")}
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-muted/20 flex flex-col p-2 sm:p-4 md:p-6">
      <div className="max-w-4xl w-full mx-auto flex-1 flex flex-col">
        <MailboxComposeDialog
          ref={controllerRef}
          inline={true}
          open={open}
          onOpenChange={handleOpenChange}
          draft={initialDraft}
          onSent={handleSent}
          onDraftSaved={() => {
            setIsDirty(false);
          }}
        />
      </div>
    </div>
  );
}

export default MailboxStandaloneCompose;
