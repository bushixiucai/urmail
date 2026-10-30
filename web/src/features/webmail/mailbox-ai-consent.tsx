import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import {
  MAILBOX_AI_CONFIGURATION_CHANGED,
  type MailboxAIPreview,
} from "./mailbox-api";

/** One confirmation authorizes only the immutable preview currently displayed. */
export function useMailboxAIConsent() {
  const { t } = useTranslation();
  const [preview, setPreview] = useState<MailboxAIPreview | null>(null);
  const pending = useRef<((confirmed: boolean) => void) | null>(null);
  const settle = useCallback((confirmed: boolean) => {
    const resolve = pending.current;
    pending.current = null;
    setPreview(null);
    resolve?.(confirmed);
  }, []);
  const cancel = useCallback(() => settle(false), [settle]);
  useEffect(() => {
    window.addEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, cancel);
    return () =>
      window.removeEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, cancel);
  }, [cancel]);
  useEffect(
    () => () => {
      pending.current?.(false);
      pending.current = null;
    },
    [],
  );
  const confirm = useCallback(
    (value: MailboxAIPreview, signal?: AbortSignal) => {
      settle(false);
      if (signal?.aborted) return Promise.resolve(false);
      setPreview(value);
      return new Promise<boolean>((resolve) => {
        const abort = () => settle(false);
        signal?.addEventListener("abort", abort, { once: true });
        pending.current = (accepted) => {
          signal?.removeEventListener("abort", abort);
          resolve(accepted);
        };
      });
    },
    [settle],
  );
  const dialog = (
    <Dialog
      open={preview !== null}
      onOpenChange={(open) => {
        if (!open) cancel();
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("page.webmail.ai_confirm_title")}</DialogTitle>
          <DialogDescription>
            {t("page.webmail.ai_confirm_scope")}
          </DialogDescription>
        </DialogHeader>
        {preview && (
          <>
            <p className="break-words text-sm">
              {t("page.webmail.ai_provider", {
                provider: preview.provider ?? "—",
                model: preview.model ?? "—",
                endpoint: preview.endpoint ?? "—",
              })}
            </p>
            <pre className="max-h-[45dvh] overflow-auto whitespace-pre-wrap break-words rounded border p-3 text-xs select-text">
              {preview.messages
                ? preview.messages
                    .map((value) => `${value.role}\n${value.content}`)
                    .join("\n\n")
                : JSON.stringify(
                    {
                      question: preview.question,
                      sources: preview.sources ?? [],
                    },
                    null,
                    2,
                  )}
            </pre>
          </>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={cancel}>
            {t("action.cancel")}
          </Button>
          <Button type="button" onClick={() => settle(true)}>
            {t("page.webmail.ai_confirm_execute")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
  return { confirm, cancel, dialog };
}
