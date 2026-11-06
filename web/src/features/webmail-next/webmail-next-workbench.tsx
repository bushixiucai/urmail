import { useState, useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Palmtree } from "lucide-react";
import { WebmailHeader } from "./components/webmail-header";
import { WebmailNavigationRail } from "./components/webmail-navigation-rail";
import { WebmailMessageList } from "./components/webmail-message-list";
import { WebmailReadingPane } from "./components/webmail-reading-pane";
import { WebmailComposeDock } from "./components/webmail-compose-dock";
import {
  WebmailSettingsDrawer,
  type SettingsTab,
} from "./components/settings/webmail-settings-drawer";
import type { AdvancedSearchParams } from "./components/search/webmail-advanced-search-popover";
import { WebmailSearchFilterChips } from "./components/search/webmail-search-filter-chips";
import { WebmailContactsModal } from "./components/contacts/webmail-contacts-modal";
import { useWebmailPreferences } from "./use-webmail-preferences";
import {
  listFolders,
  listMessages,
  crossFolderSearch,
  getMessage,
  flagMessages,
  deleteMessages,
  getMailboxRules,
  saveMailboxRules,
  type MessageDetail,
  type MessageSummary,
  type MailboxRulesView,
  type MailboxSortKey,
} from "@/features/webmail/mailbox-api";
import "./theme-vellum.css";

interface WebmailNextWorkbenchProps {
  initialFolder?: string;
}

export function WebmailNextWorkbench({
  initialFolder = "INBOX",
}: WebmailNextWorkbenchProps) {
  const { t } = useTranslation();
  const [isRailCollapsed, setIsRailCollapsed] = useState(false);
  const [activeFolder, setActiveFolder] = useState(initialFolder);
  const [searchQuery, setSearchQuery] = useState("");
  const [selectedUid, setSelectedUid] = useState<number | null>(null);

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
  const [loadImagesForUid, setLoadImagesForUid] = useState<number | null>(null);

  // Settings drawer state
  const [isSettingsOpen, setIsSettingsOpen] = useState(false);
  const [settingsTab, setSettingsTab] = useState<SettingsTab>("account");

  const { density, setDensity } = useWebmailPreferences();

  // 1. Fetch folders
  const { data: folderData, refetch: refetchFolders } = useQuery({
    queryKey: ["webmail", "folders"],
    queryFn: () => listFolders(),
    staleTime: 30_000,
  });

  const folders = folderData?.folders || [
    { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
  ];

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
  const compiledQuery = useMemo(() => {
    const parts: string[] = [];
    if (searchQuery.trim()) parts.push(searchQuery.trim());
    if (searchParams.from) parts.push(`from:${searchParams.from}`);
    if (searchParams.to) parts.push(`to:${searchParams.to}`);
    if (searchParams.subject) parts.push(`subject:${searchParams.subject}`);
    if (searchParams.body) parts.push(`body:${searchParams.body}`);
    if (searchParams.hasAttachment) parts.push("has:attachment");
    if (searchParams.filenameExt)
      parts.push(`filename:${searchParams.filenameExt}`);
    return parts.join(" ");
  }, [searchQuery, searchParams]);

  const handleRemoveFilterChip = (key: keyof AdvancedSearchParams) => {
    setSearchParams((prev) => {
      const copy = { ...prev };
      delete copy[key];
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
  } = useQuery<{ messages: MessageSummary[]; total?: number }>({
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
    staleTime: 10_000,
  });

  const messages = useMemo(
    () => messageListData?.messages || [],
    [messageListData?.messages],
  );

  // 3. Fetch selected message details
  const {
    data: messageDetail,
    isLoading: isDetailLoading,
    refetch: refetchDetail,
  } = useQuery<MessageDetail | null>({
    queryKey: [
      "webmail",
      "detail",
      activeFolder,
      selectedUid,
      loadImagesForUid === selectedUid,
    ],
    queryFn: () =>
      selectedUid
        ? getMessage(activeFolder, selectedUid, {
            loadImages: loadImagesForUid === selectedUid,
          })
        : Promise.resolve(null),
    enabled: selectedUid !== null,
  });

  useEffect(() => {
    setLoadImagesForUid(null);
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
  const toggleDensity = () => {
    setDensity(
      density === "compact"
        ? "comfortable"
        : density === "comfortable"
          ? "list_only"
          : "compact",
    );
  };

  const handleToggleStar = async (uid: number) => {
    const msg = messages.find((m: MessageSummary) => m.uid === uid);
    if (!msg) return;
    const isStarred = msg.flags.includes("\\Flagged");
    await flagMessages(activeFolder, {
      uids: [uid],
      add: isStarred ? [] : ["\\Flagged"],
      remove: isStarred ? ["\\Flagged"] : [],
    });
    refetchMessages();
    if (selectedUid === uid) refetchDetail();
  };

  const handleToggleRead = async (uid: number) => {
    const msg = messages.find((m: MessageSummary) => m.uid === uid);
    if (!msg) return;
    const isSeen = msg.flags.includes("\\Seen");
    await flagMessages(activeFolder, {
      uids: [uid],
      add: isSeen ? [] : ["\\Seen"],
      remove: isSeen ? ["\\Seen"] : [],
    });
    refetchMessages();
    if (selectedUid === uid) refetchDetail();
  };

  const handleDeleteMessage = async (uid: number) => {
    await deleteMessages(activeFolder, { uids: [uid] });
    refetchMessages();
    if (selectedUid === uid) setSelectedUid(null);
  };

  const handleReply = (msg: MessageDetail) => {
    const sender = msg.from?.[0]?.email;
    if (sender) setComposeTo([sender]);
    setComposeSubject(
      msg.subject.toLowerCase().startsWith("re:")
        ? msg.subject
        : `Re: ${msg.subject}`,
    );
    setIsComposeOpen(true);
  };

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
        onToggleDensity={toggleDensity}
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
                "休假自动回复当前开启中 ({{from}} 至 {{until}})",
                {
                  from: vacationFromStr || "即日起",
                  until: vacationUntilStr || "长期",
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
            {t("webmail_next.vacation_quick_disable", "点击快速关闭")}
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
            }}
            folders={folders}
            onCompose={() => {
              setComposeTo([]);
              setComposeSubject("");
              setIsComposeOpen(true);
            }}
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

          <WebmailMessageList
            folderName={activeFolder}
            messages={messages}
            activeUid={selectedUid}
            onSelectMessage={(uid) => setSelectedUid(uid)}
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
          />
        </div>

        {/* Pane 3: Reading Pane (Adaptive flex-1; full width on mobile when selected) */}
        <div
          data-testid="webmail-reading-pane-wrapper"
          className={`flex-1 h-full bg-[#FAF9F5] flex flex-col overflow-y-auto ${
            selectedUid ? "flex" : "hidden md:flex"
          }`}
        >
          <WebmailReadingPane
            message={messageDetail || null}
            folderEnc={activeFolder}
            isLoading={isDetailLoading}
            onBack={() => setSelectedUid(null)}
            onReply={handleReply}
            onDelete={handleDeleteMessage}
            onToggleStar={handleToggleStar}
            onLoadRemoteImages={() => setLoadImagesForUid(selectedUid)}
          />
        </div>
      </div>

      {/* 3. Floating Compose Dock */}
      <WebmailComposeDock
        isOpen={isComposeOpen}
        onClose={() => setIsComposeOpen(false)}
        initialTo={composeTo}
        initialSubject={composeSubject}
      />

      {/* 4. Immersive Settings Drawer (Layer 30, z-500) */}
      <WebmailSettingsDrawer
        isOpen={isSettingsOpen}
        onClose={() => setIsSettingsOpen(false)}
        initialTab={settingsTab}
        onVacationUpdated={refetchRules}
      />

      {/* 5. Personal Contacts Modal */}
      <WebmailContactsModal
        isOpen={isContactsOpen}
        onClose={() => setIsContactsOpen(false)}
        onComposeTo={(email) => {
          setComposeTo([email]);
          setIsComposeOpen(true);
        }}
      />
    </div>
  );
}
