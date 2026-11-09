import { useState, useEffect, useMemo, useCallback, useRef } from "react";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
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
import {
  moveToSpecialFolder,
  moveUndoFromResponse,
  type MoveUndo,
} from "./lib/move-mail";
import {
  isTrashMailbox,
  resolveSpecialFolder,
  SCHEDULED_VIEW,
} from "./lib/special-folders";
import { decodeFolderName, getFolderDisplayName } from "./lib/folder-names";
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
  renameMailboxFolder,
  deleteMailboxFolder,
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
import type {
  RowContextMenuActionHandlers,
  ContextMenuFolder,
} from "./components/webmail-row-context-menu";
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
  const [searchDraft, setSearchDraft] = useState("");
  const [searchText, setSearchText] = useState("");
  const [debouncedText, setDebouncedText] = useState("");
  const [debouncedParams, setDebouncedParams] = useState<AdvancedSearchParams>(
    {},
  );
  const [searchPhase, setSearchPhase] = useState<"idle" | "pending" | "slow">(
    "idle",
  );
  const [selectedUid, setSelectedUid] = useState<number | null>(null);
  const [readingFolder, setReadingFolder] = useState(initialFolder);
  const loadedUidsRef = useRef<number[]>([]);
  const [cursorUid, setCursorUid] = useState<number | null>(null);
  const [isHelpOpen, setIsHelpOpen] = useState(false);
  const [moveNotice, setMoveNotice] = useState<string | null>(null);
  const [moveNoticeToken, setMoveNoticeToken] = useState(0);
  const [moveUndo, setMoveUndo] = useState<MoveUndo | null>(null);
  const moveTokenRef = useRef(0);
  const [sendNonce, setSendNonce] = useState(0);
  const [composeCc, setComposeCc] = useState<string[]>([]);
  const [pendingPermanentUids, setPendingPermanentUids] = useState<
    number[] | null
  >(null);
  const selectionRef = useRef<SelectionApi | null>(null);
  const clearMoveNotice = useCallback(() => {
    setMoveNotice(null);
    setMoveUndo(null);
    setPendingSuggestRule(null);
  }, []);

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
  const [composeHtml, setComposeHtml] = useState("");
  const [composeText, setComposeText] = useState("");
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
  const [pendingSuggestRule, setPendingSuggestRule] = useState<{
    sender: string;
    folder: string;
  } | null>(null);
  const [initialRuleDraft, setInitialRuleDraft] = useState<
    { from?: string; folder?: string } | undefined
  >(undefined);

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
  const folderDisplayName = getFolderDisplayName(activeFolder, folders, t);

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

  const committedQuery = useMemo(
    () => buildSearchQuery(searchText, searchParams),
    [searchText, searchParams],
  );
  const activeQuery = useMemo(
    () => buildSearchQuery(debouncedText, debouncedParams),
    [debouncedText, debouncedParams],
  );

  useEffect(() => {
    if (!committedQuery) {
      setDebouncedText(searchText);
      setDebouncedParams(searchParams);
      return;
    }
    const timer = window.setTimeout(() => {
      setDebouncedText(searchText);
      setDebouncedParams(searchParams);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [committedQuery, searchText, searchParams]);

  useEffect(() => {
    if (searchPhase !== "pending") return;
    const timer = window.setTimeout(() => setSearchPhase("slow"), 3000);
    return () => window.clearTimeout(timer);
  }, [searchPhase, searchText, searchParams]);

  const submitSearch = () => {
    const text = searchDraft.trim();
    setSearchText(text);
    if (buildSearchQuery(text, searchParams)) setSearchPhase("pending");
    else setSearchPhase("idle");
  };

  const handleRemoveFilterChip = (key: keyof AdvancedSearchParams) => {
    setSearchParams((prev) => {
      const copy = { ...prev };
      delete copy[key];
      if (key === "hasAttachment") {
        delete copy.filenameExt;
      }
      return copy;
    });
    setSearchPhase("pending");
  };

  const handleClearAllFilters = () => {
    setSearchParams({});
    setSearchDraft("");
    setSearchText("");
    setSearchPhase("idle");
  };

  const handleApplyAdvancedSearch = (params: AdvancedSearchParams) => {
    setSearchParams(params);
    if (params.folder) {
      setActiveFolder(params.folder);
    }
    if (buildSearchQuery(searchText, params)) setSearchPhase("pending");
  };

  const isCrossFolder = Boolean(activeQuery && !debouncedParams.folder);

  const {
    data: messagePages,
    isLoading: isListLoading,
    isFetching: isListFetching,
    isFetchingNextPage,
    hasNextPage,
    fetchNextPage,
    refetch: refetchMessages,
    isRefetching,
    error: listError,
  } = useInfiniteQuery({
    queryKey: [
      "webmail",
      "messages",
      isCrossFolder ? "cross-folder" : activeFolder,
      sortKey,
      sortOrder,
      activeQuery,
    ],
    initialPageParam: 0,
    enabled: !isScheduledView,
    staleTime: 10_000,
    queryFn: async ({ pageParam, signal }) => {
      if (isCrossFolder) {
        const page = await crossFolderSearch(
          activeQuery,
          { limit: 100 },
          signal,
        );
        return {
          messages: page.messages,
          total: page.total,
          uid_validity: undefined as number | undefined,
          next_cursor: 0,
        };
      }
      return listMessages(
        activeFolder,
        {
          limit: 50,
          cursor: pageParam > 0 ? pageParam : undefined,
          sort: sortKey,
          order: sortOrder,
          q: activeQuery,
          thread: true,
        },
        signal,
      );
    },
    getNextPageParam: (last) =>
      last.next_cursor && last.next_cursor > 0 ? last.next_cursor : undefined,
  });

  const messageListData = messagePages?.pages[0];
  const messages = useMemo(() => {
    const seen = new Set<number>();
    const out: MessageSummary[] = [];
    for (const page of messagePages?.pages ?? []) {
      for (const message of page.messages ?? []) {
        if (seen.has(message.uid)) continue;
        seen.add(message.uid);
        out.push(message);
      }
    }
    return out;
  }, [messagePages]);
  const messageTotal = messageListData?.total ?? messages.length;
  const searchCapped =
    isCrossFolder && messageTotal > messages.length && !hasNextPage;

  useEffect(() => {
    if (searchPhase === "idle") return;
    if (committedQuery !== activeQuery) return;
    if (isListFetching) return;
    setSearchPhase("idle");
  }, [searchPhase, committedQuery, activeQuery, isListFetching]);

  const loadMore = useCallback(() => {
    if (!hasNextPage || isFetchingNextPage) return;
    void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const rememberLoadedUids = useCallback((uids: number[]) => {
    loadedUidsRef.current = uids;
  }, []);

  const cancelSearch = () => {
    void queryClient.cancelQueries({ queryKey: ["webmail", "messages"] });
    setSearchPhase("idle");
  };

  const openMessage = (uid: number) => {
    const message = messages.find((item) => item.uid === uid);
    setCursorUid(uid);
    setReadingFolder(message?.folder_enc || activeFolder);
    setSelectedUid(uid);
  };

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
      readingFolder,
      selectedUid,
      authSource,
      policyGeneration,
    ],
    queryFn: async () => {
      if (selectedUid === null) return null;
      const currentGen = policyGeneration;
      const currentSource = authSource;
      const currentUid = selectedUid;
      const folder = readingFolder;
      const msg = await getMessage(folder, currentUid, {
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
      setReadingFolder(messages[0].folder_enc || activeFolder);
      setSelectedUid(messages[0].uid);
    }
  }, [messages, selectedUid, activeFolder]);

  const toggleRail = () => setIsRailCollapsed((prev) => !prev);
  const listValidity = messageListData?.uid_validity;

  const openCompose = (
    to: string[],
    subject: string,
    cc: string[] = [],
    initialHtml = "",
    initialText = "",
  ) => {
    setComposeTo(to);
    setComposeCc(cc);
    setComposeSubject(subject);
    setComposeHtml(initialHtml);
    setComposeText(initialText);
    setComposeKey((key) => key + 1);
    setIsComposeOpen(true);
  };

  const showMoved = (
    folderLabel: string,
    undo: MoveUndo | null = null,
    suggestRule: { sender: string; folder: string } | null = null,
  ) => {
    moveTokenRef.current += 1;
    setMoveUndo(undo);
    setPendingSuggestRule(suggestRule);
    setMoveNotice(t("webmail_next.moved_to", { folder: folderLabel }));
    setMoveNoticeToken(moveTokenRef.current);
  };

  const undoMove = () => {
    const pending = moveUndo;
    if (!pending) return;
    const token = moveTokenRef.current;
    setMoveUndo(null);
    void moveMessages(pending.destinationEnc, {
      uids: pending.destinationUids,
      to: pending.sourceEnc,
    })
      .then(() => {
        if (moveTokenRef.current !== token) return;
        setMoveNotice(null);
        refetchMessages();
      })
      .catch(() => {
        if (moveTokenRef.current !== token) return;
        setMoveUndo(pending);
      });
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
    const moved = await moveToSpecialFolder(
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
    if (!moved) return;
    selectionRef.current?.clear();
    refetchMessages();
    showMoved(t(labelKey), moved.undo);
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

  // --- P1-1: row context menu — reuses the P0 semantic handlers. ---
  const contextRow = (uid: number) =>
    messages.find((m: MessageSummary) => m.uid === uid);

  const contextHandlers: RowContextMenuActionHandlers = {
    onOpen: (uid) => {
      const msg = contextRow(uid);
      if (!msg) return;
      openMessage(uid);
    },
    onReply: (uid) => {
      const msg = contextRow(uid);
      if (msg)
        handleReply({ from: msg.from, subject: msg.subject } as MessageDetail);
    },
    onForward: (uid) => {
      const msg = contextRow(uid);
      if (msg) handleForward({ subject: msg.subject } as MessageDetail);
    },
    onArchive: (uids) =>
      void handleMoveSpecial(
        "\\Archive",
        "Archive",
        "webmail_next.folder_archive",
        uids,
      ),
    onDelete: (uids) =>
      void handleMoveSpecial(
        "\\Trash",
        "Trash",
        "webmail_next.folder_trash",
        uids,
      ),
    onToggleRead: (uid) => void handleToggleRead(uid),
    onMoveTo: (uids, folder) => void handleMoveToFolder(folder, uids),
  };

  /** Move UIDs to an arbitrary folder by enc; same pipeline as bulk move. */
  const handleMoveToFolder = async (
    folder: ContextMenuFolder | { name: string; name_enc: string },
    uids?: number[],
  ) => {
    const targets = targetUids(uids);
    if (targets.length === 0 || isScheduledView || !listValidity) return;
    const source = activeFolder;
    if (source === folder.name_enc) return;
    if (
      isTrashMailbox(folders, source) &&
      isTrashMailbox(folders, folder.name_enc)
    )
      return;
    try {
      const response = await moveMessages(source, {
        uids: targets,
        to: folder.name,
        uid_validity: listValidity,
      });
      const undo = moveUndoFromResponse(
        source,
        folder.name_enc,
        targets,
        response,
      );
      selectionRef.current?.clear();
      refetchMessages();
      if (selectedUid !== null && targets.includes(selectedUid)) {
        setSelectedUid(null);
      }
      const targetMsg = messages.find((m: MessageSummary) =>
        targets.includes(m.uid),
      );
      const sender = targetMsg?.from?.[0]?.email || "";
      const ruleSuggestion = sender
        ? { sender, folder: decodeFolderName(folder.name) }
        : null;
      showMoved(decodeFolderName(folder.name), undo, ruleSuggestion);
    } catch {
      // Failure keeps the list untouched (rows return to their original place).
      refetchMessages();
    }
  };

  // --- P1-2: drop UIDs from the list onto a rail folder. ---
  const handleDropUids = async (folderEnc: string, uids: number[]) => {
    const folder = folders.find((f) => f.name_enc === folderEnc);
    if (!folder || uids.length === 0) return false;
    await handleMoveToFolder(folder, uids);
    return true;
  };

  // --- P1-7: folder management. ---
  const [folderActionError, setFolderActionError] = useState(false);

  const noteFolderActionError = () => {
    setFolderActionError(true);
    window.setTimeout(() => setFolderActionError(false), 4000);
  };

  const handleCreateFolder = async (name: string) => {
    try {
      const created = await createMailboxFolder(name);
      const refreshed = await refetchFolders();
      const list = refreshed.data?.folders ?? [];
      return (
        list.find((f) => f.name_enc === created.name_enc) ??
        list.find((f) => f.name === (created.name ?? name)) ??
        null
      );
    } catch {
      noteFolderActionError();
      return null;
    }
  };

  /** Rename/delete require the folder's uid_validity, which LIST does not
   * carry; a one-message probe retrieves the authoritative generation. */
  const probeFolderValidity = async (nameEnc: string): Promise<number> => {
    const page = await listMessages(nameEnc, { limit: 1 });
    return page.uid_validity ?? 0;
  };

  const handleRenameFolder = async (nameEnc: string, name: string) => {
    try {
      const validity = await probeFolderValidity(nameEnc);
      if (!validity) throw new Error("NO_VALIDITY");
      await renameMailboxFolder(nameEnc, name, validity);
      await refetchFolders();
      return true;
    } catch {
      noteFolderActionError();
      return false;
    }
  };

  const handleDeleteFolder = async (nameEnc: string) => {
    try {
      const validity = await probeFolderValidity(nameEnc);
      if (!validity) throw new Error("NO_VALIDITY");
      await deleteMailboxFolder(nameEnc, validity);
      await refetchFolders();
      if (activeFolder === nameEnc) setActiveFolder(inboxEnc);
      return true;
    } catch {
      noteFolderActionError();
      return false;
    }
  };

  /** Delete confirmation: states the server contract — the folder moves to a
   * recoverable namespace and must be empty (FOLDER_NOT_EMPTY otherwise). */
  const renderFolderDeleteConfirm = ({
    open,
    folderName,
    onCancel,
    onConfirm,
  }: {
    open: boolean;
    folderName: string;
    onCancel: () => void;
    onConfirm: () => void;
  }) => {
    if (!open) return null;
    return (
      <div className="fixed inset-0 z-[70] flex items-center justify-center bg-black/30">
        <div
          role="dialog"
          aria-modal="true"
          data-testid="folder-delete-dialog"
          className="bg-[var(--vellum-surface,var(--vellum-surface))] border border-[var(--vellum-border,var(--vellum-border))] rounded-lg p-4 w-80"
        >
          <p className="text-sm text-[var(--vellum-ink)]">
            {t("webmail_next.folder_delete_body", {
              folder: decodeFolderName(folderName),
            })}
          </p>
          <p className="mt-2 text-xs text-[var(--vellum-ink-secondary)]">
            {t("webmail_next.folder_delete_note")}
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <button
              type="button"
              data-testid="folder-delete-cancel"
              className="text-xs px-3 py-1.5 rounded border border-[var(--vellum-border,var(--vellum-border))]"
              onClick={onCancel}
            >
              {t("webmail_next.permanent_delete_cancel")}
            </button>
            <button
              type="button"
              data-testid="folder-delete-confirm"
              className="text-xs px-3 py-1.5 bg-[var(--vellum-danger-strong)] text-white rounded"
              onClick={onConfirm}
            >
              {t("webmail_next.delete_folder")}
            </button>
          </div>
        </div>
      </div>
    );
  };

  const replySubject = (subject: string) =>
    subject.toLowerCase().startsWith("re:") ? subject : `Re: ${subject}`;

  const buildReplyQuote = (msg: MessageDetail) => {
    const from = msg.from?.[0]?.name
      ? `${msg.from[0].name} <${msg.from[0].email}>`
      : msg.from?.[0]?.email || "";
    const dateStr = msg.date ? new Date(msg.date).toLocaleString() : "";
    const quoteHeader = `On ${dateStr}, ${from} wrote:`;
    const bodyHtml =
      msg.html || (msg.text ? `<p>${msg.text.replace(/\n/g, "<br>")}</p>` : "");
    const quotedHtml = `<p><br></p><div class="urmail-quoted-message"><p>${quoteHeader}</p><blockquote style="border-left: 2px solid #ccc; padding-left: 8px; margin-left: 8px;">${bodyHtml}</blockquote></div>`;
    const quotedText = msg.text
      ? `\n\n---\n${from}\n\n${msg.text.replace(/^/gm, "> ")}`
      : "";
    return { quotedHtml, quotedText };
  };

  const handleReply = (msg: MessageDetail) => {
    const sender = msg.from?.[0]?.email;
    const { quotedHtml, quotedText } = buildReplyQuote(msg);
    openCompose(
      sender ? [sender] : [],
      replySubject(msg.subject),
      [],
      quotedHtml,
      quotedText,
    );
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
    const { quotedHtml, quotedText } = buildReplyQuote(msg);
    openCompose(to, replySubject(msg.subject), cc, quotedHtml, quotedText);
  };

  const handleForward = (msg: MessageDetail) => {
    const subject = msg.subject.toLowerCase().startsWith("fwd:")
      ? msg.subject
      : `Fwd: ${msg.subject}`;
    const { quotedHtml, quotedText } = buildReplyQuote(msg);
    openCompose([], subject, [], quotedHtml, quotedText);
  };

  const loadedUids = () =>
    loadedUidsRef.current.length > 0
      ? loadedUidsRef.current
      : messages.map((message) => message.uid);

  const moveCursor = (delta: number) => {
    const uids = loadedUids();
    if (uids.length === 0) return;
    const current = cursorUid ?? uids[0];
    const index = Math.max(0, uids.indexOf(current));
    const next = uids[Math.min(uids.length - 1, Math.max(0, index + delta))];
    setCursorUid(next);
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
        const uid = cursorUid ?? loadedUids()[0];
        if (uid != null) openMessage(uid);
      } else if (action === "toggle-check") {
        const uid = cursorUid ?? loadedUids()[0];
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
      else if (action === "close-compose") setIsComposeOpen(false);
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

  // Members of the selected message's thread that are present in the
  // currently loaded list pages (oldest → newest). Used by the reading
  // pane stack; count may be < server thread_count until PR-6 lands.
  const threadMembers = useMemo(() => {
    if (selectedUid === null) return [] as MessageSummary[];
    const selected = messages.find((m) => m.uid === selectedUid);
    if (!selected?.thread_id) return selected ? [selected] : [];
    const siblings = messages.filter((m) => m.thread_id === selected.thread_id);
    return siblings.sort(
      (a, b) =>
        new Date(a.date || 0).getTime() - new Date(b.date || 0).getTime(),
    );
  }, [messages, selectedUid]);
  const threadMemberUids = useMemo(
    () => threadMembers.map((m) => m.uid),
    [threadMembers],
  );

  return (
    <div className="webmail-vellum-root min-h-screen h-screen flex flex-col bg-[var(--vellum-ground)] text-[var(--vellum-ink)] overflow-hidden">
      {/* select-none stays on navigation, list rows and controls (they opt in
          individually); the reading-pane body must stay text-selectable for
          copy, so the workbench root must not disable selection globally. */}
      {/* 1. Global Header */}
      <WebmailHeader
        onToggleRail={toggleRail}
        onRefresh={() => {
          refetchFolders();
          refetchMessages();
          refetchRules();
        }}
        searchQuery={searchDraft}
        onSearchChange={setSearchDraft}
        onSubmitSearch={submitSearch}
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
          className="bg-[var(--vellum-warning-fill)] border-b border-[var(--vellum-warning-border)] text-[var(--vellum-warning-text)] px-4 py-2 text-xs flex items-center justify-between z-20 shrink-0"
        >
          <div
            className="flex items-center gap-2 cursor-pointer hover:underline"
            onClick={() => {
              setSettingsTab("vacation");
              setIsSettingsOpen(true);
            }}
          >
            <Palmtree className="w-4 h-4 text-[var(--vellum-warning-strong)] shrink-0" />
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
            className="text-xs font-semibold text-[var(--vellum-warning-text-strong)] hover:text-[#78350F] underline ml-4 shrink-0"
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
            onDropUids={handleDropUids}
            onCreateFolder={handleCreateFolder}
            onRenameFolder={handleRenameFolder}
            onDeleteFolder={handleDeleteFolder}
            deleteConfirmSlot={renderFolderDeleteConfirm}
          />
        </div>

        {/* Pane 2: Message List (380px on desktop; full width on mobile/tablet when browsing) */}
        <div
          data-testid="webmail-message-list-pane"
          className={`h-full border-r border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex flex-col shrink-0 transition-all ${
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

          {searchPhase !== "idle" && (
            <div
              data-testid="webmail-search-status"
              role="status"
              className="px-3 py-1.5 text-xs border-b border-[var(--vellum-border)] bg-[var(--vellum-surface-muted)] text-[var(--vellum-ink)] flex items-center justify-between"
            >
              <span>
                {searchPhase === "slow"
                  ? t("webmail_next.still_searching")
                  : t("webmail_next.searching")}
              </span>
              {searchPhase === "slow" && (
                <button
                  type="button"
                  data-testid="webmail-search-cancel"
                  className="underline"
                  onClick={cancelSearch}
                >
                  {t("webmail_next.cancel_search")}
                </button>
              )}
            </div>
          )}
          {searchCapped && (
            <div
              data-testid="webmail-search-capped"
              className="px-3 py-1 text-xs text-[var(--vellum-ink-secondary)]"
            >
              {t("webmail_next.search_capped", { count: messages.length })}
            </div>
          )}

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
                        className="py-2 border-b border-[var(--vellum-border)]"
                      >
                        {item.send_at}
                      </div>
                    ))}
              </div>
            ) : (
              <WebmailMessageList
                folderName={folderDisplayName}
                messages={messages}
                activeUid={selectedUid}
                cursorUid={cursorUid}
                onSelectMessage={(uid) => openMessage(uid)}
                totalCount={messageTotal}
                isLoadingMore={isFetchingNextPage}
                isInitialLoading={isListLoading}
                loadError={listError instanceof Error ? listError : null}
                onNearEnd={loadMore}
                onLoadedUids={rememberLoadedUids}
                density={density}
                sort={sortKey}
                order={sortOrder}
                onSortChange={(key, ord) => {
                  setSortKey(key);
                  setSortOrder(ord);
                }}
                onRefresh={() => refetchMessages()}
                isRefreshing={
                  (isRefetching && !isFetchingNextPage) || isListLoading
                }
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
                  const destination = folders.find(
                    (folder) => folder.name === folderName,
                  );
                  void moveMessages(activeFolder, {
                    uids,
                    to: folderName,
                    uid_validity: listValidity,
                  }).then((response) => {
                    selectionRef.current?.clear();
                    refetchMessages();
                    const targetMsg = messages.find((m: MessageSummary) =>
                      uids.includes(m.uid),
                    );
                    const sender = targetMsg?.from?.[0]?.email || "";
                    const ruleSuggestion = sender
                      ? { sender, folder: decodeFolderName(folderName) }
                      : null;
                    showMoved(
                      decodeFolderName(folderName),
                      moveUndoFromResponse(
                        activeFolder,
                        destination?.name_enc ?? "",
                        uids,
                        response,
                      ),
                      ruleSuggestion,
                    );
                  });
                }}
                onBindSelection={(api) => {
                  selectionRef.current = api;
                }}
                contextFolders={folders.map((folder) => ({
                  name: folder.name,
                  name_enc: folder.name_enc,
                }))}
                contextHandlers={contextHandlers}
              />
            )}
          </WebmailErrorBoundary>
        </div>

        {/* Pane 3: Reading Pane (Adaptive flex-1; full width on mobile when selected) */}
        <div
          data-testid="webmail-reading-pane-wrapper"
          className={`flex-1 h-full bg-[var(--vellum-ground)] flex flex-col overflow-y-auto ${
            selectedUid ? "flex" : "hidden md:flex"
          }`}
        >
          <WebmailErrorBoundary onReset={() => refetchDetail()}>
            <WebmailReadingPane
              message={messageDetail || null}
              folderEnc={readingFolder}
              isLoading={isDetailLoading}
              isGlobalRemoteImagesAllowed={isGlobalImagesAllowed}
              isManualRemoteImagesAllowed={isCurrentManualGranted}
              threadMembers={threadMembers}
              onBack={() => setSelectedUid(null)}
              onReply={handleReply}
              onReplyAll={handleReplyAll}
              onForward={handleForward}
              onDelete={handleDeleteMessage}
              onToggleStar={handleToggleStar}
              onLoadRemoteImages={(uid) => setManualAuthorizedUid(uid)}
              onExpandThreadUid={(uid) => openMessage(uid)}
              onOpenAISettings={() => {
                setSettingsTab("ai");
                setIsSettingsOpen(true);
              }}
              onMarkThreadRead={() => {
                if (threadMemberUids.length === 0) return;
                void (async () => {
                  await flagMessages(activeFolder, {
                    uids: threadMemberUids,
                    add: ["\\Seen"],
                    remove: [],
                  });
                  refetchMessages();
                  refetchDetail();
                })();
              }}
              onArchiveThread={() =>
                void handleMoveSpecial(
                  "\\Archive",
                  "Archive",
                  "webmail_next.folder_archive",
                  threadMemberUids,
                )
              }
              onDeleteThread={() =>
                void handleMoveSpecial(
                  "\\Trash",
                  "Trash",
                  "webmail_next.folder_trash",
                  threadMemberUids,
                )
              }
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
        initialHtml={composeHtml}
        initialText={composeText}
        sendNonce={sendNonce}
        onOpenAISettings={() => {
          setSettingsTab("ai");
          setIsSettingsOpen(true);
        }}
      />
      <WebmailShortcutHelp
        isOpen={isHelpOpen}
        onClose={() => setIsHelpOpen(false)}
      />
      <WebmailMoveNotice
        message={moveNotice}
        token={moveNoticeToken}
        onDone={clearMoveNotice}
        onUndo={moveUndo ? undoMove : undefined}
        suggestRule={pendingSuggestRule}
        onSuggestRule={() => {
          if (pendingSuggestRule) {
            setInitialRuleDraft({
              from: pendingSuggestRule.sender,
              folder: pendingSuggestRule.folder,
            });
            setSettingsTab("rules");
            setIsSettingsOpen(true);
            clearMoveNotice();
          }
        }}
      />
      {folderActionError && (
        <div
          role="alert"
          data-testid="folder-action-error"
          className="fixed bottom-16 left-1/2 -translate-x-1/2 z-50 px-4 py-2 rounded-lg bg-[var(--vellum-danger-strong)] text-white text-xs shadow-lg"
        >
          {t("webmail_next.folder_action_failed")}
        </div>
      )}
      {pendingPermanentUids && (
        <div className="fixed inset-0 z-[70] flex items-center justify-center bg-black/30">
          <div
            role="dialog"
            data-testid="permanent-delete-dialog"
            className="bg-white border border-[var(--vellum-border)] rounded-lg p-4 w-80"
          >
            <p className="text-sm text-[var(--vellum-ink)]">
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
                className="text-xs px-3 py-1.5 bg-[var(--vellum-ink)] text-white rounded"
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
          onClose={() => {
            setIsSettingsOpen(false);
            setInitialRuleDraft(undefined);
          }}
          initialTab={settingsTab}
          initialRuleDraft={initialRuleDraft}
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
