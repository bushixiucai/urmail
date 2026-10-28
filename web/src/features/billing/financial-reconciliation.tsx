import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  request,
  getSessionToken,
  apiErrorSchema,
  ApiRequestError,
} from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type Fact = {
  id: number;
  order_no: string;
  status: string;
  currency: string;
  amount_cents_exact: string;
  payments: {
    id: number;
    provider: string;
    amount_cents_exact: string;
    currency: string;
    status: string;
  }[];
  refunds: {
    id: number;
    amount_cents_exact: string;
    currency: string;
    status: string;
  }[];
  terms: {
    period_start: string;
    period_end: string;
    revoked_at: string | null;
  }[];
};
export type FinancialReport = {
  items: { facts: Fact; discrepancies: string[] }[];
  totals: Record<
    string,
    {
      ordered_cents_exact: string;
      confirmed_cents_exact: string;
      refunded_cents_exact: string;
      net_cents_exact: string;
    }
  >;
  next_cursor: number;
  facts_sha256: string;
};
function financialStatusKey(
  status: string,
  kind: "order" | "payment" | "refund",
) {
  const common: Record<string, string> = {
    pending: kind === "refund" ? "finance.status.pending" : "status.pending",
    failed: "finance.status.failed",
    processing: "finance.status.processing",
    uncertain: "finance.status.uncertain",
  };
  const order: Record<string, string> = {
    paid: "status.paid",
    canceled: "status.canceled",
    refunded: "finance.reconciliation.status.refunded",
    partially_refunded: "finance.reconciliation.status.partially_refunded",
  };
  const payment: Record<string, string> = {
    succeeded: "finance.reconciliation.status.succeeded",
    refunded: "finance.reconciliation.status.refunded",
  };
  const refund: Record<string, string> = {
    approved: "finance.status.approved",
    rejected: "finance.status.rejected",
    completed: "finance.status.completed",
    requested: "finance.status.requested",
  };
  const selected = (
    kind === "order" ? order : kind === "payment" ? payment : refund
  )[status];
  if (typeof selected === "string") return selected;
  const fallback = common[status];
  return typeof fallback === "string"
    ? fallback
    : "finance.reconciliation.status.unknown";
}

export function FinancialFacts({ facts }: { facts: Fact }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-2 break-words text-sm">
      <p>
        {t("finance.reconciliation.order")} {facts.order_no} · {facts.currency}{" "}
        {facts.amount_cents_exact} ·{" "}
        {t(financialStatusKey(facts.status, "order"))}
      </p>
      <p>{t("finance.reconciliation.receipts")}</p>
      <ul>
        {facts.payments.map((p) => (
          <li key={p.id}>
            {p.provider} · {p.currency} {p.amount_cents_exact} ·{" "}
            {t(financialStatusKey(p.status, "payment"))}
          </li>
        ))}
      </ul>
      <p>{t("finance.reconciliation.refunds")}</p>
      <ul>
        {facts.refunds.map((r) => (
          <li key={r.id}>
            #{r.id} · {r.currency} {r.amount_cents_exact} ·{" "}
            {t(financialStatusKey(r.status, "refund"))}
          </li>
        ))}
      </ul>
      <p>{t("finance.reconciliation.terms")}</p>
      <ul>
        {facts.terms.map((term, i) => (
          <li key={i}>
            {term.period_start} → {term.period_end}
            {term.revoked_at ? " · " + t("finance.reconciliation.revoked") : ""}
          </li>
        ))}
      </ul>
    </div>
  );
}
export function FinancialReconciliation({
  platform = false,
}: {
  platform?: boolean;
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [tenant, setTenant] = useState("");
  const [from, setFrom] = useState(() =>
    new Date(Date.now() - 30 * 86400000).toISOString().slice(0, 10),
  );
  const [to, setTo] = useState(() =>
    new Date(Date.now() + 86400000).toISOString().slice(0, 10),
  );
  const [result, setResult] = useState<FinancialReport | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const load = async (exporting = false, after = 0) => {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      const query = new URLSearchParams({ from, to });
      if (platform) query.set("tenant_id", tenant);
      if (after) query.set("after", String(after));
      const base = platform
        ? "/api/v1/platform/financial-reconciliation"
        : "/api/v1/billing/reconciliation";
      const path = base + (exporting ? "/export" : "") + "?" + query;
      if (exporting) {
        const token = getSessionToken();
        const response = await fetch(path, {
          credentials: "same-origin",
          headers: token ? { Authorization: `Bearer ${token}` } : {},
        });
        if (!response.ok) {
          const parsed = apiErrorSchema.safeParse(
            await response.json().catch(() => null),
          );
          throw new ApiRequestError(
            response.status,
            parsed.success
              ? parsed.data
              : { error: { code: "REQUEST_FAILED" } },
          );
        }
        // Keep frozen JSON bytes intact; parsing large nested quote numbers
        // through JavaScript Number would lose financial precision.
        const url = URL.createObjectURL(await response.blob());
        const a = document.createElement("a");
        a.href = url;
        a.download = "financial-facts.json";
        a.click();
        URL.revokeObjectURL(url);
        setNotice(t("finance.reconciliation.exported"));
      } else setResult(await request<FinancialReport>(path));
    } catch (e) {
      setNotice(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("finance.reconciliation.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {t("finance.reconciliation.help")}
        </p>
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            void load();
          }}
          onChange={() => setResult(null)}
        >
          {platform && (
            <label>
              {t("finance.tenant")}
              <Input
                aria-label={t("finance.tenant")}
                type="number"
                min="1"
                required
                value={tenant}
                onChange={(e) => setTenant(e.target.value)}
              />
            </label>
          )}
          <label>
            {t("finance.reconciliation.from")}
            <Input
              aria-label={t("finance.reconciliation.from")}
              type="date"
              required
              value={from}
              onChange={(e) => setFrom(e.target.value)}
            />
          </label>
          <label>
            {t("finance.reconciliation.to")}
            <Input
              aria-label={t("finance.reconciliation.to")}
              type="date"
              required
              value={to}
              onChange={(e) => setTo(e.target.value)}
            />
          </label>
          <Button disabled={busy}>{t("finance.reconciliation.load")}</Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy || (platform && !tenant)}
            onClick={() => void load(true)}
          >
            {t("finance.reconciliation.export")}
          </Button>
        </form>
        {notice && <p role="status">{notice}</p>}
        {result && (
          <>
            <p className="text-sm">{t("finance.reconciliation.page_totals")}</p>
            <ul>
              {Object.entries(result.totals).map(([currency, total]) => (
                <li key={currency}>
                  {currency} · {t("finance.reconciliation.ordered")}{" "}
                  {total.ordered_cents_exact} ·{" "}
                  {t("finance.reconciliation.confirmed")}{" "}
                  {total.confirmed_cents_exact} ·{" "}
                  {t("finance.reconciliation.refunded")}{" "}
                  {total.refunded_cents_exact} ·{" "}
                  {t("finance.reconciliation.net")} {total.net_cents_exact}
                </li>
              ))}
            </ul>
            <ul className="divide-y rounded border">
              {result.items.map((item) => (
                <li key={item.facts.id} className="space-y-2 p-3">
                  <FinancialFacts facts={item.facts} />
                  <ul>
                    {item.discrepancies.map((issue, i) => (
                      <li key={issue + i}>
                        {t("finance.reconciliation.issue." + issue)}
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
            {!result.items.length && <p>{t("finance.empty")}</p>}
            {result.next_cursor > 0 && (
              <Button
                disabled={busy}
                onClick={() => void load(false, result.next_cursor)}
              >
                {t("finance.reconciliation.next")}
              </Button>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
export function FinancialReviewDecision({
  id,
  onDone,
  onCancel,
}: {
  id: number;
  onDone: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [action, setAction] = useState("request_refund");
  const [reason, setReason] = useState("");
  const [preview, setPreview] = useState<{
    digest: string;
    expires_at: string;
    facts: Fact;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const execute = async () => {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      if (preview) {
        await request(`/api/v1/platform/payment-reviews/${id}/resolve`, {
          method: "POST",
          body: { action, reason, preview_digest: preview.digest },
        });
        onDone();
      } else
        setPreview(
          await request(`/api/v1/platform/payment-reviews/${id}/preview`, {
            method: "POST",
            body: { action, reason },
          }),
        );
    } catch (e) {
      setPreview(null);
      setNotice(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      aria-label={t("finance.reconciliation.review")}
      className="space-y-3 rounded border p-4"
      onSubmit={(e) => {
        e.preventDefault();
        void execute();
      }}
      onChange={() => setPreview(null)}
    >
      <label>
        {t("finance.reconciliation.decision")}
        <select
          aria-label={t("finance.reconciliation.decision")}
          value={action}
          onChange={(e) => setAction(e.target.value)}
          className="block w-full rounded border p-2"
        >
          <option value="request_refund">
            {t("finance.reconciliation.request_refund")}
          </option>
          <option value="fulfill_after_prepaid">
            {t("finance.reconciliation.fulfill")}
          </option>
        </select>
      </label>
      <label>
        {t("finance.reason")}
        <textarea
          aria-label={t("finance.reason")}
          minLength={10}
          maxLength={1000}
          required
          className="block min-h-20 w-full rounded border p-2"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
      </label>
      {preview && (
        <>
          <p>{t("finance.reconciliation.preview_help")}</p>
          <FinancialFacts facts={preview.facts} />
          <p>
            {t("finance.reconciliation.expires", { time: preview.expires_at })}
          </p>
        </>
      )}
      {notice && <p role="alert">{notice}</p>}
      <Button
        disabled={
          busy ||
          reason.trim().length < 10 ||
          Boolean(preview && Date.parse(preview.expires_at) <= Date.now())
        }
      >
        {t(
          preview
            ? "finance.reconciliation.confirm"
            : "finance.reconciliation.preview",
        )}
      </Button>
      <Button
        type="button"
        variant="outline"
        disabled={busy}
        onClick={onCancel}
      >
        {t("action.cancel")}
      </Button>
    </form>
  );
}
