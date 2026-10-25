import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, Navigate, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Flag,
  LogOut,
  Mail,
  Pencil,
  RefreshCw,
  Reply,
  Forward,
  Trash2,
  KeyRound,
  Filter,
} from "lucide-react";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/notice-dialog";
import {
  clearMailboxSession,
  deleteMailboxSession,
  deleteMessages,
  flagMessages,
  formatFrom,
  getMailboxAddress,
  getMessage,
  hasFlag,
  hasMailboxSession,
  listFolders,
  listMessages,
  MailboxFolder,
  MessageDetail,
  MessageSummary,
  moveMessages,
} from "./mailbox-api";
import {
  ComposeDraft,
  draftFromMessage,
  MailboxComposeDialog,
} from "./mailbox-compose-dialog";
import { MailboxPasswordDialog } from "./mailbox-password-dialog";
import { useMailboxNative } from "./use-mailbox-native";
import { WebmailHelpPage } from "./webmail-help-page";

const PAGE_SIZE = 50;

function errorMessage(
  err: unknown,
  t: (k: string, o?: Record<string, unknown>) => string,
  fallback: string,
): string {
  if (err instanceof ApiRequestError) {
    return t(`error.${err.code}`, { defaultValue: t(fallback) });
  }
  return t(fallback);
}

export function MailboxInboxPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const { native, loaded: nativeLoaded } = useMailboxNative();

  const [folders, setFolders] = useState<MailboxFolder[]>([]);
  const [folderEnc, setFolderEnc] = useState<string | null>(null);
  const [messages, setMessages] = useState<MessageSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [selectedUid, setSelectedUid] = useState<number | null>(null);
  const [detail, setDetail] = useState<MessageDetail | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [composeOpen, setComposeOpen] = useState(false);
  const [composeDraft, setComposeDraft] = useState<ComposeDraft>({
    mode: "new",
  });
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [moveTo, setMoveTo] = useState("");

  const address = getMailboxAddress() ?? "";
  const currentFolder = useMemo(
    () => folders.find((f) => f.name_enc === folderEnc) ?? null,
    [folders, folderEnc],
  );

  const forceRelogin = useCallback(() => {
    clearMailboxSession();
    navigate("/webmail/login", { replace: true });
  }, [navigate]);

  const handleAuthError = useCallback(
    (err: unknown) => {
      if (
        err instanceof ApiRequestError &&
        (err.status === 401 ||
          err.code === "MAILBOX_AUTHORIZATION_DENIED" ||
          err.code === "MAILBOX_REAUTH_REQUIRED")
      ) {
        forceRelogin();
        return true;
      }
      return false;
    },
    [forceRelogin],
  );

  const loadFolders = useCallback(
    async (signal?: AbortSignal) => {
      const res = await listFolders(signal);
      setFolders(res.folders ?? []);
      setFolderEnc((prev) => {
        if (prev && res.folders?.some((f) => f.name_enc === prev)) return prev;
        const inbox =
          res.folders?.find((f) => f.name.toUpperCase() === "INBOX") ??
          res.folders?.[0];
        return inbox?.name_enc ?? null;
      });
    },
    [],
  );

  const loadMessages = useCallback(
    async (enc: string, pageOffset: number, signal?: AbortSignal) => {
      const res = await listMessages(
        enc,
        { limit: PAGE_SIZE, offset: pageOffset },
        signal,
      );
      setMessages(res.messages ?? []);
      setTotal(res.total);
      setOffset(res.offset);
    },
    [],
  );

  const refresh = useCallback(async () => {
    if (!hasMailboxSession()) return;
    setBusy(true);
    setError(null);
    try {
      await loadFolders();
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.load_failed"));
    } finally {
      setBusy(false);
    }
  }, [handleAuthError, loadFolders, t]);

  useEffect(() => {
    if (!nativeLoaded || !native || !hasMailboxSession()) return;
    const controller = new AbortController();
    setBusy(true);
    loadFolders(controller.signal)
      .catch((err) => {
        if (controller.signal.aborted) return;
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [nativeLoaded, native, loadFolders, handleAuthError, t]);

  useEffect(() => {
    if (!folderEnc || !hasMailboxSession()) return;
    const controller = new AbortController();
    setSelectedUid(null);
    setDetail(null);
    setBusy(true);
    loadMessages(folderEnc, 0, controller.signal)
      .catch((err) => {
        if (controller.signal.aborted) return;
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [folderEnc, loadMessages, handleAuthError, t]);

  useEffect(() => {
    if (!folderEnc || selectedUid == null) {
      setDetail(null);
      return;
    }
    const controller = new AbortController();
    getMessage(folderEnc, selectedUid, controller.signal)
      .then(async (msg) => {
        if (controller.signal.aborted) return;
        setDetail(msg);
        if (!hasFlag(msg.flags, "\\Seen")) {
          try {
            await flagMessages(folderEnc, {
              uids: [selectedUid],
              add: ["\\Seen"],
            });
            setMessages((list) =>
              list.map((m) =>
                m.uid === selectedUid
                  ? { ...m, flags: [...(m.flags || []), "\\Seen"] }
                  : m,
              ),
            );
          } catch {
            /* non-fatal */
          }
        }
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      });
    return () => controller.abort();
  }, [folderEnc, selectedUid, handleAuthError, t]);

  if (!nativeLoaded) {
    return (
      <div className="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
        {t("state.loading")}
      </div>
    );
  }

  if (!native) {
    return (
      <main className="mx-auto max-w-lg px-4 py-10">
        <WebmailHelpPage />
      </main>
    );
  }

  if (!hasMailboxSession()) {
    return <Navigate to="/webmail/login" replace />;
  }

  const openCompose = (draft: ComposeDraft) => {
    setComposeDraft(draft);
    setComposeOpen(true);
  };

  const toggleFlagged = async () => {
    if (!folderEnc || selectedUid == null || !detail) return;
    const flagged = hasFlag(detail.flags, "\\Flagged");
    try {
      await flagMessages(folderEnc, {
        uids: [selectedUid],
        add: flagged ? [] : ["\\Flagged"],
        remove: flagged ? ["\\Flagged"] : [],
      });
      const nextFlags = flagged
        ? detail.flags.filter((f) => f.toLowerCase() !== "\\flagged")
        : [...detail.flags, "\\Flagged"];
      setDetail({ ...detail, flags: nextFlags });
      setMessages((list) =>
        list.map((m) =>
          m.uid === selectedUid ? { ...m, flags: nextFlags } : m,
        ),
      );
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.action_failed"));
    }
  };

  const confirmDelete = async () => {
    if (!folderEnc || selectedUid == null) return;
    try {
      await deleteMessages(folderEnc, {
        uids: [selectedUid],
        expunge: true,
      });
      setSelectedUid(null);
      setDetail(null);
      await loadMessages(folderEnc, offset);
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.action_failed"));
    } finally {
      setDeleteOpen(false);
    }
  };

  const doMove = async () => {
    if (!folderEnc || selectedUid == null || !moveTo) return;
    try {
      await moveMessages(folderEnc, {
        uids: [selectedUid],
        to: moveTo,
      });
      setSelectedUid(null);
      setDetail(null);
      setMoveTo("");
      await loadMessages(folderEnc, offset);
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.action_failed"));
    }
  };

  const logout = async () => {
    try {
      await deleteMailboxSession();
    } catch {
      /* still clear local */
    }
    forceRelogin();
  };

  const dateFmt = (iso: string) => {
    try {
      return new Date(iso).toLocaleString(i18n.language);
    } catch {
      return iso;
    }
  };

  return (
    <div className="min-h-screen flex flex-col bg-muted/20">
      <header className="border-b bg-card px-3 sm:px-4 py-2 flex flex-wrap items-center gap-2 justify-between">
        <div className="flex items-center gap-2 min-w-0">
          <Mail className="h-4 w-4 shrink-0 text-primary" />
          <span className="text-sm font-medium truncate">{address}</span>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Button
            size="sm"
            variant="default"
            onClick={() => openCompose({ mode: "new" })}
          >
            <Pencil className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.compose_new")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => void refresh()}>
            <RefreshCw className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.refresh")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setPasswordOpen(true)}
          >
            <KeyRound className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.password_title")}
          </Button>
          <Button size="sm" variant="outline" asChild>
            <a href="/mailbox/sieve">
              <Filter className="h-3.5 w-3.5 mr-1" />
              {t("page.webmail.sieve")}
            </a>
          </Button>
          <Button size="sm" variant="ghost" asChild>
            <Link to="/webmail/help">{t("page.webmail.help_title")}</Link>
          </Button>
          <Button size="sm" variant="ghost" onClick={() => void logout()}>
            <LogOut className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.logout")}
          </Button>
        </div>
      </header>

      {error && (
        <p role="alert" className="px-4 py-2 text-sm text-destructive border-b">
          {error}
        </p>
      )}

      <div className="flex-1 grid grid-cols-1 md:grid-cols-[180px_minmax(0,1fr)_minmax(0,1.2fr)] min-h-0">
        <aside className="border-r bg-card p-2 overflow-y-auto max-h-[40vh] md:max-h-none">
          <p className="px-2 py-1 text-xs font-medium text-muted-foreground">
            {t("page.webmail.folders")}
          </p>
          <ul className="space-y-0.5">
            {folders.map((f) => (
              <li key={f.name_enc}>
                <button
                  type="button"
                  className={`w-full text-left rounded-md px-2 py-1.5 text-sm truncate ${
                    f.name_enc === folderEnc
                      ? "bg-primary text-primary-foreground"
                      : "hover:bg-muted text-foreground"
                  }`}
                  onClick={() => setFolderEnc(f.name_enc)}
                >
                  {f.name}
                </button>
              </li>
            ))}
          </ul>
        </aside>

        <section className="border-r flex flex-col min-h-0 max-h-[40vh] md:max-h-none">
          <div className="px-3 py-2 border-b text-xs text-muted-foreground">
            {currentFolder?.name ?? "—"} · {total}{" "}
            {t("page.webmail.messages_count")}
          </div>
          <ul className="flex-1 overflow-y-auto divide-y">
            {messages.length === 0 && !busy ? (
              <li className="p-4 text-sm text-muted-foreground">
                {t("page.webmail.empty_folder")}
              </li>
            ) : (
              messages.map((m) => {
                const unread = !hasFlag(m.flags, "\\Seen");
                const flagged = hasFlag(m.flags, "\\Flagged");
                return (
                  <li key={m.uid}>
                    <button
                      type="button"
                      className={`w-full text-left px-3 py-2 hover:bg-muted/60 ${
                        selectedUid === m.uid ? "bg-muted" : ""
                      }`}
                      onClick={() => setSelectedUid(m.uid)}
                    >
                      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                        {flagged && (
                          <Flag className="h-3 w-3 text-amber-500 fill-amber-500" />
                        )}
                        <span className="truncate flex-1">
                          {formatFrom(m.from) || t("page.webmail.unknown_sender")}
                        </span>
                        <span className="shrink-0">{dateFmt(m.date)}</span>
                      </div>
                      <p
                        className={`text-sm truncate ${unread ? "font-semibold" : ""}`}
                      >
                        {m.subject || t("page.webmail.no_subject")}
                      </p>
                    </button>
                  </li>
                );
              })
            )}
          </ul>
          {(offset > 0 || offset + messages.length < total) && (
            <div className="flex gap-2 p-2 border-t">
              <Button
                size="sm"
                variant="outline"
                disabled={offset <= 0 || busy || !folderEnc}
                onClick={() => {
                  if (!folderEnc) return;
                  const next = Math.max(0, offset - PAGE_SIZE);
                  setBusy(true);
                  void loadMessages(folderEnc, next)
                    .catch((err) => {
                      if (handleAuthError(err)) return;
                      setError(
                        errorMessage(err, t, "page.webmail.load_failed"),
                      );
                    })
                    .finally(() => setBusy(false));
                }}
              >
                {t("page.webmail.prev_page")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={offset + messages.length >= total || busy || !folderEnc}
                onClick={() => {
                  if (!folderEnc) return;
                  const next = offset + PAGE_SIZE;
                  setBusy(true);
                  void loadMessages(folderEnc, next)
                    .catch((err) => {
                      if (handleAuthError(err)) return;
                      setError(
                        errorMessage(err, t, "page.webmail.load_failed"),
                      );
                    })
                    .finally(() => setBusy(false));
                }}
              >
                {t("page.webmail.next_page")}
              </Button>
            </div>
          )}
        </section>

        <section className="flex flex-col min-h-0 bg-card">
          {!detail ? (
            <div className="flex-1 flex items-center justify-center p-6 text-sm text-muted-foreground">
              {busy ? t("state.loading") : t("page.webmail.select_message_read")}
            </div>
          ) : (
            <>
              <div className="border-b p-3 space-y-2">
                <h2 className="text-base font-semibold break-words">
                  {detail.subject || t("page.webmail.no_subject")}
                </h2>
                <p className="text-sm text-muted-foreground break-all">
                  {formatFrom(detail.from)} · {dateFmt(detail.date)}
                </p>
                <div className="flex flex-wrap gap-1.5">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      openCompose(draftFromMessage("reply", detail))
                    }
                  >
                    <Reply className="h-3.5 w-3.5 mr-1" />
                    {t("page.webmail.compose_reply")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      openCompose(draftFromMessage("forward", detail))
                    }
                  >
                    <Forward className="h-3.5 w-3.5 mr-1" />
                    {t("page.webmail.compose_forward")}
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => void toggleFlagged()}>
                    <Flag className="h-3.5 w-3.5 mr-1" />
                    {hasFlag(detail.flags, "\\Flagged")
                      ? t("page.webmail.unflag")
                      : t("page.webmail.flag")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="text-destructive"
                    onClick={() => setDeleteOpen(true)}
                  >
                    <Trash2 className="h-3.5 w-3.5 mr-1" />
                    {t("page.webmail.delete")}
                  </Button>
                  <div className="flex items-center gap-1">
                    <select
                      aria-label={t("page.webmail.move_to")}
                      className="h-8 rounded-md border border-input bg-background px-2 text-xs"
                      value={moveTo}
                      onChange={(e) => setMoveTo(e.target.value)}
                    >
                      <option value="">{t("page.webmail.move_to")}</option>
                      {folders
                        .filter((f) => f.name_enc !== folderEnc)
                        .map((f) => (
                          <option key={f.name_enc} value={f.name_enc}>
                            {f.name}
                          </option>
                        ))}
                    </select>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={!moveTo}
                      onClick={() => void doMove()}
                    >
                      {t("page.webmail.move")}
                    </Button>
                  </div>
                </div>
              </div>
              <div className="flex-1 overflow-y-auto p-4 space-y-3">
                {detail.html ? (
                  <iframe
                    title={t("page.webmail.message_body")}
                    sandbox=""
                    className="w-full min-h-[280px] rounded border bg-background"
                    srcDoc={detail.html}
                  />
                ) : (
                  <pre className="whitespace-pre-wrap break-words text-sm font-sans">
                    {detail.text || t("page.webmail.no_body")}
                  </pre>
                )}
                {detail.attachments?.length > 0 && (
                  <div className="border-t pt-3">
                    <p className="text-xs font-medium text-muted-foreground mb-1">
                      {t("page.webmail.attachments")}
                    </p>
                    <ul className="text-sm space-y-1">
                      {detail.attachments.map((a) => (
                        <li key={a.part_id} className="truncate">
                          {a.filename || a.part_id}{" "}
                          <span className="text-xs text-muted-foreground">
                            ({a.content_type}, {a.size} B)
                          </span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            </>
          )}
        </section>
      </div>

      <MailboxComposeDialog
        open={composeOpen}
        onOpenChange={setComposeOpen}
        draft={composeDraft}
        onSent={() => {
          if (folderEnc) void loadMessages(folderEnc, offset);
        }}
      />
      <MailboxPasswordDialog
        open={passwordOpen}
        onOpenChange={setPasswordOpen}
        onReauthRequired={() => {
          // Prefer catalogue message if present; fall through to login.
          forceRelogin();
        }}
      />
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t("page.webmail.delete_confirm_title")}
        description={t("page.webmail.delete_confirm")}
        destructive
        onConfirm={confirmDelete}
      />
    </div>
  );
}
