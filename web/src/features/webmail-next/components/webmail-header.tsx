import { useRef, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { usePublicBrand } from "@/components/brand/public-brand";
import {
  PanelLeft,
  Search,
  SlidersHorizontal,
  RefreshCw,
  LayoutList,
  Settings,
  BookUser,
} from "lucide-react";
import type { DensityMode } from "../types";
import {
  WebmailAdvancedSearchPopover,
  type AdvancedSearchParams,
} from "./search/webmail-advanced-search-popover";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

interface WebmailHeaderProps {
  onToggleRail: () => void;
  onRefresh?: () => void;
  searchQuery: string;
  onSearchChange: (q: string) => void;
  onOpenAdvancedSearch?: () => void;
  density: DensityMode;
  onToggleDensity: () => void;
  isRefreshing?: boolean;
  onOpenSettings?: () => void;
  onOpenContacts?: () => void;
  isAdvancedSearchOpen?: boolean;
  onCloseAdvancedSearch?: () => void;
  folders?: MailboxFolder[];
  searchParams?: AdvancedSearchParams;
  onApplyAdvancedSearch?: (params: AdvancedSearchParams) => void;
}

export function WebmailHeader({
  onToggleRail,
  onRefresh,
  searchQuery,
  onSearchChange,
  onOpenAdvancedSearch,
  density,
  onToggleDensity,
  isRefreshing = false,
  onOpenSettings,
  onOpenContacts,
  isAdvancedSearchOpen = false,
  onCloseAdvancedSearch,
  folders = [],
  searchParams,
  onApplyAdvancedSearch,
}: WebmailHeaderProps) {
  const { t } = useTranslation();
  const brand = usePublicBrand();
  const searchInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (
        e.key === "/" &&
        !["INPUT", "TEXTAREA"].includes((e.target as HTMLElement)?.tagName) &&
        !(e.target as HTMLElement)?.isContentEditable
      ) {
        e.preventDefault();
        searchInputRef.current?.focus();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);

  return (
    <header
      data-testid="webmail-header"
      className="h-12 border-b border-[#EBE8E0] bg-[#FFFFFF] px-4 flex items-center justify-between select-none z-30 shrink-0"
    >
      <div className="flex items-center gap-3 w-56">
        <button
          data-testid="rail-toggle-button"
          type="button"
          onClick={onToggleRail}
          className="p-1.5 rounded text-[#71717A] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
          title="Toggle Navigation"
        >
          <PanelLeft className="w-5 h-5" />
        </button>
        <div className="flex items-center gap-2 font-bold text-base text-[#18181B] tracking-tight">
          <div className="w-2 h-2 rounded-full bg-[var(--brand-primary,#2563eb)]" />
          <span>{brand?.name || t("webmail_next.brand_name")}</span>
        </div>
      </div>

      <div className="flex-1 max-w-xl mx-4 relative">
        <div className="relative w-full">
          <Search className="w-4 h-4 text-[#A1A1AA] absolute left-3 top-2.5 pointer-events-none" />
          <input
            ref={searchInputRef}
            data-testid="webmail-search-input"
            type="text"
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder="Search mail... (Press / to focus)"
            className="w-full h-8 pl-9 pr-16 bg-[#F6F4EE] rounded-full text-xs text-[#18181B] border border-transparent focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none transition-all"
          />
          {onOpenAdvancedSearch && (
            <button
              data-testid="btn-open-advanced-search"
              type="button"
              onClick={onOpenAdvancedSearch}
              className="absolute right-2 top-1.5 p-1 text-[#71717A] hover:text-[#18181B] rounded"
              title="Advanced Filter"
            >
              <SlidersHorizontal className="w-3.5 h-3.5" />
            </button>
          )}

          {/* Advanced Search Popover */}
          {isAdvancedSearchOpen &&
            onCloseAdvancedSearch &&
            onApplyAdvancedSearch && (
              <WebmailAdvancedSearchPopover
                isOpen={isAdvancedSearchOpen}
                onClose={onCloseAdvancedSearch}
                folders={folders}
                initialParams={searchParams}
                onApplySearch={onApplyAdvancedSearch}
              />
            )}
        </div>
      </div>

      <div className="flex items-center gap-2">
        {onRefresh && (
          <button
            data-testid="webmail-refresh-btn"
            type="button"
            onClick={onRefresh}
            disabled={isRefreshing}
            className={`p-1.5 rounded text-[#71717A] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors ${
              isRefreshing ? "animate-spin" : ""
            }`}
            title="Refresh"
          >
            <RefreshCw className="w-4 h-4" />
          </button>
        )}
        <button
          data-testid="webmail-density-toggle"
          type="button"
          onClick={onToggleDensity}
          className="flex items-center gap-1.5 px-2.5 py-1 text-xs rounded border border-[#EBE8E0] text-[#71717A] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
          title={`Switch density (currently ${density})`}
        >
          <LayoutList className="w-3.5 h-3.5" />
          <span className="capitalize">{density}</span>
        </button>
        {onOpenContacts && (
          <button
            data-testid="webmail-contacts-btn"
            type="button"
            onClick={onOpenContacts}
            className="p-1.5 rounded text-[#71717A] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
            title={t("contacts.title", "Contacts")}
          >
            <BookUser className="w-4 h-4" />
          </button>
        )}
        {onOpenSettings && (
          <button
            data-testid="webmail-settings-btn"
            type="button"
            onClick={onOpenSettings}
            className="p-1.5 rounded text-[#71717A] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
            title={t("webmail_next.settings", "Settings")}
          >
            <Settings className="w-4 h-4" />
          </button>
        )}
      </div>
    </header>
  );
}
