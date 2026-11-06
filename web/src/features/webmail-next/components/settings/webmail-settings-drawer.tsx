import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import {
  X,
  KeyRound,
  HardDrive,
  PenTool,
  Filter,
  Palmtree,
  Forward,
  Settings as SettingsIcon,
} from "lucide-react";
import { AccountSecuritySection } from "./account-security-section";
import { StorageQuotaSection } from "./storage-quota-section";
import { IdentitiesSignaturesSection } from "./identities-signatures-section";
import { SieveRulesSection } from "./sieve-rules-section";
import { VacationSection } from "./vacation-section";
import { ForwardingSection } from "./forwarding-section";

export type SettingsTab =
  "account" | "quota" | "signatures" | "rules" | "vacation" | "forwarding";

interface WebmailSettingsDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  initialTab?: SettingsTab;
  onVacationUpdated?: () => void;
}

export function WebmailSettingsDrawer({
  isOpen,
  onClose,
  initialTab = "account",
  onVacationUpdated,
}: WebmailSettingsDrawerProps) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<SettingsTab>(initialTab);

  useEffect(() => {
    setActiveTab(initialTab);
  }, [initialTab]);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && isOpen) {
        onClose();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  const navItems: { key: SettingsTab; label: string; icon: typeof KeyRound }[] =
    [
      {
        key: "account",
        label: t("settings.nav.account", "Security"),
        icon: KeyRound,
      },
      {
        key: "quota",
        label: t("settings.nav.quota", "Storage"),
        icon: HardDrive,
      },
      {
        key: "signatures",
        label: t("settings.nav.signatures", "Identities & Signatures"),
        icon: PenTool,
      },
      {
        key: "rules",
        label: t("settings.nav.rules", "Sieve Filters"),
        icon: Filter,
      },
      {
        key: "vacation",
        label: t("settings.nav.vacation", "Vacation"),
        icon: Palmtree,
      },
      {
        key: "forwarding",
        label: t("settings.nav.forwarding", "Forwarding"),
        icon: Forward,
      },
    ];

  return (
    <div
      data-testid="webmail-settings-drawer"
      className="fixed inset-0 z-[500] flex justify-end bg-black/40 backdrop-blur-xs select-none transition-all duration-300"
    >
      {/* Drawer Panel: Slide in from right (Desktop 760px, Mobile full) */}
      <div className="w-full sm:max-w-3xl h-full bg-[#FFFFFF] shadow-2xl flex flex-col border-l border-[#EBE8E0] overflow-hidden">
        {/* Header */}
        <div className="h-14 px-6 border-b border-[#EBE8E0] bg-[#FAF9F5] flex items-center justify-between shrink-0">
          <div className="flex items-center gap-2.5 font-bold text-base text-[#18181B]">
            <SettingsIcon className="w-5 h-5 text-[var(--brand-primary,#2563eb)]" />
            <span>{t("settings.drawer_title", "Mailbox Settings")}</span>
          </div>
          <button
            data-testid="btn-close-settings-drawer"
            type="button"
            onClick={onClose}
            className="p-1.5 rounded text-[#71717A] hover:bg-[#EBE8E0] hover:text-[#18181B] transition-colors"
            title="Close"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Body: Left Subnav + Right Content Container */}
        <div className="flex-1 flex flex-col sm:flex-row overflow-hidden">
          {/* Subnav Rail */}
          <nav
            data-testid="settings-subnav"
            className="w-full sm:w-52 border-b sm:border-b-0 sm:border-r border-[#EBE8E0] bg-[#FAF9F5]/70 p-3 flex sm:flex-col gap-1 shrink-0 overflow-x-auto sm:overflow-x-visible"
          >
            {navItems.map((item) => {
              const Icon = item.icon;
              const isActive = activeTab === item.key;
              return (
                <button
                  key={item.key}
                  data-testid={`settings-nav-${item.key}`}
                  type="button"
                  onClick={() => setActiveTab(item.key)}
                  className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-md text-xs transition-colors shrink-0 ${
                    isActive
                      ? "bg-[#EFF6FF] text-[var(--brand-primary,#2563eb)] font-semibold shadow-xs"
                      : "text-[#3F3F46] hover:bg-[#EBE8E0]/60 hover:text-[#18181B]"
                  }`}
                >
                  <Icon
                    className={`w-4 h-4 shrink-0 ${
                      isActive
                        ? "text-[var(--brand-primary,#2563eb)]"
                        : "text-[#71717A]"
                    }`}
                  />
                  <span className="truncate">{item.label}</span>
                </button>
              );
            })}
          </nav>

          {/* Active Tab Content Area */}
          <div
            data-testid="settings-tab-content"
            className="flex-1 p-6 overflow-y-auto bg-[#FFFFFF]"
          >
            {activeTab === "account" && <AccountSecuritySection />}
            {activeTab === "quota" && <StorageQuotaSection />}
            {activeTab === "signatures" && <IdentitiesSignaturesSection />}
            {activeTab === "rules" && <SieveRulesSection />}
            {activeTab === "vacation" && (
              <VacationSection onVacationUpdated={onVacationUpdated} />
            )}
            {activeTab === "forwarding" && <ForwardingSection />}
          </div>
        </div>
      </div>
    </div>
  );
}
