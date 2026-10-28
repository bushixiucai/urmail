import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { getMailboxToken, MessageDetail, formatFrom } from "./mailbox-api";
import { sanitizeHtmlMail } from "./sanitize-html-mail";

async function rawResponse(
  folder: string,
  uid: number,
  validity: number,
  signal: AbortSignal,
) {
  const token = getMailboxToken();
  const response = await fetch(
    `/api/v1/mailbox/folders/${encodeURIComponent(folder)}/messages/${uid}/raw?uid_validity=${validity}`,
    {
      headers: { Authorization: `Bearer ${token ?? ""}` },
      signal,
      credentials: "same-origin",
      cache: "no-store",
    },
  );
  if (!response.ok || !response.body) throw Error("raw read failed");
  return response;
}

// Untrusted content is rendered solely through textContent, never inserted as
// executable HTML in the application's origin or the printable document.
export function printableMessageText(message: MessageDetail): string {
  let plain = message.text ?? "";
  if (!plain && message.html) {
    const inert = new DOMParser().parseFromString(
      sanitizeHtmlMail(message.html),
      "text/html",
    );
    for (const element of inert.querySelectorAll(
      "script,style,head,template,noscript",
    ))
      element.remove();
    plain = inert.body.textContent ?? "";
  }
  return [
    message.subject,
    formatFrom(message.from),
    (message.to ?? []).map((item) => item.email).join(", "),
    message.date,
    "",
    plain,
  ].join("\n");
}

export function MailboxMessageTools({
  folder,
  message,
}: {
  folder: string;
  message: MessageDetail;
}) {
  const { t } = useTranslation();
  const [headers, setHeaders] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const controller = useRef<AbortController | null>(null);
  useEffect(
    () => () => controller.current?.abort(),
    [folder, message.uid, message.uid_validity],
  );
  const read = async (mode: "headers" | "download") => {
    if (!message.uid_validity || busy) return;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true);
    setError(false);
    try {
      const response = await rawResponse(
        folder,
        message.uid,
        message.uid_validity,
        abort.signal,
      );
      const reader = response.body!.getReader();
      let total = 0;
      const chunks: Uint8Array[] = [];
      let headerText = "";
      const decoder = new TextDecoder();
      try {
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          const previous = total;
          total += value.length;
          if (mode === "headers") {
            headerText += decoder.decode(
              value.subarray(0, Math.max(0, 128 * 1024 - previous)),
              { stream: true },
            );
            const boundary = headerText.search(/\r?\n\r?\n/);
            if (boundary >= 0) {
              setHeaders(headerText.slice(0, boundary));
              return;
            }
            if (total >= 128 * 1024)
              throw Error("bounded header read exceeded");
          } else {
            if (total > 32 * 1024 * 1024)
              throw Error("bounded raw read exceeded");
            chunks.push(value);
          }
        }
        if (mode === "headers") {
          setHeaders(headerText);
          return;
        }
        const bytes = new Uint8Array(total);
        let offset = 0;
        for (const chunk of chunks) {
          bytes.set(chunk, offset);
          offset += chunk.length;
        }
        const url = URL.createObjectURL(
          new Blob([bytes], { type: "message/rfc822" }),
        );
        const link = document.createElement("a");
        link.href = url;
        link.download = `message-${message.uid}.eml`;
        link.rel = "noopener";
        link.click();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
      } finally {
        await reader.cancel().catch(() => {});
      }
    } catch {
      if (!abort.signal.aborted) setError(true);
    } finally {
      if (controller.current === abort) setBusy(false);
    }
  };
  const print = () => {
    const target = window.open("", "_blank");
    if (!target) {
      setError(true);
      return;
    }
    target.opener = null;
    target.document.title = message.subject;
    const content = target.document.createElement("pre");
    content.style.whiteSpace = "pre-wrap";
    content.style.overflowWrap = "anywhere";
    content.textContent = printableMessageText(message);
    target.document.body.replaceChildren(content);
    target.focus();
    target.print();
  };
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={busy || !message.uid_validity}
          onClick={() => void read("headers")}
        >
          {t("page.webmail.view_headers")}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy || !message.uid_validity}
          onClick={() => void read("download")}
        >
          {t("page.webmail.download_eml")}
        </Button>
        {busy && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => controller.current?.abort()}
          >
            {t("action.cancel")}
          </Button>
        )}
        <Button size="sm" variant="outline" onClick={print}>
          {t("page.webmail.print_message")}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {t("page.webmail.message_tool_failed")}
        </p>
      )}
      <Dialog
        open={headers !== null}
        onOpenChange={(open) => {
          if (!open) setHeaders(null);
        }}
      >
        <DialogContent className="max-w-3xl max-h-[85dvh] overflow-auto">
          <DialogHeader>
            <DialogTitle>{t("page.webmail.view_headers")}</DialogTitle>
          </DialogHeader>
          <pre className="whitespace-pre-wrap break-all text-xs">{headers}</pre>
        </DialogContent>
      </Dialog>
    </div>
  );
}
