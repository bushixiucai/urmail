import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import {
  askMailboxAI,
  fetchMailboxAICitation,
  fetchMailboxAITarget,
  MailboxAICitation,
  MailboxAITarget,
  MailboxFolder,
} from "./mailbox-api";

export function MailboxAIAsk({
  folders,
  onCitation,
}: {
  folders: MailboxFolder[];
  onCitation: (source: MailboxAICitation) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [target, setTarget] = useState<MailboxAITarget | null>(null);
  const [question, setQuestion] = useState("");
  const [scope, setScope] = useState("");
  const [after, setAfter] = useState("");
  const [before, setBefore] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [answer, setAnswer] = useState<{
    text: string;
    grounded: boolean;
    question: string;
    citations: MailboxAICitation[];
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const request = useRef({
    generation: 0,
    controller: undefined as AbortController | undefined,
  });
  function cancel() {
    request.current.generation++;
    request.current.controller?.abort();
    setBusy(false);
  }
  useEffect(
    () => () => {
      request.current.generation++;
      request.current.controller?.abort();
    },
    [],
  );
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    void fetchMailboxAITarget(controller.signal)
      .then(setTarget)
      .catch(() => {
        if (!controller.signal.aborted)
          setError(t("page.webmail.ai_unavailable"));
      });
    return () => controller.abort();
  }, [open, t]);
  useEffect(() => {
    if (!answer?.citations.length) return;
    let active = true;
    async function verify() {
      try {
        await Promise.all(
          answer!.citations.map((value) => fetchMailboxAICitation(value.id)),
        );
      } catch {
        if (active) {
          setAnswer(null);
          setError(t("page.webmail.ai_source_changed"));
        }
      }
    }
    const timer = window.setInterval(() => void verify(), 10000);
    window.addEventListener("focus", verify);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", verify);
    };
  }, [answer, t]);
  async function ask() {
    if (!confirmed || !target?.configuration_digest) return;
    cancel();
    const generation = request.current.generation;
    const controller = new AbortController();
    request.current.controller = controller;
    setBusy(true);
    setError(null);
    setAnswer(null);
    try {
      const response = await askMailboxAI(
        {
          question: question.trim().replace(/\s*\n\s*/g, " "),
          configuration_digest: target.configuration_digest,
          confirmed: true,
          folder_enc: scope,
          after: after || undefined,
          before: before || undefined,
        },
        controller.signal,
      );
      const citations = await Promise.all(
        response.citations.map((id) =>
          fetchMailboxAICitation(id, controller.signal),
        ),
      );
      if (
        !controller.signal.aborted &&
        generation === request.current.generation
      )
        setAnswer({ ...response, question, citations });
    } catch (err) {
      if (
        !controller.signal.aborted &&
        generation === request.current.generation
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
    <div className="space-y-2 border-b px-3 py-2 sm:px-4">
      <Button
        type="button"
        size="sm"
        variant="ghost"
        aria-expanded={open}
        onClick={() => {
          if (open) cancel();
          setOpen(!open);
        }}
      >
        <Search className="mr-1.5 h-4 w-4" aria-hidden />
        {t("page.webmail.ai_ask")}
      </Button>
      {open && (
        <section
          aria-label={t("page.webmail.ai_ask")}
          className="max-w-3xl space-y-3 pb-2 text-sm"
        >
          <p className="text-xs text-muted-foreground">
            {t("page.webmail.ai_ask_scope")}
          </p>
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
          <label className="block space-y-1">
            <span>{t("page.webmail.ai_question")}</span>
            <textarea
              className="min-h-20 w-full rounded-md border bg-background p-2"
              maxLength={1000}
              value={question}
              onChange={(e) => {
                cancel();
                setAnswer(null);
                setConfirmed(false);
                setQuestion(e.target.value);
              }}
            />
          </label>
          <div className="grid gap-2 sm:grid-cols-3">
            <label className="space-y-1">
              <span>{t("page.webmail.ai_search_scope")}</span>
              <select
                className="h-11 w-full rounded-md border bg-background px-2"
                value={scope}
                onChange={(e) => {
                  cancel();
                  setAnswer(null);
                  setScope(e.target.value);
                  setConfirmed(false);
                }}
              >
                <option value="">{t("page.webmail.ai_all_folders")}</option>
                {folders.map((folder) => (
                  <option key={folder.name_enc} value={folder.name_enc}>
                    {folder.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="space-y-1">
              <span>{t("page.webmail.ai_after")}</span>
              <input
                className="h-11 w-full rounded-md border bg-background px-2"
                type="date"
                value={after}
                onChange={(e) => {
                  cancel();
                  setAnswer(null);
                  setAfter(e.target.value);
                  setConfirmed(false);
                }}
              />
            </label>
            <label className="space-y-1">
              <span>{t("page.webmail.ai_before")}</span>
              <input
                className="h-11 w-full rounded-md border bg-background px-2"
                type="date"
                value={before}
                onChange={(e) => {
                  cancel();
                  setAnswer(null);
                  setBefore(e.target.value);
                  setConfirmed(false);
                }}
              />
            </label>
          </div>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={confirmed}
              onChange={(e) => {
                setConfirmed(e.target.checked);
                if (!e.target.checked) {
                  cancel();
                  setAnswer(null);
                }
              }}
            />
            <span>{t("page.webmail.ai_confirm_content")}</span>
          </label>
          {error && (
            <p role="alert" className="text-destructive">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2">
            <Button
              type="button"
              size="sm"
              disabled={
                busy ||
                !question.trim() ||
                !confirmed ||
                !target?.enabled ||
                !target.allowed
              }
              onClick={() => void ask()}
            >
              {t("page.webmail.ai_ask_submit")}
            </Button>
            {busy && (
              <>
                <span role="status">{t("page.webmail.ai_generating")}</span>
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
          {answer && (
            <div className="space-y-2" aria-live="polite">
              <p className="font-medium">{answer.question}</p>
              <p className="whitespace-pre-wrap break-words">
                {answer.grounded
                  ? answer.text
                  : t("page.webmail.ai_no_support")}
              </p>
              {answer.citations.length > 0 && (
                <ul className="space-y-2">
                  {answer.citations.map((value, index) => (
                    <li key={value.id}>
                      <Button
                        type="button"
                        size="sm"
                        variant="link"
                        className="min-h-11 h-auto px-0"
                        onClick={() => {
                          void fetchMailboxAICitation(value.id)
                            .then(onCitation)
                            .catch(() => {
                              setAnswer(null);
                              setError(t("page.webmail.ai_source_changed"));
                            });
                        }}
                      >
                        {t("page.webmail.ai_open_source", {
                          number: index + 1,
                        })}
                      </Button>
                      <p className="whitespace-pre-wrap break-words text-xs text-muted-foreground">
                        {value.quote}
                      </p>
                    </li>
                  ))}
                </ul>
              )}
              <p className="text-xs text-muted-foreground">
                {t("page.webmail.ai_answer_notice")}
              </p>
            </div>
          )}
        </section>
      )}
    </div>
  );
}
