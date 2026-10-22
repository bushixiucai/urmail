import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Receipt } from "lucide-react";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { useI18nLocale } from "@/app/providers";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NoticeDialog } from "@/components/ui/notice-dialog";

interface Plan {
  code: string;
  name: Record<string, string>;
  description?: Record<string, string>;
  price_cents: number;
  currency: string;
  mailbox_limit: number;
  domain_limit: number;
  daily_send_limit: number;
}

interface Subscription {
  plan_id: string;
  status: string;
  period_start: string;
  period_end: string;
  cancel_at_period_end: boolean;
}

interface Invoice {
  id: number;
  number: string;
  amount_cents: number;
  currency: string;
  status: string;
  period_start: string;
  period_end: string;
}

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
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    void Promise.all([
      request<{ subscription: Subscription | null; plan: Plan | null }>(
        "/api/v1/billing/subscription",
      ),
      request<{ invoices: Invoice[] }>("/api/v1/billing/invoices"),
    ])
      .then(([s, i]) => {
        setSubscription(s.subscription);
        setPlan(s.plan);
        setInvoices(i.invoices ?? []);
      })
      .catch((err: unknown) => setNotice(errorMessage(err)));
  }, [errorMessage]);

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
                {subscription?.status} · {plan.mailbox_limit}{" "}
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
                      {invoice.status}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice ?? ""}
        variant="error"
      />
    </div>
  );
}
