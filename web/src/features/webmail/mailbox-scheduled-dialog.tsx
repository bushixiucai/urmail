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
  acknowledgeUncertainScheduledSend,
  repairScheduledSentCopy,
  listScheduledSends,
  listSendReceipts,
  repairSentCopy,
  SendReceipt,
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
  const [receipts, setReceipts] = useState<SendReceipt[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [offset, setOffset] = useState(0);
  const [reviewId, setReviewId] = useState<number | null>(null);
  const [reviewConfirmed, setReviewConfirmed] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const reload = useCallback(
    async (signal?: AbortSignal) => {
      setError(null);
      setBusy(true);
      try {
        const [res, receiptRes] = await Promise.all([
          listScheduledSends(signal, { limit: PAGE_SIZE, offset }),
          listSendReceipts({ limit: PAGE_SIZE, offset }, signal),
        ]);
        if (!signal?.aborted) {
          setItems(res.items ?? []);
          setReceipts(receiptRes.items ?? []);
        }
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

  const onRepair = async (id: number) => {
    setBusy(true);
    setError(null);
    try {
      await repairScheduledSentCopy(id);
      await reload();
    } catch {
      setError(t("page.webmail.compose_sent_copy_pending"));
    } finally {
      setBusy(false);
    }
  };

  const onRepairReceipt = async (id: number) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const res = await repairSentCopy(id);
      await reload();
      setNotice(
        t(
          res.receipt.sent_copy_state === "complete"
            ? "page.webmail.receipt_copy_repaired"
            : "page.webmail.compose_sent_copy_pending",
        ),
      );
    } catch (err: unknown) {
      setError(
        err instanceof ApiRequestError
          ? t(`error.${err.code}`)
          : t("page.webmail.compose_sent_copy_pending"),
      );
    } finally {
      setBusy(false);
    }
  };

  const onAcknowledge = async (id: number) => {
    if (!reviewConfirmed || reviewId !== id) return;
    setBusy(true);
    setError(null);
    try {
      await acknowledgeUncertainScheduledSend(id);
      setReviewId(null);
      setReviewConfirmed(false);
      await reload();
      setNotice(t("page.webmail.scheduled_uncertain_released"));
    } catch (err: unknown) {
      setError(
        err instanceof ApiRequestError
          ? t(`error.${err.code}`)
          : t("page.webmail.scheduled_cancel_failed"),
      );
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
        {notice && (
          <p role="status" className="text-sm text-muted-foreground">
            {notice}
          </p>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        <section
          aria-label={t("page.webmail.receipts_title")}
          className="space-y-2"
        >
          <h3 className="font-medium">{t("page.webmail.receipts_title")}</h3>
          {receipts.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t("page.webmail.receipts_empty")}
            </p>
          )}
          <ul className="space-y-3 text-sm">
            {receipts.map((receipt) => (
              <li
                key={receipt.id}
                className="flex items-start justify-between gap-2 border-b border-border pb-2"
              >
                <div className="min-w-0 space-y-1">
                  <p className="font-medium">
                    {t("page.webmail.receipt_item", {
                      id: receipt.id,
                      time: new Date(receipt.created_at).toLocaleString(
                        i18n.language,
                      ),
                      status: t(
                        `page.webmail.receipt_status_${receipt.status}`,
                      ),
                    })}
                  </p>
                  {receipt.status === "accepted" && (
                    <p className="text-xs text-muted-foreground">
                      {t(
                        `page.webmail.receipt_copy_${receipt.sent_copy_state}`,
                      )}
                    </p>
                  )}
                  {receipt.status === "uncertain" &&
                    !receipt.draft_released && (
                      <p className="text-xs text-muted-foreground">
                        {t("page.webmail.receipt_uncertain_review")}
                      </p>
                    )}
                  {receipt.last_error && (
                    <p className="text-xs text-destructive">
                      {t(`error.${receipt.last_error}`, {
                        defaultValue: t("page.webmail.scheduled_attention"),
                      })}
                    </p>
                  )}
                </div>
                {receipt.status === "accepted" &&
                  ["pending", "unknown", "held"].includes(
                    receipt.sent_copy_state,
                  ) && (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={busy}
                      onClick={() => void onRepairReceipt(receipt.id)}
                    >
                      {t("page.webmail.repair_sent_copy")}
                    </Button>
                  )}
              </li>
            ))}
          </ul>
        </section>
        <h3 className="font-medium">
          {t("page.webmail.scheduled_section_title")}
        </h3>
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
                  {["uncertain", "held"].includes(item.status) &&
                    reviewId === item.id && (
                      <div className="mt-2 space-y-2">
                        <p>{t("page.webmail.scheduled_uncertain_review")}</p>
                        <label className="flex items-start gap-2">
                          <input
                            type="checkbox"
                            checked={reviewConfirmed}
                            onChange={(event) =>
                              setReviewConfirmed(event.target.checked)
                            }
                          />
                          <span>
                            {t("page.webmail.scheduled_uncertain_confirm")}
                          </span>
                        </label>
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          disabled={busy || !reviewConfirmed}
                          onClick={() => void onAcknowledge(item.id)}
                        >
                          {t("page.webmail.scheduled_uncertain_release")}
                        </Button>
                      </div>
                    )}
                </div>
                {["uncertain", "held"].includes(item.status) ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() => {
                      setReviewId(item.id);
                      setReviewConfirmed(false);
                      setNotice(null);
                    }}
                  >
                    {t("page.webmail.scheduled_uncertain_review_action")}
                  </Button>
                ) : item.status === "sent" &&
                  ["pending", "unknown", "held"].includes(
                    item.sent_copy_state ?? "",
                  ) ? (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void onRepair(item.id)}
                  >
                    {t("page.webmail.repair_sent_copy")}
                  </Button>
                ) : (
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
                )}
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
            disabled={
              busy || (items.length < PAGE_SIZE && receipts.length < PAGE_SIZE)
            }
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
