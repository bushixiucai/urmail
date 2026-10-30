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
  SlidersHorizontal,
  X,
  Check,
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
import { MailboxMessageRow, RowDensity, RowColumnsConfig } from "./mailbox-message-row";
import { MailboxDetailHeader } from "./mailbox-detail-header";
import { getFolderInfo, sortMailboxFolders } from "./mailbox-folder-utils";
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
  MessageRef,
  MailboxSettingsView,
  MailboxIdentity,
  moveMessages,
  MAX_SEND_ATTACHMENTS,
  OutgoingAttachment,
} from "./mailbox-api";
import { decorateThreads } from "./mailbox-threading";
import {
  ComposeDraft,
  ComposeControllerHandle,
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

function messageKey(ref: MessageRef): string {
  return JSON.stringify([ref.folder_enc, ref.uid_validity, ref.uid]);
}
function sameMessage(a: MessageRef | null, b: MessageRef | null): boolean {
  return a != null && b != null && messageKey(a) === messageKey(b);
}
function toggleMessage(refs: MessageRef[], ref: MessageRef): MessageRef[] {
  return refs.some((item) => sameMessage(item, ref))
    ? refs.filter((item) => !sameMessage(item, ref))
    : [...refs, ref];
}
function groupMessageRefs(refs: MessageRef[]): MessageRef[][] {
  const groups = new Map<string, MessageRef[]>();
  for (const ref of refs) {
    const key = JSON.stringify([ref.folder_enc, ref.uid_validity]);
    groups.set(key, [...(groups.get(key) ?? []), ref]);
  }
  return [...groups.values()];
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

  const [folderValidity, setFolderValidity] = useState<number | undefined>();
  const [folderManagerOpen, setFolderManagerOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [exportBusy, setExportBusy] = useState(false);
  const [deleteRefs, setDeleteRefs] = useState<MessageRef[]>([]);
  const [mutationBusy, setMutationBusy] = useState(false);
  const [mutationNotice, setMutationNotice] = useState("");
  const [mutationUncertain, setMutationUncertain] = useState(false);
  const [offset, setOffset] = useState(0);
  const citationValidityRef = useRef<number | undefined>(undefined);
  const [selectedRef, setSelectedRef] = useState<MessageRef | null>(null);
  const selectedUid = selectedRef?.uid ?? null;
  const [checkedRefs, setCheckedRefs] = useState<MessageRef[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);
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
  const composeController = useRef<ComposeControllerHandle>(null);
  const inlineController = useRef<ComposeControllerHandle>(null);
  const navigationGeneration = useRef(0);
  const preserveEdits = async () => {
    if (composeController.current && !(await composeController.current.flush()))
      return false;
    return (
      !inlineController.current || (await inlineController.current.flush())
    );
  };
  const guardedNavigation = async (action: () => void) => {
    const generation = ++navigationGeneration.current;
    if (!(await preserveEdits()) || generation !== navigationGeneration.current)
      return;
    action();
  };
  const switchApp = (app: MailboxAppId) =>
    void guardedNavigation(() => {
      setActiveApp(app);
      setAiDrawerOpen(false);
      if (app === "mail") setMobilePane("list");
    });
  const [composeOpen, setComposeOpen] = useState(false);
  const [composeDraft, setComposeDraft] = useState<ComposeDraft>({
    mode: "new",
  });
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [scheduledOpen, setScheduledOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
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

  // WP02 / I04: Folder-scoped thread toggle
  const [folderThreading, setFolderThreading] = useState<Record<string, boolean>>(
    () => {
      try {
        const stored = localStorage.getItem("urmail.webmail.folderThreading");
        return stored ? JSON.parse(stored) : {};
      } catch {
        return {};
      }
    },
  );

  // WP02 / I01: Search folder statuses & continuation
  interface SearchFolderStatus {
    folder: string;
    folderEnc: string;
    status: "complete" | "partial" | "failed";
    count: number;
    error?: string;
  }
  const [searchFolderStates, setSearchFolderStates] = useState<
    SearchFolderStatus[]
  >([]);
  const [crossLimit, setCrossLimit] = useState(50);

  // WP03 / I02: Workspace density ('comfortable' | 'compact' | 'list')
  const [density, setDensity] = useState<RowDensity>(() => {
    try {
      return (
        (localStorage.getItem("urmail.webmail.density") as RowDensity) ||
        "comfortable"
      );
    } catch {
      return "comfortable";
    }
  });
  const updateDensity = (d: RowDensity) => {
    setDensity(d);
    try {
      localStorage.setItem("urmail.webmail.density", d);
    } catch {}
  };

  // WP03 / I02: Optional columns visibility
  const [visibleColumns, setVisibleColumns] = useState<RowColumnsConfig>(() => {
    try {
      const stored = localStorage.getItem("urmail.webmail.columns");
      return stored
        ? JSON.parse(stored)
        : { fromTo: true, subject: true, date: true, size: false, flags: true };
    } catch {
      return { fromTo: true, subject: true, date: true, size: false, flags: true };
    }
  });
  const toggleColumn = (col: keyof RowColumnsConfig) => {
    setVisibleColumns((prev) => {
      const next = { ...prev, [col]: !prev[col] };
      try {
        localStorage.setItem("urmail.webmail.columns", JSON.stringify(next));
      } catch {}
      return next;
    });
  };

  // WP03 / I05: Read timer (5s delay / immediate / manual)
  const [readTimerMode, setReadTimerMode] = useState<"5s" | "immediate" | "manual">(
    () => {
      try {
        const raw = localStorage.getItem("urmail.webmail.readTimerMode")?.replace(/"/g, "");
        if (raw === "immediate" || raw === "manual" || raw === "5s") return raw;
        return "5s";
      } catch {
        return "5s";
      }
    },
  );
  const updateReadTimerMode = (m: "5s" | "immediate" | "manual") => {
    setReadTimerMode(m);
    try {
      localStorage.setItem("urmail.webmail.readTimerMode", m);
    } catch {}
  };
  const [readTimerRemaining, setReadTimerRemaining] = useState<number | null>(
    null,
  );
  const readTimerTimeout = useRef<NodeJS.Timeout | null>(null);
  const readTimerInterval = useRef<NodeJS.Timeout | null>(null);
  const cancelReadTimer = useCallback(() => {
    if (readTimerTimeout.current) {
      clearTimeout(readTimerTimeout.current);
      readTimerTimeout.current = null;
    }
    if (readTimerInterval.current) {
      clearInterval(readTimerInterval.current);
      readTimerInterval.current = null;
    }
    setReadTimerRemaining(null);
  }, []);

  // WP03 / I06: One-click Trash undo notification & Empty folder dialog
  const [undoNotice, setUndoNotice] = useState<{
    refs: MessageRef[];
    originalFolderEnc: string;
    originalFolderName: string;
  } | null>(null);
  const undoTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const [emptyFolderOpen, setEmptyFolderOpen] = useState(false);

  // WP03 / I07: Next message navigation after delete ('next' | 'list')
  const [afterDeletePref, setAfterDeletePref] = useState<"next" | "list">(
    () => {
      try {
        return (
          (localStorage.getItem(
            "urmail.webmail.afterDelete",
          ) as "next" | "list") || "next"
        );
      } catch {
        return "next";
      }
    },
  );
  const updateAfterDeletePref = (pref: "next" | "list") => {
    setAfterDeletePref(pref);
    try {
      localStorage.setItem("urmail.webmail.afterDelete", pref);
    } catch {}
  };

  // Folder unread count map for badges
  const [folderUnreadCounts, setFolderUnreadCounts] = useState<
    Record<string, number>
  >({});
  const [cursor, setCursor] = useState(0);
  const [nextCursor, setNextCursor] = useState(0);
  const [cursorHistory, setCursorHistory] = useState<number[]>([]);
  const currentFolderRef = useRef(folderEnc);
  currentFolderRef.current = folderEnc;
  /** Folder the currently open UID lives in (differs for cross-folder hits). */
  const selectedEncRef = useRef<string | null>(null);
  selectedEncRef.current = selectedRef?.folder_enc ?? null;
  const selectedRefLive = useRef(selectedRef);
  selectedRefLive.current = selectedRef;
  const mutationLock = useRef(false);
  const detailRequest = useRef(0);
  const crossRequest = useRef<{
    generation: number;
    controller?: AbortController;
  }>({ generation: 0 });
  useEffect(() => () => crossRequest.current.controller?.abort(), []);
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

  // WP02 / I04: Sync threadGroup from folder-scoped preference
  useEffect(() => {
    if (folderEnc) {
      setThreadGroup(Boolean(folderThreading[folderEnc]));
    }
  }, [folderEnc, folderThreading]);

  // WP03 / I05: Cancel read timer immediately when tab/window is hidden or blurred
  useEffect(() => {
    const handleHideOrBlur = () => {
      if (document.hidden) {
        cancelReadTimer();
      }
    };
    document.addEventListener("visibilitychange", handleHideOrBlur);
    window.addEventListener("blur", handleHideOrBlur);
    return () => {
      document.removeEventListener("visibilitychange", handleHideOrBlur);
      window.removeEventListener("blur", handleHideOrBlur);
    };
  }, [cancelReadTimer]);

  const address = getMailboxAddress() ?? "";
  const currentFolder = useMemo(
    () => folders.find((f) => f.name_enc === folderEnc) ?? null,
    [folders, folderEnc],
  );

  const rowRef = useCallback(
    (message: MessageSummary): MessageRef => ({
      folder_enc: message.folder_enc ?? folderEnc ?? "",
      uid_validity:
        message.uid_validity ??
        (message.folder_enc ? 0 : (folderValidity ?? 0)),
      uid: message.uid,
    }),
    [folderEnc, folderValidity],
  );
  const decoratedMessages = useMemo(
    () => decorateThreads(messages),
    [messages],
  );
  const sortedCrossResults = useMemo(
    () =>
      crossResults == null
        ? null
        : [...crossResults].sort((a, b) => {
            const compare =
              sortBy === "subject"
                ? a.subject.localeCompare(b.subject)
                : sortBy === "from"
                  ? (a.from[0]?.email ?? "").localeCompare(
                      b.from[0]?.email ?? "",
                    )
                  : sortBy === "size"
                    ? a.size - b.size
                    : Date.parse(a.date) - Date.parse(b.date);
            return (sortAsc ? 1 : -1) * (Number.isNaN(compare) ? 0 : compare);
          }),
    [crossResults, sortBy, sortAsc],
  );
  const visibleMessages = useMemo(
    () =>
      sortedCrossResults ??
      (threadFilter == null
        ? messages
        : decoratedMessages
            .filter((item) => item.threadKey === threadFilter)
            .map((item) => item.message)),
    [sortedCrossResults, threadFilter, messages, decoratedMessages],
  );
  const pageRefs = visibleMessages.map(rowRef);
  const allPageSelected =
    pageRefs.length > 0 &&
    pageRefs.every((ref) => checkedRefs.some((item) => sameMessage(item, ref)));
  const somePageSelected =
    pageRefs.some((ref) =>
      checkedRefs.some((item) => sameMessage(item, ref)),
    ) && !allPageSelected;
  const detailFolder =
    folders.find((folder) => folder.name_enc === selectedRef?.folder_enc) ??
    currentFolder;
  const deleteSource = deleteRefs.length
    ? {
        trash: deleteRefs.every((ref) =>
          isTrashFolder(
            folders.find((folder) => folder.name_enc === ref.folder_enc),
          ),
        ),
      }
    : null;

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

  const clearChecked = useCallback(() => setCheckedRefs([]), []);

  const loadFolders = useCallback(
    async (signal?: AbortSignal) => {
      const res = await listFolders(signal);
      if (signal?.aborted) return;
      const sorted = sortMailboxFolders(res.folders ?? [], t);
      setFolders(sorted);
      setFolderEnc((prev) => {
        if (prev && sorted.some((f) => f.name_enc === prev)) return prev;
        const inbox =
          sorted.find((f) => f.name.toUpperCase() === "INBOX") ?? sorted[0];
        return inbox?.name_enc ?? null;
      });
    },
    [t],
  );

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
      crossRequest.current.generation += 1;
      crossRequest.current.controller?.abort();
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
        setMessages(
          (res.messages ?? []).map((message) => ({
            ...message,
            folder_enc: enc,
            uid_validity: res.uid_validity,
          })),
        );
        setFolderValidity(res.uid_validity);
        setTotal(res.total);
        setOffset(res.offset);
        setNextCursor(res.next_cursor ?? 0);
        const unreadCount = (res.messages ?? []).filter(
          (m) => !hasFlag(m.flags, "\\Seen"),
        ).length;
        setFolderUnreadCounts((prev) => ({
          ...prev,
          [enc]: unreadCount,
        }));
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

  const loadCrossMessages = useCallback(
    async (q: string, limit = 50) => {
      listRequest.current.generation += 1;
      listRequest.current.controller?.abort();
      crossRequest.current.controller?.abort();
      const controller = new AbortController();
      const generation = ++crossRequest.current.generation;
      crossRequest.current.controller = controller;
      setBusy(true);
      setError(null);
      try {
        const result = await crossFolderSearch(q, { limit }, controller.signal);
        if (
          controller.signal.aborted ||
          generation !== crossRequest.current.generation
        )
          return;
        const msgList = result.messages ?? [];
        setCrossResults(msgList);
        setTotal(result.total ?? 0);
        setCrossLimit(limit);

        // WP02 / I01: Track per-folder search statuses
        const countsByFolder = new Map<string, number>();
        for (const msg of msgList) {
          const fName = msg.folder || "Unknown";
          countsByFolder.set(fName, (countsByFolder.get(fName) ?? 0) + 1);
        }
        const activeFolders = folders.filter(
          (f) =>
            !(f.flags ?? []).some((flag) => flag.toLowerCase() === "\\noselect"),
        );
        const folderStates: SearchFolderStatus[] = (activeFolders.length ? activeFolders : folders).map((f) => {
          const count = countsByFolder.get(f.name) ?? 0;
          return {
            folder: f.name,
            folderEnc: f.name_enc,
            count,
            status: count >= limit ? "partial" : "complete",
          };
        });
        setSearchFolderStates(folderStates);
      } catch (err) {
        if (
          controller.signal.aborted ||
          generation !== crossRequest.current.generation
        )
          return;
        const msg = errorMessage(err, t, "page.webmail.load_failed");
        const activeFolders = folders.filter(
          (f) =>
            !(f.flags ?? []).some((flag) => flag.toLowerCase() === "\\noselect"),
        );
        setSearchFolderStates(
          (activeFolders.length ? activeFolders : folders).map((f) => ({
            folder: f.name,
            folderEnc: f.name_enc,
            count: 0,
            status: "failed",
            error: msg,
          })),
        );
        if (!handleAuthError(err))
          setError(msg);
      } finally {
        if (
          !controller.signal.aborted &&
          generation === crossRequest.current.generation
        )
          setBusy(false);
      }
    },
    [handleAuthError, t, folders],
  );

  const loadMoreCrossResults = () => {
    const nextLimit = crossLimit + 50;
    void loadCrossMessages(searchQ, nextLimit);
  };

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
      if (searchAllFolders && searchQ.trim()) await loadCrossMessages(searchQ);
      else if (enc)
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
    searchAllFolders,
    loadCrossMessages,
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

  const listOptionsRef = useRef(listOpts);
  listOptionsRef.current = listOpts;
  useEffect(() => {
    if (!folderEnc || !hasMailboxSession()) return;
    const controller = new AbortController();
    setSelectedRef(null);
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
    loadMessages(folderEnc, 0, controller.signal, "", listOptionsRef.current())
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
    if (!selectedRef) {
      setDetail(null);
      setDetailLoading(false);
      setDetailError(null);
      return;
    }
    const source = selectedRef;
    const generation = ++detailRequest.current;
    const controller = new AbortController();
    setDetail(null);
    setDetailError(null);
    setDetailLoading(true);
    getMessage(source.folder_enc, source.uid, {
      loadImages,
      signal: controller.signal,
      uidValidity: source.uid_validity || undefined,
    })
      .then(async (message) => {
        if (
          controller.signal.aborted ||
          generation !== detailRequest.current ||
          !sameMessage(source, selectedRefLive.current)
        )
          return;
        if (source.uid_validity && message.uid_validity !== source.uid_validity)
          throw new Error("MESSAGE_SOURCE_CHANGED");
        const loaded = { ...message, folder_enc: source.folder_enc };
        setDetail(loaded);
        cancelReadTimer();

        // WP03 / I05: Read timer (5s delay / immediate / manual)
        if (!hasFlag(message.flags, "\\Seen") && message.uid_validity) {
          const executeMarkRead = async () => {
            try {
              await flagMessages(source.folder_enc, {
                uids: [source.uid],
                uid_validity: message.uid_validity,
                add: ["\\Seen"],
              });
              if (
                controller.signal.aborted ||
                generation !== detailRequest.current ||
                !sameMessage(source, selectedRefLive.current)
              )
                return;
              const markRead = (rows: MessageSummary[]) =>
                rows.map((row) =>
                  sameMessage(rowRef(row), source)
                    ? {
                        ...row,
                        flags: [
                          ...row.flags.filter(
                            (flag) => flag.toLowerCase() !== "\\seen",
                          ),
                          "\\Seen",
                        ],
                      }
                    : row,
                );
              setMessages(markRead);
              setCrossResults((rows) => (rows == null ? null : markRead(rows)));
              setDetail((prev) =>
                prev && sameMessage(rowRef(prev as any), source)
                  ? {
                      ...prev,
                      flags: [
                        ...prev.flags.filter(
                          (flag) => flag.toLowerCase() !== "\\seen",
                        ),
                        "\\Seen",
                      ],
                    }
                  : prev,
              );
              setFolderUnreadCounts((prev) => ({
                ...prev,
                [source.folder_enc]: Math.max(0, (prev[source.folder_enc] ?? 1) - 1),
              }));
            } catch {
              /* Reading remains usable when an ancillary flag update fails. */
            } finally {
              setReadTimerRemaining(null);
            }
          };

          if (readTimerMode === "immediate") {
            void executeMarkRead();
          } else if (readTimerMode === "5s") {
            setReadTimerRemaining(5);
            let remaining = 5;
            readTimerInterval.current = setInterval(() => {
              remaining -= 1;
              if (remaining <= 0) {
                if (readTimerInterval.current) {
                  clearInterval(readTimerInterval.current);
                  readTimerInterval.current = null;
                }
                setReadTimerRemaining(null);
              } else {
                setReadTimerRemaining(remaining);
              }
            }, 1000);

            readTimerTimeout.current = setTimeout(() => {
              readTimerTimeout.current = null;
              if (readTimerInterval.current) {
                clearInterval(readTimerInterval.current);
                readTimerInterval.current = null;
              }
              void executeMarkRead();
            }, 5000);
          }
          // "manual" mode does nothing automatically
        }
      })
      .catch((err) => {
        if (controller.signal.aborted || generation !== detailRequest.current)
          return;
        if (!handleAuthError(err))
          setDetailError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => {
        if (!controller.signal.aborted && generation === detailRequest.current)
          setDetailLoading(false);
      });
    return () => {
      cancelReadTimer();
      controller.abort();
    };
  }, [selectedRef, loadImages, handleAuthError, t, rowRef, readTimerMode, cancelReadTimer]);

  const handleLoadImages = () => setLoadImages(true);

  // WP03 / I07: Auto-refresh sensing (interval polling / window focus)
  useEffect(() => {
    let timer: NodeJS.Timeout | null = null;
    const intervalMs = 60000;
    const poll = () => {
      if (!document.hidden && hasMailboxSession()) {
        void refresh();
      }
    };
    timer = setInterval(poll, intervalMs);

    const onFocus = () => {
      if (hasMailboxSession()) {
        void refresh();
      }
    };
    window.addEventListener("focus", onFocus);

    return () => {
      if (timer) clearInterval(timer);
      window.removeEventListener("focus", onFocus);
    };
  }, [refresh]);

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

  const openCompose = async (draft: ComposeDraft) => {
    const generation = navigationGeneration.current;
    if (!(await preserveEdits()) || generation !== navigationGeneration.current)
      return;
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
          ).then((outgoing) => ({
            ...outgoing,
            content_id: attachment.content_id,
            inline: attachment.inline ?? false,
          })),
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
    if (!folderEnc || !detail || !isDraftsFolder(detailFolder)) return;
    setBusy(true);
    setError(null);
    try {
      const draftFolder = selectedRef?.folder_enc ?? folderEnc;
      const message = detail;
      const atts: OutgoingAttachment[] = [];
      if (
        message.attachments_capped ||
        (message.attachments?.length ?? 0) > MAX_SEND_ATTACHMENTS
      )
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
          ).then((outgoing) => ({
            ...outgoing,
            content_id: a.content_id,
            inline: a.inline ?? false,
          })),
        );
        assertAttachmentTotal(atts);
      }
      if (
        !sameMessage(selectedRefLive.current, {
          folder_enc: draftFolder,
          uid_validity: message.uid_validity ?? 0,
          uid: message.uid,
        })
      )
        return;
      void openCompose({
        ...composeFromSavedDraft(message, atts),
        folderEnc: draftFolder,
      });
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(errorMessage(err, t, "page.webmail.draft_restore_failed"));
    } finally {
      setBusy(false);
    }
  };

  const openReference = (source: MessageRef) =>
    void guardedNavigation(() => {
      forwardRequest.current?.abort();
      detailRequest.current += 1;
      citationValidityRef.current = source.uid_validity;
      selectedRefLive.current = source;
      setDetail(null);
      setDetailError(null);
      setDetailLoading(true);
      setLoadImages(false);
      setSelectedRef(source);
      setMobilePane("detail");
    });
  const openMessage = (uid: number) => {
    const message = messages.find((row) => row.uid === uid);
    if (message) openReference(rowRef(message));
  };
  const openCrossMessage = (message: MessageSummary) => {
    if (!message.folder_enc || !message.uid_validity) {
      setError(t("page.webmail.source_changed"));
      if (searchQ.trim()) void loadCrossMessages(searchQ);
      return;
    }
    openReference(rowRef(message));
  };

  const backToList = () => {
    setMobilePane("list");
  };

  const selectFolder = (enc: string) =>
    void guardedNavigation(() => {
      forwardRequest.current?.abort();
      setMoveTo("");
      setBulkMoveTo("");
      setSelectedRef(null);
      setDetail(null);
      clearChecked();
      setFolderValidity(undefined);
      setMutationUncertain(false);
      setMutationNotice("");
      currentFolderRef.current = enc;
      listRequest.current.generation += 1;
      listRequest.current.controller?.abort();
      selectedRefLive.current = null;
      crossRequest.current.generation += 1;
      crossRequest.current.controller?.abort();
      setMessages([]);
      setFolderEnc(enc);
      setFoldersOpen(false);
    });

  const toggleSelectAllPage = () => {
    if (allPageSelected)
      setCheckedRefs((previous) =>
        previous.filter(
          (ref) => !pageRefs.some((item) => sameMessage(ref, item)),
        ),
      );
    else
      setCheckedRefs((previous) => [
        ...previous,
        ...pageRefs.filter(
          (ref) => !previous.some((item) => sameMessage(ref, item)),
        ),
      ]);
  };
  const applyLocalFlags = (
    refs: MessageRef[],
    add: string[],
    remove: string[],
  ) => {
    const update = (row: MessageSummary) => {
      if (!refs.some((ref) => sameMessage(ref, rowRef(row)))) return row;
      return {
        ...row,
        flags: [
          ...row.flags.filter(
            (flag) =>
              !remove.some((item) => item.toLowerCase() === flag.toLowerCase()),
          ),
          ...add.filter(
            (flag) =>
              !hasFlag(
                row.flags.filter((item) => !remove.includes(item)),
                flag,
              ),
          ),
        ],
      };
    };
    setMessages((rows) => rows.map(update));
    setCrossResults((rows) => (rows == null ? null : rows.map(update)));
    setDetail((previous) =>
      previous &&
      refs.some((ref) =>
        sameMessage(ref, {
          folder_enc: previous.folder_enc ?? "",
          uid_validity: previous.uid_validity ?? 0,
          uid: previous.uid,
        }),
      )
        ? { ...previous, flags: update(previous).flags }
        : previous,
    );
  };
  const refreshStaleSources = (refs: MessageRef[]) => {
    setError(t("page.webmail.source_changed"));
    setCheckedRefs((previous) =>
      previous.filter((ref) => !refs.some((item) => sameMessage(ref, item))),
    );
    if (selectedRef && refs.some((ref) => sameMessage(ref, selectedRef))) {
      setSelectedRef(null);
      setDetail(null);
    }
    void refresh().finally(() => setError(t("page.webmail.source_changed")));
  };
  const performFlags = async (
    refs: MessageRef[],
    add: string[],
    remove: string[],
  ) => {
    if (mutationLock.current || mutationUncertain || refs.length === 0) return;
    if (refs.some((ref) => !ref.folder_enc || !ref.uid_validity)) {
      refreshStaleSources(refs);
      return;
    }
    mutationLock.current = true;
    setMutationBusy(true);
    setError(null);
    try {
      for (const group of groupMessageRefs(refs)) {
        try {
          await flagMessages(group[0].folder_enc, {
            uids: group.map((ref) => ref.uid),
            uid_validity: group[0].uid_validity,
            add,
            remove,
          });
          applyLocalFlags(group, add, remove);
          setCheckedRefs((previous) =>
            previous.filter(
              (ref) => !group.some((item) => sameMessage(item, ref)),
            ),
          );
        } catch (err) {
          if (handleAuthError(err)) return;
          if (
            err instanceof ApiRequestError &&
            err.status === 409 &&
            ["DRAFT_CHANGED", "AI_SOURCE_CHANGED"].includes(err.code)
          )
            refreshStaleSources(group);
          else setError(errorMessage(err, t, "page.webmail.action_failed"));
        }
      }
    } finally {
      mutationLock.current = false;
      setMutationBusy(false);
    }
  };
  const bulkFlag = (add: string[], remove: string[]) =>
    performFlags(checkedRefs, add, remove);
  const toggleRowFlag = (message: MessageSummary, event: React.MouseEvent) => {
    event.stopPropagation();
    const flagged = hasFlag(message.flags, "\\Flagged");
    return performFlags(
      [rowRef(message)],
      flagged ? [] : ["\\Flagged"],
      flagged ? ["\\Flagged"] : [],
    );
  };
  const toggleRowRead = (message: MessageSummary, event: React.MouseEvent) => {
    event.stopPropagation();
    const seen = hasFlag(message.flags, "\\Seen");
    return performFlags(
      [rowRef(message)],
      seen ? [] : ["\\Seen"],
      seen ? ["\\Seen"] : [],
    );
  };
  const toggleFlagged = () => {
    if (!detail || !selectedRef) return;
    const flagged = hasFlag(detail.flags, "\\Flagged");
    return performFlags(
      [selectedRef],
      flagged ? [] : ["\\Flagged"],
      flagged ? ["\\Flagged"] : [],
    );
  };
  const removeCompleted = (refs: MessageRef[], removeFromList: boolean) => {
    const matches = (ref: MessageRef) =>
      refs.some((item) => sameMessage(ref, item));
    setCheckedRefs((previous) => previous.filter((ref) => !matches(ref)));
    if (removeFromList) {
      let nextToOpen: MessageSummary | null = null;
      if (selectedRefLive.current && matches(selectedRefLive.current)) {
        if (afterDeletePref === "next") {
          const currentIndex = visibleMessages.findIndex((m) =>
            sameMessage(rowRef(m), selectedRefLive.current),
          );
          if (currentIndex !== -1) {
            for (let i = currentIndex + 1; i < visibleMessages.length; i++) {
              if (!matches(rowRef(visibleMessages[i]))) {
                nextToOpen = visibleMessages[i];
                break;
              }
            }
          }
        }
      }

      setMessages((rows) => rows.filter((row) => !matches(rowRef(row))));
      setCrossResults((rows) =>
        rows == null ? null : rows.filter((row) => !matches(rowRef(row))),
      );
      if (selectedRefLive.current && matches(selectedRefLive.current)) {
        if (nextToOpen) {
          openReference(rowRef(nextToOpen));
        } else {
          setSelectedRef(null);
          setDetail(null);
          setMobilePane("list");
        }
      }
    }
  };
  type MutationAction = "copy" | "restore" | "trash" | "move" | "delete";
  const mutateReferences = async (
    action: MutationAction,
    refs: MessageRef[],
    to = "",
  ) => {
    if (!refs.length || mutationLock.current || mutationUncertain) return;
    if (refs.some((ref) => !ref.folder_enc || !ref.uid_validity)) {
      refreshStaleSources(refs);
      return;
    }
    mutationLock.current = true;
    setMutationBusy(true);
    setError(null);
    const notices: string[] = [];
    let uncertain = false;
    try {
      for (const group of groupMessageRefs(refs)) {
        const source = group[0];
        // Legacy move/delete responses do not expose per-UID outcomes. Submit
        // them individually so completed messages are never replayed with failures.
        const batches =
          action === "move" || action === "delete"
            ? group.map((ref) => [ref])
            : [group];
        for (const batch of batches) {
          try {
            if (action === "delete")
              await deleteMessages(source.folder_enc, {
                uids: batch.map((ref) => ref.uid),
                expunge: true,
                uid_validity: source.uid_validity,
              });
            else if (action === "move")
              await moveMessages(source.folder_enc, {
                uids: batch.map((ref) => ref.uid),
                to,
                uid_validity: source.uid_validity,
              });
            else {
              const result = await mailboxTransfer(source.folder_enc, action, {
                uids: batch.map((ref) => ref.uid),
                ...(to ? { to } : {}),
                uid_validity: source.uid_validity,
              });
              const successful = batch.filter((ref) =>
                result.results.some(
                  (item) => item.uid === ref.uid && item.status === "success",
                ),
              );
              removeCompleted(successful, action !== "copy");
              for (const ref of batch) {
                const outcome = result.results.find(
                  (item) => item.uid === ref.uid,
                );
                const status = outcome?.status ?? "unknown";
                notices.push(
                  `${folders.find((folder) => folder.name_enc === source.folder_enc)?.name ?? source.folder_enc} · ${ref.uid}: ${t(`page.webmail.transfer_${status}`)}`,
                );
                if (status === "unknown") uncertain = true;
              }
              continue;
            }
            removeCompleted(batch, true);
            notices.push(
              `${folders.find((folder) => folder.name_enc === source.folder_enc)?.name ?? source.folder_enc} · ${batch[0].uid}: ${t("page.webmail.transfer_success")}`,
            );
          } catch (err) {
            if (handleAuthError(err)) return;
            if (
              err instanceof ApiRequestError &&
              err.status === 409 &&
              ["DRAFT_CHANGED", "AI_SOURCE_CHANGED"].includes(err.code)
            )
              refreshStaleSources(batch);
            else {
              if (!(err instanceof ApiRequestError) || err.status >= 500)
                uncertain = true;
              setError(errorMessage(err, t, "page.webmail.action_failed"));
            }
          }
        }
      }
      setMutationNotice(notices.join(" · "));
      setMutationUncertain(uncertain);
      if (uncertain) setError(t("page.webmail.transfer_uncertain"));
      await loadFolders();
      if (!crossResults && currentFolderRef.current === folderEnc && folderEnc)
        await loadMessages(folderEnc, offset, undefined, searchQ, listOpts());
    } catch (err) {
      if (!handleAuthError(err))
        setError(errorMessage(err, t, "page.webmail.load_failed"));
    } finally {
      mutationLock.current = false;
      setMutationBusy(false);
    }
  };
  const transfer = (
    action: "copy" | "restore",
    refs: MessageRef[],
    to: string,
  ) => mutateReferences(action, refs, to);
  const openDeleteConfirm = async (refs: MessageRef[]) => {
    const generation = navigationGeneration.current;
    if (!(await preserveEdits()) || generation !== navigationGeneration.current)
      return;
    if (!refs.length || mutationLock.current || mutationUncertain) return;
    if (refs.some((ref) => !ref.uid_validity || !ref.folder_enc)) {
      setError(t("page.webmail.source_changed"));
      return;
    }
    setDeleteRefs(refs);
    setDeleteOpen(true);
  };
  const confirmDelete = async () => {
    try {
      const trash = deleteRefs.filter((ref) =>
        isTrashFolder(
          folders.find((folder) => folder.name_enc === ref.folder_enc),
        ),
      );
      const regular = deleteRefs.filter(
        (ref) => !trash.some((item) => sameMessage(item, ref)),
      );
      if (regular.length) {
        await mutateReferences("trash", regular);
        const origFolderEnc = regular[0]?.folder_enc ?? folderEnc ?? "";
        const origFolder = folders.find((f) => f.name_enc === origFolderEnc);
        setUndoNotice({
          refs: regular,
          originalFolderEnc: origFolderEnc,
          originalFolderName: origFolder?.name ?? "",
        });
        if (undoTimeoutRef.current) clearTimeout(undoTimeoutRef.current);
        undoTimeoutRef.current = setTimeout(() => {
          setUndoNotice(null);
        }, 10000);
      }
      if (trash.length) await mutateReferences("delete", trash);
    } finally {
      setDeleteOpen(false);
      setDeleteRefs([]);
    }
  };

  const moveToTrashWithUndo = async (refs: MessageRef[]) => {
    const generation = navigationGeneration.current;
    if (!(await preserveEdits()) || generation !== navigationGeneration.current)
      return;
    if (!refs.length || mutationLock.current || mutationUncertain) return;
    const trash = refs.filter((ref) =>
      isTrashFolder(
        folders.find((folder) => folder.name_enc === ref.folder_enc),
      ),
    );
    if (trash.length > 0) {
      openDeleteConfirm(refs);
      return;
    }
    await mutateReferences("trash", refs);
    const origFolderEnc = refs[0]?.folder_enc ?? folderEnc ?? "";
    const origFolder = folders.find((f) => f.name_enc === origFolderEnc);
    setUndoNotice({
      refs,
      originalFolderEnc: origFolderEnc,
      originalFolderName: origFolder?.name ?? "",
    });
    if (undoTimeoutRef.current) clearTimeout(undoTimeoutRef.current);
    undoTimeoutRef.current = setTimeout(() => {
      setUndoNotice(null);
    }, 10000);
  };

  const undoTrash = async () => {
    if (!undoNotice) return;
    const { refs, originalFolderEnc } = undoNotice;
    setUndoNotice(null);
    if (undoTimeoutRef.current) {
      clearTimeout(undoTimeoutRef.current);
      undoTimeoutRef.current = null;
    }
    const trashFolder = folders.find((f) => isTrashFolder(f));
    const trashFolderEnc = trashFolder?.name_enc ?? "";
    const updatedRefs = refs.map((r) => ({
      ...r,
      folder_enc: trashFolderEnc || r.folder_enc,
    }));
    await mutateReferences("restore", updatedRefs, originalFolderEnc);
  };

  const confirmEmptyFolder = async () => {
    if (!folderEnc || mutationLock.current) return;
    try {
      const allUids = messages.map((m) => m.uid);
      if (allUids.length > 0) {
        await deleteMessages(folderEnc, {
          uids: allUids,
          expunge: true,
          uid_validity: folderValidity,
        });
        setMessages([]);
        setTotal(0);
        setSelectedRef(null);
        setDetail(null);
      }
    } catch (err) {
      if (!handleAuthError(err))
        setError(errorMessage(err, t, "page.webmail.action_failed"));
    } finally {
      setEmptyFolderOpen(false);
    }
  };

  const doMove = async (refs: MessageRef[], to: string) => {
    if (await preserveEdits()) await mutateReferences("move", refs, to);
  };

  const logout = async () => {
    if (!(await preserveEdits())) return;
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

  const loadPage = async (dir: "next" | "prev") => {
    if (!(await preserveEdits())) return;
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
    setSelectedRef(null);
    setDetail(null);
  };

  const runSearch = async (q: string, allFolders = searchAllFolders) => {
    if (!(await preserveEdits())) return;
    if (!folderEnc) return;
    setSearchQ(q);
    resetListCursor();
    setError(null);
    if (allFolders && q.trim()) {
      setCrossResults([]);
      void loadCrossMessages(q);
      return;
    }
    setCrossResults(null);
    setBusy(true);
    const generation = listRequest.current.generation + 1;
    void loadMessages(folderEnc, 0, undefined, q, listOpts())
      .catch((err) => {
        if (generation !== listRequest.current.generation) return;
        if (!handleAuthError(err))
          setError(errorMessage(err, t, "page.webmail.load_failed"));
      })
      .finally(() => {
        if (generation === listRequest.current.generation) setBusy(false);
      });
  };

  /** Re-list on sort / direction / conversation-toggle changes. */
  const applyListOptions = async (
    sort: MailboxSortKey,
    asc: boolean,
    threads: boolean,
  ) => {
    if (!(await preserveEdits())) return;
    if (!folderEnc) return;
    setSortBy(sort);
    setSortAsc(asc);
    setThreadGroup(threads);
    setFolderThreading((prev) => {
      const next = { ...prev, [folderEnc]: threads };
      try {
        localStorage.setItem("urmail.webmail.folderThreading", JSON.stringify(next));
      } catch {}
      return next;
    });
    resetListCursor();
    if (searchAllFolders && searchQ.trim()) return;
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

  const currentFolderInfo = currentFolder
    ? getFolderInfo(currentFolder, t)
    : null;

  const isSentOrDrafts = Boolean(
    currentFolder &&
      (currentFolder.name.toLowerCase() === "sent" ||
        currentFolder.name.toLowerCase() === "drafts" ||
        (currentFolder.flags ?? []).some(
          (f) => f.toLowerCase() === "\\sent" || f.toLowerCase() === "\\drafts",
        )),
  );

  const folderList = (
    <ul className="space-y-1" aria-label={t("page.webmail.folders")}>
      {folders.map((f) => {
        const info = getFolderInfo(f, t);
        const IconComponent = info.icon;
        const isSelected = f.name_enc === folderEnc;

        return (
          <li key={f.name_enc}>
            <button
              type="button"
              className={`w-full flex items-center justify-between gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors text-left min-h-11 md:min-h-8 cursor-pointer ${
                isSelected
                  ? "bg-primary/10 text-primary font-semibold shadow-2xs"
                  : "text-muted-foreground hover:text-foreground hover:bg-muted/60"
              }`}
              onClick={() => selectFolder(f.name_enc)}
            >
              <div className="flex items-center gap-2.5 min-w-0">
                <IconComponent
                  className={`h-4 w-4 shrink-0 ${
                    isSelected ? "text-primary" : "text-muted-foreground"
                  }`}
                />
                <span className="truncate">{info.displayName}</span>
              </div>
              {(folderUnreadCounts[f.name_enc] ?? 0) > 0 && (
                <span
                  data-testid={`unread-badge-${f.name}`}
                  className="rounded-full bg-primary/20 text-primary text-[11px] font-semibold px-2 py-0.5 shrink-0"
                >
                  {folderUnreadCounts[f.name_enc]}
                </span>
              )}
            </button>
          </li>
        );
      })}
    </ul>
  );

  const moveOptions = folders.filter(
    (f) =>
      crossResults || f.name_enc !== (selectedRef?.folder_enc ?? folderEnc),
  );

  return (
    <div className="h-dvh w-full flex flex-row bg-background overflow-hidden text-foreground pb-[calc(4rem+env(safe-area-inset-bottom))] md:pb-0">
      {/* Resizing overlay to prevent iframe stealing pointer events */}
      {resizable.isDragging && (
        <div className="fixed inset-0 z-50 pointer-events-auto bg-transparent select-none" />
      )}

      {/* 1. App Rail (Mail, Calendar, Contacts, AI, Logout) */}
      <MailboxAppRail
        activeApp={activeApp}
        onSelectApp={switchApp}
        aiOpen={aiDrawerOpen}
        onToggleAi={() => setAiDrawerOpen((prev) => !prev)}
        address={address}
        onLogout={() => void logout()}
        onOpenHelp={() =>
          void guardedNavigation(() => navigate("/webmail/help"))
        }
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
                  onClick={() => switchApp("mail")}
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
                  onClick={() => switchApp("mail")}
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
            <header className="border-b bg-card px-2 sm:px-4 py-2 flex flex-wrap items-center justify-between gap-2 select-none shrink-0 shadow-2xs">
              <div className="flex items-center gap-2 min-w-0 flex-1 basis-28">
                <Sheet open={foldersOpen} onOpenChange={setFoldersOpen}>
                  <SheetTrigger asChild>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      className="xl:hidden shrink-0 min-h-11 min-w-11 px-0"
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
                    className="hidden xl:inline-flex h-8 w-8 p-0 text-muted-foreground hover:text-foreground"
                    title={t(
                      folderCollapsed
                        ? "page.webmail.expand_folders"
                        : "page.webmail.collapse_folders",
                    )}
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

              <div className="flex items-center justify-end gap-1 sm:gap-1.5 shrink-0 max-w-full">
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
                  onClick={() =>
                    void guardedNavigation(() => {
                      setMutationUncertain(false);
                      setMutationNotice("");
                      clearChecked();
                      void refresh();
                    })
                  }
                >
                  <RefreshCw className="h-3.5 w-3.5 mr-1" />
                  <span className="hidden sm:inline">
                    {t("page.webmail.refresh")}
                  </span>
                </Button>

                {/* Layout Mode Toggle (Split Right vs Split Bottom vs List Only) */}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="hidden md:inline-flex h-8 w-8 p-0 text-muted-foreground hover:text-foreground"
                  title={
                    resizable.isSplitRight
                      ? t("page.webmail.layout_horizontal")
                      : t("page.webmail.layout_vertical")
                  }
                  onClick={resizable.toggleLayoutMode}
                >
                  {resizable.isSplitRight ? (
                    <Rows2 className="h-4 w-4" />
                  ) : (
                    <Columns3 className="h-4 w-4" />
                  )}
                </Button>

                {/* View Options (Density, Layout, Columns, Read Timer, Navigation) (WP03 / I02) */}
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      className="hidden md:inline-flex h-8 w-8 p-0 text-muted-foreground hover:text-foreground"
                      title={t("page.webmail.view_options", { defaultValue: "View Options" })}
                      aria-label={t("page.webmail.view_options", { defaultValue: "View Options" })}
                    >
                      <SlidersHorizontal className="h-4 w-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="w-56 text-xs">
                    <DropdownMenuLabel>
                      {t("page.webmail.layout_density", { defaultValue: "Layout & Density" })}
                    </DropdownMenuLabel>
                    <DropdownMenuGroup>
                      <DropdownMenuItem onClick={() => resizable.setLayoutMode("split-right")}>
                        <Columns3 className="h-3.5 w-3.5 mr-2" />
                        <span className="flex-1">{t("page.webmail.layout_vertical", { defaultValue: "Split Right" })}</span>
                        {resizable.isSplitRight && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => resizable.setLayoutMode("split-bottom")}>
                        <Rows2 className="h-3.5 w-3.5 mr-2" />
                        <span className="flex-1">{t("page.webmail.layout_horizontal", { defaultValue: "Split Bottom" })}</span>
                        {resizable.isSplitBottom && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => resizable.setLayoutMode("list-only")}>
                        <Menu className="h-3.5 w-3.5 mr-2" />
                        <span className="flex-1">{t("page.webmail.layout_list_only", { defaultValue: "List Only" })}</span>
                        {resizable.isListOnly && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                    </DropdownMenuGroup>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>
                      {t("page.webmail.density", { defaultValue: "Density" })}
                    </DropdownMenuLabel>
                    <DropdownMenuGroup>
                      <DropdownMenuItem onClick={() => updateDensity("comfortable")}>
                        <span className="flex-1">{t("page.webmail.density_comfortable", { defaultValue: "Comfortable" })}</span>
                        {density === "comfortable" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => updateDensity("compact")}>
                        <span className="flex-1">{t("page.webmail.density_compact", { defaultValue: "Compact" })}</span>
                        {density === "compact" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => updateDensity("list")}>
                        <span className="flex-1">{t("page.webmail.density_list", { defaultValue: "Pure List" })}</span>
                        {density === "list" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                    </DropdownMenuGroup>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>
                      {t("page.webmail.columns", { defaultValue: "Columns" })}
                    </DropdownMenuLabel>
                    <DropdownMenuGroup>
                      {(["fromTo", "subject", "date", "size", "flags"] as const).map((col) => (
                        <DropdownMenuItem key={col} onClick={() => toggleColumn(col)}>
                          <span className="flex-1">
                            {t(`page.webmail.column_${col}`, col)}
                          </span>
                          {visibleColumns[col] !== false && (
                            <Check className="h-3.5 w-3.5 text-primary" />
                          )}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuGroup>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>
                      {t("page.webmail.read_timer", { defaultValue: "Read Timer" })}
                    </DropdownMenuLabel>
                    <DropdownMenuGroup>
                      <DropdownMenuItem onClick={() => updateReadTimerMode("5s")}>
                        <span className="flex-1">{t("page.webmail.timer_5s", { defaultValue: "5 Seconds Delay" })}</span>
                        {readTimerMode === "5s" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => updateReadTimerMode("immediate")}>
                        <span className="flex-1">{t("page.webmail.timer_immediate", { defaultValue: "Immediate" })}</span>
                        {readTimerMode === "immediate" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => updateReadTimerMode("manual")}>
                        <span className="flex-1">{t("page.webmail.timer_manual", { defaultValue: "Manual" })}</span>
                        {readTimerMode === "manual" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                    </DropdownMenuGroup>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>
                      {t("page.webmail.after_delete", { defaultValue: "After Move / Delete" })}
                    </DropdownMenuLabel>
                    <DropdownMenuGroup>
                      <DropdownMenuItem onClick={() => updateAfterDeletePref("next")}>
                        <span className="flex-1">{t("page.webmail.after_delete_next", { defaultValue: "Open Next Message" })}</span>
                        {afterDeletePref === "next" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => updateAfterDeletePref("list")}>
                        <span className="flex-1">{t("page.webmail.after_delete_list", { defaultValue: "Return to List" })}</span>
                        {afterDeletePref === "list" && <Check className="h-3.5 w-3.5 text-primary" />}
                      </DropdownMenuItem>
                    </DropdownMenuGroup>
                  </DropdownMenuContent>
                </DropdownMenu>

                {/* AI Copilot Toggle */}
                <Button
                  type="button"
                  size="sm"
                  variant={aiDrawerOpen ? "secondary" : "ghost"}
                  className="hidden md:inline-flex h-8 px-2 gap-1 text-xs text-amber-500 hover:text-amber-600 hover:bg-amber-500/10 font-medium"
                  title={t("page.webmail.ai_copilot")}
                  onClick={() => setAiDrawerOpen((prev) => !prev)}
                >
                  <SparklesIcon className="h-3.5 w-3.5" />
                  <span className="hidden sm:inline">
                    {t("page.webmail.ai_copilot")}
                  </span>
                </Button>

                <div className="hidden md:block">
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
                        void guardedNavigation(() => {
                          setSelectedRef(null);
                          setDetail(null);
                          setMobilePane("list");
                        });
                      } else if (action === "next" || action === "previous") {
                        if (crossResults) return;
                        const index = messages.findIndex(
                          (message) => message.uid === selectedUid,
                        );
                        const next =
                          index < 0 ? 0 : index + (action === "next" ? 1 : -1);
                        if (visibleMessages[next])
                          openReference(rowRef(visibleMessages[next]));
                      } else if (detail && detail.uid === selectedUid) {
                        if (action === "reply" || action === "replyAll")
                          openCompose(
                            draftFromMessage(
                              action === "reply" ? "reply" : "reply-all",
                              detail,
                            ),
                          );
                        else if (action === "forward") void openForward();
                        else if (action === "trash") {
                          if (selectedRef) void moveToTrashWithUndo([selectedRef]);
                        }
                      }
                    }}
                  />
                </div>

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
                  <DropdownMenuContent
                    align="end"
                    className="fixed right-2 top-16 w-64 max-w-[calc(100vw-1rem)] max-h-[75dvh] overflow-y-auto md:absolute md:right-0 md:top-full"
                  >
                    <div className="px-2 py-2">
                      <AppearanceControls />
                    </div>
                    <DropdownMenuLabel>{t("nav.settings")}</DropdownMenuLabel>
                    <DropdownMenuGroup>
                      <DropdownMenuItem asChild>
                        <Link
                          to="/webmail/settings"
                          onClick={(event) => {
                            event.preventDefault();
                            void guardedNavigation(() =>
                              navigate("/webmail/settings"),
                            );
                          }}
                        >
                          <Settings className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                          <span>{t("page.webmail.settings_title")}</span>
                        </Link>
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onClick={() =>
                          void guardedNavigation(() => setPasswordOpen(true))
                        }
                      >
                        <KeyRound className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                        <span>{t("page.webmail.password_title")}</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onClick={() =>
                          void guardedNavigation(() =>
                            setFolderManagerOpen(true),
                          )
                        }
                      >
                        <Folder className="h-3.5 w-3.5 mr-2 text-muted-foreground" />
                        <span>{t("page.webmail.folder_manage")}</span>
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => switchApp("contacts")}>
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
                        <a
                          href="/mailbox/sieve"
                          onClick={(event) => {
                            event.preventDefault();
                            void guardedNavigation(() => {
                              window.location.href = "/mailbox/sieve";
                            });
                          }}
                        >
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
                            checkedRefs.map((ref) => ref.uid),
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
                        <Link
                          to="/webmail/help"
                          onClick={(event) => {
                            event.preventDefault();
                            void guardedNavigation(() =>
                              navigate("/webmail/help"),
                            );
                          }}
                        >
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
              <MailboxReminders hideWhenEmpty />
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
                    className="hidden xl:flex flex-col border-r bg-card/60 p-2 overflow-y-auto min-h-0 shrink-0"
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
                        onClick={() =>
                          void guardedNavigation(() =>
                            setFolderManagerOpen(true),
                          )
                        }
                      >
                        <Settings className="h-3 w-3" />
                      </Button>
                    </div>
                    {folderList}
                  </aside>
                  <SplitterHandle
                    className="!hidden xl:!flex"
                    label={t("page.webmail.resize_folders")}
                    onAdjust={(delta) => resizable.resizeBy("folder", delta)}
                    onMouseDown={(e) =>
                      resizable.startDragging("folder", e.clientX, e.clientY)
                    }
                  />
                </>
              )}

              {/* Main Content Pane (List + Detail depending on layoutMode) */}
              <div
                className={
                  resizable.isSplitBottom
                    ? "flex-1 flex flex-col min-h-0 min-w-0 overflow-hidden"
                    : "flex-1 flex flex-row min-h-0 min-w-0 overflow-hidden"
                }
              >
                {/* Message list pane */}
                <section
                  style={
                    resizable.isSplitBottom
                      ? ({
                          "--mail-list-height": `${resizable.listHeight}px`,
                        } as React.CSSProperties)
                      : ({
                          "--mail-list-width": `${resizable.listWidth}px`,
                        } as React.CSSProperties)
                  }
                  className={`border-r flex flex-col min-h-0 min-w-0 bg-background/50 ${
                    mobilePane === "detail" ? "hidden md:flex" : "flex"
                  } ${
                    resizable.isListOnly
                      ? mobilePane === "detail"
                        ? "hidden"
                        : "w-full flex-1"
                      : resizable.isSplitBottom
                        ? "flex-1 md:flex-none md:h-[var(--mail-list-height)] md:shrink-0"
                        : "w-full md:w-[min(var(--mail-list-width),40%)] md:shrink-0"
                  }`}
                >
                  {/* Search and filters header */}
                  <div className="px-3 py-2 border-b space-y-2 bg-card/40 shrink-0">
                    <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                      <span className="truncate min-w-0 font-medium">
                        {currentFolderInfo
                          ? currentFolderInfo.displayName
                          : (currentFolder?.name ?? "—")}{" "}
                        · {total} {t("page.webmail.messages_count")}
                      </span>
                      {total > 0 && (
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-6 px-1.5 text-xs text-muted-foreground hover:text-destructive"
                          onClick={() => setEmptyFolderOpen(true)}
                          title={t("page.webmail.empty_folder", { defaultValue: "Empty Folder" })}
                        >
                          <Trash2 className="h-3 w-3 mr-1" />
                          <span>{t("page.webmail.empty_folder", { defaultValue: "Empty Folder" })}</span>
                        </Button>
                      )}
                    </div>

                    <MailboxSearchBox
                      value={searchQ}
                      disabled={busy || !folderEnc}
                      onSearch={runSearch}
                      scope={searchAllFolders ? "all" : "current"}
                      onScopeChange={(nextScope) => {
                        const isAll = nextScope === "all";
                        void guardedNavigation(() => {
                          setSearchAllFolders(isAll);
                          void runSearch(searchQ, isAll);
                        });
                      }}
                      currentFolderName={
                        currentFolderInfo?.displayName || currentFolder?.name
                      }
                    />

                    {/* WP02 / I01: Per-folder search status display and continuation */}
                    {crossResults != null && (
                      <div className="p-2 rounded-md border bg-muted/40 text-xs space-y-1.5">
                        <div className="flex items-center justify-between font-medium">
                          <span>
                            {t("page.webmail.search_results_summary", {
                              count: crossResults.length,
                              defaultValue: `${crossResults.length} cross-folder results`,
                            })}
                          </span>
                          {searchFolderStates.some((s) => s.status === "partial") && (
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              className="h-6 text-xs px-2"
                              disabled={busy}
                              onClick={loadMoreCrossResults}
                            >
                              {t("page.webmail.search_continue", { defaultValue: "Continue searching" })}
                            </Button>
                          )}
                        </div>
                        {searchFolderStates.length > 0 && (
                          <div className="flex flex-wrap gap-1">
                            {searchFolderStates.map((st) => (
                              <span
                                key={st.folderEnc}
                                className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[11px] font-medium border ${
                                  st.status === "failed"
                                    ? "bg-destructive/10 text-destructive border-destructive/20"
                                    : st.status === "partial"
                                      ? "bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/20"
                                      : "bg-background text-muted-foreground border-border/60"
                                }`}
                                title={st.error || undefined}
                              >
                                <span>{st.folder}:</span>
                                <span>{st.count}</span>
                                <span>
                                  ({t(`page.webmail.search_status_${st.status}`, st.status)})
                                </span>
                              </span>
                            ))}
                          </div>
                        )}
                      </div>
                    )}

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
                          aria-label={t("page.webmail.thread_group")}
                          checked={threadGroup}
                          disabled={busy || !folderEnc}
                          onChange={(e) =>
                            applyListOptions(sortBy, sortAsc, e.target.checked)
                          }
                        />
                        <Layers className="h-3.5 w-3.5" />
                        <span className="sr-only sm:not-sr-only">
                          {t("page.webmail.thread_group")}
                        </span>
                      </label>

                      <label className="inline-flex items-center gap-1.5 cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">
                        <input
                          type="checkbox"
                          className="h-4 w-4 rounded border-input accent-primary"
                          aria-label={t("page.webmail.search_all_folders")}
                          checked={searchAllFolders}
                          disabled={busy || !folderEnc}
                          onChange={(e) => {
                            const next = e.target.checked;
                            void guardedNavigation(() => {
                              setSearchAllFolders(next);
                              void runSearch(searchQ, next);
                            });
                          }}
                        />
                        <span className="text-xs">
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
                            disabled={
                              visibleMessages.length === 0 || mutationBusy
                            }
                            aria-label={t("page.webmail.select_all")}
                          />
                          <span className="text-xs text-muted-foreground">
                            {t("page.webmail.select_all")}
                          </span>
                        </label>
                        {checkedRefs.length > 0 && (
                          <span className="text-xs text-primary font-medium">
                            {t("page.webmail.selected_count", {
                              count: checkedRefs.length,
                            })}
                          </span>
                        )}
                      </div>

                      {checkedRefs.length > 0 && (
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
                    {checkedRefs.length > 0 && (
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
                          onClick={() => openDeleteConfirm(checkedRefs)}
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
                            {moveOptions.map((f) => {
                              const info = getFolderInfo(f, t);
                              return (
                                <option key={f.name_enc} value={f.name_enc}>
                                  {info.displayName}
                                </option>
                              );
                            })}
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
                            onClick={() => void doMove(checkedRefs, bulkMoveTo)}
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
                              void transfer("copy", checkedRefs, bulkMoveTo)
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
                      const showing = visibleMessages;
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
                        const reference = rowRef(m);
                        const checked = checkedRefs.some((ref) =>
                          sameMessage(ref, reference),
                        );
                        const isSelected = sameMessage(selectedRef, reference);
                        const threadCount = threadCountOf(m);

                        return (
                          <MailboxMessageRow
                            key={messageKey(reference)}
                            message={m}
                            isSelected={isSelected}
                            isChecked={checked}
                            threadCount={threadCount}
                            dateText={dateFmt(m.date)}
                            isSelectionMode={checkedRefs.length > 0}
                            crossResults={Boolean(crossResults)}
                            density={density}
                            visibleColumns={visibleColumns}
                            isSentOrDrafts={isSentOrDrafts}
                            onSelect={() =>
                              crossResults
                                ? openCrossMessage(m)
                                : openMessage(m.uid)
                            }
                            onToggleCheck={(e) => {
                              e.stopPropagation();
                              setCheckedRefs((prev) =>
                                toggleMessage(prev, reference),
                              );
                            }}
                            onToggleFlag={(e) => void toggleRowFlag(m, e)}
                            onToggleRead={(e) => void toggleRowRead(m, e)}
                            onDelete={(e) => {
                              e.stopPropagation();
                              void moveToTrashWithUndo([reference]);
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
                    (cursorHistory.length > 0 || nextCursor > 0) && (
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
                {!resizable.isListOnly && (
                  <SplitterHandle
                    label={t("page.webmail.resize_messages")}
                    onAdjust={(delta) =>
                      resizable.resizeBy(
                        resizable.isSplitBottom
                          ? "list-height"
                          : "list-width",
                        delta,
                      )
                    }
                    orientation={
                      resizable.isSplitBottom
                        ? "horizontal"
                        : "vertical"
                    }
                    onMouseDown={(e) =>
                      resizable.startDragging(
                        resizable.isSplitBottom
                          ? "list-height"
                          : "list-width",
                        e.clientX,
                        e.clientY,
                      )
                    }
                  />
                )}

                {/* Detail pane */}
                <section
                  className={`flex-1 flex flex-col min-h-0 min-w-0 bg-card overflow-hidden ${
                    resizable.isListOnly
                      ? mobilePane === "detail"
                        ? "flex"
                        : "hidden"
                      : mobilePane === "list"
                        ? "hidden md:flex"
                        : "flex"
                  }`}
                >
                  {!detail ? (
                    <div className="flex-1 flex flex-col items-center justify-center p-8 text-center text-muted-foreground space-y-4 select-none">
                      <div className="h-16 w-16 rounded-2xl bg-muted/40 border border-border/60 flex items-center justify-center text-muted-foreground/50 shadow-2xs">
                        <Mail className="h-8 w-8 stroke-[1.5]" />
                      </div>
                      <div className="space-y-1">
                        <p className="text-sm font-medium text-foreground">
                          {detailError ??
                            (detailLoading
                              ? t("state.loading")
                              : t("page.webmail.select_message_read"))}
                        </p>
                        {!busy && (
                          <div className="hidden sm:flex items-center justify-center gap-3 pt-2 text-xs text-muted-foreground">
                            <span className="inline-flex items-center gap-1">
                              <kbd className="px-1.5 py-0.5 rounded border border-border/70 bg-muted/60 font-mono text-[10px] shadow-2xs">
                                J
                              </kbd>
                              <kbd className="px-1.5 py-0.5 rounded border border-border/70 bg-muted/60 font-mono text-[10px] shadow-2xs">
                                K
                              </kbd>
                              <span>{t("page.webmail.shortcut_navigate")}</span>
                            </span>
                            <span>·</span>
                            <span className="inline-flex items-center gap-1">
                              <kbd className="px-1.5 py-0.5 rounded border border-border/70 bg-muted/60 font-mono text-[10px] shadow-2xs">
                                C
                              </kbd>
                              <span>{t("page.webmail.shortcut_compose")}</span>
                            </span>
                            <span>·</span>
                            <span className="inline-flex items-center gap-1">
                              <kbd className="px-1.5 py-0.5 rounded border border-border/70 bg-muted/60 font-mono text-[10px] shadow-2xs">
                                /
                              </kbd>
                              <span>{t("page.webmail.shortcut_search")}</span>
                            </span>
                            <span>·</span>
                            <span className="inline-flex items-center gap-1">
                              <kbd className="px-1.5 py-0.5 rounded border border-border/70 bg-muted/60 font-mono text-[10px] shadow-2xs">
                                ?
                              </kbd>
                              <span>{t("page.webmail.shortcuts_title")}</span>
                            </span>
                          </div>
                        )}
                      </div>
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
                            className={`${resizable.isListOnly ? "inline-flex" : "md:hidden"} shrink-0 min-h-11 min-w-11 px-0`}
                            aria-label={t("page.webmail.back_to_list")}
                            onClick={backToList}
                          >
                            <ChevronLeft className="h-5 w-5" />
                          </Button>

                          {readTimerRemaining !== null && (
                            <div className="flex items-center gap-1.5 px-2 py-0.5 rounded bg-muted/60 text-xs text-muted-foreground border border-border/50">
                              <Clock className="h-3.5 w-3.5 animate-pulse text-primary" />
                              <span>
                                {t("page.webmail.marking_read_in", {
                                  seconds: readTimerRemaining,
                                  defaultValue: `Marking read in ${readTimerRemaining}s...`,
                                })}
                              </span>
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                className="h-5 px-1.5 text-[11px] text-muted-foreground hover:text-foreground"
                                onClick={cancelReadTimer}
                              >
                                {t("action.cancel", { defaultValue: "Cancel" })}
                              </Button>
                            </div>
                          )}

                          {isDraftsFolder(detailFolder) ? (
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
                            </>
                          )}

                          <details
                            className="relative"
                            aria-label={t("page.webmail.message_actions")}
                          >
                            <summary className="inline-flex min-h-9 cursor-pointer items-center rounded-md border border-input bg-background px-2.5 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                              {t("page.webmail.message_actions")}
                            </summary>
                            <div
                              className="fixed right-2 top-36 z-40 flex w-72 max-w-[calc(100vw-1rem)] max-h-[calc(100dvh-13rem)] flex-col items-stretch gap-2 overflow-y-auto rounded-md border bg-popover p-3 text-popover-foreground shadow-md md:absolute md:top-full md:right-0 md:mt-1"
                              onClick={(event) => {
                                if ((event.target as Element).closest("button"))
                                  event.currentTarget
                                    .closest("details")
                                    ?.removeAttribute("open");
                              }}
                            >
                              <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={(event) =>
                                  event.currentTarget
                                    .closest("details")
                                    ?.removeAttribute("open")
                                }
                              >
                                {t("action.close")}
                              </Button>
                              {!isDraftsFolder(detailFolder) && (
                                <div className="flex flex-wrap gap-2">
                                  {" "}
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
                                </div>
                              )}
                              <div className="flex flex-wrap items-center gap-2">
                                {" "}
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
                                  onClick={() => {
                                    if (selectedRef) void moveToTrashWithUndo([selectedRef]);
                                  }}
                                >
                                  <Trash2 className="h-3.5 w-3.5 mr-1" />
                                  {t("page.webmail.delete")}
                                </Button>
                                {/* Move / Copy dropdown */}
                                <div className="flex flex-wrap items-center gap-1 min-w-0">
                                  <select
                                    aria-label={t("page.webmail.move_to")}
                                    className="h-8 max-w-[8rem] rounded-md border border-input bg-background px-2 text-xs"
                                    value={moveTo}
                                    onChange={(e) => setMoveTo(e.target.value)}
                                  >
                                    <option value="">
                                      {t("page.webmail.move_to")}
                                    </option>
                                    {moveOptions.map((f) => {
                                      const info = getFolderInfo(f, t);
                                      return (
                                        <option
                                          key={f.name_enc}
                                          value={f.name_enc}
                                        >
                                          {info.displayName}
                                        </option>
                                      );
                                    })}
                                  </select>
                                  <Button
                                    size="sm"
                                    variant="outline"
                                    className="h-8 text-xs px-2"
                                    disabled={!moveTo || selectedUid == null}
                                    onClick={() =>
                                      selectedUid != null &&
                                      void doMove(
                                        selectedRef ? [selectedRef] : [],
                                        moveTo,
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
                                        selectedRef ? [selectedRef] : [],
                                        moveTo,
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
                                          selectedRef ? [selectedRef] : [],
                                          moveTo || "INBOX",
                                        )
                                      }
                                    >
                                      {t("page.webmail.restore")}
                                    </Button>
                                  )}
                                </div>
                              </div>
                              <div className="border-t pt-2">
                                {" "}
                                <MailboxMessageTools
                                  key={`tools:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}`}
                                  folder={
                                    (selectedEncRef.current ?? folderEnc)!
                                  }
                                  message={detail}
                                />
                              </div>
                            </div>
                          </details>
                        </div>

                        {/* Fast AI summary action on top right */}
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-8 text-xs text-amber-500 hover:text-amber-600 hover:bg-amber-500/10 gap-1 font-medium"
                          aria-label={t("page.webmail.ai_summary")}
                          onClick={() => setAiDrawerOpen(true)}
                        >
                          <SparklesIcon className="h-3.5 w-3.5" />
                          <span className="hidden lg:inline">
                            {t("page.webmail.ai_summary")}
                          </span>
                        </Button>
                      </div>

                      {/* Detail Body Scroll Container */}
                      <div className="flex-1 overflow-y-auto p-4 sm:p-6 space-y-5 min-h-0">
                        {/* Header card */}
                        <MailboxDetailHeader
                          message={detail}
                          dateText={dateFmt(detail.date)}
                        />

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
                              key={`html:${selectedEncRef.current ?? folderEnc}:${detail.uid}:${detail.uid_validity}:${loadImages ? "remote" : "blocked"}`}
                              message={detail}
                              source={{
                                folderEnc: (selectedEncRef.current ??
                                  folderEnc)!,
                                uid: detail.uid,
                                uidValidity: detail.uid_validity ?? 0,
                              }}
                              loadImages={loadImages}
                              onLoadImages={handleLoadImages}
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
                                            detail.uid,
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
                        {!isDraftsFolder(detailFolder) && (
                          <div className="pt-2">
                            <MailboxInlineReply
                              key={
                                selectedRef ? messageKey(selectedRef) : "none"
                              }
                              ref={inlineController}
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
          openReference(source);
        }}
        onApplyAIProposal={async (proposal) => {
          if (!detail || !selectedRef || !detail.uid_validity) return;
          const source = { ...selectedRef };
          const generation = navigationGeneration.current;
          if (
            !(await preserveEdits()) ||
            generation !== navigationGeneration.current ||
            !sameMessage(selectedRefLive.current, source)
          )
            return;
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
            reply_source: source,
            text: proposal.body,
          });
          if (
            generation !== navigationGeneration.current ||
            !sameMessage(selectedRefLive.current, source)
          )
            return;
          void openCompose({
            ...reply,
            from,
            mode: "draft",
            uid: result.uid,
            uidValidity: result.uid_validity,
            folderEnc: result.name_enc,
            subject: reply.subject ?? proposal.subject,
            text: proposal.body,
          });
        }}
      />

      {/* Modals & Dialogs */}
      <MailboxComposeDialog
        ref={composeController}
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
          if (!open) setDeleteRefs([]);
        }}
        title={
          !deleteSource?.trash
            ? t("page.webmail.trash_confirm_title")
            : deleteRefs.length > 1
              ? t("page.webmail.bulk_delete_confirm_title")
              : t("page.webmail.delete_confirm_title")
        }
        description={
          !deleteSource?.trash
            ? t("page.webmail.trash_confirm")
            : deleteRefs.length > 1
              ? t("page.webmail.bulk_delete_confirm", {
                  count: deleteRefs.length,
                })
              : t("page.webmail.delete_confirm")
        }
        destructive
        onConfirm={confirmDelete}
      />

      {/* WP03 / I06: Empty folder confirmation dialog */}
      <ConfirmDialog
        open={emptyFolderOpen}
        onOpenChange={setEmptyFolderOpen}
        title={t("page.webmail.empty_folder_confirm_title", { defaultValue: "Empty folder?" })}
        description={t(
          "page.webmail.empty_folder_confirm",
          "All messages in this folder will be permanently deleted. This action cannot be undone.",
        )}
        destructive
        onConfirm={confirmEmptyFolder}
      />

      {/* WP03 / I06: One-click move to Trash undo notification */}
      {undoNotice && (
        <div
          role="status"
          className="fixed bottom-16 right-4 md:bottom-6 md:right-6 z-50 flex items-center gap-3 bg-card border border-border shadow-lg rounded-lg px-4 py-3 text-sm animate-in fade-in slide-in-from-bottom-2"
        >
          <span>
            {t("page.webmail.moved_to_trash", {
              count: undoNotice.refs.length,
              folder: undoNotice.originalFolderName,
              defaultValue: `Moved ${undoNotice.refs.length} message(s) to Trash`,
            })}
          </span>
          <Button
            size="sm"
            variant="secondary"
            className="h-7 text-xs font-semibold px-2.5"
            onClick={() => void undoTrash()}
          >
            {t("action.undo", { defaultValue: "Undo" })}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
            onClick={() => setUndoNotice(null)}
            aria-label={t("action.close")}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
