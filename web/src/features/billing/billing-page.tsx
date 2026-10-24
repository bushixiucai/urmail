import { BillingLifecycle } from "./lifecycle";
import { DocumentProfile } from "./document-profile";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import QRCode from "qrcode";
import { Input } from "@/components/ui/input";
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
  configuredPrice,
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

type PurchaseIntent = { key: string; quote: Quote; merchantID?: number };
type PaymentMethod = {
  merchant_id: number;
  provider: string;
  name: string;
  currencies: string[];
  environment: string;
};
type PurchaseDomain = { id: number; fqdn: string; status: string };
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
  const [currency, setCurrency] = useState("CNY");
  const [intent, setIntent] = useState<PurchaseIntent | null>(null);
  const [tenantID, setTenantID] = useState<number | null>(null);
  const [selfService, setSelfService] = useState(false);
  const [canManageBilling, setCanManageBilling] = useState(false);
  const [domains, setDomains] = useState<PurchaseDomain[]>([]);
  const [domainID, setDomainID] = useState(0);
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [merchantID, setMerchantID] = useState(0);
  const [couponCode, setCouponCode] = useState("");
  const [qrImage, setQRImage] = useState("");
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
      request<{
        tenant_id?: number;
        self_service?: boolean;
        can_manage_billing?: boolean;
        subscription: Subscription | null;
        plan: Plan | null;
      }>("/api/v1/billing/subscription", { signal: controller.signal }),
      request<{ plans: Plan[] }>("/api/v1/billing/plans", {
        signal: controller.signal,
      }),
    ])
      .then(async ([s, p]) => {
        if (controller.signal.aborted) return;
        setSubscription(s.subscription);
        setCanManageBilling(s.can_manage_billing !== false);
        setSelfService(s.self_service === true);
        if (s.tenant_id) {
          setTenantID(s.tenant_id);
          try {
            const saved = JSON.parse(
              sessionStorage.getItem(`urmail.purchase.${s.tenant_id}`) ??
                "null",
            ) as { key?: unknown; quote?: Quote; merchantID?: number } | null;
            if (
              saved &&
              typeof saved.key === "string" &&
              saved.quote?.tenant_id === s.tenant_id &&
              typeof saved.quote.digest === "string"
            ) {
              const restored = {
                key: saved.key,
                quote: saved.quote,
                merchantID: saved.merchantID,
              };
              setMerchantID(saved.merchantID ?? 0);
              setCurrency(saved.quote.currency);
              setBillingCycle(saved.quote.billing_cycle);
              setIntent(restored);
              setQuote(restored.quote);
            }
          } catch {
            sessionStorage.removeItem(`urmail.purchase.${s.tenant_id}`);
          }
        }
        setPlan(s.plan);
        if (s.can_manage_billing !== false) {
          const i = await request<{ invoices: Invoice[] }>(
            "/api/v1/billing/invoices",
            { signal: controller.signal },
          );
          if (!controller.signal.aborted) setInvoices(i.invoices ?? []);
        }
        setPlans(p.plans ?? []);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setNoticeVariant("error");
        setNotice(errorMessage(err));
      });
    return () => controller.abort();
  }, [errorMessage]);

  useEffect(() => {
    const controller = new AbortController();
    void request<{ domains: PurchaseDomain[] }>("/api/v1/domains", {
      signal: controller.signal,
    })
      .then((data) => {
        if (controller.signal.aborted) return;
        const owned = (data.domains ?? []).filter((d) => d.status === "active");
        setDomains(owned);
        setDomainID(owned[0]?.id ?? 0);
      })
      .catch(() => {});
    void request<{ methods: PaymentMethod[] }>(
      "/api/v1/billing/payment-methods",
      { signal: controller.signal },
    )
      .then((data) => {
        if (!controller.signal.aborted) setMethods(data.methods ?? []);
      })
      .catch(() => {});
    return () => controller.abort();
  }, []);
  const checkout = async (code: string) => {
    if (submitting) return;
    setSubmitting(true);
    try {
      const nextQuote = await request<Quote>("/api/v1/billing/quote", {
        method: "POST",
        body: {
          ...buildQuoteRequest(code, billingCycle, couponCode, currency),
          ...(domainID ? { domain_id: domainID } : {}),
        },
      });
      setQuote(nextQuote);
    } catch (err: unknown) {
      setNoticeVariant("error");
      setNotice(errorMessage(err));
    } finally {
      setSubmitting(false);
    }
  };

  const confirmCheckout = async () => {
    if (!quote || submitting || tenantID !== quote.tenant_id) return;
    setSubmitting(true);
    const activeIntent =
      intent?.quote.digest === quote.digest
        ? intent
        : { key: crypto.randomUUID(), quote, merchantID };
    setIntent(activeIntent);
    sessionStorage.setItem(
      `urmail.purchase.${quote.tenant_id}`,
      JSON.stringify(activeIntent),
    );
    try {
      const result = await request<{
        checkout_url?: string;
        qr_code?: string;
        checkout_state?: string;
        order?: { order_no: string };
      }>("/api/v1/billing/checkout", {
        method: "POST",
        // Prefer the signed quote when one is loaded; fall back to the
        // legacy inline body otherwise.
        body: {
          ...buildCheckoutBody(
            activeIntent.quote,
            activeIntent.quote.plan_code,
            activeIntent.quote.billing_cycle,
          ),
          ...(activeIntent.merchantID
            ? { merchant_id: activeIntent.merchantID }
            : {}),
        },
        idempotencyKey: activeIntent.key,
      });
      if (
        result.order &&
        "status" in result.order &&
        result.order.status === "paid"
      ) {
        sessionStorage.removeItem(`urmail.purchase.${quote.tenant_id}`);
        setIntent(null);
      }
      if (result.qr_code) {
        if (result.qr_code.length > 2048)
          throw new Error("Invalid payment destination");
        setQRImage(
          await QRCode.toDataURL(result.qr_code, { width: 256, margin: 2 }),
        );
      } else if (result.checkout_url) {
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

      {canManageBilling && <DocumentProfile />}
      {canManageBilling && <BillingLifecycle />}

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
                onClick={() => {
                  setBillingCycle(cycle);
                  setQuote(null);
                }}
              >
                {t(`page.billing.cycle_${cycle}`)}
              </Button>
            ))}
          </div>
          <div
            className="mb-4 flex flex-wrap gap-2"
            aria-label={t("page.billing.currency")}
          >
            {["CNY", "USD"].map((value) => (
              <Button
                key={value}
                size="sm"
                variant={currency === value ? "default" : "outline"}
                disabled={submitting}
                aria-pressed={currency === value}
                onClick={() => {
                  setCurrency(value);
                  setQuote(null);
                }}
              >
                {value}
              </Button>
            ))}
          </div>
          <div className="mb-4 grid gap-3 sm:grid-cols-2">
            <label className="space-y-1 text-sm">
              <span>{t("page.billing.coupon")}</span>
              <Input
                value={couponCode}
                maxLength={128}
                onChange={(e) => {
                  setCouponCode(e.target.value);
                  setQuote(null);
                }}
              />
            </label>
            <label className="space-y-1 text-sm">
              <span>{t("page.billing.payment_method")}</span>
              <select
                className="h-9 w-full rounded-md border bg-background px-3"
                value={merchantID}
                onChange={(e) => {
                  setMerchantID(Number(e.target.value));
                  setQuote(null);
                }}
              >
                <option value={0}>
                  {t("page.billing.select_payment_method")}
                </option>
                {methods
                  .filter((m) => m.currencies.includes(currency))
                  .map((m) => (
                    <option key={m.merchant_id} value={m.merchant_id}>
                      {m.name} · {m.provider}
                      {m.environment === "test"
                        ? ` · ${t("page.billing.test_method")}`
                        : ""}
                    </option>
                  ))}
              </select>
            </label>
            {selfService && (
              <label className="space-y-1 text-sm">
                <span>{t("page.billing.verified_domain")}</span>
                <select
                  className="h-9 w-full rounded-md border bg-background px-3"
                  value={domainID}
                  onChange={(e) => {
                    setDomainID(Number(e.target.value));
                    setQuote(null);
                  }}
                >
                  <option value={0}>
                    {t("page.billing.verify_domain_first")}
                  </option>
                  {domains.map((d) => (
                    <option key={d.id} value={d.id}>
                      {d.fqdn}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
          {intent && (
            <Button
              variant="outline"
              disabled={submitting}
              className="mb-4"
              onClick={() => {
                setQuote(intent.quote);
                setCurrency(intent.quote.currency);
                setBillingCycle(intent.quote.billing_cycle);
                setMerchantID(intent.merchantID ?? 0);
              }}
            >
              {t("page.billing.resume_purchase")}
            </Button>
          )}
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
                    {configuredPrice(p, billingCycle, currency) === null
                      ? t("page.billing.price_unconfigured")
                      : `${((configuredPrice(p, billingCycle, currency) ?? 0) / 100).toFixed(2)} ${currency}`}
                  </span>
                  <Button
                    size="sm"
                    disabled={
                      !canManageBilling ||
                      submitting ||
                      configuredPrice(p, billingCycle, currency) === null ||
                      (selfService && domainID === 0)
                    }
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
            <p className="mb-4" role="status">
              {t("page.billing.confirm_quote_help")}
            </p>
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
            <Button
              className="mt-4"
              disabled={
                !canManageBilling || submitting || tenantID !== quote.tenant_id
              }
              onClick={() => void confirmCheckout()}
            >
              {t(
                submitting ? "common.loading" : "page.billing.confirm_payment",
              )}
            </Button>
            <p className="mt-2 text-xs text-muted-foreground">
              {t("page.billing.quote_expires", { time: quote.expires_at })}
            </p>
          </CardContent>
        </Card>
      )}
      {qrImage && (
        <Card>
          <CardHeader>
            <CardTitle>{t("page.billing.scan_payment")}</CardTitle>
          </CardHeader>
          <CardContent>
            <img
              src={qrImage}
              width={256}
              height={256}
              alt={t("page.billing.scan_payment")}
            />
            <p className="mt-2 text-sm text-muted-foreground">
              {t("page.billing.checkout_returned")}
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
