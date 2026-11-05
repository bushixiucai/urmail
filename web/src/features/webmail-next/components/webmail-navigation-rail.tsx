import React from "react";
import { useTranslation } from "react-i18next";
import {
  Inbox,
  Star,
  FileText,
  Send,
  Clock,
  AlertOctagon,
  Trash2,
  Archive,
  SquarePen,
  Folder,
} from "lucide-react";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

interface WebmailNavigationRailProps {
  isCollapsed: boolean;
  activeFolder: string;
  onSelectFolder: (folderEnc: string) => void;
  folders: MailboxFolder[];
  unreadCounts?: Record<string, number>;
  onCompose: () => void;
  quotaPercent?: number;
  quotaText?: string;
}

export function WebmailNavigationRail({
  isCollapsed,
  activeFolder,
  onSelectFolder,
  folders,
  unreadCounts = {},
  onCompose,
  quotaPercent = 25,
  quotaText = "1.2 GB / 5.0 GB",
}: WebmailNavigationRailProps) {
  const { t } = useTranslation();

  const standardNavItems = [
    { key: "INBOX", label: t("webmail_next.inbox"), icon: Inbox, enc: "INBOX" },
    { key: "Starred", label: t("webmail_next.starred"), icon: Star, enc: "Starred" },
    { key: "Drafts", label: t("webmail_next.drafts"), icon: FileText, enc: "Drafts" },
    { key: "Sent", label: t("webmail_next.sent"), icon: Send, enc: "Sent" },
    { key: "Scheduled", label: t("webmail_next.scheduled"), icon: Clock, enc: "Scheduled" },
    { key: "Junk", label: t("webmail_next.junk"), icon: AlertOctagon, enc: "Junk" },
    { key: "Trash", label: t("webmail_next.trash"), icon: Trash2, enc: "Trash" },
    { key: "Archive", label: t("webmail_next.archive"), icon: Archive, enc: "Archive" },
  ];

  return (
    <aside
      data-testid="webmail-navigation-rail"
      data-collapsed={isCollapsed ? "true" : "false"}
      className={`border-r border-[#EBE8E0] bg-[#FAF9F5] flex flex-col justify-between transition-all duration-200 shrink-0 select-none ${
        isCollapsed ? "w-16 items-center" : "w-56"
      }`}
    >
      <div className="p-3 w-full">
        {/* Compose Button */}
        <button
          data-testid="webmail-compose-button"
          type="button"
          onClick={onCompose}
          className={`w-full flex items-center justify-center gap-2 bg-[var(--brand-primary,#2563eb)] text-white font-medium rounded-lg shadow-sm hover:brightness-105 active:brightness-95 transition-all ${
            isCollapsed ? "h-10 px-0" : "h-10 px-4"
          }`}
          title={t("webmail_next.compose")}
        >
          <SquarePen className="w-4 h-4 shrink-0" />
          {!isCollapsed && <span className="text-sm font-semibold">{t("webmail_next.compose")}</span>}
        </button>

        {/* Standard Folder List */}
        <nav className="mt-4 space-y-0.5 w-full">
          {standardNavItems.map((item) => {
            const Icon = item.icon;
            const isActive = activeFolder.toLowerCase() === item.enc.toLowerCase();
            const unread = unreadCounts[item.enc] || 0;

            return (
              <button
                key={item.key}
                data-testid={`nav-folder-${item.enc}`}
                type="button"
                onClick={() => onSelectFolder(item.enc)}
                className={`w-full flex items-center gap-3 px-3 py-2 rounded-md text-xs transition-colors ${
                  isActive
                    ? "bg-[#EFF6FF] text-[var(--brand-primary,#2563eb)] font-semibold"
                    : "text-[#18181B] hover:bg-[#F3F1EC]"
                }`}
                title={item.label}
              >
                <Icon className={`w-4 h-4 shrink-0 ${isActive ? "text-[var(--brand-primary,#2563eb)]" : "text-[#71717A]"}`} />
                {!isCollapsed && (
                  <div className="flex-1 flex items-center justify-between overflow-hidden">
                    <span className="truncate">{item.label}</span>
                    {unread > 0 && (
                      <span className="px-1.5 py-0.5 rounded-full text-[10px] bg-[#2563eb] text-white font-bold leading-none">
                        {unread}
                      </span>
                    )}
                  </div>
                )}
              </button>
            );
          })}

          {/* Custom Folders */}
          {folders.filter(f => !standardNavItems.some(s => s.enc.toLowerCase() === f.name_enc.toLowerCase())).map((cf) => {
            const isActive = activeFolder.toLowerCase() === cf.name_enc.toLowerCase();
            return (
              <button
                key={cf.name_enc}
                data-testid={`nav-folder-${cf.name_enc}`}
                type="button"
                onClick={() => onSelectFolder(cf.name_enc)}
                className={`w-full flex items-center gap-3 px-3 py-2 rounded-md text-xs transition-colors ${
                  isActive
                    ? "bg-[#EFF6FF] text-[var(--brand-primary,#2563eb)] font-semibold"
                    : "text-[#18181B] hover:bg-[#F3F1EC]"
                }`}
                title={cf.name}
              >
                <Folder className="w-4 h-4 shrink-0 text-[#71717A]" />
                {!isCollapsed && <span className="truncate flex-1 text-left">{cf.name}</span>}
              </button>
            );
          })}
        </nav>
      </div>

      {/* Quota Gauge Indicator at bottom */}
      {!isCollapsed ? (
        <div data-testid="webmail-quota-bar" className="p-4 border-t border-[#EBE8E0] bg-[#FFFFFF]/60">
          <div className="flex justify-between items-center text-[11px] text-[#71717A] mb-1.5 font-medium">
            <span>Storage</span>
            <span>{quotaPercent}%</span>
          </div>
          <div className="w-full h-1.5 bg-[#EBE8E0] rounded-full overflow-hidden">
            <div
              className={`h-full transition-all duration-300 ${
                quotaPercent > 95
                  ? "bg-[#DC2626]"
                  : quotaPercent > 80
                  ? "bg-[#D97706]"
                  : "bg-[var(--brand-primary,#2563eb)]"
              }`}
              style={{ width: `${Math.min(100, Math.max(0, quotaPercent))}%` }}
            />
          </div>
          <div className="mt-1 text-[10px] text-[#A1A1AA] truncate">{quotaText}</div>
        </div>
      ) : (
        <div data-testid="webmail-quota-bar-mini" className="p-3 border-t border-[#EBE8E0] flex justify-center">
          <div
            className="w-2.5 h-2.5 rounded-full"
            style={{
              backgroundColor: quotaPercent > 95 ? "#DC2626" : quotaPercent > 80 ? "#D97706" : "var(--brand-primary,#2563eb)",
            }}
            title={quotaText}
          />
        </div>
      )}
    </aside>
  );
}
