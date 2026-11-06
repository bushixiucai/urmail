import { useTranslation } from "react-i18next";
import {
  Inbox,
  FileText,
  Send,
  Clock,
  AlertOctagon,
  Trash2,
  Archive,
  SquarePen,
  Folder,
  type LucideIcon,
} from "lucide-react";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";
import { resolveSpecialFolder, SCHEDULED_VIEW } from "../lib/special-folders";

interface WebmailNavigationRailProps {
  isCollapsed: boolean;
  activeFolder: string;
  onSelectFolder: (folderEnc: string) => void;
  folders: MailboxFolder[];
  onCompose: () => void;
  inboxUnread?: number;
  quota?: { used_bytes: number; quota_bytes: number } | null;
}

const SYSTEM: Array<{
  flag: string;
  fallback: string;
  labelKey: string;
  icon: LucideIcon;
  testId: string;
}> = [
  {
    flag: "\\Inbox",
    fallback: "INBOX",
    labelKey: "webmail_next.inbox",
    icon: Inbox,
    testId: "inbox",
  },
  {
    flag: "\\Drafts",
    fallback: "Drafts",
    labelKey: "webmail_next.drafts",
    icon: FileText,
    testId: "drafts",
  },
  {
    flag: "\\Sent",
    fallback: "Sent",
    labelKey: "webmail_next.sent",
    icon: Send,
    testId: "sent",
  },
  {
    flag: "\\Junk",
    fallback: "Junk",
    labelKey: "webmail_next.junk",
    icon: AlertOctagon,
    testId: "junk",
  },
  {
    flag: "\\Trash",
    fallback: "Trash",
    labelKey: "webmail_next.trash",
    icon: Trash2,
    testId: "trash",
  },
  {
    flag: "\\Archive",
    fallback: "Archive",
    labelKey: "webmail_next.archive",
    icon: Archive,
    testId: "archive",
  },
];

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : 1;
  return `${size.toFixed(digits)} ${units[unit]}`;
}

export function WebmailNavigationRail({
  isCollapsed,
  activeFolder,
  onSelectFolder,
  folders,
  onCompose,
  inboxUnread = 0,
  quota = null,
}: WebmailNavigationRailProps) {
  const { t } = useTranslation();
  const resolved = SYSTEM.map((item) => ({
    ...item,
    folder: resolveSpecialFolder(folders, item.flag, item.fallback),
  })).filter((item) => item.folder);
  const systemEnc = new Set(
    resolved.map((item) => item.folder?.name_enc.toLowerCase()),
  );
  const custom = folders.filter(
    (folder) => !systemEnc.has(folder.name_enc.toLowerCase()),
  );
  const showQuota =
    quota != null &&
    Number.isFinite(quota.used_bytes) &&
    Number.isFinite(quota.quota_bytes) &&
    quota.quota_bytes > 0;
  const quotaPercent = showQuota
    ? Math.round((quota.used_bytes / quota.quota_bytes) * 100)
    : 0;
  const quotaText = showQuota
    ? `${formatBytes(quota.used_bytes)} / ${formatBytes(quota.quota_bytes)}`
    : "";

  const itemClass = (active: boolean) =>
    `w-full flex items-center gap-3 px-3 py-2 rounded-md text-xs transition-colors ${
      active
        ? "bg-[#EFF6FF] text-[var(--brand-primary,#2563eb)] font-semibold"
        : "text-[#18181B] hover:bg-[#F3F1EC]"
    }`;

  return (
    <aside
      data-testid="webmail-navigation-rail"
      data-collapsed={isCollapsed ? "true" : "false"}
      className={`border-r border-[#EBE8E0] bg-[#FAF9F5] flex flex-col justify-between transition-all duration-200 shrink-0 select-none ${
        isCollapsed ? "w-16 items-center" : "w-56"
      }`}
    >
      <div className="p-3 w-full">
        <button
          data-testid="webmail-compose-button"
          type="button"
          onClick={onCompose}
          className={`w-full flex items-center justify-center gap-2 bg-[var(--brand-primary,#2563eb)] text-white font-medium rounded-lg shadow-sm hover:brightness-105 active:brightness-95 transition-all ${
            isCollapsed ? "h-10 px-0" : "h-10 px-4"
          }`}
          aria-label={t("webmail_next.compose")}
        >
          <SquarePen className="w-4 h-4 shrink-0" />
          {!isCollapsed && (
            <span className="text-sm font-semibold">
              {t("webmail_next.compose")}
            </span>
          )}
        </button>

        <nav className="mt-4 space-y-0.5 w-full">
          {resolved.map((item) => {
            const folder = item.folder;
            if (!folder) return null;
            const Icon = item.icon;
            const isActive = activeFolder === folder.name_enc;
            const unread = item.flag === "\\Inbox" ? inboxUnread : 0;
            return (
              <button
                key={item.testId}
                data-testid={`nav-folder-${item.testId}`}
                type="button"
                onClick={() => onSelectFolder(folder.name_enc)}
                className={itemClass(isActive)}
                title={t(item.labelKey)}
              >
                <Icon className="w-4 h-4 shrink-0" />
                {!isCollapsed && (
                  <div className="flex-1 flex items-center justify-between overflow-hidden">
                    <span className="truncate">{t(item.labelKey)}</span>
                    {unread > 0 && (
                      <span
                        data-testid="inbox-unread-badge"
                        className="px-1.5 py-0.5 rounded-full text-[10px] bg-[#2563eb] text-white font-bold leading-none"
                      >
                        {unread}
                      </span>
                    )}
                  </div>
                )}
              </button>
            );
          })}

          <button
            data-testid="nav-folder-scheduled"
            type="button"
            onClick={() => onSelectFolder(SCHEDULED_VIEW)}
            className={itemClass(activeFolder === SCHEDULED_VIEW)}
            title={t("webmail_next.scheduled")}
          >
            <Clock className="w-4 h-4 shrink-0" />
            {!isCollapsed && (
              <span className="truncate">{t("webmail_next.scheduled")}</span>
            )}
          </button>

          {custom.map((folder) => (
            <button
              key={folder.name_enc}
              data-testid={`nav-folder-${folder.name_enc}`}
              type="button"
              onClick={() => onSelectFolder(folder.name_enc)}
              className={itemClass(activeFolder === folder.name_enc)}
              title={folder.name}
            >
              <Folder className="w-4 h-4 shrink-0" />
              {!isCollapsed && (
                <span className="truncate flex-1 text-left">{folder.name}</span>
              )}
            </button>
          ))}
        </nav>
      </div>

      {showQuota && !isCollapsed && (
        <div
          data-testid="webmail-quota-bar"
          className="p-4 border-t border-[#EBE8E0] bg-[#FFFFFF]/60"
        >
          <div className="flex justify-between items-center text-[11px] text-[var(--vellum-ink-secondary)] mb-1.5 font-medium">
            <span>{t("webmail_next.storage")}</span>
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
          <div className="mt-1 text-[10px] text-[var(--vellum-ink-secondary)] truncate">
            {quotaText}
          </div>
        </div>
      )}
      {showQuota && isCollapsed && (
        <div
          data-testid="webmail-quota-bar-mini"
          className="p-3 border-t border-[#EBE8E0] flex justify-center"
          title={quotaText}
        >
          <div className="w-2.5 h-2.5 rounded-full bg-[var(--brand-primary,#2563eb)]" />
        </div>
      )}
    </aside>
  );
}
