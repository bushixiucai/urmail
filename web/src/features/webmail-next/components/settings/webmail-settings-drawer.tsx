import {
  lazy,
  Suspense,
  useState,
  useEffect,
  useLayoutEffect,
  useRef,
} from "react";
import { useTranslation } from "react-i18next";
import {
  X,
  KeyRound,
  HardDrive,
  PenTool,
  Filter,
  Palmtree,
  Forward,
  Shield,
  Settings as SettingsIcon,
} from "lucide-react";
const AccountSecuritySection = lazy(() =>
  import("./account-security-section").then((module) => ({
    default: module.AccountSecuritySection,
  })),
);
const StorageQuotaSection = lazy(() =>
  import("./storage-quota-section").then((module) => ({
    default: module.StorageQuotaSection,
  })),
);
const IdentitiesSignaturesSection = lazy(() =>
  import("./identities-signatures-section").then((module) => ({
    default: module.IdentitiesSignaturesSection,
  })),
);
const SieveRulesSection = lazy(() =>
  import("./sieve-rules-section").then((module) => ({
    default: module.SieveRulesSection,
  })),
);
const VacationSection = lazy(() =>
  import("./vacation-section").then((module) => ({
    default: module.VacationSection,
  })),
);
const ForwardingSection = lazy(() =>
  import("./forwarding-section").then((module) => ({
    default: module.ForwardingSection,
  })),
);
const RemoteImagesSection = lazy(() =>
  import("./remote-images-section").then((module) => ({
    default: module.RemoteImagesSection,
  })),
);

export type SettingsTab =
  | "account"
  | "quota"
  | "signatures"
  | "rules"
  | "vacation"
  | "forwarding"
  | "privacy";

interface WebmailSettingsDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  initialTab?: SettingsTab;
  onVacationUpdated?: () => void;
  onRemoteImagesPreferenceChanged?: (allowAll: boolean) => void;
}

export function WebmailSettingsDrawer({
  isOpen,
  onClose,
  initialTab = "account",
  onVacationUpdated,
  onRemoteImagesPreferenceChanged,
}: WebmailSettingsDrawerProps) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<SettingsTab>(initialTab);

  useEffect(() => {
    setActiveTab(initialTab);
  }, [initialTab]);

  const openerRef = useRef<HTMLElement | null>(null);
  const openerRecordedRef = useRef(false);
  useLayoutEffect(() => {
    if (isOpen) {
      if (!openerRecordedRef.current) {
        const opener = document.activeElement;
        openerRef.current =
          opener instanceof HTMLElement && opener !== document.body
            ? opener
            : null;
        openerRecordedRef.current = true;
      }
      return;
    }
    if (!openerRecordedRef.current) return;
    openerRecordedRef.current = false;
    const opener = openerRef.current;
    openerRef.current = null;
    if (opener && document.contains(opener)) {
      opener.focus();
      return;
    }
    document
      .querySelector<HTMLElement>('[data-testid="webmail-settings-btn"]')
      ?.focus();
  }, [isOpen]);

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
        key: "privacy",
        label: t("settings.nav.privacy", "Privacy & Security"),
        icon: Shield,
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
      className="fixed inset-0 z-[500] flex justify-end bg-black/40 select-none"
    >
      {/* Drawer Panel: Slide in from right (Desktop 760px, Mobile full) */}
      <div className="webmail-settings-panel w-full sm:max-w-3xl h-full bg-[#FFFFFF] flex flex-col border-l border-[#EBE8E0] overflow-hidden">
        {/* Header */}
        <div className="h-14 px-6 border-b border-[#EBE8E0] bg-[#FAF9F5] flex items-center justify-between shrink-0">
          <div className="flex items-center gap-2 font-bold text-base text-[#18181B]">
            <SettingsIcon className="w-5 h-5 text-[var(--brand-primary,#2563eb)]" />
            <span>{t("settings.drawer_title", "Mailbox Settings")}</span>
          </div>
          <button
            data-testid="btn-close-settings-drawer"
            type="button"
            onClick={onClose}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[#EBE8E0] hover:text-[#18181B] transition-colors"
            aria-label={t("webmail_next.close")}
            title={t("webmail_next.close")}
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
                  className={`w-full flex items-center gap-2 px-3 py-2 rounded-md text-xs transition-colors shrink-0 ${
                    isActive
                      ? "bg-[#EFF6FF] text-[var(--brand-primary,#2563eb)] font-semibold shadow-xs"
                      : "text-[#3F3F46] hover:bg-[#EBE8E0]/60 hover:text-[#18181B]"
                  }`}
                >
                  <Icon
                    className={`w-4 h-4 shrink-0 ${
                      isActive
                        ? "text-[var(--brand-primary,#2563eb)]"
                        : "text-[var(--vellum-ink-secondary)]"
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
            <Suspense
              fallback={
                <div
                  data-testid="settings-tab-skeleton"
                  aria-busy="true"
                  className="animate-pulse space-y-3"
                >
                  <div className="h-4 w-1/3 rounded bg-[#EBE8E0]" />
                  <div className="h-24 w-full rounded bg-[#EBE8E0]" />
                </div>
              }
            >
              {activeTab === "account" && <AccountSecuritySection />}
              {activeTab === "privacy" && (
                <RemoteImagesSection
                  onPreferenceChanged={onRemoteImagesPreferenceChanged}
                />
              )}
              {activeTab === "quota" && <StorageQuotaSection />}
              {activeTab === "signatures" && <IdentitiesSignaturesSection />}
              {activeTab === "rules" && <SieveRulesSection />}
              {activeTab === "vacation" && (
                <VacationSection onVacationUpdated={onVacationUpdated} />
              )}
              {activeTab === "forwarding" && <ForwardingSection />}
            </Suspense>
          </div>
        </div>
      </div>
    </div>
  );
}
