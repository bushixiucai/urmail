import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import {
  Sparkles,
  Check,
  Loader2,
  X,
  AlertTriangle,
  RotateCcw,
} from "lucide-react";
import {
  fetchMailboxAITarget,
  previewMailboxAI,
  runMailboxAI,
  type MailboxAIChange,
  type MailboxAIAssistResponse,
} from "@/features/webmail/mailbox-api";

export interface WebmailSelectionPolishProps {
  selectedText: string;
  /**
   * Plain text of the draft the selection belongs to. The assist endpoint
   * rejects polish requests whose body is empty, so the surrounding draft
   * travels as `body` while the selection travels as `draft`.
   */
  draftBody?: string;
  position: { top: number; left: number };
  onReplace: (revisedText: string) => void;
  onClose: () => void;
  onOpenAISettings?: () => void;
}

export function WebmailSelectionPolish({
  selectedText,
  draftBody,
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
  const [isConsentStale, setIsConsentStale] = useState(false);
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
    setIsConsentStale(false);
    setShowUnconfigured(false);
    setRevisedText(null);
    setChanges([]);

    try {
      const target = await fetchMailboxAITarget();
      if (!target.enabled || !target.allowed) {
        setShowUnconfigured(true);
        setIsPopoverOpen(true);
        setIsLoading(false);
        return;
      }

      const requestKey = crypto.randomUUID();
      // The server validates that body is non-empty and refines `draft`; the
      // whole draft gives the model the surrounding context.
      const requestBody = draftBody?.trim() ? draftBody : selectedText;

      const executePolish = async (
        currentTarget: typeof target,
      ): Promise<MailboxAIAssistResponse> => {
        const input = {
          operation: "polish" as const,
          selection: selectedText,
          draft: selectedText,
          body: requestBody,
          configuration_digest: currentTarget.configuration_digest || "",
          confirmed: true,
          request_key: requestKey,
        };

        const preview = await previewMailboxAI("assist", input);
        return await runMailboxAI({
          ...input,
          content_digest: preview.content_digest,
        });
      };

      let response: MailboxAIAssistResponse;
      try {
        response = await executePolish(target);
      } catch (firstErr: unknown) {
        const apiErr = firstErr as {
          status?: number;
          code?: string;
          error?: { code?: string };
        };
        const isStale =
          apiErr?.status === 409 ||
          apiErr?.code === "AI_CONTENT_CONSENT_STALE" ||
          apiErr?.error?.code === "AI_CONTENT_CONSENT_STALE";

        if (isStale) {
          const freshTarget = await fetchMailboxAITarget();
          response = await executePolish(freshTarget);
        } else {
          throw firstErr;
        }
      }

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
      const code = apiErr?.code || apiErr?.error?.code || "";
      const isStale =
        apiErr?.status === 409 ||
        code === "AI_CONTENT_CONSENT_STALE" ||
        code === "AI_CLOUD_CONSENT_STALE";

      if (isStale) {
        setIsConsentStale(true);
      } else if (code === "AI_UNCONFIGURED" || code === "AI_DISABLED") {
        setShowUnconfigured(true);
      } else if (code) {
        // Surface the concrete server reason (e.g. INVALID_PARAMS,
        // AI_SOURCE_CHANGED, AI_PROVIDER_UNAVAILABLE) instead of swallowing it
        // into a generic failure, so the user knows what to act on.
        setErrorNotice(
          t(`error.${code}`, {
            defaultValue: t("page.webmail.ai_generation_failed"),
          }),
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
          className="h-7 px-3 bg-[#18181B] text-white rounded-[14px] shadow-[0_4px_14px_rgba(0,0,0,0.22)] flex items-center gap-2 text-xs select-none animate-in fade-in zoom-in-95 duration-150"
        >
          <button
            type="button"
            data-testid="btn-selection-polish"
            disabled={isLoading}
            onClick={handlePolish}
            className="flex items-center gap-1.5 font-medium hover:text-[#3B82F6] transition-colors disabled:opacity-50"
          >
            {isLoading ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
            ) : (
              <Sparkles className="w-3.5 h-3.5 text-[#D97706]" />
            )}
            <span>{t("webmail_next.ai_selection_polish_btn")}</span>
          </button>
          <span className="text-white/40">|</span>
          <button
            type="button"
            data-testid="btn-selection-diff-preview"
            disabled={isLoading}
            onClick={handlePolish}
            className="text-[12px] text-white/80 hover:text-white transition-colors"
          >
            {t("webmail_next.ai_selection_polish_diff")}
          </button>
        </div>
      )}

      {/* Expanded Diff & Rationale Popover Card */}
      {isPopoverOpen && (
        <div
          data-testid="selection-diff-popover"
          className="w-[360px] p-3 bg-white border border-[#EBE8E0] rounded-lg shadow-xl text-xs space-y-3 animate-in fade-in slide-in-from-top-1"
        >
          {/* Header */}
          <div className="flex items-center justify-between gap-2 border-b border-[#EBE8E0] pb-2">
            <span className="font-semibold text-xs text-[#18181B]">
              {t("webmail_next.ai_diff_view_title")}
            </span>
            <div className="flex items-center gap-1.5">
              {revisedText !== null && (
                <button
                  type="button"
                  data-testid="btn-apply-polish"
                  onClick={handleApply}
                  className="px-2 py-0.5 bg-[#2563EB] text-white rounded text-[12px] font-semibold hover:bg-[#1D4ED8] flex items-center gap-1 transition-colors"
                >
                  <Check className="w-3 h-3" />
                  <span>{t("webmail_next.ai_apply_replace_btn")}</span>
                </button>
              )}
              <button
                type="button"
                onClick={onClose}
                aria-label={t("webmail_next.close")}
                className="p-1 hover:bg-[#EBE8E0] rounded text-[#71717A]"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>

          {/* Unconfigured Alert */}
          {showUnconfigured && (
            <div className="space-y-2">
              <p className="text-[#18181B]">
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

          {/* Consent stale: silent retry already failed, offer an explicit one */}
          {isConsentStale && (
            <div
              role="alert"
              data-testid="selection-polish-consent-stale-card"
              className="p-2.5 rounded-lg border border-[#FDE68A] bg-[#FEF3C7] text-[#92400E] space-y-2"
            >
              <div className="flex items-start gap-2">
                <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-0.5 text-[#D97706]" />
                <div className="flex-1 space-y-1">
                  <p className="font-semibold text-[#B45309]">
                    {t("webmail_next.ai_consent_stale_title")}
                  </p>
                  <p className="leading-relaxed">
                    {t("error.AI_CONTENT_CONSENT_STALE")}
                  </p>
                </div>
              </div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  data-testid="btn-polish-consent-retry"
                  onClick={() => void handlePolish()}
                  disabled={isLoading}
                  className="px-2.5 py-1 bg-[#D97706] hover:bg-[#B45309] text-white rounded font-medium flex items-center gap-1 transition-colors disabled:opacity-50"
                >
                  <RotateCcw className="w-3 h-3" />
                  <span>{t("webmail_next.ai_consent_reauthorize_btn")}</span>
                </button>
              </div>
            </div>
          )}

          {/* Concrete failure reason with a retry action */}
          {errorNotice && !isConsentStale && (
            <div
              role="alert"
              data-testid="selection-polish-error"
              className="p-2 rounded bg-[#FEE2E2] text-[#991B1B] text-xs space-y-2"
            >
              <div className="flex items-start gap-2">
                <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
                <span className="flex-1">{errorNotice}</span>
              </div>
              <button
                type="button"
                data-testid="btn-polish-retry"
                onClick={() => void handlePolish()}
                disabled={isLoading}
                className="px-2.5 py-1 bg-[#DC2626] hover:bg-[#B91C1C] text-white rounded font-medium flex items-center gap-1 transition-colors disabled:opacity-50"
              >
                <RotateCcw className="w-3 h-3" />
                <span>{t("webmail_next.ai_retry")}</span>
              </button>
            </div>
          )}

          {/* Diff View */}
          {revisedText !== null && (
            <div className="p-2 bg-[#FAF9F5] border border-[#EBE8E0] rounded leading-relaxed select-text space-y-1">
              <div>
                <span className="diff-del bg-[#FEE2E2] text-[#991B1B] line-through px-0.5 rounded mr-1">
                  {selectedText}
                </span>
              </div>
              <div>
                <span className="diff-add bg-[#DCFCE7] text-[#166534] font-medium px-0.5 rounded">
                  {revisedText}
                </span>
              </div>
            </div>
          )}

          {/* Changes Rationale Block */}
          {changes.length > 0 && (
            <div className="space-y-1">
              <span className="font-semibold text-[12px] text-[#52525B]">
                {t("webmail_next.ai_changes_rationale")}
              </span>
              <ul className="list-disc list-inside space-y-0.5 text-[12px] text-[#18181B]">
                {changes.map((ch, idx) => (
                  <li key={idx}>
                    <span className="font-medium text-[#71717A]">
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
