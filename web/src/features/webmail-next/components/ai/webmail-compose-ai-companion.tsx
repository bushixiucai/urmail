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
  previewMailboxAI,
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
  const [isConsentStale, setIsConsentStale] = useState(false);
  const [lastInstruction, setLastInstruction] = useState<string | undefined>(
    undefined,
  );

  const activeVersion = versions[activeIndex] || versions[0];
  const previousVersion = activeIndex > 0 ? versions[activeIndex - 1] : null;

  const handleGenerate = async (customInstruction?: string) => {
    const textInstruction = (customInstruction ?? instruction).trim();
    if (!textInstruction && !currentBody) return;

    setLastInstruction(textInstruction);
    setErrorNotice(null);
    setIsConsentStale(false);
    setIsLoading(true);

    try {
      const target = await fetchMailboxAITarget();
      if (!target.enabled || !target.allowed) {
        setShowUnconfigured(true);
        setIsLoading(false);
        return;
      }

      const requestKey = crypto.randomUUID();

      const executeTwoStepGeneration = async (currentTarget: {
        configuration_digest?: string;
      }): Promise<MailboxAIAssistResponse> => {
        const input = {
          operation: (isReply ? "draft_reply" : "draft_new") as
            "draft_reply" | "draft_new",
          configuration_digest: currentTarget.configuration_digest || "",
          confirmed: true,
          request_key: requestKey,
          subject: activeVersion.subject || currentSubject,
          body: activeVersion.body || currentBody,
          instruction: textInstruction,
        };

        // Step 1: preview_only=true to obtain fresh content_digest
        const preview = await previewMailboxAI("assist", input);

        // Step 2: confirmed=true formal run with matching parameters, same request_key, and content_digest
        return await runMailboxAI({
          ...input,
          content_digest: preview.content_digest,
        });
      };

      let response: MailboxAIAssistResponse;
      try {
        response = await executeTwoStepGeneration(target);
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
          // Recovery path: silently re-run preview -> run once before showing error
          const freshTarget = await fetchMailboxAITarget();
          response = await executeTwoStepGeneration(freshTarget);
        } else {
          throw firstErr;
        }
      }

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
        setIsConsentStale(true);
      } else if (
        apiErr?.code === "AI_UNCONFIGURED" ||
        apiErr?.error?.code === "AI_UNCONFIGURED"
      ) {
        setShowUnconfigured(true);
      } else {
        setErrorNotice(t("page.webmail.ai_generation_failed"));
      }
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
      className="w-[380px] h-full flex flex-col bg-[var(--vellum-surface-muted)] border-l border-[var(--vellum-border)] shrink-0 overflow-hidden select-none"
    >
      {/* Top Header */}
      <div className="h-10 px-4 border-b border-[var(--vellum-border)] flex items-center justify-between shrink-0 bg-[var(--vellum-ground)]">
        <div className="flex items-center gap-2">
          <Sparkles className="w-4 h-4 text-[var(--vellum-ai-accent)]" />
          <span className="text-xs font-semibold text-[var(--vellum-ink)]">
            {t("webmail_next.ai_companion_title")}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-[12px] text-[var(--vellum-ink-secondary)]">
            {t("webmail_next.ai_history_versions_count", {
              count: versions.length,
            })}
          </span>
          <button
            type="button"
            onClick={onClose}
            aria-label={t("webmail_next.close")}
            className="p-1 hover:bg-[var(--vellum-border)] rounded text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] transition-colors"
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
            className="p-2 rounded bg-[var(--vellum-danger-fill)] text-[var(--vellum-danger-text)] text-xs flex items-center gap-2"
          >
            <AlertTriangle className="w-4 h-4 shrink-0" />
            <span className="flex-1">{errorNotice}</span>
          </div>
        )}

        {isConsentStale && (
          <div
            role="alert"
            data-testid="companion-consent-stale-card"
            className="p-3 rounded-lg border border-[var(--vellum-warning-border)] bg-[var(--vellum-warning-fill)] text-[var(--vellum-warning-text)] text-xs space-y-2"
          >
            <div className="flex items-start gap-2">
              <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5 text-[var(--vellum-warning-strong)]" />
              <div className="flex-1 space-y-1">
                <p className="font-semibold text-[var(--vellum-warning-text-strong)]">
                  {t(
                    "webmail_next.ai_consent_stale_title",
                    "Draft modified or authorization expired",
                  )}
                </p>
                <p className="leading-relaxed text-[var(--vellum-warning-text)]">
                  {t(
                    "webmail_next.ai_consent_stale_desc",
                    "Draft content or context changed before drafting. Please re-authorize to continue.",
                  )}
                </p>
              </div>
            </div>
            <div className="flex items-center gap-2 pt-1">
              <button
                type="button"
                data-testid="btn-ai-consent-retry"
                onClick={() => handleGenerate(lastInstruction)}
                disabled={isLoading}
                className="px-2.5 py-1 bg-[var(--vellum-ai-accent)] hover:bg-[var(--vellum-ai-accent-strong)] text-white rounded font-medium flex items-center gap-1 transition-colors disabled:opacity-50"
              >
                <RotateCcw className="w-3 h-3" />
                <span>
                  {t(
                    "webmail_next.ai_consent_reauthorize_btn",
                    "Re-authorize and Retry",
                  )}
                </span>
              </button>
              <button
                type="button"
                onClick={() => setIsConsentStale(false)}
                className="px-2 py-1 text-[var(--vellum-warning-text)] hover:underline"
              >
                {t("common.cancel", "Cancel")}
              </button>
            </div>
          </div>
        )}

        {showUnconfigured && (
          <div
            data-testid="companion-unconfigured-card"
            className="p-3 bg-white border border-[var(--vellum-border)] rounded-lg space-y-2 text-xs"
          >
            <p className="text-[var(--vellum-ink)]">
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
                className="px-3 py-1 bg-[var(--brand-primary)] text-white rounded text-xs font-medium hover:bg-[var(--vellum-brand-strong)] transition-colors"
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
                  ? "bg-white border-[var(--brand-primary)] shadow-sm"
                  : "bg-white/60 border-[var(--vellum-border)]"
              }`}
            >
              {/* Version Card Header */}
              <div className="flex items-center justify-between gap-2 mb-2">
                <span className="text-xs font-semibold text-[var(--vellum-ink)] truncate">
                  {`v${ver.version} · ${ver.label}`}
                </span>
                <div className="flex items-center gap-1.5 shrink-0">
                  {ver.cache_hit && (
                    <span className="text-[12px] px-1.5 py-0.2 bg-[var(--vellum-border)] text-[var(--vellum-ink-secondary)] rounded">
                      {t("webmail_next.ai_cache_hit")}
                    </span>
                  )}
                  {isActive ? (
                    <span className="text-[12px] font-medium text-[var(--vellum-success-text)] bg-[var(--vellum-success-fill)] px-2 py-0.5 rounded flex items-center gap-1">
                      <Check className="w-3 h-3" />
                      <span>{t("webmail_next.ai_current_version_badge")}</span>
                    </span>
                  ) : (
                    <button
                      type="button"
                      data-testid={`btn-rollback-${ver.id}`}
                      disabled={isLoading}
                      onClick={() => handleRollbackClick(idx)}
                      className="text-[12px] font-medium text-[var(--brand-primary)] hover:text-[var(--vellum-brand-strong)] hover:bg-[var(--brand-primary)]/10 px-2 py-0.5 rounded flex items-center gap-1 transition-colors disabled:opacity-50"
                    >
                      <RotateCcw className="w-3 h-3" />
                      <span>{t("webmail_next.ai_rollback_btn")}</span>
                    </button>
                  )}
                </div>
              </div>

              {/* Degraded Notice */}
              {ver.degraded && (
                <div className="mb-2 p-1.5 bg-[var(--vellum-warning-fill)] text-[var(--vellum-warning-text)] rounded text-[12px] flex items-center gap-1.5">
                  <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
                  <span>{t("webmail_next.ai_degraded_notice")}</span>
                </div>
              )}

              {/* Diff View if this is active version and has previous version */}
              {isActive && previousVersion && diffChunks.length > 0 ? (
                <div className="mb-2 space-y-1">
                  <div className="text-[12px] font-medium text-[var(--vellum-ink-secondary)]">
                    {t("webmail_next.ai_diff_heading")}
                  </div>
                  <div
                    data-testid="compose-diff-view"
                    className="p-2 bg-[var(--vellum-ground)] border border-[var(--vellum-border)] rounded text-xs leading-relaxed select-text font-sans max-h-48 overflow-y-auto whitespace-pre-wrap"
                  >
                    {diffChunks.map((chunk, cIdx) => {
                      if (chunk.type === "del") {
                        return (
                          <span
                            key={cIdx}
                            className="diff-del bg-[var(--vellum-danger-fill)] text-[var(--vellum-danger-text)] line-through px-0.5 rounded mr-0.5"
                          >
                            {chunk.value}
                          </span>
                        );
                      }
                      if (chunk.type === "add") {
                        return (
                          <span
                            key={cIdx}
                            className="diff-add bg-[var(--vellum-success-fill)] text-[var(--vellum-success-text)] font-medium px-0.5 rounded mr-0.5"
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
                  <div className="mb-2 p-2 bg-[var(--vellum-ground)] border border-[var(--vellum-border)] rounded text-xs text-[var(--vellum-ink)] max-h-36 overflow-y-auto whitespace-pre-wrap select-text">
                    {ver.body}
                  </div>
                )
              )}

              {/* Assumptions Review Block */}
              <details
                data-testid="assumptions-card"
                className="assumptions-card bg-white border border-dashed border-[var(--vellum-border)] rounded p-2 text-xs mb-2"
                open
              >
                <summary className="font-semibold text-[12px] text-[var(--vellum-ink-secondary)] flex items-center gap-1.5 cursor-pointer select-none">
                  <Info className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
                  <span>{t("webmail_next.ai_assumptions_title")}</span>
                </summary>
                {ver.assumptions && ver.assumptions.length > 0 ? (
                  <div className="mt-1.5 space-y-1">
                    <ul className="list-disc list-inside space-y-0.5 text-[12px] text-[var(--vellum-ink)]">
                      {ver.assumptions.map((ass, aIdx) => (
                        <li key={aIdx}>{ass}</li>
                      ))}
                    </ul>
                    <p className="text-[12px] text-[var(--vellum-ink-secondary)]">
                      {t("webmail_next.ai_assumptions_notice")}
                    </p>
                  </div>
                ) : (
                  <p className="mt-1.5 text-[12px] text-[var(--vellum-ink-secondary)]">
                    {t("webmail_next.ai_no_assumptions")}
                  </p>
                )}
              </details>

              {/* Tone notes pill */}
              {ver.tone_notes && (
                <div className="flex items-center gap-1.5 text-[12px] text-[var(--vellum-ink-secondary)]">
                  <span>{t("webmail_next.ai_tone_prefix")}</span>
                  <span className="px-1.5 py-0.5 bg-[var(--vellum-border)] text-[var(--vellum-ink)] rounded text-[12px] font-medium">
                    {ver.tone_notes}
                  </span>
                </div>
              )}
            </div>
          );
        })}
      </div>

      {/* Footer Interactive Actions */}
      <div className="p-3 border-t border-[var(--vellum-border)] bg-[var(--vellum-ground)] space-y-2 shrink-0">
        {/* Preset Chips */}
        <div className="flex items-center gap-1.5 flex-wrap">
          <button
            type="button"
            data-testid="chip-formal"
            disabled={isLoading}
            onClick={() => handleChipClick(t("webmail_next.ai_chip_formal"))}
            className="px-2 py-0.5 bg-white hover:bg-[var(--vellum-border)] border border-[var(--vellum-border)] rounded text-[12px] text-[var(--vellum-ink)] transition-colors disabled:opacity-50"
          >
            {t("webmail_next.ai_chip_formal")}
          </button>
          <button
            type="button"
            data-testid="chip-concise"
            disabled={isLoading}
            onClick={() => handleChipClick(t("webmail_next.ai_chip_concise"))}
            className="px-2 py-0.5 bg-white hover:bg-[var(--vellum-border)] border border-[var(--vellum-border)] rounded text-[12px] text-[var(--vellum-ink)] transition-colors disabled:opacity-50"
          >
            {t("webmail_next.ai_chip_concise")}
          </button>
          <button
            type="button"
            data-testid="chip-friendly"
            disabled={isLoading}
            onClick={() => handleChipClick(t("webmail_next.ai_chip_friendly"))}
            className="px-2 py-0.5 bg-white hover:bg-[var(--vellum-border)] border border-[var(--vellum-border)] rounded text-[12px] text-[var(--vellum-ink)] transition-colors disabled:opacity-50"
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
            className="px-2 py-0.5 bg-white hover:bg-[var(--vellum-border)] border border-[var(--vellum-border)] rounded text-[12px] text-[var(--vellum-ink)] transition-colors disabled:opacity-50"
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
            className="flex-1 min-w-0 px-2.5 py-1 text-xs bg-white border border-[var(--vellum-border)] rounded text-[var(--vellum-ink)] outline-none focus:border-[var(--brand-primary)] disabled:opacity-50"
          />
          <button
            type="submit"
            data-testid="btn-submit-instruction"
            disabled={isLoading || !instruction.trim()}
            className="p-1.5 bg-[var(--brand-primary)] text-white rounded hover:bg-[var(--vellum-brand-strong)] disabled:opacity-50 transition-colors shrink-0"
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
          className="w-full py-1.5 bg-[var(--vellum-ink)] text-white hover:bg-[var(--vellum-ink-hover)] text-xs font-semibold rounded shadow-sm flex items-center justify-center gap-1.5 transition-colors disabled:opacity-50"
        >
          <Check className="w-3.5 h-3.5" />
          <span>{t("webmail_next.ai_apply_draft_btn")}</span>
        </button>
      </div>
    </aside>
  );
}
