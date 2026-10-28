import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiRequestError } from "@/lib/api/client";
import {
  fetchMailboxAIContext,
  fetchMailboxAITarget,
  fetchMailboxAIBudget,
  MailboxAIOperation,
  MailboxAIResult,
  MailboxAISource,
  MailboxAITarget,
  runMailboxAI,
} from "./mailbox-api";

export function MailboxAIPanel({
  mode,
  source,
  subject,
  body,
  disabled = false,
  onApply,
}: {
  mode: "read" | "compose";
  source?: MailboxAISource;
  subject: string;
  body: string;
  disabled?: boolean;
  onApply?: (proposal: {
    subject: string;
    body: string;
  }) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [open, setOpen] = useState(false);
  const [target, setTarget] = useState<MailboxAITarget | null>(null);
  const [operation, setOperation] = useState<MailboxAIOperation>(
    mode === "read" ? "summarize" : "polish",
  );
  const [selection, setSelection] = useState("");
  const [goal, setGoal] = useState("");
  const [language, setLanguage] = useState("en");
  const [includeContext, setIncludeContext] = useState(mode === "read");
  const [confirmed, setConfirmed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<MailboxAIResult | null>(null);
  const [editRevision, setEditRevision] = useState(0);
  const [snapshot, setSnapshot] = useState({
    subject: "",
    body: "",
    revision: 0,
  });
  useEffect(() => {
    if (mode === "compose") {
      setEditRevision((value) => value + 1);
      setConfirmed(false);
    }
  }, [subject, body, mode]);
  const [budget, setBudget] = useState<string | null>(null);
  const [applied, setApplied] = useState(false);
  const request = useRef({
    generation: 0,
    controller: undefined as AbortController | undefined,
  });
  const sourceFolder = source?.folder_enc;
  const sourceUid = source?.uid;
  const sourceValidity = source?.uid_validity;
  const sourceIncluded =
    !!source &&
    (mode === "read" || includeContext || operation === "draft_reply");
  const inputReady = sourceIncluded
    ? !!selection.trim()
    : !!(body.trim() || goal.trim());
  const sourceKey = source
    ? `${source.folder_enc}/${source.uid}/${source.uid_validity}`
    : "";
  const cancel = () => {
    request.current.generation++;
    request.current.controller?.abort();
    setBusy(false);
  };
  useEffect(
    () => () => {
      request.current.generation++;
      request.current.controller?.abort();
    },
    [],
  );
  useEffect(() => {
    cancel();
    setResult(null);
    setConfirmed(false);
    setSelection("");
    setError(null);
    setApplied(false);
  }, [sourceKey]);
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    void fetchMailboxAITarget(controller.signal)
      .then(setTarget)
      .catch(() => {
        if (!controller.signal.aborted)
          setError(t("page.webmail.ai_unavailable"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    void fetchMailboxAIBudget(controller.signal)
      .then((value) => {
        setBudget(
          t("page.webmail.ai_budget", {
            used: value.daily.tokens_used,
            limit:
              (value.daily.tokens_limit ?? 0) > 0
                ? value.daily.tokens_limit
                : t("page.webmail.ai_unlimited"),
          }),
        );
      })
      .catch(() => setBudget(null));
    if (
      sourceFolder &&
      sourceUid &&
      sourceValidity &&
      (mode === "read" || includeContext || operation === "draft_reply")
    ) {
      void fetchMailboxAIContext(
        {
          folder_enc: sourceFolder,
          uid: sourceUid,
          uid_validity: sourceValidity,
        },
        controller.signal,
      )
        .then((value) => {
          if (!controller.signal.aborted) setSelection(value.body);
        })
        .catch(() => {
          if (!controller.signal.aborted)
            setError(t("page.webmail.ai_source_changed"));
        });
    }
    return () => controller.abort();
  }, [
    open,
    sourceFolder,
    sourceUid,
    sourceValidity,
    includeContext,
    mode,
    operation,
    t,
  ]);
  const resultText =
    result?.body ??
    result?.draft ??
    result?.translation ??
    result?.summary ??
    (result?.category
      ? t(`page.webmail.ai_category_${result.category}`)
      : (result?.reasons?.join("\n") ?? ""));
  const stale =
    mode === "compose" &&
    (subject !== snapshot.subject ||
      body !== snapshot.body ||
      editRevision !== snapshot.revision);
  const operations: MailboxAIOperation[] = [
    "summarize",
    "translate",
    "classify",
    "phishing",
    "polish",
    ...(mode === "compose"
      ? ["draft_new" as const, ...(source ? ["draft_reply" as const] : [])]
      : []),
  ];
  async function generate() {
    if (!target?.configuration_digest || !confirmed) return;
    cancel();
    const controller = new AbortController();
    request.current.controller = controller;
    const generation = request.current.generation;
    setBusy(true);
    setResult(null);
    setError(null);
    setApplied(false);
    setSnapshot({ subject, body, revision: editRevision });
    try {
      const useSource =
        !!source &&
        (mode === "read" || includeContext || operation === "draft_reply");
      const response = await runMailboxAI(
        {
          operation,
          configuration_digest: target.configuration_digest,
          confirmed: true,
          request_key: `native-${crypto.randomUUID()}`,
          body: body || goal,
          subject,
          instruction: goal,
          target_language: language,
          ...(useSource ? { ...source, selection } : {}),
        },
        controller.signal,
      );
      if (
        generation === request.current.generation &&
        !controller.signal.aborted
      )
        setResult(response.result);
    } catch (err) {
      if (
        generation === request.current.generation &&
        !controller.signal.aborted
      )
        setError(
          err instanceof ApiRequestError
            ? t(`error.${err.code}`, {
                defaultValue: t("page.webmail.ai_failed"),
              })
            : t("page.webmail.ai_failed"),
        );
    } finally {
      if (generation === request.current.generation) setBusy(false);
    }
  }
  return (
    <div
      className="space-y-2"
      onKeyDown={(event) => {
        if (
          event.key === "Enter" &&
          (event.target instanceof HTMLInputElement ||
            event.target instanceof HTMLSelectElement)
        )
          event.preventDefault();
      }}
    >
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="min-h-11 md:min-h-8"
        aria-expanded={open}
        aria-controls={`${id}-panel`}
        disabled={disabled}
        onClick={() => {
          if (open) cancel();
          setOpen(!open);
        }}
      >
        <Sparkles className="mr-1.5 h-4 w-4" aria-hidden />
        {t("page.webmail.ai_assistance")}
      </Button>
      {open && (
        <section
          id={`${id}-panel`}
          aria-label={t("page.webmail.ai_assistance")}
          className="space-y-3 rounded-md border bg-muted/20 p-3 text-sm"
        >
          <h3 className="font-medium">{t("page.webmail.ai_assistance")}</h3>
          {loading && <p role="status">{t("state.loading")}</p>}
          {target && (
            <p className="break-words text-xs text-muted-foreground">
              {t("page.webmail.ai_provider", {
                provider: target.provider ?? "—",
                model: target.model ?? "—",
                endpoint: target.endpoint ?? "—",
              })}
            </p>
          )}
          {target && (!target.enabled || !target.allowed) && (
            <p role="status">{t("page.webmail.ai_unavailable")}</p>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="space-y-1" htmlFor={`${id}-operation`}>
              <span>{t("page.webmail.ai_operation")}</span>
              <select
                id={`${id}-operation`}
                className="min-h-11 w-full rounded-md border bg-background px-2 md:min-h-10"
                value={operation}
                disabled={busy}
                onChange={(e) => {
                  setOperation(e.target.value as MailboxAIOperation);
                  setResult(null);
                  setConfirmed(false);
                }}
              >
                {operations.map((value) => (
                  <option key={value} value={value}>
                    {t(`page.webmail.ai_${value}`)}
                  </option>
                ))}
              </select>
            </label>
            {operation === "translate" && (
              <label className="space-y-1" htmlFor={`${id}-language`}>
                <span>{t("page.webmail.ai_language")}</span>
                <select
                  id={`${id}-language`}
                  className="min-h-11 w-full rounded-md border bg-background px-2 md:min-h-10"
                  value={language}
                  onChange={(e) => {
                    setLanguage(e.target.value);
                    setResult(null);
                    setConfirmed(false);
                  }}
                >
                  {["en", "zh-CN", "ja"].map((value) => (
                    <option key={value} value={value}>
                      {t(`page.webmail.ai_language_${value}`)}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
          {mode === "compose" && (
            <label className="block space-y-1" htmlFor={`${id}-goal`}>
              <span>{t("page.webmail.ai_goal")}</span>
              <Input
                id={`${id}-goal`}
                maxLength={4000}
                value={goal}
                onChange={(e) => {
                  cancel();
                  setGoal(e.target.value);
                  setConfirmed(false);
                  setResult(null);
                }}
              />
            </label>
          )}
          {mode === "compose" && source && (
            <label className="flex min-h-11 items-center gap-2">
              <input
                type="checkbox"
                checked={includeContext || operation === "draft_reply"}
                disabled={busy || operation === "draft_reply"}
                onChange={(e) => {
                  cancel();
                  setResult(null);
                  setIncludeContext(e.target.checked);
                  setConfirmed(false);
                }}
              />
              {t("page.webmail.ai_include_context")}
            </label>
          )}
          {source &&
            (mode === "read" ||
              includeContext ||
              operation === "draft_reply") && (
              <label className="block space-y-1" htmlFor={`${id}-selection`}>
                <span>{t("page.webmail.ai_selected_content")}</span>
                <textarea
                  id={`${id}-selection`}
                  className="min-h-24 w-full rounded-md border bg-background p-2"
                  maxLength={65000}
                  value={selection}
                  onChange={(e) => {
                    cancel();
                    setSelection(e.target.value);
                    setResult(null);
                    setConfirmed(false);
                  }}
                />
                <span className="block text-xs text-muted-foreground">
                  {t("page.webmail.ai_selection_hint")}
                </span>
              </label>
            )}
          <p className="text-xs text-muted-foreground">
            {t("page.webmail.ai_scope_notice")}
          </p>
          <label className="flex min-h-11 items-start gap-2 py-2">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={confirmed}
              onChange={(e) => {
                setConfirmed(e.target.checked);
                if (!e.target.checked) {
                  cancel();
                  setResult(null);
                }
              }}
            />
            <span>{t("page.webmail.ai_confirm_content")}</span>
          </label>
          {budget && <p className="text-xs text-muted-foreground">{budget}</p>}
          {error && (
            <p role="alert" className="text-destructive">
              {error}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              size="sm"
              disabled={
                disabled ||
                busy ||
                loading ||
                !target?.enabled ||
                !target.allowed ||
                !confirmed ||
                !inputReady
              }
              onClick={() => void generate()}
            >
              {t("page.webmail.ai_generate")}
            </Button>
            {busy && (
              <>
                <p role="status" className="self-center">
                  {t("page.webmail.ai_generating")}
                </p>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={cancel}
                >
                  {t("page.webmail.ai_cancel")}
                </Button>
              </>
            )}
          </div>
          {result && (
            <div className="space-y-2" aria-live="polite">
              <h4 className="font-medium">{t("page.webmail.ai_preview")}</h4>
              {result.subject && (
                <p className="break-words font-medium">{result.subject}</p>
              )}
              <p className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md border bg-background p-3">
                {resultText}
              </p>
              {typeof result.score === "number" && (
                <p>
                  {t("page.webmail.ai_score", {
                    score: Math.round(result.score * 100),
                  })}
                </p>
              )}
              {operation === "phishing" && (
                <p className="text-xs text-muted-foreground">
                  {t("page.webmail.ai_phishing_notice")}
                </p>
              )}
              {mode === "compose" && (
                <details>
                  <summary className="cursor-pointer">
                    {t("page.webmail.ai_previous_version")}
                  </summary>
                  <p className="max-h-40 overflow-auto whitespace-pre-wrap break-words">
                    {snapshot.body}
                  </p>
                </details>
              )}
              {stale && !applied && (
                <p role="status">{t("page.webmail.ai_stale")}</p>
              )}
              {applied && <p role="status">{t("page.webmail.ai_applied")}</p>}
              <div className="flex flex-wrap gap-2">
                {onApply &&
                  (result.body || result.draft || result.translation) && (
                    <Button
                      type="button"
                      size="sm"
                      disabled={disabled || stale || applied}
                      onClick={() => {
                        void Promise.resolve(
                          onApply({
                            subject: result.subject ?? subject,
                            body: resultText,
                          }),
                        )
                          .then(() => setApplied(true))
                          .catch(() =>
                            setError(t("page.webmail.ai_draft_failed")),
                          );
                      }}
                    >
                      {t(
                        mode === "compose"
                          ? "page.webmail.ai_apply"
                          : "page.webmail.ai_use_reply",
                      )}
                    </Button>
                  )}
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(resultText)
                      .catch(() => setError(t("page.webmail.ai_copy_failed")));
                  }}
                >
                  {t("page.webmail.ai_copy")}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() => setResult(null)}
                >
                  {t("page.webmail.ai_discard")}
                </Button>
              </div>
            </div>
          )}
        </section>
      )}
    </div>
  );
}
