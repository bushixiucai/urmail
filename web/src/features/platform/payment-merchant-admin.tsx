import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type AmountLimit = {
  min_cents: number;
  max_cents: number;
  settlement_currency: string;
  limits_source: string;
};
type Merchant = {
  id: number;
  provider: string;
  environment: string;
  account_id: string;
  name: string;
  enabled: boolean;
  currencies: string[];
  checkout_limits?: Record<string, AmountLimit>;
  entrypoint: string;
  ready: boolean;
  adapter_available: boolean;
};
const fields: Record<string, string[]> = {
  stripe: ["secret_key", "webhook_secret"],
  paypal: ["client_id", "client_secret", "webhook_id"],
  alipay: ["app_id", "private_key_pem", "provider_public_key_pem"],
  wechat: [
    "app_id",
    "private_key_pem",
    "provider_public_key_pem",
    "provider_key_id",
    "certificate_serial",
    "api_v3_key",
  ],
  aggregated: [],
  tax_invoice: [],
};
export function PaymentMerchantAdmin() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [merchants, setMerchants] = useState<Merchant[]>([]);
  const [selected, setSelected] = useState<Merchant | null>(null);
  const [provider, setProvider] = useState("stripe");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void request<{ merchants: Merchant[] }>(
      "/api/v1/platform/payment-merchants",
      { signal: controller.signal },
    )
      .then((d) => {
        if (!controller.signal.aborted) setMerchants(d.merchants ?? []);
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(e));
      });
    return () => controller.abort();
  }, [revision, errorMessage]);
  const optional = provider === "aggregated" || provider === "tax_invoice";
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("payment.config.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-5">
        <p className="text-sm text-muted-foreground">
          {t("payment.config.intro")}
        </p>
        {notice && (
          <p role="status" className="text-sm">
            {notice}
          </p>
        )}
        <ul className="divide-y rounded-lg border">
          {merchants.map((m) => (
            <li
              className="flex flex-wrap items-center justify-between gap-3 p-3"
              key={m.id}
            >
              <div>
                <p className="font-medium">{m.name}</p>
                <p className="text-sm text-muted-foreground">
                  {t(`payment.provider.${m.provider}`)} ·{" "}
                  {t(
                    m.enabled
                      ? "payment.config.enabled"
                      : "payment.config.disabled",
                  )}{" "}
                  ·{" "}
                  {t(
                    m.ready
                      ? "payment.config.ready"
                      : "payment.config.not_ready",
                  )}{" "}
                  · {m.currencies.join(" / ")}
                </p>
              </div>
              {m.provider === "stripe" && (
                <div className="space-y-2">
                  <p className="max-w-prose text-xs text-muted-foreground">
                    {t("billing_hosted.merchant_help")}
                  </p>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy || !m.enabled || !m.ready}
                    onClick={() => {
                      setBusy(true);
                      setNotice("");
                      void request<{ verified: boolean }>(
                        `/api/v1/platform/payment-merchants/${m.id}/recurring/verify`,
                        {
                          method: "POST",
                          body: { adapter_key: "stripe_off_session_v1" },
                        },
                      )
                        .then((result) => {
                          setNotice(
                            t(
                              result.verified
                                ? "billing_hosted.merchant_verified"
                                : "billing_hosted.merchant_unverified",
                            ),
                          );
                          setRevision((r) => r + 1);
                        })
                        .catch((e: unknown) => setNotice(errorMessage(e)))
                        .finally(() => setBusy(false));
                    }}
                  >
                    {t("billing_hosted.merchant_verify")}
                  </Button>
                  {(!m.enabled || !m.ready) && (
                    <p className="text-xs">
                      {t("billing_hosted.merchant_unverified")}
                    </p>
                  )}
                </div>
              )}
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setSelected(m);
                  setProvider(m.provider);
                }}
              >
                {t("payment.config.edit")}
              </Button>
            </li>
          ))}
        </ul>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            setSelected(null);
            setProvider("stripe");
          }}
        >
          {t("payment.config.new")}
        </Button>
        <form
          key={`${selected?.id ?? "new"}-${provider}`}
          className="grid gap-3 md:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (busy) return;
            const form = e.currentTarget;
            const data = new FormData(form);
            const credentials: Record<string, string> = {};
            for (const f of fields[provider] ?? []) {
              const value = String(data.get(f) ?? "").trim();
              if (value) credentials[f] = value;
            }
            const checkoutLimits: Record<string, AmountLimit> = {};
            for (const rawCurrency of data.getAll("currency")) {
              const currency = String(rawCurrency);
              const minimum = String(data.get(`min_${currency}`) ?? "").trim();
              const maximum = String(data.get(`max_${currency}`) ?? "").trim();
              const settlement = String(
                data.get(`settlement_${currency}`) ?? "",
              )
                .trim()
                .toUpperCase();
              if (!minimum && !maximum && !settlement) continue;
              const min = Number(minimum),
                max = Number(maximum);
              if (
                !/^[0-9]+$/.test(minimum) ||
                !/^[0-9]+$/.test(maximum) ||
                !Number.isSafeInteger(min) ||
                !Number.isSafeInteger(max) ||
                min < 1 ||
                max < min ||
                !/^[A-Z]{3}$/.test(settlement)
              ) {
                setNotice(t("payment.config.invalid_limits"));
                return;
              }
              checkoutLimits[currency] = {
                min_cents: min,
                max_cents: max,
                settlement_currency: settlement,
                limits_source: String(
                  data.get(`source_${currency}`) ?? "merchant_contract",
                ),
              };
            }
            const body = {
              checkout_limits: checkoutLimits,
              provider,
              environment: data.get("environment"),
              account_id: data.get("account_id"),
              name: data.get("name"),
              enabled: data.get("enabled") === "on",
              currencies: data.getAll("currency"),
              entrypoint: optional
                ? ""
                : provider === "stripe" || provider === "paypal"
                  ? "hosted"
                  : "native",
              ...(Object.keys(credentials).length ? { credentials } : {}),
            };
            setBusy(true);
            setNotice("");
            void request(
              selected
                ? `/api/v1/platform/payment-merchants/${selected.id}`
                : "/api/v1/platform/payment-merchants",
              { method: selected ? "PATCH" : "POST", body },
            )
              .then(() => {
                setNotice(t("payment.config.saved"));
                setSelected(null);
                setRevision((v) => v + 1);
                form.reset();
              })
              .catch((err: unknown) => setNotice(errorMessage(err)))
              .finally(() => setBusy(false));
          }}
        >
          <label className="grid gap-1 text-sm">
            {t("payment.config.provider")}
            <select
              className="h-10 rounded-md border bg-background px-3"
              name="provider"
              value={provider}
              disabled={!!selected}
              onChange={(e) => setProvider(e.currentTarget.value)}
            >
              {Object.keys(fields).map((p) => (
                <option key={p} value={p}>
                  {t(`payment.provider.${p}`)}
                </option>
              ))}
            </select>
          </label>
          <label className="grid gap-1 text-sm">
            {t("payment.config.environment")}
            <select
              className="h-10 rounded-md border bg-background px-3"
              name="environment"
              defaultValue={selected?.environment ?? "sandbox"}
              disabled={!!selected}
            >
              <option value="sandbox">{t("payment.config.sandbox")}</option>
              <option value="live">{t("payment.config.live")}</option>
            </select>
            {selected && (
              <input
                type="hidden"
                name="environment"
                value={selected.environment}
              />
            )}
          </label>
          <label className="grid gap-1 text-sm">
            {t("payment.config.account")}
            <Input
              name="account_id"
              required
              defaultValue={selected?.account_id ?? ""}
              readOnly={!!selected}
              maxLength={120}
            />
          </label>
          <label className="grid gap-1 text-sm">
            {t("payment.config.name")}
            <Input
              name="name"
              required
              defaultValue={selected?.name ?? ""}
              maxLength={200}
            />
          </label>
          <fieldset className="flex flex-wrap gap-4 md:col-span-2">
            <legend className="mb-2 text-sm">
              {t("payment.config.currencies")}
            </legend>
            {(provider === "stripe"
              ? ["CNY", "USD"]
              : provider === "paypal"
                ? ["USD"]
                : optional
                  ? []
                  : ["CNY"]
            ).map((currency) => (
              <label key={currency} className="flex gap-2 text-sm">
                <input
                  type="checkbox"
                  name="currency"
                  value={currency}
                  defaultChecked={
                    selected?.currencies.includes(currency) ?? false
                  }
                />
                {currency}
              </label>
            ))}
          </fieldset>
          {!optional && (
            <fieldset className="grid gap-3 rounded-lg border p-3 md:col-span-2">
              <legend className="px-1 text-sm">
                {t("payment.config.limits")}
              </legend>
              <p className="text-xs text-muted-foreground">
                {t("payment.config.limits_help")}
              </p>
              {(provider === "stripe"
                ? ["CNY", "USD"]
                : provider === "paypal"
                  ? ["USD"]
                  : ["CNY"]
              ).map((currency) => (
                <div className="grid gap-2 sm:grid-cols-4" key={currency}>
                  <label className="grid gap-1 text-sm">
                    {currency} · {t("payment.config.min_cents")}
                    <Input
                      name={`min_${currency}`}
                      inputMode="numeric"
                      defaultValue={
                        selected?.checkout_limits?.[currency]?.min_cents ?? ""
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t("payment.config.max_cents")}
                    <Input
                      name={`max_${currency}`}
                      inputMode="numeric"
                      defaultValue={
                        selected?.checkout_limits?.[currency]?.max_cents ?? ""
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t("payment.config.settlement_currency")}
                    <Input
                      name={`settlement_${currency}`}
                      maxLength={3}
                      defaultValue={
                        selected?.checkout_limits?.[currency]
                          ?.settlement_currency ?? ""
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t("payment.config.limits_source")}
                    <select
                      name={`source_${currency}`}
                      className="h-10 rounded-md border bg-background px-3"
                      defaultValue={
                        selected?.checkout_limits?.[currency]?.limits_source ??
                        "merchant_contract"
                      }
                    >
                      <option value="merchant_contract">
                        {t("payment.config.merchant_contract")}
                      </option>
                      {provider === "stripe" && currency === "USD" && (
                        <option value="official">
                          {t("payment.config.official")}
                        </option>
                      )}
                    </select>
                  </label>
                </div>
              ))}
            </fieldset>
          )}
          {optional ? (
            <p className="text-sm text-muted-foreground md:col-span-2">
              {t("payment.config.optional_unavailable")}
            </p>
          ) : (
            (fields[provider] ?? []).map((field) => (
              <label key={field} className="grid gap-1 text-sm">
                {t(`payment.credentials.${field}`)}
                {field.endsWith("_pem") ? (
                  <textarea
                    className="min-h-28 w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs"
                    name={field}
                    autoComplete="off"
                    maxLength={20000}
                  />
                ) : (
                  <Input
                    name={field}
                    type={
                      [
                        "secret_key",
                        "webhook_secret",
                        "client_secret",
                        "api_v3_key",
                      ].includes(field)
                        ? "password"
                        : "text"
                    }
                    autoComplete="off"
                    maxLength={2000}
                  />
                )}
              </label>
            ))
          )}
          <p className="text-xs text-muted-foreground md:col-span-2">
            {t("payment.config.credentials_help")}
          </p>
          <label className="flex items-start gap-2 text-sm md:col-span-2">
            <input
              name="enabled"
              type="checkbox"
              defaultChecked={selected?.enabled ?? false}
              disabled={optional}
            />
            {t("payment.config.enable")}
          </label>
          <Button disabled={busy}>{t("payment.config.save")}</Button>
        </form>
      </CardContent>
    </Card>
  );
}
