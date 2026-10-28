import { RenewalEnrollment } from "./renewal-enrollment";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { Plan, Subscription } from "./types";

type Order = {
  id: number;
  order_no: string;
  status: string;
  amount_cents: number;
  currency: string;
};
type Receipt = {
  id: number;
  order_id: number;
  amount_cents: number;
  amount_cents_exact?: string;
  currency: string;
  status: string;
};
type Refund = {
  id: number;
  refund_no: string;
  status: string;
  amount_cents: number;
  currency: string;
};
type Change = {
  target_plan_code: string;
  status: string;
  effective_at: string;
};
type Domain = {
  id: number;
  fqdn: string;
  status: string;
  verified_at?: string;
};

function formatReceipt(receipt: Receipt) {
  const exact = receipt.amount_cents_exact;
  if (exact && /^\d+$/.test(exact)) {
    const digits = exact.padStart(3, "0");
    return `${digits.slice(0, -2)}.${digits.slice(-2)}`;
  }
  return (receipt.amount_cents / 100).toFixed(2);
}

export function BillingLifecycle() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [orders, setOrders] = useState<Order[]>([]);
  const [receipts, setReceipts] = useState<Receipt[]>([]);
  const [refunds, setRefunds] = useState<Refund[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [subscription, setSubscription] = useState<Subscription | null>(null);
  const [renewalRevision, setRenewalRevision] = useState(0);
  const [change, setChange] = useState<Change | null>(null);
  const [plan, setPlan] = useState("");
  const [domain, setDomain] = useState("");
  const [coupon, setCoupon] = useState("");
  const [payment, setPayment] = useState("");
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [refundKey, setRefundKey] = useState(() => crypto.randomUUID());
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(async (signal?: AbortSignal) => {
    const [o, r, s, p, d] = await Promise.all([
      request<{ orders: Order[]; payments: Receipt[] }>(
        "/api/v1/billing/orders",
        { signal },
      ),
      request<{ refunds: Refund[] }>("/api/v1/billing/refunds", { signal }),
      request<{
        subscription: Subscription | null;
        scheduled_change: Change | null;
      }>("/api/v1/billing/subscription", { signal }),
      request<{ plans: Plan[] }>("/api/v1/billing/plans", { signal }),
      request<{ domains: Domain[] }>("/api/v1/domains", { signal }),
    ]);
    if (signal?.aborted) return;
    setOrders(o.orders ?? []);
    setReceipts(o.payments ?? []);
    setRefunds(r.refunds ?? []);
    setSubscription(s.subscription);
    setRenewalRevision((revision) => revision + 1);
    setChange(s.scheduled_change);
    setPlans(p.plans ?? []);
    setDomains(
      (d.domains ?? []).filter(
        (item) => item.status === "active" && !!item.verified_at,
      ),
    );
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal).catch((error: unknown) => {
      if (!controller.signal.aborted) setNotice(errorMessage(error));
    });
    return () => controller.abort();
  }, [load, errorMessage]);
  const act = async (path: string, body: object = {}) => {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      await request(`/api/v1/billing/${path}`, { method: "POST", body });
      await load();
      setNotice(t("billing_lifecycle.saved"));
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setBusy(false);
    }
  };
  const refundable = receipts.filter((item) => item.status === "succeeded");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("billing_lifecycle.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <p className="text-sm text-muted-foreground">
          {t("billing_lifecycle.explanation")}
        </p>
        {notice && (
          <p role="status" className="break-words">
            {notice}
          </p>
        )}
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => {
            void load().catch((error: unknown) =>
              setNotice(errorMessage(error)),
            );
          }}
        >
          {t("billing_lifecycle.refresh")}
        </Button>
        {subscription && (
          <section className="space-y-2">
            <h3 className="font-semibold">
              {t("billing_lifecycle.subscription")}
            </h3>
            <p>
              {subscription.plan_id} · {subscription.status} ·{" "}
              {subscription.entitlement_source === "migration_legacy"
                ? t("billing_lifecycle.migration_preserved")
                : subscription.period_end}
            </p>
            {subscription.entitlement_source === "migration_legacy" ? (
              <p>{t("billing_lifecycle.migration_purchase")}</p>
            ) : subscription.cancel_at_period_end ? (
              <>
                <p>{t("billing_lifecycle.cancellation_scheduled")}</p>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    void act("subscription/cancel", {
                      cancel_at_period_end: false,
                    })
                  }
                >
                  {t("billing_lifecycle.resume_subscription")}
                </Button>
              </>
            ) : (
              <Button
                variant="outline"
                disabled={busy}
                onClick={() =>
                  void act("subscription/cancel", {
                    cancel_at_period_end: true,
                  })
                }
              >
                {t("billing_lifecycle.cancel_subscription")}
              </Button>
            )}
          </section>
        )}
        {renewalRevision > 0 && <RenewalEnrollment key={renewalRevision} />}
        <section className="space-y-3">
          <h3 className="font-semibold">{t("billing_lifecycle.change")}</h3>
          {change && (
            <p>
              {change.target_plan_code} · {change.status} ·{" "}
              {change.effective_at}
            </p>
          )}
          <label className="block">
            {t("billing_lifecycle.plan")}
            <select
              className="block w-full rounded-md border bg-background p-2"
              value={plan}
              onChange={(event) => setPlan(event.target.value)}
            >
              <option value="">{t("billing_lifecycle.choose")}</option>
              {plans.map((item) => (
                <option key={item.code} value={item.code}>
                  {item.name.en ?? item.code} ({item.code})
                </option>
              ))}
            </select>
          </label>
          <Button
            disabled={
              busy ||
              !plan ||
              !subscription ||
              subscription.entitlement_source === "migration_legacy"
            }
            onClick={() => void act("subscription/change", { plan_code: plan })}
          >
            {t("billing_lifecycle.schedule")}
          </Button>
          {change && ["scheduled", "blocked"].includes(change.status) && (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => void act("subscription/change/cancel")}
            >
              {t("billing_lifecycle.cancel_change")}
            </Button>
          )}
        </section>
        <section className="space-y-3">
          <h3 className="font-semibold">{t("billing_lifecycle.trial")}</h3>
          <label className="block">
            {t("billing_lifecycle.domain")}
            <select
              className="block w-full rounded-md border bg-background p-2"
              value={domain}
              onChange={(event) => setDomain(event.target.value)}
            >
              <option value="">{t("billing_lifecycle.choose")}</option>
              {domains.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.fqdn}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            {t("billing_lifecycle.trial_code")}
            <Input
              value={coupon}
              onChange={(event) => setCoupon(event.target.value)}
            />
          </label>
          <Button
            disabled={
              busy ||
              !plan ||
              !domain ||
              !coupon.trim() ||
              subscription?.entitlement_source === "migration_legacy"
            }
            onClick={() =>
              void act("trial", {
                plan_code: plan,
                domain_id: Number(domain),
                coupon_code: coupon.trim(),
              })
            }
          >
            {t("billing_lifecycle.redeem")}
          </Button>
        </section>
        <section className="space-y-2">
          <h3 className="font-semibold">{t("billing_lifecycle.orders")}</h3>
          {!orders.length && <p>{t("billing_lifecycle.empty")}</p>}
          {orders.map((order) => (
            <div
              key={order.id}
              className="flex flex-wrap items-center gap-3 rounded-md border p-3"
            >
              <span className="min-w-0 break-all">
                {order.order_no} · {order.currency}{" "}
                {(order.amount_cents / 100).toFixed(2)} · {order.status}
              </span>
              {order.status === "pending" && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => void act(`orders/${order.id}/cancel`)}
                >
                  {t("billing_lifecycle.cancel_order")}
                </Button>
              )}
            </div>
          ))}
        </section>
        <section className="space-y-3">
          <h3 className="font-semibold">{t("billing_lifecycle.refund")}</h3>
          <p className="text-sm text-muted-foreground">
            {t("billing_lifecycle.refund_explanation")}
          </p>
          <label className="block">
            {t("billing_lifecycle.receipt")}
            <select
              className="block w-full rounded-md border bg-background p-2"
              value={payment}
              onChange={(event) => {
                setPayment(event.target.value);
                setAmount("");
                setRefundKey(crypto.randomUUID());
              }}
            >
              <option value="">{t("billing_lifecycle.choose")}</option>
              {refundable.map((item) => (
                <option key={item.id} value={item.id}>
                  #{item.id} · {item.currency} {formatReceipt(item)}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            {t("billing_lifecycle.amount_minor")}
            <Input
              inputMode="numeric"
              value={amount}
              onChange={(event) => {
                setAmount(event.target.value);
                setRefundKey(crypto.randomUUID());
              }}
            />
          </label>
          <label className="block">
            {t("billing_lifecycle.reason")}
            <Input
              maxLength={2000}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <Button
            disabled={
              busy ||
              !payment ||
              !/^[1-9]\d*$/.test(amount) ||
              !Number.isSafeInteger(Number(amount)) ||
              !reason.trim()
            }
            onClick={() =>
              void act("refunds", {
                payment_id: Number(payment),
                amount_cents: Number(amount),
                reason: reason.trim(),
                refund_no: `tenant-${refundKey}`,
              })
            }
          >
            {t("billing_lifecycle.request_refund")}
          </Button>
          {refunds.map((item) => (
            <p key={item.id} className="break-all">
              {item.refund_no} · {item.currency}{" "}
              {(item.amount_cents / 100).toFixed(2)} · {item.status}
            </p>
          ))}
        </section>
      </CardContent>
    </Card>
  );
}
