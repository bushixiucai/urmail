import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Sparkles, Check, Loader2, X, AlertTriangle } from "lucide-react";
import {
  fetchMailboxAITarget,
  previewMailboxAI,
  runMailboxAI,
  type MailboxAIChange,
} from "@/features/webmail/mailbox-api";

export interface WebmailSelectionPolishProps {
  selectedText: string;
  position: { top: number; left: number };
  onReplace: (revisedText: string) => void;
  onClose: () => void;
  onOpenAISettings?: () => void;
}

export function WebmailSelectionPolish({
  selectedText,
  position,
  onReplace,
  onClose,
  onOpenAISettings,
}: WebmailSelectionPolishProps) {
  const { t } = useTranslation();
  const [isLoading, setIsLoading] = useState(false);
  const [revisedText, setRevisedText] = useState<string | null>(null);
  const [changes, setChanges] = useState<MailboxAIChange[]>([]);
  const [errorNotice, setErrorNotice] = useState<string | null>(null);
  const [isPopoverOpen, setIsPopoverOpen] = useState(false);
  const [showUnconfigured, setShowUnconfigured] = useState(false);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  const handlePolish = async () => {
    if (!selectedText.trim()) return;
    setIsLoading(true);
    setErrorNotice(null);

    try {
      const target = await fetchMailboxAITarget();
      if (!target.enabled || !target.allowed) {
        setShowUnconfigured(true);
        setIsPopoverOpen(true);
        setIsLoading(false);
        return;
      }

      const input = {
        operation: "polish" as const,
        selection: selectedText,
        configuration_digest: target.configuration_digest || "",
        confirmed: true,
        request_key: crypto.randomUUID(),
      };

      let contentDigest = "";
      try {
        const preview = await previewMailboxAI("assist", input);
        contentDigest = preview.content_digest;
      } catch {
        // Fallback or let runMailboxAI throw
      }

      const response = await runMailboxAI({
        ...input,
        content_digest: contentDigest,
      });

      const revised =
        response.result.revised_text || response.result.text || selectedText;
      setRevisedText(revised);
      setChanges(response.result.changes || []);
      setIsPopoverOpen(true);
    } catch (err: unknown) {
      const apiErr = err as {
        status?: number;
        code?: string;
        error?: { code?: string };
      };
      const isStale =
        apiErr?.status === 409 ||
        apiErr?.code === "AI_CONTENT_CONSENT_STALE" ||
        apiErr?.error?.code === "AI_CONTENT_CONSENT_STALE";

      if (isStale) {
        setErrorNotice(
          t(
            "error.AI_CONTENT_CONSENT_STALE",
            "Content or sources changed. Preview and confirm again.",
          ),
        );
      } else {
        setErrorNotice(t("page.webmail.ai_generation_failed"));
      }
      setIsPopoverOpen(true);
    } finally {
      setIsLoading(false);
    }
  };

  const getChangeTypeLabel = (type: string) => {
    switch (type) {
      case "clarity":
        return t("webmail_next.ai_change_clarity");
      case "grammar":
        return t("webmail_next.ai_change_grammar");
      case "tone":
        return t("webmail_next.ai_change_tone");
      case "structure":
        return t("webmail_next.ai_change_structure");
      default:
        return type;
    }
  };

  const handleApply = () => {
    if (revisedText !== null) {
      onReplace(revisedText);
      onClose();
    }
  };

  return (
    <div
      data-testid="selection-polish-container"
      style={{
        top: `${position.top}px`,
        left: `${position.left}px`,
      }}
      className="fixed z-50 -translate-x-1/2 flex flex-col items-center pointer-events-auto"
    >
      {/* 28px Floating Micro-Capsule */}
      {!isPopoverOpen && (
        <div
          data-testid="selection-polish-capsule"
          className="h-7 px-3 bg-[#18181B] dark:bg-white text-white dark:text-[#18181B] rounded-[14px] shadow-[0_4px_14px_rgba(0,0,0,0.22)] flex items-center gap-2 text-xs select-none animate-in fade-in zoom-in-95 duration-150"
        >
          <button
            type="button"
            data-testid="btn-selection-polish"
            disabled={isLoading}
            onClick={handlePolish}
            className="flex items-center gap-1.5 font-medium hover:text-[#3B82F6] dark:hover:text-[#2563EB] transition-colors disabled:opacity-50"
          >
            {isLoading ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
            ) : (
              <Sparkles className="w-3.5 h-3.5 text-[#D97706]" />
            )}
            <span>{t("webmail_next.ai_selection_polish_btn")}</span>
          </button>
          <span className="text-white/40 dark:text-[#18181B]/40">|</span>
          <button
            type="button"
            data-testid="btn-selection-diff-preview"
            disabled={isLoading}
            onClick={handlePolish}
            className="text-[11px] text-white/80 dark:text-[#18181B]/80 hover:text-white dark:hover:text-[#18181B] transition-colors"
          >
            {t("webmail_next.ai_selection_polish_diff")}
          </button>
        </div>
      )}

      {/* Expanded Diff & Rationale Popover Card */}
      {isPopoverOpen && (
        <div
          data-testid="selection-diff-popover"
          className="w-[360px] p-3 bg-white dark:bg-[#1E1E22] border border-[#EBE8E0] dark:border-[#323238] rounded-lg shadow-xl text-xs space-y-3 animate-in fade-in slide-in-from-top-1"
        >
          {/* Header */}
          <div className="flex items-center justify-between gap-2 border-b border-[#EBE8E0] dark:border-[#323238] pb-2">
            <span className="font-semibold text-xs text-[#18181B] dark:text-[#F4F4F6]">
              {t("webmail_next.ai_diff_view_title")}
            </span>
            <div className="flex items-center gap-1.5">
              {revisedText !== null && (
                <button
                  type="button"
                  data-testid="btn-apply-polish"
                  onClick={handleApply}
                  className="px-2 py-0.5 bg-[#2563EB] text-white rounded text-[11px] font-semibold hover:bg-[#1D4ED8] flex items-center gap-1 transition-colors"
                >
                  <Check className="w-3 h-3" />
                  <span>{t("webmail_next.ai_apply_replace_btn")}</span>
                </button>
              )}
              <button
                type="button"
                onClick={onClose}
                aria-label={t("webmail_next.close")}
                className="p-1 hover:bg-[#EBE8E0] dark:hover:bg-[#323238] rounded text-[#71717A] dark:text-[#A1A1AA]"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>

          {/* Unconfigured Alert */}
          {showUnconfigured && (
            <div className="space-y-2">
              <p className="text-[#18181B] dark:text-[#F4F4F6]">
                {t("webmail_next.ai_unconfigured_prompt")}
              </p>
              {onOpenAISettings && (
                <button
                  type="button"
                  data-testid="btn-go-to-settings"
                  onClick={() => {
                    onClose();
                    onOpenAISettings();
                  }}
                  className="px-3 py-1 bg-[#2563EB] text-white rounded text-xs font-medium hover:bg-[#1D4ED8] transition-colors"
                >
                  {t("webmail_next.ai_go_to_settings")}
                </button>
              )}
            </div>
          )}

          {/* Error Notice */}
          {errorNotice && (
            <div
              role="alert"
              className="p-2 rounded bg-[#FEE2E2] dark:bg-[rgba(239,68,68,0.2)] text-[#991B1B] dark:text-[#F87171] text-xs flex items-center gap-2"
            >
              <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
              <span>{errorNotice}</span>
            </div>
          )}

          {/* Diff View */}
          {revisedText !== null && (
            <div className="p-2 bg-[#FAF9F5] dark:bg-[#141416] border border-[#EBE8E0] dark:border-[#323238] rounded leading-relaxed select-text space-y-1">
              <div>
                <span className="diff-del bg-[#FEE2E2] dark:bg-[rgba(239,68,68,0.2)] text-[#991B1B] dark:text-[#F87171] line-through px-0.5 rounded mr-1">
                  {selectedText}
                </span>
              </div>
              <div>
                <span className="diff-add bg-[#DCFCE7] dark:bg-[rgba(74,222,128,0.2)] text-[#166534] dark:text-[#4ADE80] font-medium px-0.5 rounded">
                  {revisedText}
                </span>
              </div>
            </div>
          )}

          {/* Changes Rationale Block */}
          {changes.length > 0 && (
            <div className="space-y-1">
              <span className="font-semibold text-[11px] text-[#52525B] dark:text-[#A1A1AA]">
                {t("webmail_next.ai_changes_rationale")}
              </span>
              <ul className="list-disc list-inside space-y-0.5 text-[11px] text-[#18181B] dark:text-[#F4F4F6]">
                {changes.map((ch, idx) => (
                  <li key={idx}>
                    <span className="font-medium text-[#71717A] dark:text-[#A1A1AA]">
                      [{getChangeTypeLabel(ch.type)}]{" "}
                    </span>
                    <span>{ch.note}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
