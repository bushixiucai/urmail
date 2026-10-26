import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  cancelScheduledSend,
  listScheduledSends,
  ScheduledSendItem,
} from "./mailbox-api";

const PAGE_SIZE = 50;

interface MailboxScheduledDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function MailboxScheduledDialog({
  open,
  onOpenChange,
}: MailboxScheduledDialogProps) {
  const { t, i18n } = useTranslation();
  const [items, setItems] = useState<ScheduledSendItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [offset, setOffset] = useState(0);

  const reload = useCallback(
    async (signal?: AbortSignal) => {
      setError(null);
      setBusy(true);
      try {
        const res = await listScheduledSends(signal, {
          limit: PAGE_SIZE,
          offset,
        });
        if (!signal?.aborted) setItems(res.items ?? []);
      } catch (err: unknown) {
        if (signal?.aborted) return;
        if (err instanceof ApiRequestError) {
          setError(
            t(`error.${err.code}`, {
              defaultValue: t("page.webmail.scheduled_list_failed"),
            }),
          );
        } else {
          setError(t("page.webmail.scheduled_list_failed"));
        }
      } finally {
        if (!signal?.aborted) setBusy(false);
      }
    },
    [t, offset],
  );

  useEffect(() => {
    if (!open) {
      setOffset(0);
      return;
    }
    const ac = new AbortController();
    void reload(ac.signal);
    return () => ac.abort();
  }, [open, reload]);

  const onCancel = async (id: number) => {
    setBusy(true);
    setError(null);
    try {
      await cancelScheduledSend(id);
      await reload();
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.scheduled_cancel_failed"),
          }),
        );
      } else {
        setError(t("page.webmail.scheduled_cancel_failed"));
      }
      // A task can cross the submission boundary after the list was loaded.
      // Refresh without losing the cancellation error shown above.
      try {
        const res = await listScheduledSends(undefined, {
          limit: PAGE_SIZE,
          offset,
        });
        setItems(res.items ?? []);
      } catch {
        /* keep the original error */
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md max-h-[80vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("page.webmail.scheduled_title")}</DialogTitle>
        </DialogHeader>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        {items.length === 0 && !error ? (
          <p className="text-sm text-muted-foreground">
            {t("page.webmail.scheduled_empty")}
          </p>
        ) : (
          <ul className="space-y-2 text-sm">
            {items.map((item) => (
              <li
                key={item.id}
                className="flex items-start justify-between gap-2 border-b border-border pb-2"
              >
                <div className="min-w-0">
                  <div className="font-medium truncate">
                    {t("page.webmail.scheduled_item", {
                      time: new Date(item.send_at).toLocaleString(
                        i18n.language,
                      ),
                      status: t(
                        `page.webmail.scheduled_status_${item.status}`,
                        {
                          defaultValue: t(
                            "page.webmail.scheduled_status_unknown",
                          ),
                        },
                      ),
                    })}
                  </div>
                  {item.status === "pending" &&
                    item.attempts > 0 &&
                    item.next_attempt_at && (
                      <p className="text-xs text-muted-foreground">
                        {t("page.webmail.scheduled_retry_at", {
                          time: new Date(item.next_attempt_at).toLocaleString(
                            i18n.language,
                          ),
                        })}
                      </p>
                    )}
                  {item.last_error && (
                    <p className="text-xs text-destructive">
                      {t(`error.${item.last_error}`, {
                        defaultValue: t("page.webmail.scheduled_attention"),
                      })}
                    </p>
                  )}
                  {(item.status === "uncertain" || item.status === "held") && (
                    <p className="text-xs text-muted-foreground">
                      {t("page.webmail.scheduled_review_required")}
                    </p>
                  )}
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={
                    busy || !["pending", "preparing"].includes(item.status)
                  }
                  onClick={() => void onCancel(item.id)}
                >
                  {t("page.webmail.scheduled_cancel")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={busy || offset === 0}
            onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
          >
            {t("page.webmail.prev_page")}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy || items.length < PAGE_SIZE}
            onClick={() => setOffset(offset + PAGE_SIZE)}
          >
            {t("page.webmail.next_page")}
          </Button>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            {t("action.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
