import React from "react";
import { useTranslation } from "react-i18next";
import { X, Paperclip, Calendar, User, Folder, FileText } from "lucide-react";
import type { AdvancedSearchParams } from "./webmail-advanced-search-popover";

interface WebmailSearchFilterChipsProps {
  params: AdvancedSearchParams;
  onRemoveParam: (key: keyof AdvancedSearchParams) => void;
  onClearAll: () => void;
}

export function WebmailSearchFilterChips({
  params,
  onRemoveParam,
  onClearAll,
}: WebmailSearchFilterChipsProps) {
  const { t } = useTranslation();

  const chips: {
    key: keyof AdvancedSearchParams;
    label: string;
    icon?: React.ReactNode;
  }[] = [];

  if (params.from) {
    chips.push({
      key: "from",
      label: `From: ${params.from}`,
      icon: <User className="w-3 h-3 text-[var(--brand-primary)]" />,
    });
  }

  if (params.to) {
    chips.push({
      key: "to",
      label: `To: ${params.to}`,
      icon: <User className="w-3 h-3 text-[var(--brand-primary)]" />,
    });
  }

  if (params.subject) {
    chips.push({
      key: "subject",
      label: `Subject: "${params.subject}"`,
      icon: <FileText className="w-3 h-3 text-[var(--brand-primary)]" />,
    });
  }

  if (params.hasAttachment || params.filenameExt) {
    chips.push({
      key: "hasAttachment",
      label: params.filenameExt
        ? `${t("search.chip_attachment", "Attachment")}: *${params.filenameExt}`
        : t("search.chip_has_attachment", "Has attachment"),
      icon: <Paperclip className="w-3 h-3 text-[var(--brand-primary)]" />,
    });
  }

  if (params.folder) {
    chips.push({
      key: "folder",
      label: `Folder: ${params.folder}`,
      icon: <Folder className="w-3 h-3 text-[var(--brand-primary)]" />,
    });
  }

  if (params.dateRange && params.dateRange !== "all") {
    let dateLabel: string = params.dateRange;
    if (params.dateRange === "7d") dateLabel = "Past 7 days";
    if (params.dateRange === "30d") dateLabel = "Past 30 days";
    if (params.dateRange === "1y") dateLabel = "Past year";
    if (params.dateRange === "custom") {
      dateLabel = `${params.dateFrom || "Start"} to ${params.dateTo || "Now"}`;
    }
    chips.push({
      key: "dateRange",
      label: dateLabel,
      icon: <Calendar className="w-3 h-3 text-[var(--brand-primary)]" />,
    });
  }

  if (chips.length === 0) return null;

  return (
    <div
      data-testid="search-filter-chips-bar"
      className="px-4 py-2 bg-[var(--vellum-surface-muted)]/80 border-b border-[var(--vellum-border)] flex flex-wrap items-center gap-1.5 text-xs z-10 shrink-0 select-none"
    >
      <span className="text-xs text-[var(--vellum-ink-secondary)] font-semibold mr-1">
        {t("search.active_filters", "Filters:")}
      </span>

      {chips.map((chip) => (
        <div
          key={chip.key}
          data-testid={`filter-chip-${chip.key}`}
          className="flex items-center gap-1 px-2 py-0.5 bg-[var(--vellum-surface)] border border-[var(--vellum-brand-border)] text-[var(--vellum-brand-strong)] rounded-full text-xs font-medium shadow-2xs"
        >
          {chip.icon}
          <span className="truncate max-w-[200px]">{chip.label}</span>
          <button
            data-testid={`btn-remove-chip-${chip.key}`}
            type="button"
            onClick={() => onRemoveParam(chip.key)}
            className="p-0.5 rounded-full hover:bg-[var(--vellum-brand-border)] text-[var(--vellum-brand-strong)]"
            title="Remove filter"
          >
            <X className="w-3 h-3" />
          </button>
        </div>
      ))}

      <button
        data-testid="btn-clear-all-chips"
        type="button"
        onClick={onClearAll}
        className="text-xs text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-danger-strong)] underline ml-2 cursor-pointer"
      >
        {t("search.clear_all", "Clear all")}
      </button>
    </div>
  );
}
