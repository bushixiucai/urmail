import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, Navigate, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  ArrowDownWideNarrow,
  ChevronDown,
  ChevronLeft,
  Flag,
  Folder,
  HelpCircle,
  Layers,
  LogOut,
  Mail,
  MailOpen,
  Menu,
  Pencil,
  RefreshCw,
  Reply,
  ReplyAll,
  Forward,
  Settings,
  Trash2,
  KeyRound,
  Filter,
  Clock,
  Upload,
  Download,
  Users,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ApiRequestError } from "@/lib/api/client";
import { AppearanceControls } from "@/components/appearance-controls";
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
  crossFolderSearch,
  deleteMailboxSession,
  deleteMessages,
  mailboxTransfer,
  isTrashFolder,
  downloadAttachment,
  fetchAttachmentAsOutgoing,
  fetchMessageAsOutgoing,
  flagMessages,
  formatFrom,
  getMailboxAddress,
  getMailboxSettings,
  listMailboxIdentities,
  getMessage,
  saveDraft,
  hasFlag,
  hasMailboxSession,
  isDraftsFolder,
  listFolders,
  listMessages,
  MailboxFolder,
  MailboxSortKey,
  MessageDetail,
  MessageSummary,
  moveMessages,
  MAX_SEND_ATTACHMENTS,
  OutgoingAttachment,
} from "./mailbox-api";
import { decorateThreads } from "./mailbox-threading";
import {
  ComposeDraft,
  chooseMailboxIdentity,
  composeFromSavedDraft,
  draftFromMessage,
  MailboxComposeDialog,
} from "./mailbox-compose-dialog";
import { MailboxPasswordDialog } from "./mailbox-password-dialog";
import { MailboxFolderDialog } from "./mailbox-folder-dialog";
import { MailboxScheduledDialog } from "./mailbox-scheduled-dialog";
import { MailboxImportDialog } from "./mailbox-import-dialog";
import { downloadMailboxMbox } from "./mailbox-import-api";
import { usableMessageDate } from "./message-date";
import { MailboxSearchBox } from "./mailbox-search-box";
import { attachmentDisplayName } from "./attachment-name";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import { MailboxAIAsk } from "./mailbox-ai-ask";
import { MailboxMessageTools } from "./mailbox-message-tools";
import {
  AttachmentPreview,
  MessageHTMLWithAttachments,
} from "./mailbox-attachment-view";
import { MailboxKeyboardShortcuts } from "./mailbox-keyboard-shortcuts";
import { MailboxReminders } from "./mailbox-reminders";
import { MailboxInvitationPanel } from "./mailbox-invitation-panel";
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
  const [datePreferences, setDatePreferences] = useState<
    Record<string, unknown>
  >({});
  useEffect(() => {
    let active = true;
    getMailboxSettings()
      .then((settings) => {
        if (active) setDatePreferences(settings.preferences ?? {});
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);
  const [folderValidity, setFolderValidity] = useState<number | undefined>();
  const [folderManagerOpen, setFolderManagerOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [exportBusy, setExportBusy] = useState(false);
  const [deleteSource, setDeleteSource] = useState<{
    enc: string;
    validity: number;
    trash: boolean;
  } | null>(null);
  const [mutationBusy, setMutationBusy] = useState(false);
  const [mutationNotice, setMutationNotice] = useState("");
  const [mutationUncertain, setMutationUncertain] = useState(false);
  const [offset, setOffset] = useState(0);
  const citationValidityRef = useRef<number | undefined>(undefined);
  const [citationNavigation, setCitationNavigation] = useState(0);
  const [selectedUid, setSelectedUid] = useState<number | null>(null);
  const [checkedUids, setCheckedUids] = useState<number[]>([]);
  const [detail, setDetail] = useState<MessageDetail | null>(null);
  const liveDetail = useRef(detail);
  liveDetail.current = detail;
  const forwardRequest = useRef<AbortController | null>(null);
  const [forwardBusy, setForwardBusy] = useState(false);
  useEffect(() => () => forwardRequest.current?.abort(), []);
  // Per-message "load images" privacy opt-in (WEBMAIL-007); reset when
  // another message is opened.
  const [loadImages, setLoadImages] = useState(false);
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
  // WEBMAIL-004: sort + conversation grouping + cross-folder search scope.
  const [sortBy, setSortBy] = useState<MailboxSortKey>("");
  const [sortAsc, setSortAsc] = useState(false);
  const [threadGroup, setThreadGroup] = useState(false);
  const [searchAllFolders, setSearchAllFolders] = useState(false);
  const [crossResults, setCrossResults] = useState<MessageSummary[] | null>(
    null,
  );
  const [threadFilter, setThreadFilter] = useState<string | null>(null);
  const [cursor, setCursor] = useState(0);
  const [nextCursor, setNextCursor] = useState(0);
  const [cursorHistory, setCursorHistory] = useState<number[]>([]);
  const currentFolderRef = useRef(folderEnc);
  currentFolderRef.current = folderEnc;
  /** Folder the currently open UID lives in (differs for cross-folder hits). */
  const selectedEncRef = useRef<string | null>(null);
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
  /** Conversation grouping for the current page (server ids when present). */
  const decoratedMessages = useMemo(
    () => decorateThreads(messages),
    [messages],
  );
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
      pageOpts?: {
        cursor?: number;
        sort?: MailboxSortKey;
        order?: "asc" | "desc";
        thread?: boolean;
      },
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
          {
            limit: PAGE_SIZE,
            offset: pageOpts?.cursor ? 0 : pageOffset,
            q,
            cursor: pageOpts?.cursor,
            sort: pageOpts?.sort,
            order: pageOpts?.order,
            thread: pageOpts?.thread,
          },
          controller.signal,
        );
        if (
          controller.signal.aborted ||
          generation !== listRequest.current.generation ||
          currentFolderRef.current !== enc
        )
          return;
        setMessages(res.messages ?? []);
        setFolderValidity(res.uid_validity);
        setTotal(res.total);
        setOffset(res.offset);
        setNextCursor(res.next_cursor ?? 0);
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

  /** Options describing the current sort/group selection for reloads. */
  const listOpts = useCallback(
    () => ({
      sort: sortBy,
      order: (sortAsc ? "asc" : "desc") as "asc" | "desc",
      thread: threadGroup,
    }),
    [sortBy, sortAsc, threadGroup],
  );

  const refresh = useCallback(async () => {
    if (!hasMailboxSession()) return;
    setBusy(true);
    setError(null);
    try {
      await loadFolders();
      const enc = currentFolderRef.current;
      if (enc)
        await loadMessages(
          enc,
          offset,
          undefined,
          searchQ,
          cursor || cursorHistory.length
            ? { ...listOpts(), cursor }
            : listOpts(),
        );
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.load_failed"));
    } finally {
      setBusy(false);
    }
  }, [
    handleAuthError,
    loadFolders,
    loadMessages,
    offset,
    searchQ,
    t,
    listOpts,
    cursor,
    cursorHistory.length,
  ]);

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
    setCrossResults(null);
    setThreadFilter(null);
    setCursor(0);
    setNextCursor(0);
    setCursorHistory([]);
    setBusy(true);
    loadMessages(folderEnc, 0, controller.signal, "", listOpts())
      .catch((err) => {
        if (controller.signal.aborted) return;
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(false);
      });
    return () => controller.abort();
  }, [folderEnc, loadMessages, handleAuthError, t, clearChecked, listOpts]);

  useEffect(() => {
    if (!folderEnc || selectedUid == null) {
      setDetail(null);
      return;
    }
    // Cross-folder results can open a UID that lives in another mailbox.
    const detailEnc = selectedEncRef.current ?? folderEnc;
    setLoadImages(false);
    const controller = new AbortController();
    getMessage(detailEnc, selectedUid, {
      loadImages,
      signal: controller.signal,
      uidValidity: citationValidityRef.current,
    })
      .then(async (msg) => {
        if (controller.signal.aborted) return;
        setDetail(msg);
        if (!hasFlag(msg.flags, "\\Seen")) {
          try {
            await flagMessages(detailEnc, {
              uids: [selectedUid],
              uid_validity: msg.uid_validity,
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
  }, [
    folderEnc,
    selectedUid,
    loadImages,
    handleAuthError,
    t,
    citationNavigation,
  ]);

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
    if (
      (draft.mode === "reply" || draft.mode === "reply-all") &&
      detail?.uid_validity &&
      (selectedEncRef.current ?? folderEnc)
    ) {
      draft = {
        ...draft,
        aiSource: {
          folder_enc: (selectedEncRef.current ?? folderEnc)!,
          uid: detail.uid,
          uid_validity: detail.uid_validity,
        },
      };
    }
    setComposeDraft(draft);
    setComposeOpen(true);
  };

  const openForward = async (mode: "body" | "eml" = "body") => {
    const message = detail;
    const source = selectedEncRef.current ?? folderEnc;
    if (!message || !source || !message.uid_validity || forwardBusy) return;
    forwardRequest.current?.abort();
    const controller = new AbortController();
    forwardRequest.current = controller;
    setForwardBusy(true);
    setError(null);
    try {
      if (
        mode === "body" &&
        (message.attachments_capped ||
          (message.attachments?.length ?? 0) > MAX_SEND_ATTACHMENTS)
      )
        throw Error("INCOMPLETE_FORWARD_ATTACHMENTS");
      const attachments: OutgoingAttachment[] = [];
      if (mode === "eml")
        attachments.push(
          await fetchMessageAsOutgoing(
            source,
            message.uid,
            message.uid_validity,
            controller.signal,
          ),
        );
      for (const attachment of mode === "body"
        ? (message.attachments ?? [])
        : []) {
        attachments.push(
          await fetchAttachmentAsOutgoing(
            source,
            message.uid,
            attachment.part_id,
            attachment.filename || `attachment-${attachment.part_id}`,
            attachment.content_type || "application/octet-stream",
            message.uid_validity,
            controller.signal,
          ),
        );
        assertAttachmentTotal(attachments);
      }
      if (
        controller.signal.aborted ||
        liveDetail.current?.uid !== message.uid ||
        liveDetail.current?.uid_validity !== message.uid_validity ||
        (selectedEncRef.current ?? currentFolderRef.current) !== source
      )
        return;
      assertAttachmentTotal(attachments);
      openCompose({
        ...draftFromMessage("forward", message),
        ...(mode === "eml" ? { text: "", quoteText: undefined } : {}),
        attachments,
      });
    } catch (err) {
      if (!controller.signal.aborted && !handleAuthError(err))
        setError(
          errorMessage(err, t, "page.webmail.forward_attachments_failed"),
        );
    } finally {
      if (forwardRequest.current === controller) {
        forwardRequest.current = null;
        setForwardBusy(false);
      }
    }
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
            message.uid_validity,
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
    forwardRequest.current?.abort();
    citationValidityRef.current = undefined;
    selectedEncRef.current = folderEnc;
    setSelectedUid(uid);
    setMobilePane("detail");
  };

  /** Open a cross-folder search hit in its own mailbox. */
  const openCrossMessage = (m: MessageSummary) => {
    forwardRequest.current?.abort();
    citationValidityRef.current = undefined;
    selectedEncRef.current = m.folder_enc ?? folderEnc;
    setSelectedUid(m.uid);
    setMobilePane("detail");
  };

  const backToList = () => {
    setMobilePane("list");
  };

  const selectFolder = (enc: string) => {
    forwardRequest.current?.abort();
    setMoveTo("");
    setBulkMoveTo("");
    setSelectedUid(null);
    setDetail(null);
    clearChecked();
    setFolderValidity(undefined);
    setMutationUncertain(false);
    setMutationNotice("");
    currentFolderRef.current = enc;
    listRequest.current.generation += 1;
    listRequest.current.controller?.abort();
    selectedEncRef.current = null;
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
      if (!folderValidity) return;
      await flagMessages(folderEnc, {
        uids: checkedUids,
        add,
        remove,
        uid_validity: folderValidity,
      });
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
      await flagMessages(selectedEncRef.current ?? folderEnc, {
        uid_validity: detail.uid_validity,
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

  const reportMutation = (
    results: Array<{ uid: number; status: string; code?: string }>,
  ) => {
    const uncertain = results.some((item) => item.status === "unknown");
    setMutationUncertain(uncertain);
    setMutationNotice(
      results
        .map(
          (item) =>
            `${item.uid}: ${t(`page.webmail.transfer_${item.status}`, { defaultValue: item.status })}${item.code ? ` (${item.code})` : ""}`,
        )
        .join(" · "),
    );
    if (uncertain) setError(t("page.webmail.transfer_uncertain"));
  };
  const transfer = async (
    action: "copy" | "restore",
    uids: number[],
    to: string,
    source = folderEnc,
    validity = folderValidity,
  ) => {
    if (
      !source ||
      !validity ||
      !uids.length ||
      mutationBusy ||
      mutationUncertain
    )
      return;
    setMutationBusy(true);
    setError(null);
    try {
      const result = await mailboxTransfer(source, action, {
        uids,
        to,
        uid_validity: validity,
      });
      reportMutation(result.results);
      const successful = new Set(
        result.results
          .filter((item) => item.status === "success")
          .map((item) => item.uid),
      );
      setCheckedUids((previous) =>
        previous.filter((uid) => !successful.has(uid)),
      );
      if (action === "restore" && selectedUid && successful.has(selectedUid)) {
        setDetail(null);
        setSelectedUid(null);
        setMobilePane("list");
      }
      await loadFolders();
      if (folderEnc)
        await loadMessages(folderEnc, offset, undefined, searchQ, listOpts());
    } catch (err) {
      if (!handleAuthError(err)) {
        if (err instanceof ApiRequestError && err.status < 500)
          setError(errorMessage(err, t, "page.webmail.action_failed"));
        else {
          setMutationUncertain(true);
          setError(t("page.webmail.transfer_uncertain"));
        }
      }
    } finally {
      setMutationBusy(false);
    }
  };
  const openDeleteConfirm = (
    uids: number[],
    source = folderEnc,
    validity = folderValidity,
  ) => {
    if (
      uids.length === 0 ||
      !source ||
      !validity ||
      mutationBusy ||
      mutationUncertain
    )
      return;
    setDeleteSource({
      enc: source,
      validity,
      trash: isTrashFolder(
        folders.find((folder) => folder.name_enc === source),
      ),
    });
    setDeleteUids(uids);
    setDeleteOpen(true);
  };

  const confirmDelete = async () => {
    if (!folderEnc || !deleteSource || deleteUids.length === 0 || mutationBusy)
      return;
    setMutationBusy(true);
    try {
      let successful = new Set(deleteUids);
      if (deleteSource.trash)
        await deleteMessages(deleteSource.enc, {
          uids: deleteUids,
          expunge: true,
          uid_validity: deleteSource.validity,
        });
      else {
        const result = await mailboxTransfer(deleteSource.enc, "trash", {
          uids: deleteUids,
          uid_validity: deleteSource.validity,
        });
        reportMutation(result.results);
        successful = new Set(
          result.results
            .filter((item) => item.status === "success")
            .map((item) => item.uid),
        );
      }
      await loadFolders();
      if (selectedUid != null && successful.has(selectedUid)) {
        setSelectedUid(null);
        setDetail(null);
        setMobilePane("list");
      }
      setCheckedUids((previous) =>
        previous.filter((uid) => !successful.has(uid)),
      );
      await loadMessages(folderEnc, offset, undefined, searchQ, listOpts());
    } catch (err) {
      if (handleAuthError(err)) return;
      if (!(err instanceof ApiRequestError) || err.status >= 500) {
        setMutationUncertain(true);
        setError(t("page.webmail.transfer_uncertain"));
      } else setError(errorMessage(err, t, "page.webmail.action_failed"));
    } finally {
      setDeleteOpen(false);
      setDeleteUids([]);
      setMutationBusy(false);
    }
  };

  const doMove = async (
    uids: number[],
    to: string,
    source = folderEnc,
    validity = folderValidity,
  ) => {
    if (!folderEnc || !source || !validity || uids.length === 0 || !to) return;
    try {
      await moveMessages(source, { uids, to, uid_validity: validity });
      if (selectedUid != null && uids.includes(selectedUid)) {
        setSelectedUid(null);
        setDetail(null);
        setMobilePane("list");
      }
      setMoveTo("");
      setBulkMoveTo("");
      clearChecked();
      await loadMessages(folderEnc, offset, undefined, searchQ, listOpts());
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
    const date = usableMessageDate(iso);
    if (!date) return "—";
    try {
      const preference = datePreferences.date_format;
      const locale =
        preference === "iso"
          ? "sv-SE"
          : preference === "day_first"
            ? "en-GB"
            : preference === "month_first"
              ? "en-US"
              : i18n.language;
      return new Intl.DateTimeFormat(locale, {
        dateStyle: "short",
        timeStyle: "medium",
        ...(typeof datePreferences.timezone === "string" &&
        datePreferences.timezone
          ? { timeZone: datePreferences.timezone }
          : {}),
      }).format(date);
    } catch {
      return iso;
    }
  };

  const loadPage = (dir: "next" | "prev") => {
    if (!folderEnc) return;
    clearChecked();
    setThreadFilter(null);
    setBusy(true);
    let target = 0;
    if (dir === "next") {
      target = nextCursor;
      setCursorHistory((h) => [...h, cursor]);
    } else {
      const history = [...cursorHistory];
      target = history.pop() ?? 0;
      setCursorHistory(history);
    }
    setCursor(target);
    void loadMessages(folderEnc, 0, undefined, searchQ, {
      ...listOpts(),
      cursor: target,
    })
      .catch((err) => {
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => setBusy(false));
  };

  const resetListCursor = () => {
    setCursor(0);
    setNextCursor(0);
    setCursorHistory([]);
    setThreadFilter(null);
    clearChecked();
    setSelectedUid(null);
    setDetail(null);
  };

  const runSearch = (q: string) => {
    if (!folderEnc) return;
    setSearchQ(q);
    resetListCursor();
    setBusy(true);
    if (searchAllFolders && q.trim() !== "") {
      setCrossResults(null);
      void crossFolderSearch(q)
        .then((res) => {
          setCrossResults(res.messages ?? []);
          setTotal(res.total ?? 0);
        })
        .catch((err) => {
          if (handleAuthError(err)) return;
          setError(errorMessage(err, t, "page.webmail.load_failed"));
        })
        .finally(() => setBusy(false));
      return;
    }
    setCrossResults(null);
    void loadMessages(folderEnc, 0, undefined, q, listOpts())
      .catch((err) => {
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => setBusy(false));
  };

  /** Re-list on sort / direction / conversation-toggle changes. */
  const applyListOptions = (
    sort: MailboxSortKey,
    asc: boolean,
    threads: boolean,
  ) => {
    if (!folderEnc) return;
    setSortBy(sort);
    setSortAsc(asc);
    setThreadGroup(threads);
    resetListCursor();
    setBusy(true);
    void loadMessages(folderEnc, 0, undefined, searchQ, {
      sort,
      order: asc ? "asc" : "desc",
      thread: threads,
    })
      .catch((err) => {
        if (handleAuthError(err)) return;
        setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => setBusy(false));
  };

  const folderList = (
    <ul className="space-y-0.5" aria-label={t("page.webmail.folders")}>
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
      <header className="border-b bg-card px-3 sm:px-4 py-2.5 flex items-center justify-between gap-2 select-none">
        <div className="flex items-center gap-2.5 min-w-0">
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
          <div className="flex items-center gap-1.5 min-w-0">
            <Mail className="h-4 w-4 shrink-0 text-primary" />
            <span className="font-bold text-sm tracking-tight hidden sm:inline">urMail</span>
            <span className="text-muted-foreground/60 hidden sm:inline">·</span>
            <span className="text-sm font-medium truncate">{address}</span>
          </div>
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          <Button
            size="sm"
            variant="default"
            className="min-h-11 md:min-h-8 font-medium shadow-xs"
            onClick={() => openCompose({ mode: "new" })}
          >
            <Pencil className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.compose_new")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="min-h-11 md:min-h-8"
            onClick={() => {
              setMutationUncertain(false);
              setMutationNotice("");
              clearChecked();
              void refresh();
            }}
          >
            <RefreshCw className="h-3.5 w-3.5 mr-1" />
            {t("page.webmail.refresh")}
          </Button>
          <AppearanceControls />
          <MailboxKeyboardShortcuts
            disabled={
              busy ||
              mutationBusy ||
              forwardBusy ||
              composeOpen ||
              passwordOpen ||
              deleteOpen ||
              folderManagerOpen ||
              scheduledOpen ||
              importOpen
            }
            onAction={(action) => {
              if (action === "compose") openCompose({ mode: "new" });
              else if (action === "search")
                document
                  .querySelector<HTMLInputElement>('form[role="search"] input')
                  ?.focus();
              else if (action === "close") {
                setSelectedUid(null);
                setDetail(null);
                setMobilePane("list");
              } else if (action === "next" || action === "previous") {
                if (crossResults) return;
                const index = messages.findIndex(
                  (message) => message.uid === selectedUid,
                );
                const next =
                  index < 0 ? 0 : index + (action === "next" ? 1 : -1);
                if (messages[next]) openMessage(messages[next].uid);
              } else if (detail && detail.uid === selectedUid) {
                if (action === "reply" || action === "replyAll")
                  openCompose(
                    draftFromMessage(
                      action === "reply" ? "reply" : "reply-all",
                      detail,
                    ),
                  );
                else if (action === "forward") void openForward();
                else if (action === "trash")
                  openDeleteConfirm(
                    [detail.uid],
                    selectedEncRef.current ?? folderEnc,
                    detail.uid_validity,
                  );
              }
            }}
          />
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="sm"
                variant="outline"
                className="min-h-11 md:min-h-8 gap-1"
                aria-label={t("page.webmail.more_menu")}
              >
                <Settings className="h-3.5 w-3.5" />
                <span className="hidden sm:inline">{t("page.webmail.more_menu")}</span>
                <ChevronDown className="h-3 w-3 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuLabel>{t("nav.settings")}</DropdownMenuLabel>
              <DropdownMenuGroup>
                <DropdownMenuItem asChild>
                  <Link to="/webmail/settings">
                    <Settings className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                    <span>{t("page.webmail.settings_title")}</span>
                  </Link>
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setPasswordOpen(true)}>
                  <KeyRound className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                  <span>{t("page.webmail.password_title")}</span>
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setFolderManagerOpen(true)}>
                  <Folder className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                  <span>{t("page.webmail.folder_manage")}</span>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <Link to="/webmail/people">
                    <Users className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                    <span>{t("people.title")}</span>
                  </Link>
                </DropdownMenuItem>
              </DropdownMenuGroup>
              <DropdownMenuSeparator />
              <DropdownMenuLabel>{t("page.webmail.sieve")}</DropdownMenuLabel>
              <DropdownMenuGroup>
                <DropdownMenuItem asChild>
                  <a href="/mailbox/sieve">
                    <Filter className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                    <span>{t("page.webmail.sieve")}</span>
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setScheduledOpen(true)}>
                  <Clock className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                  <span>{t("page.webmail.scheduled_title")}</span>
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={
                    busy ||
                    mutationBusy ||
                    !folderEnc ||
                    !folderValidity ||
                    !!crossResults
                  }
                  onClick={() => setImportOpen(true)}
                >
                  <Upload className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                  <span>{t("page.webmail.import.title")}</span>
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={
                    exportBusy ||
                    mutationBusy ||
                    !folderEnc ||
                    !folderValidity ||
                    !!crossResults
                  }
                  onClick={() => {
                    if (!folderEnc || !folderValidity) return;
                    setExportBusy(true);
                    setError(null);
                    void downloadMailboxMbox(
                      folderEnc,
                      folderValidity,
                      checkedUids,
                    )
                      .catch((cause) => {
                        if (handleAuthError(cause)) return;
                        setError(
                          errorMessage(
                            cause,
                            t,
                            "page.webmail.import.export_failed",
                          ),
                        );
                      })
                      .finally(() => setExportBusy(false));
                  }}
                >
                  <Download className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                  <span>{t("page.webmail.import.export_mbox")}</span>
                </DropdownMenuItem>
              </DropdownMenuGroup>
              <DropdownMenuSeparator />
              <DropdownMenuGroup>
                <DropdownMenuItem asChild>
                  <Link to="/webmail/help">
                    <HelpCircle className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                    <span>{t("page.webmail.help_title")}</span>
                  </Link>
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => void logout()}
                  className="text-destructive hover:bg-destructive/10 focus:text-destructive"
                >
                  <LogOut className="h-3.5 w-3.5 mr-2" />
                  <span>{t("page.webmail.logout")}</span>
                </DropdownMenuItem>
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      <div className="px-3 sm:px-4">
        <MailboxReminders />
      </div>
      <MailboxAIAsk
        folders={folders}
        onCitation={(source) => {
          selectFolder(source.folder_enc);
          selectedEncRef.current = source.folder_enc;
          citationValidityRef.current = source.uid_validity;
          setSelectedUid(source.uid);
          setCitationNavigation((value) => value + 1);
          setMobilePane("detail");
        }}
      />

      {mutationNotice && (
        <p role="status" className="px-4 py-2 text-sm border-b break-words">
          {mutationNotice}
        </p>
      )}
      {error && (
        <p role="alert" className="px-4 py-2 text-sm text-destructive border-b">
          {error}{" "}
          <Link
            className="underline underline-offset-4"
            to="/support?topic=sync"
          >
            {t("support.sync_help_action")}
          </Link>
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
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <select
                aria-label={t("page.webmail.sort_by")}
                className="h-11 md:h-8 rounded-md border border-input bg-background px-2 text-xs"
                value={sortBy}
                onChange={(e) =>
                  applyListOptions(
                    e.target.value as MailboxSortKey,
                    sortAsc,
                    threadGroup,
                  )
                }
                disabled={busy || !folderEnc}
              >
                <option value="">{t("page.webmail.sort_date")}</option>
                <option value="date">{t("page.webmail.sort_date")}</option>
                <option value="subject">
                  {t("page.webmail.sort_subject")}
                </option>
                <option value="from">{t("page.webmail.sort_from")}</option>
                <option value="size">{t("page.webmail.sort_size")}</option>
              </select>
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="min-h-11 md:min-h-8"
                aria-label={
                  sortAsc
                    ? t("page.webmail.sort_ascending")
                    : t("page.webmail.sort_descending")
                }
                disabled={busy || !folderEnc}
                onClick={() => applyListOptions(sortBy, !sortAsc, threadGroup)}
              >
                <ArrowDownWideNarrow
                  className={sortAsc ? "h-3.5 w-3.5 rotate-180" : "h-3.5 w-3.5"}
                />
                {sortAsc
                  ? t("page.webmail.sort_ascending")
                  : t("page.webmail.sort_descending")}
              </Button>
              <label className="inline-flex items-center gap-1.5 cursor-pointer select-none min-h-11 md:min-h-0">
                <input
                  type="checkbox"
                  className="h-5 w-5 md:h-4 md:w-4 accent-primary"
                  checked={threadGroup}
                  disabled={busy || !folderEnc}
                  onChange={(e) =>
                    applyListOptions(sortBy, sortAsc, e.target.checked)
                  }
                />
                <Layers className="h-3.5 w-3.5" />
                {t("page.webmail.thread_group")}
              </label>
              <label className="inline-flex items-center gap-1.5 cursor-pointer select-none min-h-11 md:min-h-0">
                <input
                  type="checkbox"
                  className="h-5 w-5 md:h-4 md:w-4 accent-primary"
                  checked={searchAllFolders}
                  disabled={busy || !folderEnc}
                  onChange={(e) => {
                    setSearchAllFolders(e.target.checked);
                    setCrossResults(null);
                  }}
                />
                {t("page.webmail.search_all_folders")}
              </label>
              {threadFilter != null && (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="min-h-11 md:min-h-8"
                  onClick={() => setThreadFilter(null)}
                >
                  {t("page.webmail.thread_show_all")}
                </Button>
              )}
            </div>
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
                  disabled={
                    mutationBusy || mutationUncertain || !folderValidity
                  }
                  onClick={() => openDeleteConfirm(checkedUids)}
                >
                  <Trash2 className="h-3.5 w-3.5 mr-1" />
                  {t("page.webmail.delete")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-11 md:min-h-8"
                  disabled={
                    exportBusy ||
                    mutationBusy ||
                    mutationUncertain ||
                    !folderValidity ||
                    !folderEnc ||
                    !!crossResults
                  }
                  onClick={() => {
                    if (!folderEnc || !folderValidity) return;
                    setExportBusy(true);
                    setError(null);
                    void downloadMailboxMbox(
                      folderEnc,
                      folderValidity,
                      checkedUids,
                    )
                      .catch((cause) => {
                        if (handleAuthError(cause)) return;
                        setError(
                          errorMessage(
                            cause,
                            t,
                            "page.webmail.import.export_failed",
                          ),
                        );
                      })
                      .finally(() => setExportBusy(false));
                  }}
                >
                  <Download className="h-3.5 w-3.5 mr-1" />
                  {t("page.webmail.import.export_mbox")}
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
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={
                      !bulkMoveTo ||
                      !folderValidity ||
                      mutationBusy ||
                      mutationUncertain
                    }
                    onClick={() =>
                      void transfer("copy", checkedUids, bulkMoveTo)
                    }
                  >
                    {t("page.webmail.copy")}
                  </Button>
                  {isTrashFolder(currentFolder) && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={
                        !folderValidity || mutationBusy || mutationUncertain
                      }
                      onClick={() =>
                        void transfer(
                          "restore",
                          checkedUids,
                          bulkMoveTo || "INBOX",
                        )
                      }
                    >
                      {t("page.webmail.restore")}
                    </Button>
                  )}
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
          <ul
            className="flex-1 overflow-y-auto divide-y min-h-0"
            aria-label={t("page.webmail.message_list")}
          >
            {(() => {
              const showing =
                crossResults ??
                (() => {
                  if (threadFilter == null) return messages;
                  return decoratedMessages
                    .filter((d) => d.threadKey === threadFilter)
                    .map((d) => d.message);
                })();
              if (showing.length === 0 && !busy) {
                return (
                  <li className="p-4 text-sm text-muted-foreground">
                    {crossResults
                      ? t("page.webmail.search_no_results")
                      : t("page.webmail.empty_folder")}
                  </li>
                );
              }
              const threadCountOf = (m: MessageSummary): number =>
                crossResults
                  ? 1
                  : (decoratedMessages.find((d) => d.message.uid === m.uid)
                      ?.threadCount ?? 1);
              const threadKeyOf = (m: MessageSummary): string =>
                decoratedMessages.find((d) => d.message.uid === m.uid)
                  ?.threadKey ?? String(m.uid);
              return showing.map((m) => {
                const unread = !hasFlag(m.flags, "\\Seen");
                const flagged = hasFlag(m.flags, "\\Flagged");
                const checked = checkedUids.includes(m.uid);
                const threadCount = threadCountOf(m);
                return (
                  <li key={m.uid} className="flex items-stretch min-w-0">
                    <label className="flex items-center px-2 shrink-0 cursor-pointer min-h-14 md:min-h-0">
                      <input
                        type="checkbox"
                        className="h-5 w-5 accent-primary"
                        checked={checked}
                        aria-label={t("page.webmail.select_message_named", {
                          name:
                            formatFrom(m.from) ||
                            m.subject ||
                            t("page.webmail.no_subject"),
                        })}
                        onChange={() =>
                          setCheckedUids((prev) => toggleUid(prev, m.uid))
                        }
                        onClick={(e) => e.stopPropagation()}
                      />
                    </label>
                    <button
                      type="button"
                      className={
                        "flex-1 min-w-0 text-left px-2 py-3 md:py-2 hover:bg-muted/60 min-h-14 md:min-h-0 " +
                        (selectedUid === m.uid ? "bg-muted" : "")
                      }
                      onClick={() =>
                        crossResults ? openCrossMessage(m) : openMessage(m.uid)
                      }
                    >
                      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                        {flagged && (
                          <Flag className="h-3 w-3 text-amber-500 fill-amber-500 shrink-0" />
                        )}
                        {crossResults && m.folder && (
                          <span className="shrink-0 rounded bg-muted px-1 py-0.5">
                            {m.folder}
                          </span>
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
                        className={
                          "text-sm truncate " + (unread ? "font-semibold" : "")
                        }
                      >
                        {m.subject || t("page.webmail.no_subject")}
                      </p>
                    </button>
                    {threadGroup && !crossResults && threadCount > 1 && (
                      <button
                        type="button"
                        className="flex items-center gap-1 px-2 shrink-0 text-xs text-muted-foreground hover:text-foreground"
                        aria-label={t("page.webmail.thread_filter", {
                          count: threadCount,
                        })}
                        title={t("page.webmail.thread_filter", {
                          count: threadCount,
                        })}
                        onClick={() =>
                          setThreadFilter(
                            threadKeyOf(m) === threadFilter
                              ? null
                              : threadKeyOf(m),
                          )
                        }
                      >
                        <Layers className="h-3 w-3" />
                        {threadCount}
                      </button>
                    )}
                  </li>
                );
              });
            })()}
          </ul>
          {!crossResults && (cursorHistory.length > 0 || nextCursor) && (
            <div className="flex gap-2 p-2 border-t">
              <Button
                size="sm"
                variant="outline"
                className="min-h-11 md:min-h-8"
                disabled={cursorHistory.length === 0 || busy || !folderEnc}
                onClick={() => loadPage("prev")}
              >
                {t("page.webmail.prev_page")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                className="min-h-11 md:min-h-8"
                disabled={!nextCursor || busy || !folderEnc}
                onClick={() => loadPage("next")}
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
                          openCompose(draftFromMessage("reply-all", detail))
                        }
                      >
                        <ReplyAll className="h-3.5 w-3.5 mr-1" />
                        {t("page.webmail.compose_reply_all")}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        className="min-h-11 md:min-h-8"
                        disabled={forwardBusy}
                        onClick={() => void openForward()}
                      >
                        <Forward className="h-3.5 w-3.5 mr-1" />
                        {t("page.webmail.compose_forward")}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        className="min-h-11 md:min-h-8"
                        disabled={forwardBusy}
                        onClick={() => void openForward("eml")}
                      >
                        {t("page.webmail.compose_forward_eml")}
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
                    disabled={
                      mutationBusy || mutationUncertain || !detail.uid_validity
                    }
                    onClick={() =>
                      openDeleteConfirm(
                        selectedUid != null ? [selectedUid] : [],
                        selectedEncRef.current ?? folderEnc,
                        detail.uid_validity,
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
                        void doMove(
                          [selectedUid],
                          moveTo,
                          selectedEncRef.current ?? folderEnc,
                          detail.uid_validity,
                        )
                      }
                    >
                      {t("page.webmail.move")}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={
                        !moveTo ||
                        !detail.uid_validity ||
                        mutationBusy ||
                        mutationUncertain
                      }
                      onClick={() =>
                        selectedUid != null &&
                        void transfer(
                          "copy",
                          [selectedUid],
                          moveTo,
                          selectedEncRef.current ?? folderEnc,
                          detail.uid_validity,
                        )
                      }
                    >
                      {t("page.webmail.copy")}
                    </Button>
                    {isTrashFolder(
                      folders.find(
                        (folder) =>
                          folder.name_enc ===
                          (selectedEncRef.current ?? folderEnc),
                      ),
                    ) && (
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={
                          !detail.uid_validity ||
                          mutationBusy ||
                          mutationUncertain
                        }
                        onClick={() =>
                          selectedUid != null &&
                          void transfer(
                            "restore",
                            [selectedUid],
                            moveTo || "INBOX",
                            selectedEncRef.current ?? folderEnc,
                            detail.uid_validity,
                          )
                        }
                      >
                        {t("page.webmail.restore")}
                      </Button>
                    )}
                  </div>
                </div>
              </div>
              <div className="flex-1 overflow-y-auto p-4 space-y-3 min-h-0">
                <MailboxMessageTools
                  key={`tools:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                  folder={(selectedEncRef.current ?? folderEnc)!}
                  message={detail}
                />
                {detail.uid_validity &&
                  (selectedEncRef.current ?? folderEnc) && (
                    <MailboxAIPanel
                      key={`ai:${selectedEncRef.current ?? folderEnc}:${detail.uid}`}
                      mode="read"
                      source={{
                        folder_enc: (selectedEncRef.current ?? folderEnc)!,
                        uid: detail.uid,
                        uid_validity: detail.uid_validity,
                      }}
                      subject={detail.subject}
                      body={detail.text ?? ""}
                      onApply={async (proposal) => {
                        const reply = draftFromMessage("reply", detail);
                        const [settings, identityList] = await Promise.all([
                          getMailboxSettings(),
                          listMailboxIdentities(),
                        ]);
                        const from = chooseMailboxIdentity(
                          settings,
                          identityList.identities,
                          reply,
                        );
                        const result = await saveDraft({
                          from,
                          to: (reply.to ?? "").split(/[,;\s]+/).filter(Boolean),
                          cc: (reply.cc ?? "").split(/[,;\s]+/).filter(Boolean),
                          subject: reply.subject ?? proposal.subject,
                          in_reply_to: detail.message_id,
                          reply_source: {
                            folder_enc: (selectedEncRef.current ?? folderEnc)!,
                            uid: detail.uid,
                            uid_validity: detail.uid_validity!,
                          },
                          text: proposal.body,
                        });
                        openCompose({
                          ...reply,
                          from,
                          mode: "draft",
                          uid: result.uid,
                          uidValidity: result.uid_validity,
                          subject: reply.subject ?? proposal.subject,
                          text: proposal.body,
                        });
                      }}
                    />
                  )}
                {detail.uid_validity && folderEnc && (
                  <MailboxInvitationPanel
                    key={`invitation:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                    folderEnc={selectedEncRef.current ?? folderEnc}
                    uid={detail.uid}
                    uidValidity={detail.uid_validity}
                  />
                )}
                {detail.html ? (
                  <MessageHTMLWithAttachments
                    key={`html:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                    message={detail}
                    source={{
                      folderEnc: (selectedEncRef.current ?? folderEnc)!,
                      uid: detail.uid,
                      uidValidity: detail.uid_validity ?? 0,
                    }}
                    loadImages={loadImages}
                    onLoadImages={() => setLoadImages(true)}
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
                            {attachmentDisplayName(a.filename, a.part_id)}{" "}
                            <span className="text-xs text-muted-foreground">
                              ({a.content_type}, {a.size} B)
                            </span>
                          </span>
                          {folderEnc && selectedUid != null && (
                            <AttachmentPreview
                              key={`${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}:${a.part_id}`}
                              source={{
                                folderEnc: selectedEncRef.current ?? folderEnc,
                                uid: detail.uid,
                                uidValidity: detail.uid_validity ?? 0,
                              }}
                              attachment={a}
                            />
                          )}
                          {folderEnc && selectedUid != null && (
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              className="shrink-0 min-h-11 md:min-h-8"
                              onClick={() => {
                                void downloadAttachment(
                                  selectedEncRef.current ?? folderEnc,
                                  selectedUid,
                                  a.part_id,
                                  a.filename || `attachment-${a.part_id}`,
                                  detail.uid_validity,
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
            void loadMessages(
              folderEnc,
              offset,
              undefined,
              searchQ,
              listOpts(),
            );
        }}
        onDraftSaved={() => {
          const drafts = folders.find((f) => isDraftsFolder(f));
          if (drafts && folderEnc === drafts.name_enc) {
            void loadMessages(
              folderEnc,
              offset,
              undefined,
              searchQ,
              listOpts(),
            );
          }
        }}
      />
      <MailboxFolderDialog
        open={folderManagerOpen}
        onOpenChange={setFolderManagerOpen}
        folder={currentFolder}
        uidValidity={folderValidity}
        onChanged={async (enc) => {
          await loadFolders();
          if (enc) setFolderEnc(enc);
        }}
      />
      {folderEnc && folderValidity && (
        <MailboxImportDialog
          open={importOpen}
          onOpenChange={setImportOpen}
          folder={folderEnc}
          validity={folderValidity}
          onImported={() => {
            void refresh();
          }}
        />
      )}
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
          !deleteSource?.trash
            ? t("page.webmail.trash_confirm_title")
            : deleteUids.length > 1
              ? t("page.webmail.bulk_delete_confirm_title")
              : t("page.webmail.delete_confirm_title")
        }
        description={
          !deleteSource?.trash
            ? t("page.webmail.trash_confirm")
            : deleteUids.length > 1
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
