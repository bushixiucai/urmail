import React, { useState, useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { WebmailHeader } from "./components/webmail-header";
import { WebmailNavigationRail } from "./components/webmail-navigation-rail";
import { WebmailMessageList } from "./components/webmail-message-list";
import { WebmailReadingPane } from "./components/webmail-reading-pane";
import { WebmailComposeDock } from "./components/webmail-compose-dock";
import { useWebmailPreferences } from "./use-webmail-preferences";
import {
  listFolders,
  listMessages,
  getMessage,
  flagMessages,
  deleteMessages,
  type MessageDetail,
  type MailboxSortKey,
} from "@/features/webmail/mailbox-api";
import "./theme-vellum.css";

interface WebmailNextWorkbenchProps {
  initialFolder?: string;
}

export function WebmailNextWorkbench({
  initialFolder = "INBOX",
}: WebmailNextWorkbenchProps) {
  const queryClient = useQueryClient();
  const [isRailCollapsed, setIsRailCollapsed] = useState(false);
  const [activeFolder, setActiveFolder] = useState(initialFolder);
  const [searchQuery, setSearchQuery] = useState("");
  const [selectedUid, setSelectedUid] = useState<number | null>(null);

  // Sorting state
  const [sortKey, setSortKey] = useState<MailboxSortKey>("date");
  const [sortOrder, setSortOrder] = useState<"asc" | "desc">("desc");

  // Compose dock state
  const [isComposeOpen, setIsComposeOpen] = useState(false);
  const [composeTo, setComposeTo] = useState<string[]>([]);
  const [composeSubject, setComposeSubject] = useState("");

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

  // 2. Fetch message list for active folder
  const {
    data: messageListData,
    isLoading: isListLoading,
    refetch: refetchMessages,
    isRefetching,
  } = useQuery({
    queryKey: ["webmail", "messages", activeFolder, sortKey, sortOrder, searchQuery],
    queryFn: () =>
      listMessages(activeFolder, {
        sort: sortKey,
        order: sortOrder,
        q: searchQuery,
        thread: true,
      }),
    staleTime: 10_000,
  });

  const messages = messageListData?.messages || [];

  // 3. Fetch selected message details
  const { data: messageDetail, isLoading: isDetailLoading, refetch: refetchDetail } =
    useQuery<MessageDetail | null>({
      queryKey: ["webmail", "detail", activeFolder, selectedUid],
      queryFn: () =>
        selectedUid ? getMessage(activeFolder, selectedUid) : Promise.resolve(null),
      enabled: selectedUid !== null,
    });

  // Automatically select the first message on wide screen if none selected
  useEffect(() => {
    if (messages.length > 0 && selectedUid === null && window.innerWidth >= 1024) {
      setSelectedUid(messages[0].uid);
    }
  }, [messages, selectedUid]);

  const toggleRail = () => setIsRailCollapsed((prev) => !prev);
  const toggleDensity = () =>
    setDensity(density === "compact" ? "comfortable" : "compact");

  const handleToggleStar = async (uid: number) => {
    const msg = messages.find((m) => m.uid === uid);
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
    const msg = messages.find((m) => m.uid === uid);
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
        : `Re: ${msg.subject}`
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
        }}
        searchQuery={searchQuery}
        onSearchChange={setSearchQuery}
        density={density}
        onToggleDensity={toggleDensity}
        isRefreshing={isRefetching}
      />

      {/* 2. Main Workbench Area: 3-pane responsive layout */}
      <div className="flex-1 flex overflow-hidden relative">
        {/* Pane 1: Navigation Rail (220px or 64px collapsed; hidden on mobile 375px if message selected) */}
        <div className={`hidden md:flex h-full shrink-0 ${selectedUid ? "hidden lg:flex" : ""}`}>
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
            onLoadRemoteImages={() => refetchDetail()}
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
    </div>
  );
}
