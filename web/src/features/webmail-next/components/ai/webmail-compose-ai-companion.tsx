import { useState, useId } from "react";
import { useTranslation } from "react-i18next";
import {
  Sparkles,
  X,
  Info,
  Send,
  Loader2,
  AlertTriangle,
  RotateCcw,
  Check,
} from "lucide-react";
import {
  fetchMailboxAITarget,
  runMailboxAI,
  type MailboxAIAssistResponse,
} from "@/features/webmail/mailbox-api";
import { computeTextDiff } from "../../utils/text-diff";

export interface ComposeAIVersion {
  id: string;
  version: number;
  label: string;
  subject: string;
  body: string;
  assumptions?: string[];
  tone_notes?: string;
  degraded?: boolean;
  cache_hit?: boolean;
  timestamp: number;
}

export interface WebmailComposeAICompanionProps {
  currentSubject: string;
  currentBody: string;
  isReply?: boolean;
  onRollback: (version: ComposeAIVersion) => void;
  onApply: (version: ComposeAIVersion) => void;
  onClose: () => void;
  onOpenAISettings?: () => void;
}

export function WebmailComposeAICompanion({
  currentSubject,
  currentBody,
  isReply = false,
  onRollback,
  onApply,
  onClose,
  onOpenAISettings,
}: WebmailComposeAICompanionProps) {
  const { t } = useTranslation();
  const idPrefix = useId();

  const [versions, setVersions] = useState<ComposeAIVersion[]>(() => [
    {
      id: `${idPrefix}-v1`,
      version: 1,
      label: t("webmail_next.ai_initial_draft"),
      subject: currentSubject,
      body: currentBody,
      assumptions: [],
      timestamp: Date.now(),
    },
  ]);

  const [activeIndex, setActiveIndex] = useState(0);
  const [instruction, setInstruction] = useState("");
  const [isLoading, setIsLoading] = useState(false);
  const [errorNotice, setErrorNotice] = useState<string | null>(null);
  const [showUnconfigured, setShowUnconfigured] = useState(false);

  const activeVersion = versions[activeIndex] || versions[0];
  const previousVersion = activeIndex > 0 ? versions[activeIndex - 1] : null;

  const handleGenerate = async (customInstruction?: string) => {
    const textInstruction = (customInstruction ?? instruction).trim();
    if (!textInstruction && !currentBody) return;

    setErrorNotice(null);
    setIsLoading(true);

    try {
      const target = await fetchMailboxAITarget();
      if (!target.enabled || !target.allowed) {
        setShowUnconfigured(true);
        setIsLoading(false);
        return;
      }

      const response: MailboxAIAssistResponse = await runMailboxAI({
        operation: isReply ? "draft_reply" : "draft_new",
        configuration_digest: target.configuration_digest || "",
        confirmed: true,
        request_key: crypto.randomUUID(),
        subject: activeVersion.subject || currentSubject,
        body: activeVersion.body || currentBody,
        instruction: textInstruction,
      });

      const nextVerNum = versions.length + 1;
      const newVer: ComposeAIVersion = {
        id: `${idPrefix}-v${nextVerNum}`,
        version: nextVerNum,
        label:
          textInstruction ||
          t("webmail_next.ai_version_label", { version: nextVerNum }),
        subject:
          response.result.subject || activeVersion.subject || currentSubject,
        body:
          response.result.body || response.result.text || activeVersion.body,
        assumptions: response.result.assumptions || [],
        tone_notes: response.result.tone_notes,
        degraded: response.degraded,
        cache_hit: response.cache_hit,
        timestamp: Date.now(),
      };

      setVersions((prev) => [...prev, newVer]);
      setActiveIndex(versions.length);
      setInstruction("");
    } catch {
      setErrorNotice(t("page.webmail.ai_generation_failed"));
    } finally {
      setIsLoading(false);
    }
  };

  const handleChipClick = (presetLabel: string) => {
    void handleGenerate(presetLabel);
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    void handleGenerate();
  };

  const handleRollbackClick = (idx: number) => {
    setActiveIndex(idx);
    onRollback(versions[idx]);
  };

  const diffChunks =
    previousVersion && activeVersion
      ? computeTextDiff(previousVersion.body, activeVersion.body)
      : [];

  return (
    <aside
      data-testid="compose-ai-companion"
      aria-label={t("webmail_next.ai_companion_title")}
      className="w-[380px] h-full flex flex-col bg-[#F6F4EE] dark:bg-[#27272C] border-l border-[#EBE8E0] dark:border-[#323238] shrink-0 overflow-hidden select-none"
    >
      {/* Top Header */}
      <div className="h-10 px-4 border-b border-[#EBE8E0] dark:border-[#323238] flex items-center justify-between shrink-0 bg-[#FAF9F5] dark:bg-[#1E1E22]">
        <div className="flex items-center gap-2">
          <Sparkles className="w-4 h-4 text-[#D97706]" />
          <span className="text-xs font-semibold text-[#18181B] dark:text-[#F4F4F6]">
            {t("webmail_next.ai_companion_title")}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-[11px] text-[#71717A] dark:text-[#A1A1AA]">
            {t("webmail_next.ai_history_versions_count", {
              count: versions.length,
            })}
          </span>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("webmail_next.close")}
            className="p-1 hover:bg-[#EBE8E0] dark:hover:bg-[#323238] rounded text-[#71717A] dark:text-[#A1A1AA] hover:text-[#18181B] dark:hover:text-[#F4F4F6] transition-colors"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>

      {/* Main Scrollable Stack */}
      <div className="flex-1 overflow-y-auto p-3 space-y-3">
        {errorNotice && (
          <div
            role="alert"
            data-testid="companion-error-notice"
            className="p-2 rounded bg-[#FEE2E2] dark:bg-[rgba(239,68,68,0.2)] text-[#991B1B] dark:text-[#F87171] text-xs flex items-center gap-2"
          >
            <AlertTriangle className="w-4 h-4 shrink-0" />
            <span className="flex-1">{errorNotice}</span>
          </div>
        )}

        {showUnconfigured && (
          <div
            data-testid="companion-unconfigured-card"
            className="p-3 bg-white dark:bg-[#1E1E22] border border-[#EBE8E0] dark:border-[#323238] rounded-lg space-y-2 text-xs"
          >
            <p className="text-[#18181B] dark:text-[#F4F4F6]">
              {t("webmail_next.ai_unconfigured_prompt")}
            </p>
            {onOpenAISettings && (
              <button
                type="button"
                data-testid="btn-go-to-settings"
                onClick={() => {
                  setShowUnconfigured(false);
                  onOpenAISettings();
                }}
                className="px-3 py-1 bg-[#2563EB] text-white rounded text-xs font-medium hover:bg-[#1D4ED8] transition-colors"
              >
                {t("webmail_next.ai_go_to_settings")}
              </button>
            )}
          </div>
        )}

        {/* Version Stack Cards */}
        {versions.map((ver, idx) => {
          const isActive = idx === activeIndex;
          return (
            <div
              key={ver.id}
              data-testid={`version-card-${ver.id}`}
              className={`p-3 rounded-lg border transition-all ${
                isActive
                  ? "bg-white dark:bg-[#1E1E22] border-[#2563EB] shadow-sm"
                  : "bg-white/60 dark:bg-[#1E1E22]/60 border-[#EBE8E0] dark:border-[#323238]"
              }`}
            >
              {/* Version Card Header */}
              <div className="flex items-center justify-between gap-2 mb-2">
                <span className="text-xs font-semibold text-[#18181B] dark:text-[#F4F4F6] truncate">
                  {`v${ver.version} · ${ver.label}`}
                </span>
                <div className="flex items-center gap-1.5 shrink-0">
                  {ver.cache_hit && (
                    <span className="text-[10px] px-1.5 py-0.2 bg-[#EBE8E0] dark:bg-[#323238] text-[#71717A] dark:text-[#A1A1AA] rounded">
                      {t("webmail_next.ai_cache_hit")}
                    </span>
                  )}
                  {isActive ? (
                    <span className="text-[11px] font-medium text-[#166534] dark:text-[#4ADE80] bg-[#DCFCE7] dark:bg-[rgba(74,222,128,0.2)] px-2 py-0.5 rounded flex items-center gap-1">
                      <Check className="w-3 h-3" />
                      <span>{t("webmail_next.ai_current_version_badge")}</span>
                    </span>
                  ) : (
                    <button
                      type="button"
                      data-testid={`btn-rollback-${ver.id}`}
                      onClick={() => handleRollbackClick(idx)}
                      className="text-[11px] font-medium text-[#2563EB] hover:text-[#1D4ED8] hover:bg-[#2563EB]/10 px-2 py-0.5 rounded flex items-center gap-1 transition-colors"
                    >
                      <RotateCcw className="w-3 h-3" />
                      <span>{t("webmail_next.ai_rollback_btn")}</span>
                    </button>
                  )}
                </div>
              </div>

              {/* Degraded Notice */}
              {ver.degraded && (
                <div className="mb-2 p-1.5 bg-[#FEF3C7] dark:bg-[rgba(245,158,11,0.2)] text-[#92400E] dark:text-[#FCD34D] rounded text-[11px] flex items-center gap-1.5">
                  <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
                  <span>{t("webmail_next.ai_degraded_notice")}</span>
                </div>
              )}

              {/* Diff View if this is active version and has previous version */}
              {isActive && previousVersion && diffChunks.length > 0 ? (
                <div className="mb-2 space-y-1">
                  <div className="text-[11px] font-medium text-[#71717A] dark:text-[#A1A1AA]">
                    {t("webmail_next.ai_diff_heading")}
                  </div>
                  <div
                    data-testid="compose-diff-view"
                    className="p-2 bg-[#FAF9F5] dark:bg-[#141416] border border-[#EBE8E0] dark:border-[#323238] rounded text-xs leading-relaxed select-text font-sans max-h-48 overflow-y-auto whitespace-pre-wrap"
                  >
                    {diffChunks.map((chunk, cIdx) => {
                      if (chunk.type === "del") {
                        return (
                          <span
                            key={cIdx}
                            className="diff-del bg-[#FEE2E2] dark:bg-[rgba(239,68,68,0.2)] text-[#991B1B] dark:text-[#F87171] line-through px-0.5 rounded mr-0.5"
                          >
                            {chunk.value}
                          </span>
                        );
                      }
                      if (chunk.type === "add") {
                        return (
                          <span
                            key={cIdx}
                            className="diff-add bg-[#DCFCE7] dark:bg-[rgba(74,222,128,0.2)] text-[#166534] dark:text-[#4ADE80] font-medium px-0.5 rounded mr-0.5"
                          >
                            {chunk.value}
                          </span>
                        );
                      }
                      return <span key={cIdx}>{chunk.value}</span>;
                    })}
                  </div>
                </div>
              ) : (
                /* Plain body preview */
                ver.body && (
                  <div className="mb-2 p-2 bg-[#FAF9F5] dark:bg-[#141416] border border-[#EBE8E0] dark:border-[#323238] rounded text-xs text-[#18181B] dark:text-[#F4F4F6] max-h-36 overflow-y-auto whitespace-pre-wrap select-text">
                    {ver.body}
                  </div>
                )
              )}

              {/* Assumptions Review Block */}
              <details
                data-testid="assumptions-card"
                className="assumptions-card bg-white dark:bg-[#1E1E22] border border-dashed border-[#EBE8E0] dark:border-[#323238] rounded p-2 text-xs mb-2"
                open
              >
                <summary className="font-semibold text-[11px] text-[#52525B] dark:text-[#A1A1AA] flex items-center gap-1.5 cursor-pointer select-none">
                  <Info className="w-3.5 h-3.5 text-[#52525B] dark:text-[#A1A1AA]" />
                  <span>{t("webmail_next.ai_assumptions_title")}</span>
                </summary>
                {ver.assumptions && ver.assumptions.length > 0 ? (
                  <div className="mt-1.5 space-y-1">
                    <ul className="list-disc list-inside space-y-0.5 text-[11px] text-[#18181B] dark:text-[#F4F4F6]">
                      {ver.assumptions.map((ass, aIdx) => (
                        <li key={aIdx}>{ass}</li>
                      ))}
                    </ul>
                    <p className="text-[10px] text-[#71717A] dark:text-[#A1A1AA]">
                      {t("webmail_next.ai_assumptions_notice")}
                    </p>
                  </div>
                ) : (
                  <p className="mt-1.5 text-[11px] text-[#71717A] dark:text-[#A1A1AA]">
                    {t("webmail_next.ai_no_assumptions")}
                  </p>
                )}
              </details>

              {/* Tone notes pill */}
              {ver.tone_notes && (
                <div className="flex items-center gap-1.5 text-[11px] text-[#52525B] dark:text-[#A1A1AA]">
                  <span>{t("webmail_next.ai_tone_prefix")}</span>
                  <span className="px-1.5 py-0.5 bg-[#EBE8E0] dark:bg-[#323238] text-[#18181B] dark:text-[#F4F4F6] rounded text-[10px] font-medium">
                    {ver.tone_notes}
                  </span>
                </div>
              )}
            </div>
          );
        })}
      </div>

      {/* Footer Interactive Actions */}
      <div className="p-3 border-t border-[#EBE8E0] dark:border-[#323238] bg-[#FAF9F5] dark:bg-[#1E1E22] space-y-2 shrink-0">
        {/* Preset Chips */}
        <div className="flex items-center gap-1.5 flex-wrap">
          <button
            type="button"
            data-testid="chip-formal"
            disabled={isLoading}
            onClick={() => handleChipClick(t("webmail_next.ai_chip_formal"))}
            className="px-2 py-0.5 bg-white dark:bg-[#27272C] hover:bg-[#EBE8E0] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded text-[11px] text-[#18181B] dark:text-[#F4F4F6] transition-colors disabled:opacity-50"
          >
            {t("webmail_next.ai_chip_formal")}
          </button>
          <button
            type="button"
            data-testid="chip-concise"
            disabled={isLoading}
            onClick={() => handleChipClick(t("webmail_next.ai_chip_concise"))}
            className="px-2 py-0.5 bg-white dark:bg-[#27272C] hover:bg-[#EBE8E0] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded text-[11px] text-[#18181B] dark:text-[#F4F4F6] transition-colors disabled:opacity-50"
          >
            {t("webmail_next.ai_chip_concise")}
          </button>
          <button
            type="button"
            data-testid="chip-friendly"
            disabled={isLoading}
            onClick={() => handleChipClick(t("webmail_next.ai_chip_friendly"))}
            className="px-2 py-0.5 bg-white dark:bg-[#27272C] hover:bg-[#EBE8E0] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded text-[11px] text-[#18181B] dark:text-[#F4F4F6] transition-colors disabled:opacity-50"
          >
            {t("webmail_next.ai_chip_friendly")}
          </button>
          <button
            type="button"
            data-testid="chip-translate-en"
            disabled={isLoading}
            onClick={() =>
              handleChipClick(t("webmail_next.ai_chip_translate_en"))
            }
            className="px-2 py-0.5 bg-white dark:bg-[#27272C] hover:bg-[#EBE8E0] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded text-[11px] text-[#18181B] dark:text-[#F4F4F6] transition-colors disabled:opacity-50"
          >
            {t("webmail_next.ai_chip_translate_en")}
          </button>
        </div>

        {/* Natural Language Follow-up Instruction */}
        <form onSubmit={handleSubmit} className="flex items-center gap-1.5">
          <input
            data-testid="compose-ai-instruction-input"
            type="text"
            value={instruction}
            onChange={(e) => setInstruction(e.target.value)}
            placeholder={t("webmail_next.ai_instruction_placeholder")}
            disabled={isLoading}
            className="flex-1 min-w-0 px-2.5 py-1 text-xs bg-white dark:bg-[#27272C] border border-[#EBE8E0] dark:border-[#323238] rounded text-[#18181B] dark:text-[#F4F4F6] outline-none focus:border-[#2563EB] disabled:opacity-50"
          />
          <button
            type="submit"
            data-testid="btn-submit-instruction"
            disabled={isLoading || !instruction.trim()}
            className="p-1.5 bg-[#2563EB] text-white rounded hover:bg-[#1D4ED8] disabled:opacity-50 transition-colors shrink-0"
          >
            {isLoading ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
            ) : (
              <Send className="w-3.5 h-3.5" />
            )}
          </button>
        </form>

        {/* Apply Button */}
        <button
          type="button"
          data-testid="btn-apply-draft"
          onClick={() => onApply(activeVersion)}
          disabled={isLoading}
          className="w-full py-1.5 bg-[#18181B] dark:bg-white text-white dark:text-[#18181B] hover:bg-[#27272C] dark:hover:bg-[#EBE8E0] text-xs font-semibold rounded shadow-sm flex items-center justify-center gap-1.5 transition-colors disabled:opacity-50"
        >
          <Check className="w-3.5 h-3.5" />
          <span>{t("webmail_next.ai_apply_draft_btn")}</span>
        </button>
      </div>
    </aside>
  );
}
