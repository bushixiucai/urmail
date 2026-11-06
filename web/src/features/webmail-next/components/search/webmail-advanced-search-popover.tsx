import React, { useState, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  SlidersHorizontal,
  Search,
  RotateCcw,
  X,
  Paperclip,
  Calendar,
  Folder,
} from "lucide-react";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

export interface AdvancedSearchParams {
  from?: string;
  to?: string;
  subject?: string;
  body?: string;
  hasAttachment?: boolean;
  filenameExt?: string;
  folder?: string;
  dateRange?: "all" | "7d" | "30d" | "1y" | "custom";
  dateFrom?: string;
  dateTo?: string;
}

interface WebmailAdvancedSearchPopoverProps {
  isOpen: boolean;
  onClose: () => void;
  folders: MailboxFolder[];
  onApplySearch: (params: AdvancedSearchParams) => void;
  initialParams?: AdvancedSearchParams;
}

const EMPTY_PARAMS: AdvancedSearchParams = {};

export function WebmailAdvancedSearchPopover({
  isOpen,
  onClose,
  folders,
  onApplySearch,
  initialParams = EMPTY_PARAMS,
}: WebmailAdvancedSearchPopoverProps) {
  const { t } = useTranslation();
  const popoverRef = useRef<HTMLDivElement>(null);

  const [from, setFrom] = useState(initialParams.from || "");
  const [to, setTo] = useState(initialParams.to || "");
  const [subject, setSubject] = useState(initialParams.subject || "");
  const [body, setBody] = useState(initialParams.body || "");
  const [hasAttachment, setHasAttachment] = useState(
    initialParams.hasAttachment || false,
  );
  const [filenameExt, setFilenameExt] = useState(
    initialParams.filenameExt || "",
  );
  const [folder, setFolder] = useState(initialParams.folder || "");
  const [dateRange, setDateRange] = useState<
    "all" | "7d" | "30d" | "1y" | "custom"
  >(initialParams.dateRange || "all");
  const [dateFrom, setDateFrom] = useState(initialParams.dateFrom || "");
  const [dateTo, setDateTo] = useState(initialParams.dateTo || "");

  const prevInitialParamsRef = useRef(initialParams);

  useEffect(() => {
    if (
      initialParams !== EMPTY_PARAMS &&
      initialParams !== prevInitialParamsRef.current
    ) {
      prevInitialParamsRef.current = initialParams;
      setFrom(initialParams.from || "");
      setTo(initialParams.to || "");
      setSubject(initialParams.subject || "");
      setBody(initialParams.body || "");
      setHasAttachment(initialParams.hasAttachment || false);
      setFilenameExt(initialParams.filenameExt || "");
      setFolder(initialParams.folder || "");
      setDateRange(initialParams.dateRange || "all");
      setDateFrom(initialParams.dateFrom || "");
      setDateTo(initialParams.dateTo || "");
    }
  }, [initialParams]);

  // Click outside to close
  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (
        popoverRef.current &&
        !popoverRef.current.contains(e.target as Node)
      ) {
        onClose();
      }
    };
    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
    }
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  const handleReset = () => {
    setFrom("");
    setTo("");
    setSubject("");
    setBody("");
    setHasAttachment(false);
    setFilenameExt("");
    setFolder("");
    setDateRange("all");
    setDateFrom("");
    setDateTo("");
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    onApplySearch({
      from: from.trim() || undefined,
      to: to.trim() || undefined,
      subject: subject.trim() || undefined,
      body: body.trim() || undefined,
      hasAttachment: hasAttachment || undefined,
      filenameExt: filenameExt.trim() || undefined,
      folder: folder || undefined,
      dateRange: dateRange !== "all" ? dateRange : undefined,
      dateFrom: dateRange === "custom" && dateFrom ? dateFrom : undefined,
      dateTo: dateRange === "custom" && dateTo ? dateTo : undefined,
    });
    onClose();
  };

  return (
    <div
      ref={popoverRef}
      data-testid="advanced-search-popover"
      className="absolute right-0 top-full mt-2 w-[420px] bg-[#FFFFFF] border border-[#EBE8E0] rounded-xl shadow-2xl z-40 p-5 space-y-4 select-none text-xs"
    >
      <div className="flex items-center justify-between border-b border-[#EBE8E0] pb-2">
        <div className="flex items-center gap-2 font-bold text-[#18181B] text-xs">
          <SlidersHorizontal className="w-4 h-4 text-[var(--brand-primary,#2563eb)]" />
          <span>
            {t("search.advanced_filter_title", "Advanced Mail Filters")}
          </span>
        </div>
        <button
          type="button"
          onClick={onClose}
          className="p-1 rounded text-[var(--vellum-ink-secondary)] hover:text-[#18181B] hover:bg-[#F6F4EE]"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      <form onSubmit={handleSubmit} className="space-y-3">
        {/* From */}
        <div className="flex items-center gap-2">
          <label className="w-16 text-[var(--vellum-ink-secondary)] font-medium shrink-0">
            {t("search.from", "From:")}
          </label>
          <input
            data-testid="search-input-from"
            type="text"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            placeholder="sender@domain.com"
            className="flex-1 h-8 px-2 rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none"
          />
        </div>

        {/* To */}
        <div className="flex items-center gap-2">
          <label className="w-16 text-[var(--vellum-ink-secondary)] font-medium shrink-0">
            {t("search.to", "To:")}
          </label>
          <input
            data-testid="search-input-to"
            type="text"
            value={to}
            onChange={(e) => setTo(e.target.value)}
            placeholder="recipient@domain.com"
            className="flex-1 h-8 px-2 rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none"
          />
        </div>

        {/* Subject */}
        <div className="flex items-center gap-2">
          <label className="w-16 text-[var(--vellum-ink-secondary)] font-medium shrink-0">
            {t("search.subject", "Subject:")}
          </label>
          <input
            data-testid="search-input-subject"
            type="text"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            placeholder={t("search.subject_placeholder", "Keywords in subject")}
            className="flex-1 h-8 px-2 rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none"
          />
        </div>

        {/* Body content */}
        <div className="flex items-center gap-2">
          <label className="w-16 text-[var(--vellum-ink-secondary)] font-medium shrink-0">
            {t("search.body", "Has words:")}
          </label>
          <input
            data-testid="search-input-body"
            type="text"
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder={t("search.body_placeholder", "Keywords in body")}
            className="flex-1 h-8 px-2 rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none"
          />
        </div>

        {/* Folder scope */}
        <div className="flex items-center gap-2">
          <label className="w-16 text-[var(--vellum-ink-secondary)] font-medium shrink-0 flex items-center gap-1">
            <Folder className="w-3 h-3" />
            <span>{t("search.folder", "Folder:")}</span>
          </label>
          <select
            data-testid="search-select-folder"
            value={folder}
            onChange={(e) => setFolder(e.target.value)}
            className="flex-1 h-8 px-2 rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none"
          >
            <option value="">
              {t("search.all_folders", "All Mailboxes (Cross-Folder)")}
            </option>
            {folders.map((f) => (
              <option key={f.name_enc} value={f.name_enc}>
                {f.name}
              </option>
            ))}
          </select>
        </div>

        {/* Attachments filter */}
        <div className="pt-1 border-t border-[#F3F1EC] space-y-2">
          <div className="flex items-center justify-between">
            <label className="flex items-center gap-2 cursor-pointer font-medium text-[#18181B]">
              <input
                data-testid="search-checkbox-attachments"
                type="checkbox"
                checked={hasAttachment}
                onChange={(e) => setHasAttachment(e.target.checked)}
                className="rounded border-[#D4D4D8] text-[var(--brand-primary,#2563eb)] focus:ring-[var(--brand-primary,#2563eb)]"
              />
              <span className="flex items-center gap-1">
                <Paperclip className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
                {t("search.has_attachments", "Has attachment")}
              </span>
            </label>
            <input
              data-testid="search-input-filename-ext"
              type="text"
              value={filenameExt}
              onChange={(e) => setFilenameExt(e.target.value)}
              placeholder="e.g. .pdf, .xlsx"
              className="w-32 h-7 px-2 text-xs rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:outline-none"
            />
          </div>
        </div>

        {/* Date Range */}
        <div className="pt-1 border-t border-[#F3F1EC] space-y-2">
          <div className="flex items-center gap-2">
            <label className="w-16 text-[var(--vellum-ink-secondary)] font-medium shrink-0 flex items-center gap-1">
              <Calendar className="w-3 h-3" />
              <span>{t("search.date", "Date:")}</span>
            </label>
            <select
              data-testid="search-select-daterange"
              value={dateRange}
              onChange={(e) =>
                setDateRange(
                  e.target.value as "all" | "7d" | "30d" | "1y" | "custom",
                )
              }
              className="flex-1 h-8 px-2 rounded border border-[#D4D4D8] bg-[#F6F4EE] focus:bg-white focus:border-[var(--brand-primary,#2563eb)] focus:outline-none"
            >
              <option value="all">{t("search.date_all", "Any time")}</option>
              <option value="7d">{t("search.date_7d", "Past 7 days")}</option>
              <option value="30d">
                {t("search.date_30d", "Past 30 days")}
              </option>
              <option value="1y">{t("search.date_1y", "Past year")}</option>
              <option value="custom">
                {t("search.date_custom", "Custom range...")}
              </option>
            </select>
          </div>

          {dateRange === "custom" && (
            <div className="grid grid-cols-2 gap-2 pl-18">
              <input
                data-testid="search-input-date-from"
                type="date"
                value={dateFrom}
                onChange={(e) => setDateFrom(e.target.value)}
                className="h-7 px-2 text-xs rounded border border-[#D4D4D8] bg-[#F6F4EE]"
              />
              <input
                data-testid="search-input-date-to"
                type="date"
                value={dateTo}
                onChange={(e) => setDateTo(e.target.value)}
                className="h-7 px-2 text-xs rounded border border-[#D4D4D8] bg-[#F6F4EE]"
              />
            </div>
          )}
        </div>

        {/* Buttons */}
        <div className="pt-3 border-t border-[#EBE8E0] flex items-center justify-between">
          <button
            data-testid="btn-reset-filters"
            type="button"
            onClick={handleReset}
            className="flex items-center gap-1 text-[var(--vellum-ink-secondary)] hover:text-[#18181B] text-xs px-2 py-1 rounded"
          >
            <RotateCcw className="w-3 h-3" />
            <span>{t("common.reset", "Reset")}</span>
          </button>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 text-xs text-[var(--vellum-ink-secondary)] hover:text-[#18181B] rounded border border-[#EBE8E0]"
            >
              {t("common.cancel", "Cancel")}
            </button>
            <button
              data-testid="btn-submit-advanced-search"
              type="submit"
              className="px-4 py-1.5 bg-[var(--brand-primary,#2563eb)] text-white font-semibold text-xs rounded-md shadow-xs hover:brightness-105 flex items-center gap-1.5"
            >
              <Search className="w-3.5 h-3.5" />
              <span>{t("search.submit_search", "Search")}</span>
            </button>
          </div>
        </div>
      </form>
    </div>
  );
}
