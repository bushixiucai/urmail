import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type Review = {
  id: number;
  tenant_id: number;
  order_id: number;
  received_amount_cents: number;
  received_amount_cents_exact?: string;
  received_currency: string;
  expected_amount_cents: number;
  expected_amount_cents_exact?: string;
  expected_currency: string;
  provider: string;
  reason: string;
};
type Refund = {
  id: number;
  tenant_id: number;
  order_id: number;
  refund_no: string;
  requested_amount_cents: number;
  amount_cents: number;
  currency: string;
  status: string;
  reason: string;
  entitlement_action: string;
  last_error_code: string | null;
};
const exactMoney = (minor: string) => {
  if (!/^[0-9]+$/.test(minor)) return minor;
  const padded = minor.padStart(3, "0");
  return padded.slice(0, -2) + "." + padded.slice(-2);
};
const area =
  "min-h-20 rounded-md border border-input bg-background p-3 text-sm";
export function FinancialReviewAdmin() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [reviews, setReviews] = useState<Review[]>([]),
    [refunds, setRefunds] = useState<Refund[]>([]),
    [selected, setSelected] = useState<Review | Refund | null>(null),
    [mode, setMode] = useState<"review" | "approve" | "query">("review"),
    [revision, setRevision] = useState(0),
    [busy, setBusy] = useState(false),
    [notice, setNotice] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      request<{ reviews: Review[] }>("/api/v1/platform/payment-reviews", {
        signal: controller.signal,
      }),
      request<{ refunds: Refund[] }>("/api/v1/platform/refunds", {
        signal: controller.signal,
      }),
    ])
      .then(([a, b]) => {
        if (!controller.signal.aborted) {
          setReviews(a.reviews ?? []);
          setRefunds(b.refunds ?? []);
        }
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(e));
      });
    return () => controller.abort();
  }, [revision, errorMessage]);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("finance.review.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-5">
        <p className="text-sm text-muted-foreground">
          {t("finance.review.help")}
        </p>
        {notice && <p role="status">{notice}</p>}
        <h3 className="font-medium">{t("finance.review.receipts")}</h3>
        <ul className="divide-y rounded-md border">
          {reviews.map((r) => (
            <li key={r.id} className="space-y-2 p-3">
              <p>
                #{r.id} · {t("finance.tenant")} {r.tenant_id} ·{" "}
                {t("finance.order")} {r.order_id} · {r.provider}
              </p>
              <p>
                {t("finance.received")} {r.received_currency}{" "}
                {exactMoney(
                  r.received_amount_cents_exact ??
                    String(r.received_amount_cents),
                )}{" "}
                / {t("finance.expected")} {r.expected_currency}{" "}
                {exactMoney(
                  r.expected_amount_cents_exact ??
                    String(r.expected_amount_cents),
                )}
              </p>
              <p className="break-words text-sm">{r.reason}</p>
              <Button
                variant="outline"
                onClick={() => {
                  setSelected(r);
                  setMode("review");
                }}
              >
                {t("finance.review.handle")}
              </Button>
            </li>
          ))}
        </ul>
        {!reviews.length && (
          <p className="text-sm text-muted-foreground">{t("finance.empty")}</p>
        )}
        <h3 className="font-medium">{t("finance.refunds")}</h3>
        <ul className="divide-y rounded-md border">
          {refunds.map((r) => (
            <li
              key={r.id}
              className="flex flex-wrap items-center justify-between gap-3 p-3"
            >
              <div>
                <p>
                  {r.refund_no} · {t("finance.tenant")} {r.tenant_id} ·{" "}
                  {t("finance.order")} {r.order_id}
                </p>
                <p>
                  {r.currency} {(r.requested_amount_cents / 100).toFixed(2)} ·{" "}
                  {t(`finance.status.${r.status}`)}
                </p>
                <p className="break-words text-sm">{r.reason}</p>
                {r.last_error_code && (
                  <p className="text-sm">{r.last_error_code}</p>
                )}
              </div>
              <div className="flex gap-2">
                {r.status === "pending" && (
                  <Button
                    variant="outline"
                    onClick={() => {
                      setSelected(r);
                      setMode("approve");
                    }}
                  >
                    {t("finance.approval")}
                  </Button>
                )}
                <Button
                  variant="outline"
                  onClick={() => {
                    setSelected(r);
                    setMode("query");
                  }}
                >
                  {t("finance.query")}
                </Button>
              </div>
            </li>
          ))}
        </ul>
        {selected && (
          <form
            key={`${mode}-${selected.id}`}
            className="grid gap-3 rounded-lg border p-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (busy) return;
              const data = new FormData(e.currentTarget);
              const reason = String(data.get("reason") ?? "").trim();
              if (reason.length < 10) {
                setNotice(t("finance.reason_required"));
                return;
              }
              let path = "";
              const body: Record<string, unknown> = { reason };
              if (mode === "review") {
                path = `/api/v1/platform/payment-reviews/${selected.id}/resolve`;
                body.action = data.get("action");
              } else if (mode === "query") {
                path = `/api/v1/platform/refunds/${selected.id}/query`;
                body.confirm_observed = data.get("confirm_observed") === "on";
              } else {
                path = `/api/v1/platform/refunds/${selected.id}/approve`;
                body.decision = data.get("decision");
                body.entitlement_action = data.get("entitlement_action");
                if (body.decision === "approve") {
                  const amount = Number(data.get("amount_cents"));
                  if (!Number.isSafeInteger(amount) || amount <= 0) {
                    setNotice(t("finance.amount_invalid"));
                    return;
                  }
                  body.amount_cents = amount;
                  if (body.entitlement_action !== "none") {
                    const when = String(data.get("effective_at") ?? "");
                    const term = Number(data.get("term_order_id"));
                    if (!when || !Number.isSafeInteger(term) || term < 1) {
                      setNotice(t("finance.target_required"));
                      return;
                    }
                    body.effective_at = new Date(when).toISOString();
                    body.term_order_id = term;
                  }
                }
              }
              setBusy(true);
              void request(path, { method: "POST", body })
                .then(() => {
                  setNotice(
                    t(
                      mode === "approve"
                        ? "finance.approval_saved"
                        : mode === "query"
                          ? "finance.query_saved"
                          : "finance.review_saved",
                    ),
                  );
                  setSelected(null);
                  setRevision((v) => v + 1);
                })
                .catch((err: unknown) => setNotice(errorMessage(err)))
                .finally(() => setBusy(false));
            }}
          >
            <h3 className="font-medium">
              {t(
                mode === "review"
                  ? "finance.review.handle"
                  : mode === "query"
                    ? "finance.query"
                    : "finance.approval",
              )}{" "}
              #{selected.id}
            </h3>
            {mode === "review" && (
              <label className="grid gap-1 text-sm">
                {t("finance.action")}
                <select
                  name="action"
                  className="h-10 rounded-md border bg-background px-3"
                  defaultValue="request_refund"
                >
                  <option value="request_refund">
                    {t("finance.request_refund")}
                  </option>
                  <option value="fulfill_after_prepaid">
                    {t("finance.fulfill")}
                  </option>
                </select>
              </label>
            )}
            {mode === "approve" && (
              <>
                <label className="grid gap-1 text-sm">
                  {t("finance.decision")}
                  <select
                    name="decision"
                    className="h-10 rounded-md border bg-background px-3"
                  >
                    <option value="reject">{t("finance.reject")}</option>
                    <option value="approve">{t("finance.approve")}</option>
                  </select>
                </label>
                <label className="grid gap-1 text-sm">
                  {t("finance.amount_cents")}
                  <Input
                    name="amount_cents"
                    type="number"
                    min={1}
                    step={1}
                    defaultValue={(selected as Refund).requested_amount_cents}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("finance.entitlements")}
                  <select
                    name="entitlement_action"
                    className="h-10 rounded-md border bg-background px-3"
                    defaultValue="none"
                  >
                    <option value="none">{t("finance.none")}</option>
                    <option value="revoke_future">
                      {t("finance.revoke_future")}
                    </option>
                    <option value="terminate_current">
                      {t("finance.terminate_current")}
                    </option>
                  </select>
                </label>
                <label className="grid gap-1 text-sm">
                  {t("finance.term_order")}
                  <Input name="term_order_id" type="number" min={1} step={1} />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("finance.effective")}
                  <Input name="effective_at" type="datetime-local" />
                </label>
              </>
            )}
            {mode === "query" && (
              <>
                <p className="text-sm text-muted-foreground">
                  {t("finance.query_help")}
                </p>
                <label className="flex gap-2 text-sm">
                  <input name="confirm_observed" type="checkbox" />
                  {t("finance.confirm_observed")}
                </label>
              </>
            )}
            <label className="grid gap-1 text-sm">
              {t("finance.reason")}
              <textarea
                className={area}
                name="reason"
                minLength={10}
                maxLength={1000}
                required
              />
            </label>
            <label className="flex gap-2 text-sm">
              <input type="checkbox" required />
              {t("finance.confirm")}
            </label>
            <div className="flex gap-2">
              <Button disabled={busy}>{t("finance.submit")}</Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => setSelected(null)}
              >
                {t("common.cancel")}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  );
}
