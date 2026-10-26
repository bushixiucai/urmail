import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, Navigate, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  ChevronLeft,
  Flag,
  Folder,
  LogOut,
  Mail,
  MailOpen,
  Menu,
  Pencil,
  RefreshCw,
  Reply,
  Forward,
  Trash2,
  KeyRound,
  Filter,
  Clock,
} from "lucide-react";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/notice-dialog";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import {
  assertAttachmentTotal,
  clearMailboxSession,
  deleteMailboxSession,
  deleteMessages,
  downloadAttachment,
  fetchAttachmentAsOutgoing,
  flagMessages,
  formatFrom,
  getMailboxAddress,
  getMessage,
  hasFlag,
  hasMailboxSession,
  isDraftsFolder,
  listFolders,
  listMessages,
  MailboxFolder,
  MessageDetail,
  MessageSummary,
  moveMessages,
  MAX_SEND_ATTACHMENTS,
  OutgoingAttachment,
} from "./mailbox-api";
import {
  ComposeDraft,
  composeFromSavedDraft,
  draftFromMessage,
  MailboxComposeDialog,
} from "./mailbox-compose-dialog";
import { MailboxPasswordDialog } from "./mailbox-password-dialog";
import { MailboxScheduledDialog } from "./mailbox-scheduled-dialog";
import { MailboxSearchBox } from "./mailbox-search-box";
import { sanitizeHtmlMail } from "./sanitize-html-mail";
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

function toggleUid(uids: number[], uid: number): number[] {
  return uids.includes(uid) ? uids.filter((u) => u !== uid) : [...uids, uid];
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
  const [checkedUids, setCheckedUids] = useState<number[]>([]);
  const [detail, setDetail] = useState<MessageDetail | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [composeOpen, setComposeOpen] = useState(false);
  const [composeDraft, setComposeDraft] = useState<ComposeDraft>({
    mode: "new",
  });
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [scheduledOpen, setScheduledOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteUids, setDeleteUids] = useState<number[]>([]);
  const [moveTo, setMoveTo] = useState("");
  const [bulkMoveTo, setBulkMoveTo] = useState("");
  /** Narrow screens: one primary pane at a time (folders use a drawer). */
  const [mobilePane, setMobilePane] = useState<"list" | "detail">("list");
  const [foldersOpen, setFoldersOpen] = useState(false);
  const [searchQ, setSearchQ] = useState("");
  const currentFolderRef = useRef(folderEnc);
  currentFolderRef.current = folderEnc;
  const listRequest = useRef<{
    generation: number;
    controller?: AbortController;
  }>({ generation: 0 });
  useEffect(
    () => () => {
      listRequest.current.generation += 1;
      listRequest.current.controller?.abort();
    },
    [],
  );

  const address = getMailboxAddress() ?? "";
  const currentFolder = useMemo(
    () => folders.find((f) => f.name_enc === folderEnc) ?? null,
    [folders, folderEnc],
  );

  const pageUids = useMemo(() => messages.map((m) => m.uid), [messages]);
  const allPageSelected =
    pageUids.length > 0 && pageUids.every((uid) => checkedUids.includes(uid));
  const somePageSelected =
    pageUids.some((uid) => checkedUids.includes(uid)) && !allPageSelected;

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

  const clearChecked = useCallback(() => setCheckedUids([]), []);

  const loadFolders = useCallback(async (signal?: AbortSignal) => {
    const res = await listFolders(signal);
    if (signal?.aborted) return;
    setFolders(res.folders ?? []);
    setFolderEnc((prev) => {
      if (prev && res.folders?.some((f) => f.name_enc === prev)) return prev;
      const inbox =
        res.folders?.find((f) => f.name.toUpperCase() === "INBOX") ??
        res.folders?.[0];
      return inbox?.name_enc ?? null;
    });
  }, []);

  const loadMessages = useCallback(
    async (
      enc: string,
      pageOffset: number,
      signal?: AbortSignal,
      q?: string,
    ) => {
      listRequest.current.controller?.abort();
      const controller = new AbortController();
      const generation = ++listRequest.current.generation;
      listRequest.current.controller = controller;
      const abort = () => controller.abort();
      signal?.addEventListener("abort", abort, { once: true });
      if (signal?.aborted) controller.abort();
      try {
        const res = await listMessages(
          enc,
          { limit: PAGE_SIZE, offset: pageOffset, q },
          controller.signal,
        );
        if (
          controller.signal.aborted ||
          generation !== listRequest.current.generation ||
          currentFolderRef.current !== enc
        )
          return;
        setMessages(res.messages ?? []);
        setTotal(res.total);
        setOffset(res.offset);
      } catch (err) {
        if (
          !controller.signal.aborted &&
          generation === listRequest.current.generation &&
          currentFolderRef.current === enc
        )
          throw err;
      } finally {
        signal?.removeEventListener("abort", abort);
      }
    },
    [],
  );

  const refresh = useCallback(async () => {
    if (!hasMailboxSession()) return;
    setBusy(true);
    setError(null);
    try {
      await loadFolders();
      const enc = currentFolderRef.current;
      if (enc) await loadMessages(enc, offset, undefined, searchQ);
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.load_failed"));
    } finally {
      setBusy(false);
    }
  }, [handleAuthError, loadFolders, loadMessages, offset, searchQ, t]);

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
    clearChecked();
    setSearchQ("");
    setMobilePane("list");
    setBusy(true);
    loadMessages(folderEnc, 0, controller.signal, "")
      .catch((err) => {
        if (controller.signal.aborted) return;
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [folderEnc, loadMessages, handleAuthError, t, clearChecked]);

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
            if (controller.signal.aborted) return;
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

  const openSavedDraft = async () => {
    if (!folderEnc || !detail || !isDraftsFolder(currentFolder)) return;
    setBusy(true);
    setError(null);
    try {
      const draftFolder = folderEnc;
      const message = detail;
      const atts: OutgoingAttachment[] = [];
      if ((message.attachments?.length ?? 0) > MAX_SEND_ATTACHMENTS)
        throw new Error("INCOMPLETE_DRAFT_ATTACHMENTS");
      for (const a of message.attachments ?? []) {
        atts.push(
          await fetchAttachmentAsOutgoing(
            draftFolder,
            message.uid,
            a.part_id,
            a.filename || `attachment-${a.part_id}`,
            a.content_type || "application/octet-stream",
          ),
        );
        assertAttachmentTotal(atts);
      }
      if (currentFolderRef.current !== draftFolder) return;
      openCompose(composeFromSavedDraft(message, atts));
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.draft_restore_failed"));
    } finally {
      setBusy(false);
    }
  };

  const openMessage = (uid: number) => {
    setSelectedUid(uid);
    setMobilePane("detail");
  };

  const backToList = () => {
    setMobilePane("list");
  };

  const selectFolder = (enc: string) => {
    currentFolderRef.current = enc;
    listRequest.current.generation += 1;
    listRequest.current.controller?.abort();
    setMessages([]);
    setFolderEnc(enc);
    setFoldersOpen(false);
  };

  const toggleSelectAllPage = () => {
    if (allPageSelected) {
      setCheckedUids((prev) => prev.filter((uid) => !pageUids.includes(uid)));
    } else {
      setCheckedUids((prev) => {
        const next = new Set(prev);
        for (const uid of pageUids) next.add(uid);
        return [...next];
      });
    }
  };

  const applyLocalFlags = (uids: number[], add: string[], remove: string[]) => {
    const removeLower = new Set(remove.map((f) => f.toLowerCase()));
    setMessages((list) =>
      list.map((m) => {
        if (!uids.includes(m.uid)) return m;
        const kept = (m.flags || []).filter(
          (f) => !removeLower.has(f.toLowerCase()),
        );
        const merged = [...kept];
        for (const flag of add) {
          if (!hasFlag(merged, flag)) merged.push(flag);
        }
        return { ...m, flags: merged };
      }),
    );
    if (detail && uids.includes(detail.uid)) {
      const kept = detail.flags.filter(
        (f) => !removeLower.has(f.toLowerCase()),
      );
      const merged = [...kept];
      for (const flag of add) {
        if (!hasFlag(merged, flag)) merged.push(flag);
      }
      setDetail({ ...detail, flags: merged });
    }
  };

  const bulkFlag = async (add: string[], remove: string[]) => {
    if (!folderEnc || checkedUids.length === 0) return;
    try {
      await flagMessages(folderEnc, { uids: checkedUids, add, remove });
      applyLocalFlags(checkedUids, add, remove);
      clearChecked();
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.action_failed"));
    }
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

  const openDeleteConfirm = (uids: number[]) => {
    if (uids.length === 0) return;
    setDeleteUids(uids);
    setDeleteOpen(true);
  };

  const confirmDelete = async () => {
    if (!folderEnc || deleteUids.length === 0) return;
    try {
      await deleteMessages(folderEnc, {
        uids: deleteUids,
        expunge: true,
      });
      if (selectedUid != null && deleteUids.includes(selectedUid)) {
        setSelectedUid(null);
        setDetail(null);
        setMobilePane("list");
      }
      clearChecked();
      await loadMessages(folderEnc, offset, undefined, searchQ);
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.action_failed"));
    } finally {
      setDeleteOpen(false);
      setDeleteUids([]);
    }
  };

  const doMove = async (uids: number[], to: string) => {
    if (!folderEnc || uids.length === 0 || !to) return;
    try {
      await moveMessages(folderEnc, { uids, to });
      if (selectedUid != null && uids.includes(selectedUid)) {
        setSelectedUid(null);
        setDetail(null);
        setMobilePane("list");
      }
      setMoveTo("");
      setBulkMoveTo("");
      clearChecked();
      await loadMessages(folderEnc, offset, undefined, searchQ);
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

  const loadPage = (nextOffset: number) => {
    if (!folderEnc) return;
    clearChecked();
    setBusy(true);
    void loadMessages(folderEnc, nextOffset, undefined, searchQ)
      .catch((err) => {
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => setBusy(false));
  };

  const runSearch = (q: string) => {
    if (!folderEnc) return;
    setSearchQ(q);
    clearChecked();
    setSelectedUid(null);
    setDetail(null);
    setBusy(true);
    void loadMessages(folderEnc, 0, undefined, q)
      .catch((err) => {
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => setBusy(false));
  };

  const folderList = (
    <ul className="space-y-0.5">
      {folders.map((f) => (
        <li key={f.name_enc}>
          <button
            type="button"
            className={`w-full text-left rounded-md px-3 py-3 md:px-2 md:py-1.5 text-sm truncate min-h-11 md:min-h-0 ${
              f.name_enc === folderEnc
                ? "bg-primary text-primary-foreground"
                : "hover:bg-muted text-foreground"
            }`}
            onClick={() => selectFolder(f.name_enc)}
          >
            {f.name}
          </button>
        </li>
      ))}
    </ul>
  );

  const moveOptions = folders.filter((f) => f.name_enc !== folderEnc);

  return (
    <div className="min-h-screen flex flex-col bg-muted/20">
      <header className="border-b bg-card px-3 sm:px-4 py-2 flex flex-wrap items-center gap-2 justify-between">
        <div className="flex items-center gap-2 min-w-0">
          <Sheet open={foldersOpen} onOpenChange={setFoldersOpen}>
            <SheetTrigger asChild>
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="md:hidden shrink-0 min-h-11 min-w-11 px-0"
                aria-label={t("page.webmail.folders_menu")}
              >
                <Menu className="h-5 w-5" />
              </Button>
            </SheetTrigger>
            <SheetContent side="left" className="p-0 flex flex-col w-72">
              <SheetHeader className="px-4 pt-4 pb-2 border-b">
                <SheetTitle className="flex items-center gap-2 text-base">
                  <Folder className="h-4 w-4" />
                  {t("page.webmail.folders")}
                </SheetTitle>
              </SheetHeader>
              <div className="flex-1 overflow-y-auto p-2">{folderList}</div>
            </SheetContent>
          </Sheet>
          <Mail className="h-4 w-4 shrink-0 text-primary" />
          <span className="text-sm font-medium truncate">{address}</span>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Button
            size="sm"
            variant="default"
            className="min-h-11 md:min-h-8"
            onClick={() => openCompose({ mode: "new" })}
          >
            <Pencil className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.compose_new")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="min-h-11 md:min-h-8"
            onClick={() => void refresh()}
          >
            <RefreshCw className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.refresh")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="min-h-11 md:min-h-8"
            onClick={() => setScheduledOpen(true)}
          >
            <Clock className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.scheduled_title")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="min-h-11 md:min-h-8"
            onClick={() => setPasswordOpen(true)}
          >
            <KeyRound className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.password_title")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="min-h-11 md:min-h-8"
            asChild
          >
            <a href="/mailbox/sieve">
              <Filter className="h-3.5 w-3.5 mr-1" />
              {t("page.webmail.sieve")}
            </a>
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="min-h-11 md:min-h-8"
            asChild
          >
            <Link to="/webmail/help">{t("page.webmail.help_title")}</Link>
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="min-h-11 md:min-h-8"
            onClick={() => void logout()}
          >
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

      <div className="flex-1 grid grid-cols-1 md:grid-cols-[200px_minmax(0,1fr)_minmax(0,1.2fr)] min-h-0 min-w-0 overflow-hidden">
        {/* Desktop folder pane */}
        <aside className="hidden md:block border-r bg-card p-2 overflow-y-auto min-h-0">
          <p className="px-2 py-1 text-xs font-medium text-muted-foreground">
            {t("page.webmail.folders")}
          </p>
          {folderList}
        </aside>

        {/* Message list pane */}
        <section
          className={`border-r flex flex-col min-h-0 min-w-0 ${
            mobilePane === "detail" ? "hidden md:flex" : "flex"
          }`}
        >
          <div className="px-3 py-2 border-b space-y-2">
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className="truncate min-w-0">
                {currentFolder?.name ?? "—"} · {total}{" "}
                {t("page.webmail.messages_count")}
              </span>
            </div>
            <MailboxSearchBox
              value={searchQ}
              disabled={busy || !folderEnc}
              onSearch={runSearch}
            />
            <div className="flex items-center gap-2 min-h-11">
              <label className="inline-flex items-center gap-2 text-sm cursor-pointer select-none min-h-11">
                <input
                  type="checkbox"
                  className="h-5 w-5 accent-primary"
                  checked={allPageSelected}
                  ref={(el) => {
                    if (el) el.indeterminate = somePageSelected;
                  }}
                  onChange={toggleSelectAllPage}
                  disabled={messages.length === 0}
                  aria-label={t("page.webmail.select_all")}
                />
                <span className="text-xs text-muted-foreground">
                  {t("page.webmail.select_all")}
                </span>
              </label>
              {checkedUids.length > 0 && (
                <span className="text-xs text-muted-foreground">
                  {t("page.webmail.selected_count", {
                    count: checkedUids.length,
                  })}
                </span>
              )}
            </div>
            {checkedUids.length > 0 && (
              <div
                className="flex flex-wrap items-center gap-1.5"
                role="toolbar"
                aria-label={t("page.webmail.bulk_actions")}
              >
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-11 md:min-h-8"
                  onClick={() => void bulkFlag(["\\Seen"], [])}
                >
                  <MailOpen className="h-3.5 w-3.5 mr-1" />
                  {t("page.webmail.mark_read")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-11 md:min-h-8"
                  onClick={() => void bulkFlag([], ["\\Seen"])}
                >
                  <Mail className="h-3.5 w-3.5 mr-1" />
                  {t("page.webmail.mark_unread")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-11 md:min-h-8 text-destructive"
                  onClick={() => openDeleteConfirm(checkedUids)}
                >
                  <Trash2 className="h-3.5 w-3.5 mr-1" />
                  {t("page.webmail.delete")}
                </Button>
                <div className="flex items-center gap-1 min-w-0">
                  <select
                    aria-label={t("page.webmail.move_to")}
                    className="h-11 md:h-8 max-w-[10rem] rounded-md border border-input bg-background px-2 text-xs"
                    value={bulkMoveTo}
                    onChange={(e) => setBulkMoveTo(e.target.value)}
                  >
                    <option value="">{t("page.webmail.move_to")}</option>
                    {moveOptions.map((f) => (
                      <option key={f.name_enc} value={f.name_enc}>
                        {f.name}
                      </option>
                    ))}
                  </select>
                  <Button
                    size="sm"
                    variant="outline"
                    className="min-h-11 md:min-h-8"
                    disabled={!bulkMoveTo}
                    onClick={() => void doMove(checkedUids, bulkMoveTo)}
                  >
                    {t("page.webmail.move")}
                  </Button>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  className="min-h-11 md:min-h-8"
                  onClick={clearChecked}
                >
                  {t("page.webmail.clear_selection")}
                </Button>
              </div>
            )}
          </div>
          <ul className="flex-1 overflow-y-auto divide-y min-h-0">
            {messages.length === 0 && !busy ? (
              <li className="p-4 text-sm text-muted-foreground">
                {t("page.webmail.empty_folder")}
              </li>
            ) : (
              messages.map((m) => {
                const unread = !hasFlag(m.flags, "\\Seen");
                const flagged = hasFlag(m.flags, "\\Flagged");
                const checked = checkedUids.includes(m.uid);
                return (
                  <li key={m.uid} className="flex items-stretch min-w-0">
                    <label className="flex items-center px-2 shrink-0 cursor-pointer min-h-14 md:min-h-0">
                      <input
                        type="checkbox"
                        className="h-5 w-5 accent-primary"
                        checked={checked}
                        aria-label={t("page.webmail.select_message_row")}
                        onChange={() =>
                          setCheckedUids((prev) => toggleUid(prev, m.uid))
                        }
                        onClick={(e) => e.stopPropagation()}
                      />
                    </label>
                    <button
                      type="button"
                      className={`flex-1 min-w-0 text-left px-2 py-3 md:py-2 hover:bg-muted/60 min-h-14 md:min-h-0 ${
                        selectedUid === m.uid ? "bg-muted" : ""
                      }`}
                      onClick={() => openMessage(m.uid)}
                    >
                      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                        {flagged && (
                          <Flag className="h-3 w-3 text-amber-500 fill-amber-500 shrink-0" />
                        )}
                        <span className="truncate flex-1 min-w-0">
                          {formatFrom(m.from) ||
                            t("page.webmail.unknown_sender")}
                        </span>
                        <span className="shrink-0 tabular-nums">
                          {dateFmt(m.date)}
                        </span>
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
                className="min-h-11 md:min-h-8"
                disabled={offset <= 0 || busy || !folderEnc}
                onClick={() => loadPage(Math.max(0, offset - PAGE_SIZE))}
              >
                {t("page.webmail.prev_page")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                className="min-h-11 md:min-h-8"
                disabled={
                  offset + messages.length >= total || busy || !folderEnc
                }
                onClick={() => loadPage(offset + PAGE_SIZE)}
              >
                {t("page.webmail.next_page")}
              </Button>
            </div>
          )}
        </section>

        {/* Detail pane */}
        <section
          className={`flex flex-col min-h-0 min-w-0 bg-card ${
            mobilePane === "list" ? "hidden md:flex" : "flex"
          }`}
        >
          {!detail ? (
            <div className="flex-1 flex items-center justify-center p-6 text-sm text-muted-foreground">
              {busy
                ? t("state.loading")
                : t("page.webmail.select_message_read")}
            </div>
          ) : (
            <>
              <div className="border-b p-3 space-y-2">
                <div className="flex items-start gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="md:hidden shrink-0 min-h-11 min-w-11 px-0"
                    aria-label={t("page.webmail.back_to_list")}
                    onClick={backToList}
                  >
                    <ChevronLeft className="h-5 w-5" />
                  </Button>
                  <div className="min-w-0 flex-1 space-y-1">
                    <h2 className="text-base font-semibold break-words">
                      {detail.subject || t("page.webmail.no_subject")}
                    </h2>
                    <p className="text-sm text-muted-foreground break-all">
                      {formatFrom(detail.from)} · {dateFmt(detail.date)}
                    </p>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {isDraftsFolder(currentFolder) ? (
                    <Button
                      size="sm"
                      variant="outline"
                      className="min-h-11 md:min-h-8"
                      onClick={() => void openSavedDraft()}
                      disabled={busy}
                    >
                      <Pencil className="h-3.5 w-3.5 mr-1" />
                      {t("page.webmail.compose_edit_draft")}
                    </Button>
                  ) : (
                    <>
                      <Button
                        size="sm"
                        variant="outline"
                        className="min-h-11 md:min-h-8"
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
                        className="min-h-11 md:min-h-8"
                        onClick={() =>
                          openCompose(draftFromMessage("forward", detail))
                        }
                      >
                        <Forward className="h-3.5 w-3.5 mr-1" />
                        {t("page.webmail.compose_forward")}
                      </Button>
                    </>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    className="min-h-11 md:min-h-8"
                    onClick={() => void toggleFlagged()}
                  >
                    <Flag className="h-3.5 w-3.5 mr-1" />
                    {hasFlag(detail.flags, "\\Flagged")
                      ? t("page.webmail.unflag")
                      : t("page.webmail.flag")}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="min-h-11 md:min-h-8 text-destructive"
                    onClick={() =>
                      openDeleteConfirm(
                        selectedUid != null ? [selectedUid] : [],
                      )
                    }
                  >
                    <Trash2 className="h-3.5 w-3.5 mr-1" />
                    {t("page.webmail.delete")}
                  </Button>
                  <div className="flex items-center gap-1 min-w-0">
                    <select
                      aria-label={t("page.webmail.move_to")}
                      className="h-11 md:h-8 max-w-[10rem] rounded-md border border-input bg-background px-2 text-xs"
                      value={moveTo}
                      onChange={(e) => setMoveTo(e.target.value)}
                    >
                      <option value="">{t("page.webmail.move_to")}</option>
                      {moveOptions.map((f) => (
                        <option key={f.name_enc} value={f.name_enc}>
                          {f.name}
                        </option>
                      ))}
                    </select>
                    <Button
                      size="sm"
                      variant="outline"
                      className="min-h-11 md:min-h-8"
                      disabled={!moveTo || selectedUid == null}
                      onClick={() =>
                        selectedUid != null &&
                        void doMove([selectedUid], moveTo)
                      }
                    >
                      {t("page.webmail.move")}
                    </Button>
                  </div>
                </div>
              </div>
              <div className="flex-1 overflow-y-auto p-4 space-y-3 min-h-0">
                {detail.html ? (
                  <iframe
                    title={t("page.webmail.message_body")}
                    sandbox=""
                    referrerPolicy="no-referrer"
                    className="w-full min-h-[280px] rounded border bg-background"
                    srcDoc={sanitizeHtmlMail(detail.html)}
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
                        <li
                          key={a.part_id}
                          className="flex flex-wrap items-center gap-2 min-w-0"
                        >
                          <span className="truncate min-w-0">
                            {a.filename || a.part_id}{" "}
                            <span className="text-xs text-muted-foreground">
                              ({a.content_type}, {a.size} B)
                            </span>
                          </span>
                          {folderEnc && selectedUid != null && (
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              className="shrink-0 min-h-11 md:min-h-8"
                              onClick={() => {
                                void downloadAttachment(
                                  folderEnc,
                                  selectedUid,
                                  a.part_id,
                                  a.filename || `attachment-${a.part_id}`,
                                ).catch((err: unknown) => {
                                  setError(
                                    errorMessage(
                                      err,
                                      t,
                                      "page.webmail.attach_download_failed",
                                    ),
                                  );
                                });
                              }}
                            >
                              {t("page.webmail.attach_download")}
                            </Button>
                          )}
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
          if (folderEnc)
            void loadMessages(folderEnc, offset, undefined, searchQ);
        }}
        onDraftSaved={() => {
          const drafts = folders.find((f) => isDraftsFolder(f));
          if (drafts && folderEnc === drafts.name_enc) {
            void loadMessages(folderEnc, offset, undefined, searchQ);
          }
        }}
      />
      <MailboxScheduledDialog
        open={scheduledOpen}
        onOpenChange={setScheduledOpen}
      />
      <MailboxPasswordDialog
        open={passwordOpen}
        onOpenChange={setPasswordOpen}
        onReauthRequired={() => {
          forceRelogin();
        }}
      />
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={(open) => {
          setDeleteOpen(open);
          if (!open) setDeleteUids([]);
        }}
        title={
          deleteUids.length > 1
            ? t("page.webmail.bulk_delete_confirm_title")
            : t("page.webmail.delete_confirm_title")
        }
        description={
          deleteUids.length > 1
            ? t("page.webmail.bulk_delete_confirm", {
                count: deleteUids.length,
              })
            : t("page.webmail.delete_confirm")
        }
        destructive
        onConfirm={confirmDelete}
      />
    </div>
  );
}
