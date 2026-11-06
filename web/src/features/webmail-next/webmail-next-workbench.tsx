import { useState, useEffect, useMemo, useCallback, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Palmtree } from "lucide-react";
import { WebmailHeader } from "./components/webmail-header";
import { WebmailNavigationRail } from "./components/webmail-navigation-rail";
import { WebmailMessageList } from "./components/webmail-message-list";
import { WebmailReadingPane } from "./components/webmail-reading-pane";
import { WebmailComposeDock } from "./components/webmail-compose-dock";
import { WebmailShortcutHelp } from "./components/webmail-shortcut-help";
import { WebmailMoveNotice } from "./components/webmail-move-notice";
import { useMailboxShortcuts } from "./hooks/use-mailbox-shortcuts";
import type { SelectionApi } from "./components/webmail-message-list";
import { moveToSpecialFolder } from "./lib/move-mail";
import {
  isTrashMailbox,
  resolveSpecialFolder,
  SCHEDULED_VIEW,
} from "./lib/special-folders";
import type { ShortcutAction } from "./lib/mailbox-shortcuts";
import {
  WebmailSettingsDrawer,
  type SettingsTab,
} from "./components/settings/webmail-settings-drawer";
import { REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT } from "./components/settings/remote-images-section";
import type { AdvancedSearchParams } from "./components/search/webmail-advanced-search-popover";
import { WebmailSearchFilterChips } from "./components/search/webmail-search-filter-chips";
import { WebmailContactsModal } from "./components/contacts/webmail-contacts-modal";
import { WebmailErrorBoundary } from "./components/webmail-error-boundary";
import { useWebmailPreferences } from "./use-webmail-preferences";
import {
  listFolders,
  listMessages,
  crossFolderSearch,
  getMessage,
  flagMessages,
  deleteMessages,
  moveMessages,
  createMailboxFolder,
  getMailboxQuota,
  listScheduledSends,
  getMailboxAddress,
  getMailboxRules,
  saveMailboxRules,
  getMailboxSettings,
  type MailboxSettingsView,
  type MessageDetail,
  type MessageSummary,
  type MailboxRulesView,
  type MailboxSortKey,
} from "@/features/webmail/mailbox-api";
import "./theme-vellum.css";

interface WebmailNextWorkbenchProps {
  initialFolder?: string;
}

// Pure search query builder function aligned with backend grammar (internal/webmail/search.go).
// Supported tokens: from:, to:, subject:, body:, text:, has:attachment, since:, before:
// Note: Backend parser does NOT support filename: token and rejects unknown key: with 400.
// Filename extension constraints are mapped to has:attachment at query time.
export function buildSearchQuery(
  searchQuery: string,
  searchParams: AdvancedSearchParams,
): string {
  const parts: string[] = [];
  if (searchQuery.trim()) parts.push(searchQuery.trim());
  if (searchParams.from?.trim()) parts.push(`from:${searchParams.from.trim()}`);
  if (searchParams.to?.trim()) parts.push(`to:${searchParams.to.trim()}`);
  if (searchParams.subject?.trim())
    parts.push(`subject:${searchParams.subject.trim()}`);
  if (searchParams.body?.trim()) parts.push(`body:${searchParams.body.trim()}`);
  if (searchParams.hasAttachment || searchParams.filenameExt?.trim()) {
    parts.push("has:attachment");
  }
  if (searchParams.dateRange === "7d") {
    const d = new Date(Date.now() - 7 * 86400000).toISOString().slice(0, 10);
    parts.push(`since:${d}`);
  } else if (searchParams.dateRange === "30d") {
    const d = new Date(Date.now() - 30 * 86400000).toISOString().slice(0, 10);
    parts.push(`since:${d}`);
  } else if (searchParams.dateRange === "1y") {
    const d = new Date(Date.now() - 365 * 86400000).toISOString().slice(0, 10);
    parts.push(`since:${d}`);
  } else if (searchParams.dateRange === "custom") {
    if (searchParams.dateFrom?.trim())
      parts.push(`since:${searchParams.dateFrom.trim()}`);
    if (searchParams.dateTo?.trim())
      parts.push(`before:${searchParams.dateTo.trim()}`);
  }
  return parts.join(" ");
}

export function WebmailNextWorkbench({
  initialFolder = "INBOX",
}: WebmailNextWorkbenchProps) {
  const { t } = useTranslation();
  const [isRailCollapsed, setIsRailCollapsed] = useState(false);
  const [activeFolder, setActiveFolder] = useState(initialFolder);
  const [searchQuery, setSearchQuery] = useState("");
  const [selectedUid, setSelectedUid] = useState<number | null>(null);
  const [cursorUid, setCursorUid] = useState<number | null>(null);
  const [isHelpOpen, setIsHelpOpen] = useState(false);
  const [moveNotice, setMoveNotice] = useState<string | null>(null);
  const [moveNoticeToken, setMoveNoticeToken] = useState(0);
  const [sendNonce, setSendNonce] = useState(0);
  const [minimizeNonce, setMinimizeNonce] = useState(0);
  const [composeCc, setComposeCc] = useState<string[]>([]);
  const [pendingPermanentUids, setPendingPermanentUids] = useState<
    number[] | null
  >(null);
  const selectionRef = useRef<SelectionApi | null>(null);
  const clearMoveNotice = useCallback(() => setMoveNotice(null), []);

  // Search & Filter popover state
  const [isAdvancedSearchOpen, setIsAdvancedSearchOpen] = useState(false);
  const [searchParams, setSearchParams] = useState<AdvancedSearchParams>({});

  // Contacts modal state
  const [isContactsOpen, setIsContactsOpen] = useState(false);

  // Sorting state
  const [sortKey, setSortKey] = useState<MailboxSortKey>("date");
  const [sortOrder, setSortOrder] = useState<"asc" | "desc">("desc");

  // Compose dock state
  const [isComposeOpen, setIsComposeOpen] = useState(false);
  const [composeTo, setComposeTo] = useState<string[]>([]);
  const [composeSubject, setComposeSubject] = useState("");
  const [composeKey, setComposeKey] = useState(0);

  const queryClient = useQueryClient();

  // Remote images dual authorization model:
  // 1. Account-level confirmed global preference
  // 2. Monotonic policy generation counter
  // 3. Explicit per-message manual consent (cleared on UID change)
  const [policyGeneration, setPolicyGeneration] = useState(1);
  const [manualAuthorizedUid, setManualAuthorizedUid] = useState<number | null>(
    null,
  );
  // Null until a successful preference event. The event value is applied
  // synchronously; a later settings GET may confirm it only for this generation.
  const [confirmedGlobalAllow, setConfirmedGlobalAllow] = useState<
    boolean | null
  >(null);
  const settingsGenerationRef = useRef(0);
  const policyGenerationRef = useRef(policyGeneration);
  const globalAllowedRef = useRef(false);
  policyGenerationRef.current = policyGeneration;

  // Settings drawer state
  const [isSettingsOpen, setIsSettingsOpen] = useState(false);
  const [settingsTab, setSettingsTab] = useState<SettingsTab>("account");

  const { density, setDensity } = useWebmailPreferences();

  // 1a. Fetch mailbox settings for account preferences (allow_all_remote_images)
  const { data: mailboxSettings, refetch: refetchSettings } = useQuery({
    queryKey: ["webmail", "settings"],
    queryFn: async () => {
      const generation = settingsGenerationRef.current;
      const data = await getMailboxSettings();
      // An older GET must not replace a newer confirmed preference.
      if (generation !== settingsGenerationRef.current) {
        const cached = queryClient.getQueryData<MailboxSettingsView>([
          "webmail",
          "settings",
        ]);
        if (cached) return cached;
      }
      return data;
    },
    staleTime: 60_000,
  });

  const persistedGlobalAllow =
    mailboxSettings?.preferences?.allow_all_remote_images === true;
  const isGlobalImagesAllowed =
    confirmedGlobalAllow !== null ? confirmedGlobalAllow : persistedGlobalAllow;
  globalAllowedRef.current = isGlobalImagesAllowed;

  const handleRemoteImagesPreferenceChanged = useCallback(
    (allowAll?: boolean) => {
      // PUT already succeeded. Revoke or grant before any settings refetch,
      // so a stale allow=true cache cannot keep authSource=global.
      const next = allowAll === true;
      settingsGenerationRef.current += 1;
      const generation = settingsGenerationRef.current;
      const nextPolicyGeneration = policyGenerationRef.current + 1;
      policyGenerationRef.current = nextPolicyGeneration;
      globalAllowedRef.current = next;
      setConfirmedGlobalAllow(next);
      setPolicyGeneration(nextPolicyGeneration);
      queryClient.setQueryData<MailboxSettingsView | undefined>(
        ["webmail", "settings"],
        (current) => {
          if (!current) return current;
          return {
            ...current,
            preferences: {
              ...(current.preferences ?? {}),
              allow_all_remote_images: next,
            },
          };
        },
      );
      if (!next) {
        queryClient.removeQueries({
          predicate: (query) => {
            const key = query.queryKey;
            return (
              Array.isArray(key) &&
              key[0] === "webmail" &&
              key[1] === "detail" &&
              key[4] === "global"
            );
          },
        });
      }
      void refetchSettings().then((result) => {
        if (settingsGenerationRef.current !== generation) return;
        const persisted =
          result.data?.preferences?.allow_all_remote_images === true;
        // Same-generation confirmation must not widen the event. A stale
        // GET that still says true cannot reopen a grant this event closed.
        // A true event stays true only when the GET is strictly true.
        const confirmed = next && persisted;
        globalAllowedRef.current = confirmed;
        setConfirmedGlobalAllow(confirmed);
        if (!confirmed) {
          queryClient.setQueryData<MailboxSettingsView | undefined>(
            ["webmail", "settings"],
            (current) => {
              if (!current) return current;
              return {
                ...current,
                preferences: {
                  ...(current.preferences ?? {}),
                  allow_all_remote_images: false,
                },
              };
            },
          );
          queryClient.removeQueries({
            predicate: (query) => {
              const key = query.queryKey;
              return (
                Array.isArray(key) &&
                key[0] === "webmail" &&
                key[1] === "detail" &&
                key[4] === "global"
              );
            },
          });
        }
      });
    },
    [refetchSettings, queryClient],
  );

  useEffect(() => {
    const handler = (e: Event) => {
      const customEvent = e as CustomEvent<{ allowAll: boolean }>;
      handleRemoteImagesPreferenceChanged(customEvent.detail?.allowAll);
    };
    window.addEventListener(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, handler);
    return () =>
      window.removeEventListener(
        REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT,
        handler,
      );
  }, [handleRemoteImagesPreferenceChanged]);

  // 1b. Fetch folders
  const { data: folderData, refetch: refetchFolders } = useQuery({
    queryKey: ["webmail", "folders"],
    queryFn: () => listFolders(),
    staleTime: 30_000,
  });

  const folders = folderData?.folders || [
    { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
  ];
  const inboxFolder = resolveSpecialFolder(folders, "\\Inbox", "INBOX");
  const inboxEnc = inboxFolder?.name_enc ?? "INBOX";
  const isScheduledView = activeFolder === SCHEDULED_VIEW;

  const { data: quotaData } = useQuery({
    queryKey: ["webmail", "quota"],
    queryFn: () => getMailboxQuota(),
    retry: false,
    staleTime: 60_000,
  });
  const quota =
    typeof quotaData?.used_bytes === "number" &&
    typeof quotaData?.quota_bytes === "number"
      ? { used_bytes: quotaData.used_bytes, quota_bytes: quotaData.quota_bytes }
      : null;

  const { data: inboxUnseen = 0 } = useQuery({
    queryKey: ["webmail", "unseen", inboxEnc],
    queryFn: async () => {
      const page = await listMessages(inboxEnc, { q: "is:unseen", limit: 1 });
      return page.total ?? 0;
    },
    enabled: Boolean(inboxEnc),
    staleTime: 15_000,
  });

  useEffect(() => {
    const title =
      inboxUnseen > 0
        ? `(${inboxUnseen}) ${t("webmail_next.inbox")}`
        : t("webmail_next.inbox");
    document.title = title;
  }, [inboxUnseen, t]);

  const { data: scheduledData } = useQuery({
    queryKey: ["webmail", "scheduled"],
    queryFn: () => listScheduledSends(),
    enabled: isScheduledView,
    staleTime: 10_000,
  });

  // 2. Fetch rules for vacation autoresponder status
  const { data: rulesData, refetch: refetchRules } = useQuery<MailboxRulesView>(
    {
      queryKey: ["webmail", "rules"],
      queryFn: () => getMailboxRules(),
      staleTime: 60_000,
    },
  );

  const vacation = rulesData?.rules?.vacation;
  const isVacationActive = Boolean(vacation?.enabled);
  const vacationFromStr = vacation?.from ? vacation.from.slice(0, 10) : "";
  const vacationUntilStr = vacation?.until ? vacation.until.slice(0, 10) : "";

  const handleQuickDisableVacation = async () => {
    if (!rulesData) return;
    try {
      await saveMailboxRules({
        rules: {
          ...rulesData.rules,
          vacation: {
            ...rulesData.rules.vacation,
            enabled: false,
          },
        },
        confirmed: true,
        expected_version: rulesData.version,
      });
      refetchRules();
    } catch {
      // ignore
    }
  };

  // Search query builder from simple search + advanced filter params
  const compiledQuery = useMemo(
    () => buildSearchQuery(searchQuery, searchParams),
    [searchQuery, searchParams],
  );

  const handleRemoveFilterChip = (key: keyof AdvancedSearchParams) => {
    setSearchParams((prev) => {
      const copy = { ...prev };
      delete copy[key];
      if (key === "hasAttachment") {
        delete copy.filenameExt;
      }
      return copy;
    });
  };

  const handleClearAllFilters = () => {
    setSearchParams({});
    setSearchQuery("");
  };

  const handleApplyAdvancedSearch = (params: AdvancedSearchParams) => {
    setSearchParams(params);
    if (params.folder) {
      setActiveFolder(params.folder);
    }
  };

  // 3. Fetch message list (supports folder-scoped or cross-folder advanced search)
  const isCrossFolder = Boolean(compiledQuery && !searchParams.folder);

  const {
    data: messageListData,
    isLoading: isListLoading,
    refetch: refetchMessages,
    isRefetching,
  } = useQuery<{
    messages: MessageSummary[];
    total?: number;
    uid_validity?: number;
  }>({
    queryKey: [
      "webmail",
      "messages",
      isCrossFolder ? "cross-folder" : activeFolder,
      sortKey,
      sortOrder,
      compiledQuery,
    ],
    queryFn: async () => {
      if (isCrossFolder) {
        return crossFolderSearch(compiledQuery, { limit: 100 });
      }
      return listMessages(activeFolder, {
        sort: sortKey,
        order: sortOrder,
        q: compiledQuery,
        thread: true,
      });
    },
    enabled: !isScheduledView,
    staleTime: 10_000,
  });

  const messages = useMemo(
    () => messageListData?.messages || [],
    [messageListData?.messages],
  );

  // Derive authorization source for currently selected message:
  // Decoupled dual authorization model:
  // - "manual" if user explicitly granted consent for this UID
  // - "global" if account settings have allow_all_remote_images === true
  // - "none" otherwise (fail-closed default)
  const isCurrentManualGranted = Boolean(
    selectedUid !== null && manualAuthorizedUid === selectedUid,
  );

  const authSource: "global" | "manual" | "none" = isCurrentManualGranted
    ? "manual"
    : isGlobalImagesAllowed
      ? "global"
      : "none";

  const shouldRequestImages = authSource !== "none";

  interface MessageDetailResult {
    message: MessageDetail;
    authSource: "global" | "manual" | "none";
    policyGeneration: number;
    uid: number;
  }

  // 3. Fetch selected message details with isolated cache key and generation tracking
  const {
    data: detailResult,
    isLoading: isDetailLoading,
    refetch: refetchDetail,
  } = useQuery<MessageDetailResult | null>({
    queryKey: [
      "webmail",
      "detail",
      activeFolder,
      selectedUid,
      authSource,
      policyGeneration,
    ],
    queryFn: async () => {
      if (selectedUid === null) return null;
      const currentGen = policyGeneration;
      const currentSource = authSource;
      const currentUid = selectedUid;
      const msg = await getMessage(activeFolder, currentUid, {
        loadImages: shouldRequestImages,
      });
      // Drop a late response that missed a newer policy generation or a
      // global grant that has already been revoked.
      if (
        currentGen !== policyGenerationRef.current ||
        (currentSource === "global" && !globalAllowedRef.current)
      ) {
        return null;
      }
      return {
        message: msg,
        authSource: currentSource,
        policyGeneration: currentGen,
        uid: currentUid,
      };
    },
    enabled: selectedUid !== null,
    staleTime: 5_000,
  });

  // Stale response rejection: verify response generation and authorization source match current state
  const messageDetail = useMemo<MessageDetail | null>(() => {
    if (!detailResult || !detailResult.message) return null;
    // A cached or late body may be shown only for the current UID, policy
    // generation, and authorization source. A global/manual payload must not
    // cross into a later "none" policy.
    if (
      detailResult.uid !== selectedUid ||
      detailResult.policyGeneration !== policyGeneration ||
      detailResult.authSource !== authSource
    ) {
      return null;
    }
    if (detailResult.authSource === "global" && !isGlobalImagesAllowed) {
      return null;
    }
    if (
      detailResult.authSource === "manual" &&
      manualAuthorizedUid !== selectedUid
    ) {
      return null;
    }
    return detailResult.message;
  }, [
    detailResult,
    selectedUid,
    authSource,
    isGlobalImagesAllowed,
    policyGeneration,
    manualAuthorizedUid,
  ]);

  // Reset manual authorization on UID change
  useEffect(() => {
    setManualAuthorizedUid(null);
  }, [selectedUid]);

  // Automatically select the first message on wide screen if none selected
  useEffect(() => {
    if (
      messages.length > 0 &&
      selectedUid === null &&
      window.innerWidth >= 1024
    ) {
      setSelectedUid(messages[0].uid);
    }
  }, [messages, selectedUid]);

  const toggleRail = () => setIsRailCollapsed((prev) => !prev);
  const listValidity = messageListData?.uid_validity;

  const openCompose = (to: string[], subject: string, cc: string[] = []) => {
    setComposeTo(to);
    setComposeCc(cc);
    setComposeSubject(subject);
    setComposeKey((key) => key + 1);
    setIsComposeOpen(true);
  };

  const showMoved = (folderLabel: string) => {
    setMoveNotice(t("webmail_next.moved_to", { folder: folderLabel }));
    setMoveNoticeToken((token) => token + 1);
  };

  const flagUids = async (uids: number[], add: string[], remove: string[]) => {
    if (uids.length === 0 || isScheduledView) return;
    await flagMessages(activeFolder, {
      uids,
      add,
      remove,
      uid_validity: listValidity,
    });
    refetchMessages();
    if (selectedUid !== null && uids.includes(selectedUid)) refetchDetail();
  };

  const handleToggleStar = async (uid: number) => {
    const msg = messages.find((m: MessageSummary) => m.uid === uid);
    if (!msg) return;
    const isStarred = msg.flags.includes("\\Flagged");
    await flagUids(
      [uid],
      isStarred ? [] : ["\\Flagged"],
      isStarred ? ["\\Flagged"] : [],
    );
  };

  const handleToggleRead = async (uid: number) => {
    const msg = messages.find((m: MessageSummary) => m.uid === uid);
    if (!msg) return;
    const isSeen = msg.flags.includes("\\Seen");
    await flagUids([uid], isSeen ? [] : ["\\Seen"], isSeen ? ["\\Seen"] : []);
  };

  const targetUids = (explicit?: number[]) => {
    if (explicit && explicit.length > 0) return explicit;
    const checked = selectionRef.current?.selected() ?? [];
    if (checked.length > 0) return checked;
    if (cursorUid !== null) return [cursorUid];
    if (selectedUid !== null) return [selectedUid];
    return [];
  };

  const handleMoveSpecial = async (
    flag: "\\Archive" | "\\Trash",
    fallbackName: string,
    labelKey: string,
    explicit?: number[],
  ) => {
    const uids = targetUids(explicit);
    if (uids.length === 0 || isScheduledView) return;
    if (flag === "\\Trash" && isTrashMailbox(folders, activeFolder)) {
      setPendingPermanentUids(uids);
      return;
    }
    const destination = await moveToSpecialFolder(
      folders,
      activeFolder,
      uids,
      listValidity,
      flag,
      fallbackName,
      {
        createFolder: (name) => createMailboxFolder(name),
        move: (source, body) => moveMessages(source, body),
        reloadFolders: async () => {
          const result = await refetchFolders();
          return result.data?.folders ?? folders;
        },
      },
    );
    if (!destination) return;
    selectionRef.current?.clear();
    refetchMessages();
    showMoved(t(labelKey));
  };

  const confirmPermanentDelete = async () => {
    const uids = pendingPermanentUids ?? [];
    setPendingPermanentUids(null);
    if (uids.length === 0) return;
    await deleteMessages(activeFolder, {
      uids,
      uid_validity: listValidity,
    });
    selectionRef.current?.clear();
    refetchMessages();
    if (selectedUid !== null && uids.includes(selectedUid)) {
      setSelectedUid(null);
    }
  };

  const handleDeleteMessage = (uid: number) => {
    void handleMoveSpecial("\\Trash", "Trash", "webmail_next.folder_trash", [
      uid,
    ]);
  };

  const replySubject = (subject: string) =>
    subject.toLowerCase().startsWith("re:") ? subject : `Re: ${subject}`;

  const handleReply = (msg: MessageDetail) => {
    const sender = msg.from?.[0]?.email;
    openCompose(sender ? [sender] : [], replySubject(msg.subject));
  };

  const handleReplyAll = (msg: MessageDetail) => {
    const me = (getMailboxAddress() ?? "").toLowerCase();
    const to = (msg.from ?? [])
      .map((item) => item.email)
      .filter((email) => email && email.toLowerCase() !== me);
    const seen = new Set(to.map((email) => email.toLowerCase()));
    const cc = [...(msg.to ?? []), ...(msg.cc ?? [])]
      .map((item) => item.email)
      .filter((email) => {
        const key = email.toLowerCase();
        if (!email || key === me || seen.has(key)) return false;
        seen.add(key);
        return true;
      });
    openCompose(to, replySubject(msg.subject), cc);
  };

  const handleForward = (msg: MessageDetail) => {
    const subject = msg.subject.toLowerCase().startsWith("fwd:")
      ? msg.subject
      : `Fwd: ${msg.subject}`;
    openCompose([], subject);
  };

  const visibleRowUids = () =>
    [...document.querySelectorAll<HTMLElement>("[data-testid^='mail-row-']")]
      .map((node) => Number(node.getAttribute("data-testid")?.slice(9)))
      .filter((uid) => Number.isFinite(uid));

  const moveCursor = (delta: number) => {
    const uids = visibleRowUids();
    if (uids.length === 0) return;
    const current = cursorUid ?? uids[0];
    const index = Math.max(0, uids.indexOf(current));
    const next = uids[Math.min(uids.length - 1, Math.max(0, index + delta))];
    setCursorUid(next);
    document
      .querySelector<HTMLElement>(`[data-testid="mail-row-${next}"]`)
      ?.scrollIntoView?.({ block: "nearest" });
  };

  const focusSearch = () => {
    document
      .querySelector<HTMLInputElement>('[data-testid="webmail-search-input"]')
      ?.focus();
  };

  const onShortcut = useCallback(
    (action: ShortcutAction) => {
      if (action === "cursor-next") moveCursor(1);
      else if (action === "cursor-prev") moveCursor(-1);
      else if (action === "open") {
        const uid = cursorUid ?? visibleRowUids()[0];
        if (uid != null) setSelectedUid(uid);
      } else if (action === "toggle-check") {
        const uid = cursorUid ?? visibleRowUids()[0];
        if (uid != null) selectionRef.current?.toggle(uid, false, false);
      } else if (action === "archive") {
        void handleMoveSpecial(
          "\\Archive",
          "Archive",
          "webmail_next.folder_archive",
        );
      } else if (action === "trash") {
        void handleMoveSpecial("\\Trash", "Trash", "webmail_next.folder_trash");
      } else if (action === "toggle-star") {
        const uids = targetUids();
        if (uids.length === 1) void handleToggleStar(uids[0]);
        else void flagUids(uids, ["\\Flagged"], []);
      } else if (action === "mark-read") {
        void flagUids(targetUids(), ["\\Seen"], []);
      } else if (action === "mark-unread") {
        void flagUids(targetUids(), [], ["\\Seen"]);
      } else if (action === "reply" && messageDetail)
        handleReply(messageDetail);
      else if (action === "reply-all" && messageDetail)
        handleReplyAll(messageDetail);
      else if (action === "forward" && messageDetail)
        handleForward(messageDetail);
      else if (action === "compose") openCompose([], "");
      else if (action === "focus-search") focusSearch();
      else if (action === "help") setIsHelpOpen(true);
      else if (action === "go-inbox") {
        setActiveFolder(inboxEnc);
        setSelectedUid(null);
      } else if (action === "send") setSendNonce((nonce) => nonce + 1);
      else if (action === "minimize-compose")
        setMinimizeNonce((nonce) => nonce + 1);
      else if (action === "close-overlay") {
        if (isHelpOpen) setIsHelpOpen(false);
        else if (isSettingsOpen) setIsSettingsOpen(false);
        else if (isContactsOpen) setIsContactsOpen(false);
      }
    },
    // Handlers close over the latest list snapshot. Recreating the callback
    // is safe: the shortcut hook reads it from a ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      cursorUid,
      messageDetail,
      inboxEnc,
      isHelpOpen,
      isSettingsOpen,
      isContactsOpen,
      activeFolder,
      folders,
      listValidity,
      messages,
      selectedUid,
    ],
  );

  useMailboxShortcuts({
    composeOpen: isComposeOpen,
    overlayOpen: isHelpOpen || isSettingsOpen || isContactsOpen,
    onAction: onShortcut,
  });

  return (
    <div className="webmail-vellum-root min-h-screen h-screen flex flex-col bg-[#FAF9F5] text-[#18181B] overflow-hidden select-none">
      {/* 1. Global Header */}
      <WebmailHeader
        onToggleRail={toggleRail}
        onRefresh={() => {
          refetchFolders();
          refetchMessages();
          refetchRules();
        }}
        searchQuery={searchQuery}
        onSearchChange={setSearchQuery}
        density={density}
        onSetDensity={setDensity}
        isRefreshing={isRefetching}
        onOpenSettings={() => {
          setSettingsTab("account");
          setIsSettingsOpen(true);
        }}
        onOpenContacts={() => setIsContactsOpen(true)}
        isAdvancedSearchOpen={isAdvancedSearchOpen}
        onOpenAdvancedSearch={() => setIsAdvancedSearchOpen((prev) => !prev)}
        onCloseAdvancedSearch={() => setIsAdvancedSearchOpen(false)}
        folders={folders}
        searchParams={searchParams}
        onApplyAdvancedSearch={handleApplyAdvancedSearch}
      />

      {/* Vacation Active Top Banner */}
      {isVacationActive && (
        <div
          data-testid="vacation-active-banner"
          className="bg-[#FEF3C7] border-b border-[#FDE68A] text-[#92400E] px-4 py-2 text-xs flex items-center justify-between z-20 shrink-0"
        >
          <div
            className="flex items-center gap-2 cursor-pointer hover:underline"
            onClick={() => {
              setSettingsTab("vacation");
              setIsSettingsOpen(true);
            }}
          >
            <Palmtree className="w-4 h-4 text-[#D97706] shrink-0" />
            <span>
              {t(
                "webmail_next.vacation_banner_active",
                "Vacation autoresponder is currently active ({{from}} to {{until}})",
                {
                  from:
                    vacationFromStr ||
                    t("webmail_next.vacation_from_default", "today"),
                  until:
                    vacationUntilStr ||
                    t("webmail_next.vacation_until_default", "indefinite"),
                },
              )}
            </span>
          </div>
          <button
            data-testid="btn-quick-dismiss-vacation"
            type="button"
            onClick={handleQuickDisableVacation}
            className="text-xs font-semibold text-[#B45309] hover:text-[#78350F] underline ml-4 shrink-0"
          >
            {t("webmail_next.vacation_quick_disable", "Turn off")}
          </button>
        </div>
      )}

      {/* 2. Main Workbench Area: 3-pane responsive layout */}
      <div className="flex-1 flex overflow-hidden relative">
        {/* Pane 1: Navigation Rail (220px or 64px collapsed; hidden on mobile 375px if message selected) */}
        <div
          className={`hidden md:flex h-full shrink-0 ${selectedUid ? "hidden lg:flex" : ""}`}
        >
          <WebmailNavigationRail
            isCollapsed={isRailCollapsed}
            activeFolder={activeFolder}
            onSelectFolder={(f) => {
              setActiveFolder(f);
              setSelectedUid(null);
              setCursorUid(null);
            }}
            folders={folders}
            inboxUnread={inboxUnseen}
            quota={quota}
            onCompose={() => openCompose([], "")}
          />
        </div>

        {/* Pane 2: Message List (380px on desktop; full width on mobile/tablet when browsing) */}
        <div
          data-testid="webmail-message-list-pane"
          className={`h-full border-r border-[#EBE8E0] bg-[#FFFFFF] flex flex-col shrink-0 transition-all ${
            selectedUid
              ? "hidden md:flex md:w-[340px] lg:w-[380px]"
              : "flex-1 md:w-[340px] lg:w-[380px]"
          }`}
        >
          {/* Active Filter Chips Bar */}
          <WebmailSearchFilterChips
            params={searchParams}
            onRemoveParam={handleRemoveFilterChip}
            onClearAll={handleClearAllFilters}
          />

          <WebmailErrorBoundary onReset={() => refetchMessages()}>
            {isScheduledView ? (
              <div
                data-testid="webmail-scheduled-list"
                className="p-4 text-xs text-[var(--vellum-ink-secondary)]"
              >
                {(scheduledData?.items ?? []).length === 0
                  ? t("webmail_next.scheduled_empty")
                  : (scheduledData?.items ?? []).map((item) => (
                      <div
                        key={item.id}
                        className="py-2 border-b border-[#EBE8E0]"
                      >
                        {item.send_at}
                      </div>
                    ))}
              </div>
            ) : (
              <WebmailMessageList
                folderName={activeFolder}
                messages={messages}
                activeUid={selectedUid}
                cursorUid={cursorUid}
                onSelectMessage={(uid) => {
                  setCursorUid(uid);
                  setSelectedUid(uid);
                }}
                density={density}
                sort={sortKey}
                order={sortOrder}
                onSortChange={(key, ord) => {
                  setSortKey(key);
                  setSortOrder(ord);
                }}
                onRefresh={() => refetchMessages()}
                isRefreshing={isRefetching || isListLoading}
                onToggleStar={handleToggleStar}
                onDeleteMessage={handleDeleteMessage}
                onToggleRead={handleToggleRead}
                moveFolders={folders.map((folder) => ({
                  name: folder.name,
                  name_enc: folder.name_enc,
                }))}
                onBulkArchive={(uids) =>
                  void handleMoveSpecial(
                    "\\Archive",
                    "Archive",
                    "webmail_next.folder_archive",
                    uids,
                  )
                }
                onBulkTrash={(uids) =>
                  void handleMoveSpecial(
                    "\\Trash",
                    "Trash",
                    "webmail_next.folder_trash",
                    uids,
                  )
                }
                onBulkRead={(uids) => void flagUids(uids, ["\\Seen"], [])}
                onBulkUnread={(uids) => void flagUids(uids, [], ["\\Seen"])}
                onBulkStar={(uids) => void flagUids(uids, ["\\Flagged"], [])}
                onBulkMove={(uids, folderName) => {
                  if (!listValidity) return;
                  void moveMessages(activeFolder, {
                    uids,
                    to: folderName,
                    uid_validity: listValidity,
                  }).then(() => {
                    selectionRef.current?.clear();
                    refetchMessages();
                    showMoved(folderName);
                  });
                }}
                onBindSelection={(api) => {
                  selectionRef.current = api;
                }}
              />
            )}
          </WebmailErrorBoundary>
        </div>

        {/* Pane 3: Reading Pane (Adaptive flex-1; full width on mobile when selected) */}
        <div
          data-testid="webmail-reading-pane-wrapper"
          className={`flex-1 h-full bg-[#FAF9F5] flex flex-col overflow-y-auto ${
            selectedUid ? "flex" : "hidden md:flex"
          }`}
        >
          <WebmailErrorBoundary onReset={() => refetchDetail()}>
            <WebmailReadingPane
              message={messageDetail || null}
              folderEnc={activeFolder}
              isLoading={isDetailLoading}
              isGlobalRemoteImagesAllowed={isGlobalImagesAllowed}
              isManualRemoteImagesAllowed={isCurrentManualGranted}
              onBack={() => setSelectedUid(null)}
              onReply={handleReply}
              onReplyAll={handleReplyAll}
              onForward={handleForward}
              onDelete={handleDeleteMessage}
              onToggleStar={handleToggleStar}
              onLoadRemoteImages={(uid) => setManualAuthorizedUid(uid)}
            />
          </WebmailErrorBoundary>
        </div>
      </div>

      {/* 3. Floating Compose Dock */}
      <WebmailComposeDock
        key={composeKey}
        isOpen={isComposeOpen}
        onClose={() => setIsComposeOpen(false)}
        initialTo={composeTo}
        initialCc={composeCc}
        initialSubject={composeSubject}
        sendNonce={sendNonce}
        minimizeNonce={minimizeNonce}
      />
      <WebmailShortcutHelp
        isOpen={isHelpOpen}
        onClose={() => setIsHelpOpen(false)}
      />
      <WebmailMoveNotice
        message={moveNotice}
        token={moveNoticeToken}
        onDone={clearMoveNotice}
      />
      {pendingPermanentUids && (
        <div className="fixed inset-0 z-[70] flex items-center justify-center bg-black/30">
          <div
            role="dialog"
            data-testid="permanent-delete-dialog"
            className="bg-white border border-[#EBE8E0] rounded-lg p-4 w-80"
          >
            <p className="text-sm text-[#18181B]">
              {t("webmail_next.permanent_delete_body")}
            </p>
            <div className="mt-4 flex justify-end gap-2">
              <button
                type="button"
                data-testid="permanent-delete-cancel"
                className="text-xs px-3 py-1.5"
                onClick={() => setPendingPermanentUids(null)}
              >
                {t("webmail_next.permanent_delete_cancel")}
              </button>
              <button
                type="button"
                data-testid="permanent-delete-confirm"
                className="text-xs px-3 py-1.5 bg-[#18181B] text-white rounded"
                onClick={() => void confirmPermanentDelete()}
              >
                {t("webmail_next.permanent_delete_confirm")}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* 4. Immersive Settings Drawer (Layer 30, z-500) */}
      <WebmailErrorBoundary>
        <WebmailSettingsDrawer
          isOpen={isSettingsOpen}
          onClose={() => setIsSettingsOpen(false)}
          initialTab={settingsTab}
          onVacationUpdated={refetchRules}
        />
      </WebmailErrorBoundary>

      {/* 5. Personal Contacts Modal */}
      <WebmailErrorBoundary>
        <WebmailContactsModal
          isOpen={isContactsOpen}
          onClose={() => setIsContactsOpen(false)}
          onComposeTo={(email) => {
            setComposeTo([email]);
            setIsComposeOpen(true);
          }}
        />
      </WebmailErrorBoundary>
    </div>
  );
}
