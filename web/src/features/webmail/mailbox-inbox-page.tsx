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
import { MailboxAppRail, MailboxAppId } from "./mailbox-app-rail";
import { useResizablePanes, SplitterHandle } from "./mailbox-splitter";
import { MailboxAIDrawer } from "./mailbox-ai-drawer";
import { MailboxInlineReply } from "./mailbox-inline-reply";
import { MailboxMessageRow } from "./mailbox-message-row";
import { MailboxDetailHeader } from "./mailbox-detail-header";
import { PeopleCalendarPanel } from "./people-calendar-panel";
import { PeopleContactsPanel } from "./people-contacts-panel";
import {
  Columns3,
  Rows2,
  PanelLeftClose,
  PanelLeftOpen,
  Sparkles as SparklesIcon,
  Paperclip,
  Calendar as CalendarIcon,
} from "lucide-react";
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
  MailboxSettingsView,
  MailboxIdentity,
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
  const [mailboxSettings, setMailboxSettings] =
    useState<MailboxSettingsView | null>(null);
  const [identities, setIdentities] = useState<MailboxIdentity[]>([]);
  const [activeApp, setActiveApp] = useState<MailboxAppId>("mail");
  const [aiDrawerOpen, setAiDrawerOpen] = useState(false);
  const [folderCollapsed, setFolderCollapsed] = useState(false);
  const resizable = useResizablePanes();

  useEffect(() => {
    let active = true;
    Promise.all([
      getMailboxSettings().catch(() => null),
      listMailboxIdentities().catch(() => ({ identities: [] })),
    ]).then(([settings, idList]) => {
      if (!active) return;
      if (settings) {
        setMailboxSettings(settings);
        setDatePreferences(settings.preferences ?? {});
      }
      if (idList?.identities) {
        setIdentities(idList.identities);
      }
    });
    return () => {
      active = false;
    };
  }, []);

  const toggleRowFlag = async (m: MessageSummary, e: React.MouseEvent) => {
    e.stopPropagation();
    const isFlagged = hasFlag(m.flags, "\\Flagged");
    const toAdd = isFlagged ? [] : ["\\Flagged"];
    const toRemove = isFlagged ? ["\\Flagged"] : [];
    setMessages((prev) =>
      prev.map((item) =>
        item.uid === m.uid
          ? {
              ...item,
              flags: isFlagged
                ? item.flags.filter((f) => f !== "\\Flagged")
                : [...item.flags, "\\Flagged"],
            }
          : item,
      ),
    );
    try {
      if (folderEnc) {
        await flagMessages(folderEnc, {
          uids: [m.uid],
          add: toAdd,
          remove: toRemove,
          uid_validity: folderValidity,
        });
      }
    } catch {
      void refresh();
    }
  };

  const toggleRowRead = async (m: MessageSummary, e: React.MouseEvent) => {
    e.stopPropagation();
    const isRead = hasFlag(m.flags, "\\Seen");
    const toAdd = isRead ? [] : ["\\Seen"];
    const toRemove = isRead ? ["\\Seen"] : [];
    setMessages((prev) =>
      prev.map((item) =>
        item.uid === m.uid
          ? {
              ...item,
              flags: isRead
                ? item.flags.filter((f) => f !== "\\Seen")
                : [...item.flags, "\\Seen"],
            }
          : item,
      ),
    );
    try {
      if (folderEnc) {
        await flagMessages(folderEnc, {
          uids: [m.uid],
          add: toAdd,
          remove: toRemove,
          uid_validity: folderValidity,
        });
      }
    } catch {
      void refresh();
    }
  };
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
    <div className="h-screen w-screen flex flex-row bg-background overflow-hidden text-foreground">
      {/* Resizing overlay to prevent iframe stealing pointer events */}
      {resizable.isDragging && (
        <div className="fixed inset-0 z-50 pointer-events-auto bg-transparent select-none" />
      )}

      {/* 1. App Rail (Mail, Calendar, Contacts, AI, Logout) */}
      <MailboxAppRail
        activeApp={activeApp}
        onSelectApp={(app) => {
          setActiveApp(app);
          if (app === "mail") setMobilePane("list");
        }}
        aiOpen={aiDrawerOpen}
        onToggleAi={() => setAiDrawerOpen((prev) => !prev)}
        address={address}
        onLogout={() => void logout()}
        onOpenHelp={() => navigate("/webmail/help")}
      />

      {/* 2. Main Workspace */}
      <div className="flex-1 flex flex-col min-h-0 min-w-0 overflow-hidden bg-background">
        {activeApp === "calendar" ? (
          <div className="flex-1 overflow-y-auto p-4 sm:p-6 bg-card">
            <div className="max-w-6xl mx-auto space-y-4">
              <div className="flex items-center justify-between pb-3 border-b">
                <h2 className="text-xl font-bold flex items-center gap-2">
                  <CalendarIcon className="h-5 w-5 text-primary" />
                  {t("page.webmail.calendar_title", {
                    defaultValue: "日历日程",
                  })}
                </h2>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setActiveApp("mail")}
                >
                  {t("people.back_to_mail", { defaultValue: "返回邮件" })}
                </Button>
              </div>
              <PeopleCalendarPanel />
            </div>
          </div>
        ) : activeApp === "contacts" ? (
          <div className="flex-1 overflow-y-auto p-4 sm:p-6 bg-card">
            <div className="max-w-6xl mx-auto space-y-4">
              <div className="flex items-center justify-between pb-3 border-b">
                <h2 className="text-xl font-bold flex items-center gap-2">
                  <Users className="h-5 w-5 text-primary" />
                  {t("page.webmail.contacts_title", { defaultValue: "通讯录" })}
                </h2>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setActiveApp("mail")}
                >
                  {t("people.back_to_mail", { defaultValue: "返回邮件" })}
                </Button>
              </div>
              <PeopleContactsPanel />
            </div>
          </div>
        ) : (
          /* Mail App Workspace */
          <div className="flex-1 flex flex-col min-h-0 min-w-0 overflow-hidden">
            {/* Header */}
            <header className="border-b bg-card px-3 sm:px-4 py-2 flex items-center justify-between gap-2 select-none shrink-0 shadow-2xs">
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
                    <div className="flex-1 overflow-y-auto p-2">
                      {folderList}
                    </div>
                  </SheetContent>
                </Sheet>

                <div className="flex items-center gap-2 min-w-0">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="hidden md:inline-flex h-8 w-8 p-0 text-muted-foreground hover:text-foreground"
                    title={folderCollapsed ? "展开文件夹栏" : "折叠文件夹栏"}
                    onClick={() => setFolderCollapsed((prev) => !prev)}
                  >
                    {folderCollapsed ? (
                      <PanelLeftOpen className="h-4 w-4" />
                    ) : (
                      <PanelLeftClose className="h-4 w-4" />
                    )}
                  </Button>
                  <div className="flex items-center gap-1.5 min-w-0">
                    <Mail className="h-4 w-4 shrink-0 text-primary" />
                    <span className="font-bold text-sm tracking-tight hidden sm:inline">
                      urMail
                    </span>
                    <span className="text-muted-foreground/60 hidden sm:inline">
                      ·
                    </span>
                    <span className="text-sm font-medium truncate">
                      {address}
                    </span>
                  </div>
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
                  title={t("page.webmail.refresh")}
                  onClick={() => {
                    setMutationUncertain(false);
                    setMutationNotice("");
                    clearChecked();
                    void refresh();
                  }}
                >
                  <RefreshCw className="h-3.5 w-3.5 mr-1" />
                  <span className="hidden sm:inline">
                    {t("page.webmail.refresh")}
                  </span>
                </Button>

                {/* Layout Mode Toggle (Vertical 3-pane vs Horizontal Top-Bottom) */}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="hidden md:inline-flex h-8 w-8 p-0 text-muted-foreground hover:text-foreground"
                  title={
                    resizable.layoutMode === "vertical"
                      ? "切换为上下水平分栏"
                      : "切换为左右垂直三栏"
                  }
                  onClick={resizable.toggleLayoutMode}
                >
                  {resizable.layoutMode === "vertical" ? (
                    <Rows2 className="h-4 w-4" />
                  ) : (
                    <Columns3 className="h-4 w-4" />
                  )}
                </Button>

                {/* AI Copilot Toggle */}
                <Button
                  type="button"
                  size="sm"
                  variant={aiDrawerOpen ? "secondary" : "ghost"}
                  className="h-8 px-2 gap-1 text-xs text-amber-500 hover:text-amber-600 hover:bg-amber-500/10 font-medium"
                  title="AI 智能助手 (Copilot)"
                  onClick={() => setAiDrawerOpen((prev) => !prev)}
                >
                  <SparklesIcon className="h-3.5 w-3.5" />
                  <span className="hidden sm:inline">AI 助手</span>
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
                        .querySelector<HTMLInputElement>(
                          'form[role="search"] input',
                        )
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
                      <span className="hidden sm:inline">
                        {t("page.webmail.more_menu")}
                      </span>
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
                      <DropdownMenuItem
                        onClick={() => setFolderManagerOpen(true)}
                      >
                        <Folder className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                        <span>{t("page.webmail.folder_manage")}</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onClick={() => setActiveApp("contacts")}
                      >
                        <Users className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                        <span>{t("people.title")}</span>
                      </DropdownMenuItem>
                    </DropdownMenuGroup>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>
                      {t("page.webmail.sieve")}
                    </DropdownMenuLabel>
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

            {/* Accessible MailboxAIAsk container so tests can locate citations or questions */}
            <div className="sr-only">
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
            </div>

            {mutationNotice && (
              <p
                role="status"
                className="px-4 py-2 text-sm border-b break-words"
              >
                {mutationNotice}
              </p>
            )}
            {error && (
              <p
                role="alert"
                className="px-4 py-2 text-sm text-destructive border-b"
              >
                {error}{" "}
                <Link
                  className="underline underline-offset-4"
                  to="/support?topic=sync"
                >
                  {t("support.sync_help_action")}
                </Link>
              </p>
            )}

            {/* Splitter Panes Area */}
            <div className="flex-1 flex min-h-0 min-w-0 overflow-hidden">
              {/* Desktop folder pane */}
              {!folderCollapsed && (
                <>
                  <aside
                    style={{ width: `${resizable.folderWidth}px` }}
                    className="hidden md:flex flex-col border-r bg-card/60 p-2 overflow-y-auto min-h-0 shrink-0"
                  >
                    <div className="flex items-center justify-between px-2 py-1 mb-1">
                      <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        {t("page.webmail.folders")}
                      </p>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="h-5 w-5 p-0 text-muted-foreground hover:text-foreground"
                        title={t("page.webmail.folder_manage")}
                        onClick={() => setFolderManagerOpen(true)}
                      >
                        <Settings className="h-3 w-3" />
                      </Button>
                    </div>
                    {folderList}
                  </aside>
                  <SplitterHandle
                    onMouseDown={(e) =>
                      resizable.startDragging("folder", e.clientX, e.clientY)
                    }
                  />
                </>
              )}

              {/* Main Content Pane (List + Detail depending on layoutMode) */}
              <div
                className={
                  resizable.layoutMode === "horizontal"
                    ? "flex-1 flex flex-col min-h-0 min-w-0 overflow-hidden"
                    : "flex-1 flex flex-row min-h-0 min-w-0 overflow-hidden"
                }
              >
                {/* Message list pane */}
                <section
                  style={
                    resizable.layoutMode === "horizontal"
                      ? { height: `${resizable.listHeight}px` }
                      : { width: `${resizable.listWidth}px` }
                  }
                  className={`border-r flex flex-col min-h-0 min-w-0 bg-background/50 ${
                    mobilePane === "detail" ? "hidden md:flex" : "flex"
                  } ${resizable.layoutMode === "vertical" ? "shrink-0" : "shrink-0"}`}
                >
                  {/* Search and filters header */}
                  <div className="px-3 py-2 border-b space-y-2 bg-card/40 shrink-0">
                    <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                      <span className="truncate min-w-0 font-medium">
                        {currentFolder?.name ?? "—"} · {total}{" "}
                        {t("page.webmail.messages_count")}
                      </span>
                    </div>

                    <MailboxSearchBox
                      value={searchQ}
                      disabled={busy || !folderEnc}
                      onSearch={runSearch}
                    />

                    <div className="flex flex-wrap items-center gap-1.5 text-xs">
                      {/* Preserved accessible select for sort_by to guarantee test compatibility */}
                      <select
                        aria-label={t("page.webmail.sort_by")}
                        className="h-8 rounded-md border border-input bg-background px-2 text-xs"
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
                        <option value="date">
                          {t("page.webmail.sort_date")}
                        </option>
                        <option value="subject">
                          {t("page.webmail.sort_subject")}
                        </option>
                        <option value="from">
                          {t("page.webmail.sort_from")}
                        </option>
                        <option value="size">
                          {t("page.webmail.sort_size")}
                        </option>
                      </select>

                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        className="h-8 px-2"
                        aria-label={
                          sortAsc
                            ? t("page.webmail.sort_ascending")
                            : t("page.webmail.sort_descending")
                        }
                        disabled={busy || !folderEnc}
                        onClick={() =>
                          applyListOptions(sortBy, !sortAsc, threadGroup)
                        }
                      >
                        <ArrowDownWideNarrow
                          className={
                            sortAsc ? "h-3.5 w-3.5 rotate-180" : "h-3.5 w-3.5"
                          }
                        />
                      </Button>

                      <label className="inline-flex items-center gap-1.5 cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">
                        <input
                          type="checkbox"
                          className="h-4 w-4 rounded border-input accent-primary"
                          checked={threadGroup}
                          disabled={busy || !folderEnc}
                          onChange={(e) =>
                            applyListOptions(sortBy, sortAsc, e.target.checked)
                          }
                        />
                        <Layers className="h-3.5 w-3.5" />
                        <span className="hidden sm:inline">
                          {t("page.webmail.thread_group")}
                        </span>
                      </label>

                      <label className="inline-flex items-center gap-1.5 cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">
                        <input
                          type="checkbox"
                          className="h-4 w-4 rounded border-input accent-primary"
                          checked={searchAllFolders}
                          disabled={busy || !folderEnc}
                          onChange={(e) => {
                            setSearchAllFolders(e.target.checked);
                            setCrossResults(null);
                          }}
                        />
                        <span className="hidden sm:inline">
                          {t("page.webmail.search_all_folders")}
                        </span>
                      </label>

                      {threadFilter != null && (
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-8 px-2 text-xs"
                          onClick={() => setThreadFilter(null)}
                        >
                          {t("page.webmail.thread_show_all")}
                        </Button>
                      )}
                    </div>

                    {/* Bulk Selection Header */}
                    <div className="flex items-center justify-between pt-1">
                      <div className="flex items-center gap-2">
                        <label className="inline-flex items-center gap-1.5 text-xs cursor-pointer select-none">
                          <input
                            type="checkbox"
                            className="h-4 w-4 rounded border-input accent-primary"
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
                          <span className="text-xs text-primary font-medium">
                            {t("page.webmail.selected_count", {
                              count: checkedUids.length,
                            })}
                          </span>
                        )}
                      </div>

                      {checkedUids.length > 0 && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-7 text-xs px-2 text-muted-foreground hover:text-foreground"
                          onClick={clearChecked}
                        >
                          {t("page.webmail.clear_selection")}
                        </Button>
                      )}
                    </div>

                    {/* Bulk Action Toolbar */}
                    {checkedUids.length > 0 && (
                      <div
                        className="flex flex-wrap items-center gap-1.5 pt-1.5 border-t border-border/50"
                        role="toolbar"
                        aria-label={t("page.webmail.bulk_actions")}
                      >
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs px-2"
                          onClick={() => void bulkFlag(["\\Seen"], [])}
                        >
                          <MailOpen className="h-3.5 w-3.5 mr-1" />
                          {t("page.webmail.mark_read")}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs px-2"
                          onClick={() => void bulkFlag([], ["\\Seen"])}
                        >
                          <Mail className="h-3.5 w-3.5 mr-1" />
                          {t("page.webmail.mark_unread")}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs px-2 text-destructive hover:bg-destructive/10"
                          disabled={
                            mutationBusy || mutationUncertain || !folderValidity
                          }
                          onClick={() => openDeleteConfirm(checkedUids)}
                        >
                          <Trash2 className="h-3.5 w-3.5 mr-1" />
                          {t("page.webmail.delete")}
                        </Button>

                        <div className="flex items-center gap-1">
                          <select
                            aria-label={t("page.webmail.move_to")}
                            className="h-7 max-w-[8rem] rounded border border-input bg-background px-1.5 text-xs"
                            value={bulkMoveTo}
                            onChange={(e) => setBulkMoveTo(e.target.value)}
                          >
                            <option value="">
                              {t("page.webmail.move_to")}
                            </option>
                            {moveOptions.map((f) => (
                              <option key={f.name_enc} value={f.name_enc}>
                                {f.name}
                              </option>
                            ))}
                          </select>
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-7 text-xs px-2"
                            disabled={
                              !bulkMoveTo ||
                              !folderValidity ||
                              mutationBusy ||
                              mutationUncertain
                            }
                            onClick={() => void doMove(checkedUids, bulkMoveTo)}
                          >
                            {t("page.webmail.move")}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-7 text-xs px-2"
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
                        </div>
                      </div>
                    )}
                  </div>

                  {/* Message List */}
                  <ul
                    className="flex-1 overflow-y-auto divide-y divide-border/40 min-h-0 select-none"
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
                          <li className="p-8 text-center text-sm text-muted-foreground flex flex-col items-center justify-center space-y-2">
                            <Mail className="h-8 w-8 stroke-[1.5] opacity-40" />
                            <p>
                              {crossResults
                                ? t("page.webmail.search_no_results")
                                : t("page.webmail.empty_folder")}
                            </p>
                          </li>
                        );
                      }
                      const threadCountOf = (m: MessageSummary): number =>
                        crossResults
                          ? 1
                          : (decoratedMessages.find(
                              (d) => d.message.uid === m.uid,
                            )?.threadCount ?? 1);
                      const threadKeyOf = (m: MessageSummary): string =>
                        decoratedMessages.find((d) => d.message.uid === m.uid)
                          ?.threadKey ?? String(m.uid);

                      return showing.map((m) => {
                        const checked = checkedUids.includes(m.uid);
                        const isSelected = selectedUid === m.uid;
                        const threadCount = threadCountOf(m);

                        return (
                          <MailboxMessageRow
                            key={m.uid}
                            message={m}
                            isSelected={isSelected}
                            isChecked={checked}
                            threadCount={threadCount}
                            dateText={dateFmt(m.date)}
                            isSelectionMode={checkedUids.length > 0}
                            crossResults={Boolean(crossResults)}
                            onSelect={() =>
                              crossResults
                                ? openCrossMessage(m)
                                : openMessage(m.uid)
                            }
                            onToggleCheck={(e) => {
                              e.stopPropagation();
                              setCheckedUids((prev) => toggleUid(prev, m.uid));
                            }}
                            onToggleFlag={(e) => void toggleRowFlag(m, e)}
                            onToggleRead={(e) => void toggleRowRead(m, e)}
                            onDelete={(e) => {
                              e.stopPropagation();
                              openDeleteConfirm(
                                [m.uid],
                                selectedEncRef.current ?? folderEnc,
                                folderValidity,
                              );
                            }}
                            onThreadFilter={
                              threadGroup && !crossResults && threadCount > 1
                                ? () =>
                                    setThreadFilter(
                                      threadKeyOf(m) === threadFilter
                                        ? null
                                        : threadKeyOf(m),
                                    )
                                : undefined
                            }
                          />
                        );
                      });
                    })()}
                  </ul>

                  {/* Pagination footer */}
                  {!crossResults &&
                    (cursorHistory.length > 0 || nextCursor) && (
                      <div className="flex items-center justify-between p-2 border-t bg-card/40 shrink-0">
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs"
                          disabled={
                            cursorHistory.length === 0 || busy || !folderEnc
                          }
                          onClick={() => loadPage("prev")}
                        >
                          {t("page.webmail.prev_page")}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs"
                          disabled={!nextCursor || busy || !folderEnc}
                          onClick={() => loadPage("next")}
                        >
                          {t("page.webmail.next_page")}
                        </Button>
                      </div>
                    )}
                </section>

                {/* Resizable handle between list and detail */}
                <SplitterHandle
                  orientation={
                    resizable.layoutMode === "horizontal"
                      ? "horizontal"
                      : "vertical"
                  }
                  onMouseDown={(e) =>
                    resizable.startDragging(
                      resizable.layoutMode === "horizontal"
                        ? "list-height"
                        : "list-width",
                      e.clientX,
                      e.clientY,
                    )
                  }
                />

                {/* Detail pane */}
                <section
                  className={`flex-1 flex flex-col min-h-0 min-w-0 bg-card overflow-hidden ${
                    mobilePane === "list" ? "hidden md:flex" : "flex"
                  }`}
                >
                  {!detail ? (
                    <div className="flex-1 flex flex-col items-center justify-center p-8 text-center text-muted-foreground space-y-3">
                      <Mail className="h-12 w-12 stroke-[1.2] opacity-30" />
                      <p className="text-sm">
                        {busy
                          ? t("state.loading")
                          : t("page.webmail.select_message_read")}
                      </p>
                    </div>
                  ) : (
                    <>
                      {/* Top Action Toolbar */}
                      <div className="border-b px-4 py-2 bg-card/60 flex items-center justify-between gap-2 shrink-0 select-none shadow-2xs">
                        <div className="flex items-center gap-1.5 flex-wrap min-w-0">
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

                          {isDraftsFolder(currentFolder) ? (
                            <Button
                              size="sm"
                              variant="outline"
                              className="h-8 text-xs font-medium"
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
                                className="h-8 text-xs font-medium"
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
                                className="h-8 text-xs font-medium"
                                onClick={() =>
                                  openCompose(
                                    draftFromMessage("reply-all", detail),
                                  )
                                }
                              >
                                <ReplyAll className="h-3.5 w-3.5 mr-1" />
                                {t("page.webmail.compose_reply_all")}
                              </Button>
                              <Button
                                size="sm"
                                variant="outline"
                                className="h-8 text-xs font-medium"
                                disabled={forwardBusy}
                                onClick={() => void openForward()}
                              >
                                <Forward className="h-3.5 w-3.5 mr-1" />
                                {t("page.webmail.compose_forward")}
                              </Button>
                              <Button
                                size="sm"
                                variant="outline"
                                className="h-8 text-xs"
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
                            className="h-8 text-xs"
                            onClick={() => void toggleFlagged()}
                          >
                            <Flag className="h-3.5 w-3.5 mr-1 text-amber-500 fill-amber-500" />
                            {hasFlag(detail.flags, "\\Flagged")
                              ? t("page.webmail.unflag")
                              : t("page.webmail.flag")}
                          </Button>

                          <Button
                            size="sm"
                            variant="outline"
                            className="h-8 text-xs text-destructive hover:bg-destructive/10"
                            disabled={
                              mutationBusy ||
                              mutationUncertain ||
                              !detail.uid_validity
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

                          {/* Move / Copy dropdown */}
                          <div className="flex items-center gap-1 min-w-0">
                            <select
                              aria-label={t("page.webmail.move_to")}
                              className="h-8 max-w-[8rem] rounded-md border border-input bg-background px-2 text-xs"
                              value={moveTo}
                              onChange={(e) => setMoveTo(e.target.value)}
                            >
                              <option value="">
                                {t("page.webmail.move_to")}
                              </option>
                              {moveOptions.map((f) => (
                                <option key={f.name_enc} value={f.name_enc}>
                                  {f.name}
                                </option>
                              ))}
                            </select>
                            <Button
                              size="sm"
                              variant="outline"
                              className="h-8 text-xs px-2"
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
                              className="h-8 text-xs px-2"
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
                                className="h-8 text-xs px-2"
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

                        {/* Fast AI summary action on top right */}
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-8 text-xs text-amber-500 hover:text-amber-600 hover:bg-amber-500/10 gap-1 font-medium"
                          onClick={() => setAiDrawerOpen(true)}
                        >
                          <SparklesIcon className="h-3.5 w-3.5" />
                          <span className="hidden lg:inline">AI 摘要</span>
                        </Button>
                      </div>

                      {/* Detail Body Scroll Container */}
                      <div className="flex-1 overflow-y-auto p-4 sm:p-6 space-y-5 min-h-0">
                        {/* Header card */}
                        <MailboxDetailHeader
                          message={detail}
                          dateText={dateFmt(detail.date)}
                        />

                        <MailboxMessageTools
                          key={`tools:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                          folder={(selectedEncRef.current ?? folderEnc)!}
                          message={detail}
                        />

                        {/* Preserved accessible MailboxAIPanel */}
                        <div className="sr-only">
                          {detail.uid_validity &&
                            (selectedEncRef.current ?? folderEnc) && (
                              <MailboxAIPanel
                                key={`ai:${selectedEncRef.current ?? folderEnc}:${detail.uid}`}
                                mode="read"
                                source={{
                                  folder_enc: (selectedEncRef.current ??
                                    folderEnc)!,
                                  uid: detail.uid,
                                  uid_validity: detail.uid_validity,
                                }}
                                subject={detail.subject}
                                body={detail.text ?? ""}
                                onApply={async (proposal) => {
                                  const reply = draftFromMessage(
                                    "reply",
                                    detail,
                                  );
                                  const [settings, identityList] =
                                    await Promise.all([
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
                                    to: (reply.to ?? "")
                                      .split(/[,;\s]+/)
                                      .filter(Boolean),
                                    cc: (reply.cc ?? "")
                                      .split(/[,;\s]+/)
                                      .filter(Boolean),
                                    subject: reply.subject ?? proposal.subject,
                                    in_reply_to: detail.message_id,
                                    reply_source: {
                                      folder_enc: (selectedEncRef.current ??
                                        folderEnc)!,
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
                        </div>

                        {detail.uid_validity && folderEnc && (
                          <MailboxInvitationPanel
                            key={`invitation:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                            folderEnc={selectedEncRef.current ?? folderEnc}
                            uid={detail.uid}
                            uidValidity={detail.uid_validity}
                          />
                        )}

                        {/* HTML or Plaintext message body */}
                        <div className="prose prose-sm max-w-none dark:prose-invert">
                          {detail.html ? (
                            <MessageHTMLWithAttachments
                              key={`html:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                              message={detail}
                              source={{
                                folderEnc: (selectedEncRef.current ??
                                  folderEnc)!,
                                uid: detail.uid,
                                uidValidity: detail.uid_validity ?? 0,
                              }}
                              loadImages={loadImages}
                              onLoadImages={() => setLoadImages(true)}
                            />
                          ) : (
                            <pre className="whitespace-pre-wrap break-words text-sm font-sans p-4 rounded-xl bg-muted/20 border border-border/50">
                              {detail.text || t("page.webmail.no_body")}
                            </pre>
                          )}
                        </div>

                        {/* Attachments Section */}
                        {detail.attachments?.length > 0 && (
                          <div className="border-t border-border/60 pt-4 space-y-2">
                            <p className="text-xs font-semibold text-muted-foreground uppercase tracking-wider flex items-center gap-1.5">
                              <Paperclip className="h-3.5 w-3.5" />
                              {t("page.webmail.attachments")} (
                              {detail.attachments.length})
                            </p>
                            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                              {detail.attachments.map((a) => (
                                <div
                                  key={a.part_id}
                                  className="flex items-center justify-between gap-2 p-2.5 rounded-lg border border-border/60 bg-muted/30 hover:bg-muted/50 transition-colors"
                                >
                                  <div className="min-w-0 flex items-center gap-2">
                                    <Paperclip className="h-4 w-4 text-primary shrink-0" />
                                    <div className="min-w-0">
                                      <p className="text-xs font-medium text-foreground truncate max-w-[180px]">
                                        {attachmentDisplayName(
                                          a.filename,
                                          a.part_id,
                                        )}
                                      </p>
                                      <p className="text-[10px] text-muted-foreground">
                                        {a.content_type} · {a.size} B
                                      </p>
                                    </div>
                                  </div>

                                  <div className="flex items-center gap-1 shrink-0">
                                    {folderEnc && selectedUid != null && (
                                      <AttachmentPreview
                                        key={`${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}:${a.part_id}`}
                                        source={{
                                          folderEnc:
                                            selectedEncRef.current ?? folderEnc,
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
                                        className="h-7 text-xs px-2"
                                        onClick={() => {
                                          void downloadAttachment(
                                            selectedEncRef.current ?? folderEnc,
                                            selectedUid,
                                            a.part_id,
                                            a.filename ||
                                              `attachment-${a.part_id}`,
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
                                  </div>
                                </div>
                              ))}
                            </div>
                          </div>
                        )}

                        {/* Inline Quick Reply at bottom of message detail */}
                        {!isDraftsFolder(currentFolder) && (
                          <div className="pt-2">
                            <MailboxInlineReply
                              message={detail}
                              folderEnc={
                                selectedEncRef.current ?? folderEnc ?? ""
                              }
                              settings={mailboxSettings}
                              identities={identities}
                              onOpenFullCompose={(mode) =>
                                openCompose(draftFromMessage(mode, detail))
                              }
                              onSent={() => {
                                void refresh();
                              }}
                            />
                          </div>
                        )}
                      </div>
                    </>
                  )}
                </section>
              </div>
            </div>
          </div>
        )}
      </div>

      {/* 3. AI Copilot Drawer */}
      <MailboxAIDrawer
        open={aiDrawerOpen}
        onClose={() => setAiDrawerOpen(false)}
        folders={folders}
        selectedFolderEnc={selectedEncRef.current ?? folderEnc ?? undefined}
        detail={detail}
        onCitation={(source) => {
          selectFolder(source.folder_enc);
          selectedEncRef.current = source.folder_enc;
          citationValidityRef.current = source.uid_validity;
          setSelectedUid(source.uid);
          setCitationNavigation((value) => value + 1);
          setMobilePane("detail");
        }}
        onApplyAIProposal={async (proposal) => {
          if (!detail) return;
          const reply = draftFromMessage("reply", detail);
          const from = mailboxSettings
            ? chooseMailboxIdentity(mailboxSettings, identities, reply)
            : identities[0]?.address || "";
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

      {/* Modals & Dialogs */}
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
