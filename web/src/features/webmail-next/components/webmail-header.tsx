import { useEffect, useRef, useState } from "react";
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
  Check,
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
  onSubmitSearch?: () => void;
  onOpenAdvancedSearch?: () => void;
  density: DensityMode;
  onSetDensity: (mode: DensityMode) => void;
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
  onSubmitSearch,
  onOpenAdvancedSearch,
  density,
  onSetDensity,
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
  const densityRef = useRef<HTMLDivElement>(null);
  const [densityOpen, setDensityOpen] = useState(false);
  const densityLabel: Record<DensityMode, string> = {
    comfortable: t("webmail_next.density_comfortable"),
    compact: t("webmail_next.density_compact"),
    list_only: t("webmail_next.density_list_only"),
  };

  useEffect(() => {
    if (!densityOpen) return;
    const close = (event: MouseEvent) => {
      if (!densityRef.current?.contains(event.target as Node)) {
        setDensityOpen(false);
      }
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [densityOpen]);

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
          className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
          aria-label={t("webmail_next.toggle_navigation")}
          title={t("webmail_next.toggle_navigation")}
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
          <Search className="w-4 h-4 text-[var(--vellum-ink-secondary)] absolute left-3 top-2.5 pointer-events-none" />
          <input
            ref={searchInputRef}
            data-testid="webmail-search-input"
            type="text"
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                onSubmitSearch?.();
              }
            }}
            placeholder={t("webmail.header.search_placeholder")}
            className="w-full h-8 pl-9 pr-16 bg-[#F6F4EE] rounded-full text-xs text-[#18181B] border border-transparent focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none transition-all"
          />
          {onOpenAdvancedSearch && (
            <button
              data-testid="btn-open-advanced-search"
              type="button"
              onClick={onOpenAdvancedSearch}
              className="absolute right-1 top-1 flex h-6 w-6 min-h-6 min-w-6 items-center justify-center text-[var(--vellum-ink-secondary)] hover:text-[#18181B] rounded"
              aria-label={t("webmail_next.advanced_filter")}
              title={t("webmail_next.advanced_filter")}
            >
              <SlidersHorizontal className="w-4 h-4" />
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
            className={`p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors ${
              isRefreshing ? "animate-spin" : ""
            }`}
            aria-label={t("webmail_next.refresh")}
            title={t("webmail_next.refresh")}
          >
            <RefreshCw className="w-4 h-4" />
          </button>
        )}
        <div className="relative" ref={densityRef}>
          <button
            data-testid="webmail-density-toggle"
            type="button"
            aria-haspopup="menu"
            aria-expanded={densityOpen}
            onClick={() => setDensityOpen((open) => !open)}
            className="flex items-center gap-1.5 px-2 py-1 text-xs rounded border border-[#EBE8E0] text-[var(--vellum-ink-secondary)] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
            aria-label={t("webmail_next.density_menu")}
          >
            <LayoutList className="w-3.5 h-3.5" />
            <span>{densityLabel[density]}</span>
          </button>
          {densityOpen && (
            <div
              role="menu"
              data-testid="webmail-density-menu"
              className="absolute right-0 top-8 z-40 min-w-36 bg-white border border-[#EBE8E0] rounded-md shadow-lg py-1"
            >
              {(Object.keys(densityLabel) as DensityMode[]).map((mode) => (
                <button
                  key={mode}
                  type="button"
                  role="menuitemradio"
                  aria-checked={density === mode}
                  data-testid={`webmail-density-${mode}`}
                  className="w-full flex items-center justify-between gap-3 px-3 py-1.5 text-xs text-[#18181B] hover:bg-[#F6F4EE]"
                  onClick={() => {
                    onSetDensity(mode);
                    setDensityOpen(false);
                  }}
                >
                  <span>{densityLabel[mode]}</span>
                  {density === mode && <Check className="w-3.5 h-3.5" />}
                </button>
              ))}
            </div>
          )}
        </div>
        {onOpenContacts && (
          <button
            data-testid="webmail-contacts-btn"
            type="button"
            onClick={onOpenContacts}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
            aria-label={t("contacts.title")}
            title={t("contacts.title")}
          >
            <BookUser className="w-4 h-4" />
          </button>
        )}
        {onOpenSettings && (
          <button
            data-testid="webmail-settings-btn"
            type="button"
            onClick={onOpenSettings}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[#F6F4EE] hover:text-[#18181B] transition-colors"
            aria-label={t("webmail_next.settings")}
            title={t("webmail_next.settings")}
          >
            <Settings className="w-4 h-4" />
          </button>
        )}
      </div>
    </header>
  );
}
