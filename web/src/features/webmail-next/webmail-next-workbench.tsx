import React, { useState } from "react";
import { WebmailHeader } from "./components/webmail-header";
import { WebmailNavigationRail } from "./components/webmail-navigation-rail";
import { useWebmailPreferences } from "./use-webmail-preferences";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";
import "./theme-vellum.css";

interface WebmailNextWorkbenchProps {
  initialFolder?: string;
}

export function WebmailNextWorkbench({
  initialFolder = "INBOX",
}: WebmailNextWorkbenchProps) {
  const [isRailCollapsed, setIsRailCollapsed] = useState(false);
  const [activeFolder, setActiveFolder] = useState(initialFolder);
  const [searchQuery, setSearchQuery] = useState("");
  const [selectedUid, setSelectedUid] = useState<number | null>(null);

  const { density, setDensity } = useWebmailPreferences();

  // Mock initial folders for skeleton tests
  const [folders] = useState<MailboxFolder[]>([
    { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
    { name: "Archive", name_enc: "Archive", delimiter: "/" },
    { name: "Drafts", name_enc: "Drafts", delimiter: "/" },
  ]);

  const toggleRail = () => setIsRailCollapsed((prev) => !prev);
  const toggleDensity = () =>
    setDensity(density === "compact" ? "comfortable" : "compact");

  return (
    <div className="webmail-vellum-root min-h-screen h-screen flex flex-col bg-[#FAF9F5] text-[#18181B] overflow-hidden select-none">
      {/* 1. Global Header */}
      <WebmailHeader
        onToggleRail={toggleRail}
        searchQuery={searchQuery}
        onSearchChange={setSearchQuery}
        density={density}
        onToggleDensity={toggleDensity}
      />

      {/* 2. Main Workbench Area: 3-pane responsive layout */}
      <div className="flex-1 flex overflow-hidden relative">
        {/* Pane 1: Navigation Rail (220px or 64px collapsed; hidden on mobile 375px if message selected) */}
        <div className={`hidden md:flex h-full shrink-0 ${selectedUid ? "hidden lg:flex" : ""}`}>
          <WebmailNavigationRail
            isCollapsed={isRailCollapsed}
            activeFolder={activeFolder}
            onSelectFolder={setActiveFolder}
            folders={folders}
            onCompose={() => {}}
          />
        </div>

        {/* Pane 2: Message List (380px on desktop; full width on mobile/tablet when browsing) */}
        <div
          data-testid="webmail-message-list"
          className={`h-full border-r border-[#EBE8E0] bg-[#FFFFFF] flex flex-col shrink-0 transition-all ${
            selectedUid
              ? "hidden md:flex md:w-[340px] lg:w-[380px]"
              : "flex-1 md:w-[340px] lg:w-[380px]"
          }`}
        >
          {/* Skeleton placeholder, will be replaced by full component in Task 4 */}
          <div className="p-3 border-b border-[#EBE8E0] flex items-center justify-between text-xs font-semibold text-[#71717A]">
            <span>{activeFolder}</span>
            <span>0 messages</span>
          </div>
          <div className="flex-1 flex items-center justify-center p-4 text-xs text-[#A1A1AA]">
            List View Area
          </div>
        </div>

        {/* Pane 3: Reading Pane (Adaptive flex-1; full width on mobile when selected) */}
        <div
          data-testid="webmail-reading-pane"
          className={`flex-1 h-full bg-[#FAF9F5] flex flex-col overflow-y-auto ${
            selectedUid ? "flex" : "hidden md:flex"
          }`}
        >
          {/* Skeleton placeholder, will be replaced by full component in Task 5 */}
          <div className="flex-1 flex items-center justify-center p-8 text-xs text-[#A1A1AA]">
            Reading Pane Area
          </div>
        </div>
      </div>
    </div>
  );
}
