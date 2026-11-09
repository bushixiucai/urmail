import { useState, useEffect, useCallback } from "react";
import { useTranslation } from "react-i18next";
import {
  Calendar,
  CheckSquare,
  Globe,
  Loader2,
  X,
  AlertTriangle,
  RotateCcw,
} from "lucide-react";
import type { MailboxAIAssistResponse } from "@/features/webmail/mailbox-api";

export interface WebmailStructuredSummaryProps {
  response: MailboxAIAssistResponse | null;
  isLoading?: boolean;
  error?: string | null;
  messageUid: number;
  activeOperation?: "standard" | "shorter" | "todos" | "translate" | string;
  onDismiss: () => void;
  onRetry?: () => void;
  onRefineStandard?: () => void;
  onRefineShorter?: () => void;
  onRefineTodos?: () => void;
  onRefineTranslate?: () => void;
  onCancel?: () => void;
}

export function WebmailStructuredSummary({
  response,
  isLoading,
  error,
  messageUid,
  activeOperation,
  onDismiss,
  onRetry,
  onRefineStandard,
  onRefineShorter,
  onRefineTodos,
  onRefineTranslate,
  onCancel,
}: WebmailStructuredSummaryProps) {
  const { t } = useTranslation();
  const [checkedTodos, setCheckedTodos] = useState<Record<number, boolean>>({});

  // Escape key handler for accessible collapse
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onDismiss();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [onDismiss]);

  const todos = response?.result?.todos ?? response?.result?.items ?? [];

  // Load persistent checked state from localStorage
  useEffect(() => {
    if (!messageUid || todos.length === 0) {
      setCheckedTodos({});
      return;
    }
    const initial: Record<number, boolean> = {};
    todos.forEach((_, idx) => {
      try {
        const stored = localStorage.getItem(
          `urmail_ai_todo_${messageUid}_${idx}`,
        );
        if (stored === "true") {
          initial[idx] = true;
        }
      } catch {
        // Ignore localStorage errors
      }
    });
    setCheckedTodos(initial);
  }, [messageUid, todos.length]);

  const toggleTodo = useCallback(
    (idx: number) => {
      setCheckedTodos((prev) => {
        const nextState = !prev[idx];
        try {
          localStorage.setItem(
            `urmail_ai_todo_${messageUid}_${idx}`,
            String(nextState),
          );
        } catch {
          // Ignore localStorage errors
        }
        return { ...prev, [idx]: nextState };
      });
    },
    [messageUid],
  );

  const result = response?.result;
  const keyDates = result?.key_dates ?? [];
  const bullets = result?.bullets ?? [];
  const language = result?.language || result?.detected_source;
  const isTodosOp = activeOperation === "todos";
  const isShorterOp = activeOperation === "shorter";
  const isCacheHit = Boolean(response?.cache_hit);
  const isDegraded = Boolean(response?.degraded);
  const confidenceScore =
    result?.confidence != null
      ? Math.round(result.confidence * 100)
      : result?.score != null
        ? Math.round(result.score * 100)
        : null;

  return (
    <div
      role="region"
      aria-live="polite"
      data-testid="structured-summary-card"
      className="p-4 sm:p-5 mb-5 rounded-lg border border-[#EBE8E0] dark:border-[#323238] border-l-4 border-l-[#2563EB] bg-[#F6F4EE] dark:bg-[#27272C] transition-all"
    >
      {/* 1. Top Meta Row: Language, Dates, Cache Hit, Dismiss */}
      <div className="flex flex-wrap items-center justify-between gap-2 pb-3 border-b border-[#EBE8E0] dark:border-[#323238]">
        <div className="flex flex-wrap items-center gap-2">
          {language && (
            <span
              data-testid="ai-summary-language"
              className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium bg-[#E0E7FF] text-[#3730A3] dark:bg-[#312E81] dark:text-[#C7D2FE]"
            >
              <Globe className="w-3 h-3" />
              <span>{language}</span>
            </span>
          )}
          {keyDates.map((kd, idx) => (
            <span
              key={idx}
              data-testid="ai-key-date-badge"
              className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium bg-[#FEF3C7] text-[#92400E] dark:bg-[#78350F]/40 dark:text-[#FDE68A]"
            >
              <Calendar className="w-3 h-3" />
              <span>
                {kd.label}: {kd.date}
              </span>
            </span>
          ))}
        </div>

        <div className="flex items-center gap-2 ml-auto">
          {isCacheHit && (
            <span
              data-testid="ai-cache-hit"
              className="text-xs text-[#71717A] dark:text-[#A1A1AA] flex items-center gap-1 font-medium"
            >
              {t("webmail_next.ai_cache_hit")}
            </span>
          )}
          <button
            data-testid="btn-dismiss-summary"
            type="button"
            onClick={onDismiss}
            className="p-1 text-[#71717A] hover:text-[#18181B] dark:text-[#A1A1AA] dark:hover:text-white rounded hover:bg-black/5 dark:hover:bg-white/5 transition-colors"
            aria-label={t("webmail_next.ai_dismiss")}
            title={t("webmail_next.ai_dismiss")}
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* 2. Degraded Warning Notice */}
      {isDegraded && (
        <div
          data-testid="ai-degraded-notice"
          className="mt-3 p-2.5 bg-[#FEF3C7] dark:bg-[#78350F]/30 border border-[#FDE68A] dark:border-[#B45309] rounded-md text-xs text-[#92400E] dark:text-[#FDE68A] flex items-center gap-2"
        >
          <AlertTriangle className="w-4 h-4 shrink-0 text-[#D97706]" />
          <span>{t("webmail_next.ai_degraded_notice")}</span>
        </div>
      )}

      {/* 3. Loading State */}
      {isLoading && (
        <div
          data-testid="ai-summary-loading"
          className="flex items-center justify-between py-6 text-xs text-[var(--vellum-ink-secondary)]"
        >
          <div className="flex items-center gap-2">
            <Loader2 className="w-4 h-4 animate-spin text-[#2563EB]" />
            <span>{t("page.webmail.ai_generating")}</span>
          </div>
          {onCancel && (
            <button
              type="button"
              onClick={onCancel}
              className="text-xs text-[#DC2626] hover:underline font-medium"
            >
              {t("page.webmail.ai_cancel")}
            </button>
          )}
        </div>
      )}

      {/* 4. Error State */}
      {!isLoading && error && (
        <div
          data-testid="ai-summary-error"
          className="py-4 text-xs text-[#DC2626] flex items-center justify-between gap-2"
        >
          <span>{error}</span>
          {onRetry && (
            <button
              type="button"
              onClick={onRetry}
              className="px-2.5 py-1 bg-white dark:bg-[#1E1E22] border border-[#DC2626]/30 text-[#DC2626] rounded hover:bg-[#FEF2F2] dark:hover:bg-[#450A0A] font-medium flex items-center gap-1 transition-colors"
            >
              <RotateCcw className="w-3 h-3" />
              <span>{t("webmail_next.ai_retry")}</span>
            </button>
          )}
        </div>
      )}

      {/* 5. Summary Content Body */}
      {!isLoading && !error && result && (
        <div className="pt-3 space-y-4">
          {/* Key Points */}
          {!isTodosOp &&
            (bullets.length > 0 || result.summary || result.text) && (
              <div>
                <div className="flex items-center justify-between mb-2">
                  <h4 className="text-xs font-bold text-[#18181B] dark:text-[#F4F4F6]">
                    {isShorterOp
                      ? t(
                          "webmail_next.ai_shorter_summary_title",
                          "One-line summary",
                        )
                      : t("webmail_next.ai_key_points_title")}
                  </h4>
                  {confidenceScore != null && (
                    <span className="text-xs text-[var(--vellum-ink-secondary)]">
                      {t("webmail_next.ai_confidence", {
                        score: confidenceScore,
                      })}
                    </span>
                  )}
                </div>

                {bullets.length > 0 ? (
                  <ul
                    data-testid="ai-key-points-list"
                    className="list-disc list-inside space-y-1.5 text-xs text-[#18181B] dark:text-[#E4E4E7] leading-relaxed"
                  >
                    {bullets.map((bullet, idx) => (
                      <li key={idx} className="pl-1">
                        {bullet}
                      </li>
                    ))}
                  </ul>
                ) : result.summary || result.text ? (
                  <p
                    data-testid="ai-fallback-text"
                    className="text-xs text-[#18181B] dark:text-[#E4E4E7] leading-relaxed"
                  >
                    {result.summary || result.text}
                  </p>
                ) : null}
              </div>
            )}

          {/* Action Items / Todos */}
          {(isTodosOp || todos.length > 0) && (
            <div
              data-testid="ai-todo-list"
              className={`space-y-2 ${
                !isTodosOp &&
                (bullets.length > 0 || result.summary || result.text)
                  ? "pt-3 border-t border-[#EBE8E0] dark:border-[#323238]"
                  : ""
              }`}
            >
              <div className="flex items-center gap-1.5 text-xs font-bold text-[#18181B] dark:text-[#F4F4F6] mb-2">
                <CheckSquare className="w-3.5 h-3.5 text-[#2563EB]" />
                <span>
                  {t("webmail_next.ai_action_items_title")}
                  {todos.length > 0 ? ` (${todos.length})` : ""}
                </span>
              </div>
              {todos.length > 0 ? (
                <div className="space-y-1.5">
                  {todos.map((todo, idx) => {
                    const isChecked = checkedTodos[idx] || false;
                    return (
                      <label
                        key={idx}
                        className="flex items-start gap-2.5 text-xs text-[#18181B] dark:text-[#E4E4E7] cursor-pointer hover:bg-black/5 dark:hover:bg-white/5 p-1 rounded transition-colors"
                      >
                        <input
                          type="checkbox"
                          checked={isChecked}
                          onChange={() => toggleTodo(idx)}
                          className="mt-0.5 rounded border-[#D4D4D8] text-[#2563EB] focus:ring-[#2563EB]"
                        />
                        <span
                          className={`flex-1 leading-snug ${
                            isChecked
                              ? "line-through text-[var(--vellum-ink-secondary)]"
                              : ""
                          }`}
                        >
                          {todo.task}
                        </span>
                        {(todo.due_date || todo.assignee) && (
                          <span className="text-[11px] text-[var(--vellum-ink-secondary)] shrink-0">
                            {todo.due_date && todo.assignee
                              ? t("webmail_next.ai_due_date_with_assignee", {
                                  assignee: todo.assignee,
                                  date: todo.due_date,
                                })
                              : todo.due_date
                                ? t("webmail_next.ai_due_date", {
                                    date: todo.due_date,
                                  })
                                : t("webmail_next.ai_assignee", {
                                    assignee: todo.assignee,
                                  })}
                          </span>
                        )}
                      </label>
                    );
                  })}
                </div>
              ) : (
                <p
                  data-testid="ai-todos-empty"
                  className="text-xs text-[var(--vellum-ink-secondary)] py-1"
                >
                  {t(
                    "webmail_next.ai_no_todos",
                    "No action items detected in email",
                  )}
                </p>
              )}
            </div>
          )}

          {/* Full Translation View Companion */}
          {result.translated_text && (
            <div
              data-testid="ai-translation-block"
              className="pt-3 border-t border-[#EBE8E0] dark:border-[#323238] text-xs space-y-1"
            >
              <div className="font-semibold text-[#18181B] dark:text-[#F4F4F6] flex items-center gap-1.5">
                <Globe className="w-3.5 h-3.5 text-[#2563EB]" />
                <span>{t("webmail_next.ai_refine_translate")}</span>
              </div>
              <p className="text-[#18181B] dark:text-[#E4E4E7] leading-relaxed whitespace-pre-wrap">
                {result.translated_text}
              </p>
            </div>
          )}

          {/* 6. Refine Chips Row */}
          <div className="pt-3 border-t border-[#EBE8E0] dark:border-[#323238] flex flex-wrap items-center gap-2">
            {onRefineStandard && (
              <button
                data-testid="chip-refine-standard"
                type="button"
                onClick={onRefineStandard}
                className="h-6 px-2.5 bg-white dark:bg-[#1E1E22] hover:bg-[#F3F1EC] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded-full text-xs text-[#18181B] dark:text-[#F4F4F6] font-medium transition-colors"
              >
                {t("webmail_next.ai_refine_standard")}
              </button>
            )}
            {onRefineShorter && (
              <button
                data-testid="chip-refine-shorter"
                type="button"
                onClick={onRefineShorter}
                className="h-6 px-2.5 bg-white dark:bg-[#1E1E22] hover:bg-[#F3F1EC] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded-full text-xs text-[#18181B] dark:text-[#F4F4F6] font-medium transition-colors"
              >
                {t("webmail_next.ai_refine_shorter")}
              </button>
            )}
            {onRefineTodos && (
              <button
                data-testid="chip-refine-todos"
                type="button"
                onClick={onRefineTodos}
                className="h-6 px-2.5 bg-white dark:bg-[#1E1E22] hover:bg-[#F3F1EC] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded-full text-xs text-[#18181B] dark:text-[#F4F4F6] font-medium transition-colors"
              >
                {t("webmail_next.ai_refine_todos")}
              </button>
            )}
            {onRefineTranslate && (
              <button
                data-testid="chip-refine-translate"
                type="button"
                onClick={onRefineTranslate}
                className="h-6 px-2.5 bg-white dark:bg-[#1E1E22] hover:bg-[#F3F1EC] dark:hover:bg-[#323238] border border-[#EBE8E0] dark:border-[#323238] rounded-full text-xs text-[#18181B] dark:text-[#F4F4F6] font-medium transition-colors"
              >
                {t("webmail_next.ai_refine_translate")}
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
