import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useI18nLocale } from "@/app/providers";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import type { Plan } from "./types";

export function PricingPage() {
  const { t } = useTranslation();
  const { locale } = useI18nLocale();
  const errorMessage = useApiErrorMessage();
  const [plans, setPlans] = useState<Plan[]>([]);
  const [notice, setNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setNotice("");
    request<{ plans: Plan[] }>("/api/v1/public/plans", {
      signal: controller.signal,
    })
      .then((result) => {
        if (!controller.signal.aborted) setPlans(result.plans ?? []);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [attempt, errorMessage]);
  return (
    <main className="mx-auto max-w-5xl space-y-6 p-4 sm:p-8">
      <h1 className="text-3xl font-bold">{t("pricing.title")}</h1>
      <p className="text-muted-foreground">{t("pricing.explanation")}</p>
      <Link className="underline" to="/login">
        {t("pricing.login")}
      </Link>
      {loading && <p role="status">{t("pricing.loading")}</p>}
      {notice && (
        <div role="alert">
          <p>{notice}</p>
          <Button
            variant="outline"
            onClick={() => setAttempt((value) => value + 1)}
          >
            {t("pricing.retry")}
          </Button>
        </div>
      )}
      {!loading && !notice && !plans.length && <p>{t("pricing.empty")}</p>}
      <div className="grid gap-4 md:grid-cols-2">
        {plans.map((plan) => (
          <Card key={plan.code}>
            <CardHeader>
              <CardTitle>
                {plan.name[locale] ?? plan.name.en ?? plan.code}
              </CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <p>{plan.description?.[locale] ?? plan.description?.en}</p>
              <p>
                {t("pricing.capacity", {
                  domains: plan.domain_limit,
                  mailboxes: plan.mailbox_limit,
                  daily: plan.daily_send_limit,
                })}
              </p>
              <ul className="space-y-2">
                {plan.prices?.map((price) => (
                  <li
                    key={`${price.currency}:${price.billing_cycle}`}
                    className="flex flex-wrap justify-between gap-2"
                  >
                    <span>
                      {t(`page.billing.cycle_${price.billing_cycle}`)}
                    </span>
                    <strong>
                      {(price.amount_cents / 100).toFixed(2)} {price.currency}{" "}
                      {t("pricing.cycle_total")}
                    </strong>
                  </li>
                ))}
              </ul>
              <Link className="inline-block underline" to="/billing">
                {t("pricing.review_quote")}
              </Link>
            </CardContent>
          </Card>
        ))}
      </div>
    </main>
  );
}
