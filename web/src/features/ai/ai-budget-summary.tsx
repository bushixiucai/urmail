import { useTranslation } from "react-i18next";
import type { AIBudgetStatus } from "./ai-budget";

export function AIBudgetSummary({ budget }: { budget: AIBudgetStatus | null }) {
  const { t, i18n } = useTranslation();
  if (!budget?.daily || !budget.monthly) return null;
  const quota = budget.entitlement;
  const source = quota?.source;
  const timezone = quota?.timezone || budget.quota_timezone || "";
  const sourceKey =
    source === "plan" || source === "configured_nonplatform"
      ? `ai.quota.source.${source}`
      : "ai.quota.source.unconfigured";
  const number = (value: number | null | undefined) =>
    value == null ? t("ai.quota.not_available") : String(value);
  const periodDate = (value: string | null | undefined) => {
    if (!value) return t("ai.quota.not_available");
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return t("ai.quota.not_available");
    try {
      return new Intl.DateTimeFormat(i18n?.language ?? "en", {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: timezone || "UTC",
      }).format(date);
    } catch {
      return value;
    }
  };
  return (
    <section
      className="min-w-0 scroll-mt-20 space-y-3 rounded-md border p-3 text-sm"
      data-testid="ai-budget-summary"
    >
      <h3 className="font-medium">{t("ai.quota.status_title")}</h3>
      <p>{t(sourceKey)}</p>
      <p>
        {t(
          quota?.available &&
            (source === "plan" || source === "configured_nonplatform")
            ? "ai.quota.available"
            : "ai.quota.unavailable",
        )}
      </p>
      {quota && (source === "plan" || source === "configured_nonplatform") && (
        <>
          <p className="break-words">
            {t("ai.quota.period", {
              start: periodDate(quota.period_start),
              end: periodDate(quota.period_end),
              timezone: timezone || t("ai.quota.not_available"),
            })}
          </p>
          <dl className="grid gap-3 sm:grid-cols-2">
            <div>
              <dt className="text-muted-foreground">
                {t("ai.quota.tenant_calls")}
              </dt>
              <dd>
                {t("ai.quota.calls_status", {
                  used: number(quota.calls_used),
                  limit: number(quota.call_limit),
                  remaining: number(quota.calls_remaining),
                })}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">
                {t("ai.quota.mailbox_calls")}
              </dt>
              <dd>
                {quota.mailbox_calls_used == null
                  ? t("ai.quota.mailbox_cap", {
                      limit: number(quota.mailbox_call_limit),
                    })
                  : t("ai.quota.calls_status", {
                      used: number(quota.mailbox_calls_used),
                      limit: number(quota.mailbox_call_limit),
                      remaining: number(quota.mailbox_calls_remaining),
                    })}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">
                {t("ai.quota.concurrency")}
              </dt>
              <dd>
                {t("ai.quota.concurrent_status", {
                  used: number(quota.concurrent_reserved),
                  limit: number(quota.concurrency_limit),
                })}
              </dd>
            </div>
          </dl>
        </>
      )}
      <div className="space-y-2 border-t pt-3">
        <h4 className="font-medium">{t("ai.quota.provider_ceilings")}</h4>
        {(["daily", "monthly"] as const).map((window) => {
          const value = budget[window];
          const ceiling = (limit: number | null) =>
            limit == null || limit <= 0
              ? t("ai.quota.no_extra_ceiling")
              : String(limit);
          return (
            <p key={window} className="break-words">
              {t(`ai.quota.${window}`)} ·{" "}
              {t("ai.quota.provider_usage", {
                tokens: number(value.tokens_used),
                token_limit: ceiling(value.tokens_limit),
                cost: number(value.cost_micros_used),
                cost_limit: ceiling(value.cost_micros_limit),
                currency: budget.cost_currency || t("ai.quota.not_available"),
              })}
            </p>
          );
        })}
      </div>
      <p className="text-xs text-muted-foreground">
        {t("ai.quota.accounting_help")}
      </p>
    </section>
  );
}
