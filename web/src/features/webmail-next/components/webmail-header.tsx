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
  MoreHorizontal,
} from "lucide-react";
import type { DensityMode } from "../types";
import { useIsMobileViewport } from "../hooks/use-is-mobile-viewport";
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
  const moreRef = useRef<HTMLDivElement>(null);
  const [moreOpen, setMoreOpen] = useState(false);
  // B2-3：<md 视口密度按钮 icon-only（顶栏高度有限，防「舒适」折行）。
  const mobileViewport = useIsMobileViewport();
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

  useEffect(() => {
    if (!moreOpen) return;
    const close = (event: MouseEvent) => {
      if (!moreRef.current?.contains(event.target as Node)) {
        setMoreOpen(false);
      }
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [moreOpen]);

  return (
    <header
      data-testid="webmail-header"
      className="h-12 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] px-4 flex items-center justify-between select-none z-30 shrink-0"
    >
      <div className="flex items-center gap-3 w-56 max-md:w-40">
        <button
          data-testid="rail-toggle-button"
          type="button"
          onClick={onToggleRail}
          className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-surface-muted)] hover:text-[var(--vellum-ink)] transition-colors"
          aria-label={t("webmail_next.toggle_navigation")}
          title={t("webmail_next.toggle_navigation")}
        >
          <PanelLeft className="w-5 h-5" />
        </button>
        <div className="flex items-center gap-2 font-bold text-base text-[var(--vellum-ink)] tracking-tight min-w-0">
          <div className="w-2 h-2 rounded-full bg-[var(--brand-primary,var(--brand-primary))] shrink-0" />
          <span className="truncate whitespace-nowrap max-md:hidden">
            {brand?.name || t("webmail_next.brand_name")}
          </span>
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
            className="w-full h-8 pl-9 pr-16 bg-[var(--vellum-surface-muted)] rounded-full text-xs text-[var(--vellum-ink)] border border-transparent focus:bg-white focus:border-[var(--brand-primary,var(--brand-primary))] focus:outline-none transition-all"
          />
          {onOpenAdvancedSearch && (
            <button
              data-testid="btn-open-advanced-search"
              type="button"
              onClick={onOpenAdvancedSearch}
              className="absolute right-1 top-1/2 -translate-y-1/2 flex h-11 w-11 items-center justify-center text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] rounded"
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

      <div className="flex items-center gap-2 max-md:gap-1">
        {onRefresh && (
          <button
            data-testid="webmail-refresh-btn"
            type="button"
            onClick={onRefresh}
            disabled={isRefreshing}
            className={`flex items-center justify-center min-w-11 min-h-11 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-surface-muted)] hover:text-[var(--vellum-ink)] transition-colors ${
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
            title={mobileViewport ? densityLabel[density] : undefined}
            className={`flex items-center text-xs rounded border border-[var(--vellum-border)] text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-surface-muted)] hover:text-[var(--vellum-ink)] transition-colors ${
              mobileViewport ? "h-11 w-11 justify-center" : "gap-1.5 px-2 py-1"
            }`}
            aria-label={t("webmail_next.density_menu")}
          >
            <LayoutList className="w-3.5 h-3.5" />
            {!mobileViewport && <span>{densityLabel[density]}</span>}
          </button>
          {densityOpen && (
            <div
              role="menu"
              data-testid="webmail-density-menu"
              className="absolute right-0 top-8 z-40 min-w-36 bg-white border border-[var(--vellum-border)] rounded-md shadow-lg py-1"
            >
              {(Object.keys(densityLabel) as DensityMode[]).map((mode) => (
                <button
                  key={mode}
                  type="button"
                  role="menuitemradio"
                  aria-checked={density === mode}
                  data-testid={`webmail-density-${mode}`}
                  className="w-full flex items-center justify-between gap-3 px-3 py-1.5 text-xs text-[var(--vellum-ink)] hover:bg-[var(--vellum-surface-muted)]"
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
            className="max-md:hidden flex items-center justify-center min-w-11 min-h-11 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-surface-muted)] hover:text-[var(--vellum-ink)] transition-colors"
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
            className="max-md:hidden flex items-center justify-center min-w-11 min-h-11 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-surface-muted)] hover:text-[var(--vellum-ink)] transition-colors"
            aria-label={t("webmail_next.settings")}
            title={t("webmail_next.settings")}
          >
            <Settings className="w-4 h-4" />
          </button>
        )}
        {(onOpenContacts || onOpenSettings) && (
          <div className="relative md:hidden" ref={moreRef}>
            <button
              data-testid="webmail-more-btn"
              type="button"
              aria-haspopup="menu"
              aria-expanded={moreOpen}
              onClick={() => setMoreOpen((open) => !open)}
              className="flex items-center justify-center min-w-11 min-h-11 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-surface-muted)] hover:text-[var(--vellum-ink)] transition-colors"
              aria-label={t("webmail_next.more_actions")}
              title={t("webmail_next.more_actions")}
            >
              <MoreHorizontal className="w-4 h-4" />
            </button>
            {moreOpen && (
              <div
                role="menu"
                data-testid="webmail-more-menu"
                className="absolute right-0 top-11 z-40 min-w-40 bg-white border border-[var(--vellum-border)] rounded-md shadow-lg py-1"
              >
                {onOpenContacts && (
                  <button
                    data-testid="webmail-more-contacts"
                    type="button"
                    role="menuitem"
                    className="w-full flex items-center gap-2 px-3 h-11 text-xs text-[var(--vellum-ink)] hover:bg-[var(--vellum-surface-muted)]"
                    onClick={() => {
                      setMoreOpen(false);
                      onOpenContacts();
                    }}
                  >
                    <BookUser className="w-4 h-4" />
                    {t("contacts.title")}
                  </button>
                )}
                {onOpenSettings && (
                  <button
                    data-testid="webmail-more-settings"
                    type="button"
                    role="menuitem"
                    className="w-full flex items-center gap-2 px-3 h-11 text-xs text-[var(--vellum-ink)] hover:bg-[var(--vellum-surface-muted)]"
                    onClick={() => {
                      setMoreOpen(false);
                      onOpenSettings();
                    }}
                  >
                    <Settings className="w-4 h-4" />
                    {t("webmail_next.settings")}
                  </button>
                )}
              </div>
            )}
          </div>
        )}
      </div>
    </header>
  );
}
