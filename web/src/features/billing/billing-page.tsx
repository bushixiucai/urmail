import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Receipt } from "lucide-react";
import {
  checkoutDestination,
  downloadInvoice,
  request,
} from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { useI18nLocale } from "@/app/providers";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NoticeDialog } from "@/components/ui/notice-dialog";
import {
  BILLING_CYCLES,
  buildCheckoutBody,
  buildQuoteRequest,
  type BillingCycle,
  type Invoice,
  type Plan,
  type Quote,
  type Subscription,
} from "./types";

function localized(value: Record<string, string> | undefined, locale: string) {
  if (!value) return "";
  return value[locale] ?? value.en ?? Object.values(value)[0] ?? "";
}

export function BillingPage() {
  const { t } = useTranslation();
  const { locale } = useI18nLocale();
  const errorMessage = useApiErrorMessage();
  const [subscription, setSubscription] = useState<Subscription | null>(null);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [invoices, setInvoices] = useState<Invoice[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [noticeVariant, setNoticeVariant] = useState<"error" | "info">("info");
  const [submitting, setSubmitting] = useState(false);
  const [billingCycle, setBillingCycle] = useState<BillingCycle>("monthly");
  const [quote, setQuote] = useState<Quote | null>(null);
  const returnParams = new URLSearchParams(window.location.search);
  const returnNotice =
    returnParams.get("canceled") === "1"
      ? "page.billing.checkout_canceled"
      : returnParams.get("paid") === "1"
        ? "page.billing.checkout_returned"
        : null;

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      request<{ subscription: Subscription | null; plan: Plan | null }>(
        "/api/v1/billing/subscription",
        { signal: controller.signal },
      ),
      request<{ invoices: Invoice[] }>("/api/v1/billing/invoices", {
        signal: controller.signal,
      }),
      request<{ plans: Plan[] }>("/api/v1/billing/plans", {
        signal: controller.signal,
      }),
    ])
      .then(([s, i, p]) => {
        if (controller.signal.aborted) return;
        setSubscription(s.subscription);
        setPlan(s.plan);
        setInvoices(i.invoices ?? []);
        setPlans(p.plans ?? []);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setNoticeVariant("error");
        setNotice(errorMessage(err));
      });
    return () => controller.abort();
  }, [errorMessage]);

  const checkout = async (code: string) => {
    if (submitting) return;
    setSubmitting(true);
    try {
      const nextQuote = await request<Quote>("/api/v1/billing/quote", {
        method: "POST",
        body: buildQuoteRequest(code, billingCycle),
      });
      setQuote(nextQuote);
      const result = await request<{
        checkout_url?: string;
        order?: { order_no: string };
      }>("/api/v1/billing/checkout", {
        method: "POST",
        // Prefer the signed quote when one is loaded; fall back to the
        // legacy inline body otherwise.
        body: buildCheckoutBody(nextQuote, code, billingCycle),
      });
      if (result.checkout_url) {
        window.location.assign(checkoutDestination(result.checkout_url));
      } else {
        setNoticeVariant("info");
        setNotice(
          t("page.billing.manual_order", {
            number: result.order?.order_no ?? "",
          }),
        );
      }
    } catch (err: unknown) {
      setNoticeVariant("error");
      setNotice(errorMessage(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          {t("page.billing.title")}
        </h2>
        <p className="text-sm text-muted-foreground">
          {t("page.billing.subtitle")}
        </p>
      </div>

      {returnNotice && (
        <p role="status" className="rounded-md border bg-muted p-4 text-sm">
          {t(returnNotice)}
        </p>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.billing.current_plan")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {plan ? (
            <div className="space-y-1">
              <div className="text-lg font-semibold">
                {localized(plan.name, locale)}
              </div>
              <div className="text-sm text-muted-foreground">
                {t(`status.${subscription?.status}`, {
                  defaultValue: subscription?.status ?? "",
                })}{" "}
                · {plan.mailbox_limit === -1 ? "∞" : plan.mailbox_limit}{" "}
                {t("page.billing.mailboxes")} · {plan.domain_limit}{" "}
                {t("page.billing.domains")} · {plan.daily_send_limit}{" "}
                {t("page.billing.daily_send")}
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              {t("page.billing.no_plan")}
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.billing.invoices")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {invoices.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("page.billing.no_invoices")}
            </p>
          ) : (
            <div className="border rounded-md divide-y">
              {invoices.map((invoice) => (
                <div
                  key={invoice.id}
                  className="p-4 flex items-center justify-between gap-3"
                >
                  <div className="flex items-center gap-2">
                    <Receipt className="h-4 w-4 text-muted-foreground" />
                    <div>
                      <div className="text-sm font-medium">
                        {invoice.number}
                      </div>
                      <div className="text-xs text-muted-foreground">
                        {invoice.period_start} → {invoice.period_end}
                      </div>
                    </div>
                  </div>
                  <div className="text-sm text-right">
                    <div>
                      {(invoice.amount_cents / 100).toFixed(2)}{" "}
                      {invoice.currency}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {t(`status.${invoice.status}`, {
                        defaultValue: invoice.status,
                      })}
                    </div>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        void downloadInvoice(invoice.id).catch((err: unknown) =>
                          setNotice(errorMessage(err)),
                        );
                      }}
                    >
                      {t("page.billing.download_pdf")}
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.billing.choose_plan")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {t("page.billing.billing_cycle")}:
            </span>
            {BILLING_CYCLES.map((cycle) => (
              <Button
                key={cycle}
                size="sm"
                variant={billingCycle === cycle ? "default" : "outline"}
                aria-pressed={billingCycle === cycle}
                disabled={submitting}
                onClick={() => setBillingCycle(cycle)}
              >
                {t(`page.billing.cycle_${cycle}`)}
              </Button>
            ))}
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {plans.map((p) => (
              <div key={p.code} className="border rounded-md p-3 space-y-2">
                <div className="font-medium">{localized(p.name, locale)}</div>
                <div className="text-xs text-muted-foreground">
                  {p.mailbox_limit} {t("page.billing.mailboxes")} ·{" "}
                  {p.domain_limit} {t("page.billing.domains")} ·{" "}
                  {p.daily_send_limit} {t("page.billing.daily_send")}
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-sm">
                    {(p.price_cents / 100).toFixed(2)} {p.currency}
                  </span>
                  <Button
                    size="sm"
                    disabled={submitting}
                    onClick={() => checkout(p.code)}
                  >
                    {t("page.billing.select")}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      {quote && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">
              {t("page.billing.quote_summary")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="grid gap-1 text-sm sm:grid-cols-[auto_1fr]">
              <dt className="text-muted-foreground">
                {t("page.billing.cycle_" + quote.billing_cycle, {
                  defaultValue: quote.billing_cycle,
                })}
              </dt>
              <dd>
                {quote.period_start} → {quote.period_end}
              </dd>
              <dt className="text-muted-foreground">
                {t("page.billing.quote_base")}
              </dt>
              <dd>
                {(quote.base_amount_cents / 100).toFixed(2)} {quote.currency}
              </dd>
              <dt className="text-muted-foreground">
                {t("page.billing.quote_discount")}
              </dt>
              <dd>
                -{(quote.discount_cents / 100).toFixed(2)} {quote.currency}
              </dd>
              {quote.prorated_credit_cents > 0 && (
                <>
                  <dt className="text-muted-foreground">
                    {t("page.billing.quote_credit")}
                  </dt>
                  <dd>
                    -{(quote.prorated_credit_cents / 100).toFixed(2)}{" "}
                    {quote.currency}
                  </dd>
                </>
              )}
              <dt className="text-muted-foreground">
                {t("page.billing.quote_final")}
              </dt>
              <dd className="font-semibold">
                {(quote.final_amount_cents / 100).toFixed(2)} {quote.currency}
              </dd>
            </dl>
            <p className="mt-2 text-xs text-muted-foreground">
              {t("page.billing.quote_expires", { time: quote.expires_at })}
            </p>
          </CardContent>
        </Card>
      )}
      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice ?? ""}
        variant={noticeVariant}
      />
    </div>
  );
}
