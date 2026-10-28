import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiRequestError, request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { HostedRenewalAuthorization } from "./hosted-renewal-authorization";

type Capability = {
  merchant_id: number;
  provider: string;
  name: string;
  environment: string;
  status: "supported" | "unsupported" | "unverified";
  reason: string;
  evidence_at: string | null;
  adapter_registered: boolean;
  merchant_approved: boolean;
};
type Binding = {
  id: number;
  merchant_id: number;
  provider: string;
  label: string;
  verified_at: string;
  expires_at: string | null;
  currency: string;
};
type Preview = {
  eligible: boolean;
  reason: string;
  amount_cents: number;
  amount_cents_exact?: string;
  currency: string;
  billing_cycle: string;
  period_start: string;
  period_end: string;
  quote_digest: string;
};
type Enrollment = {
  id: number;
  state: string;
  version: number;
  binding_id: number;
  consent_version: string;
  consented_at: string;
  revoked_at: string | null;
};
type Attempt = {
  id: number;
  state: string;
  period_end: string;
  last_error: string;
  in_flight: boolean;
  amount_cents_exact?: string | null;
  currency?: string | null;
};
export type RenewalView = {
  can_manage: boolean;
  consent_version: string;
  exempt: boolean;
  exempt_reason: string;
  capabilities: Capability[];
  bindings: Binding[];
  preview: Preview | null;
  enrollment: Enrollment | null;
  attempts: Attempt[];
};

// Do not round a signed monetary snapshot through floating point arithmetic.
function amount(
  preview: Pick<
    Preview,
    "amount_cents" | "amount_cents_exact" | "currency"
  > | null,
): string | null {
  if (!preview) return null;
  const cents =
    preview.amount_cents_exact ??
    (Number.isSafeInteger(preview.amount_cents) && preview.amount_cents >= 0
      ? String(preview.amount_cents)
      : "");
  if (!/^\d+$/.test(cents)) return null;
  const digits = cents.padStart(3, "0");
  return `${digits.slice(0, -2)}.${digits.slice(-2)} ${preview.currency}`;
}

export function RenewalEnrollment() {
  const { t, i18n } = useTranslation();
  const [data, setData] = useState<RenewalView | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const [bindingID, setBindingID] = useState("");
  const [consent, setConsent] = useState(false);
  const [cancelConsent, setCancelConsent] = useState(false);
  const load = useCallback(async (signal?: AbortSignal) => {
    const result = await request<RenewalView>("/api/v1/billing/auto-renewal", {
      signal,
    });
    if (signal?.aborted) return;
    if (
      !Array.isArray(result.capabilities) ||
      !Array.isArray(result.bindings)
    ) {
      setFailure("unavailable");
      setData(null);
      return;
    }
    setData({ ...result, attempts: result.attempts ?? [] });
    setConsent(false);
    setCancelConsent(false);
    setBindingID("");
    setFailure("");
  }, []);
  const fail = useCallback((error: unknown, fallback: string) => {
    if (
      error instanceof ApiRequestError &&
      error.status === 403 &&
      ["FORBIDDEN_ROLE", "FORBIDDEN", "BILLING_ROLE_REQUIRED"].includes(
        error.code,
      )
    ) {
      setData(null);
      setNotice("");
      setFailure("forbidden");
    } else setFailure(fallback);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal)
      .catch((error: unknown) => {
        if (!controller.signal.aborted) fail(error, "unavailable");
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [load, fail]);
  const date = (value: string | null) => {
    if (!value || Number.isNaN(Date.parse(value)))
      return t("billing_renewal.not_verified");
    return new Intl.DateTimeFormat(i18n?.language ?? "en", {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(value));
  };
  const reason = (code: string) =>
    t(`billing_renewal.reason.${code}`, {
      defaultValue: t("billing_renewal.reason.not_available"),
    });
  const usableBindings = (data?.bindings ?? []).filter(
    (binding) =>
      Number.isSafeInteger(binding.id) &&
      binding.id > 0 &&
      binding.currency === data?.preview?.currency &&
      !!binding.verified_at &&
      !Number.isNaN(Date.parse(binding.verified_at)) &&
      (!binding.expires_at || Date.parse(binding.expires_at) > Date.now()) &&
      data?.capabilities.some(
        (capability) =>
          capability.merchant_id === binding.merchant_id &&
          capability.provider === binding.provider &&
          capability.status === "supported" &&
          capability.adapter_registered &&
          capability.merchant_approved &&
          !!capability.evidence_at &&
          !Number.isNaN(Date.parse(capability.evidence_at)),
      ),
  );
  const preview = data?.preview ?? null;
  const formattedAmount = amount(preview);
  const enabled = data?.enrollment?.state === "active";
  const inFlight = data?.attempts.some((attempt) => attempt.in_flight) ?? false;
  const canEnroll =
    !loading &&
    !failure &&
    data?.can_manage === true &&
    !data.exempt &&
    preview?.eligible === true &&
    formattedAmount !== null &&
    /^[a-f0-9]{64}$/.test(preview.quote_digest) &&
    /^[A-Z]{3}$/.test(preview.currency) &&
    Number.isFinite(Date.parse(preview.period_start)) &&
    Date.parse(preview.period_end) > Date.parse(preview.period_start) &&
    !!data.consent_version &&
    usableBindings.some((binding) => String(binding.id) === bindingID);
  const act = async (cancel: boolean) => {
    if (
      busy ||
      !data ||
      (cancel ? !cancelConsent || !enabled : !consent || !canEnroll)
    )
      return;
    setBusy(true);
    setFailure("");
    setNotice("");
    try {
      const result = await request<{ in_flight?: boolean }>(
        `/api/v1/billing/auto-renewal/${cancel ? "cancel" : "enroll"}`,
        {
          method: "POST",
          body: cancel
            ? {}
            : {
                binding_id: Number(bindingID),
                consent_version: data.consent_version,
                quote_digest: preview!.quote_digest,
                confirmed: true,
              },
        },
      );
      await load();
      setNotice(
        cancel
          ? result.in_flight || inFlight
            ? "disabled_in_flight"
            : "disabled"
          : "enabled_notice",
      );
    } catch (error) {
      fail(error, "action_failed");
    } finally {
      setBusy(false);
    }
  };
  return (
    <section
      aria-label={t("billing_renewal.title")}
      className="min-w-0 space-y-4 border-t pt-5"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="font-semibold">{t("billing_renewal.title")}</h3>
        <Button
          variant="outline"
          disabled={busy || loading}
          onClick={() => {
            setLoading(true);
            void load()
              .catch((error: unknown) => fail(error, "unavailable"))
              .finally(() => setLoading(false));
          }}
        >
          {t("billing_renewal.refresh")}
        </Button>
      </div>
      <p className="max-w-prose text-sm text-muted-foreground">
        {t("billing_renewal.explanation")}
      </p>
      {loading && <p role="status">{t("state.loading")}</p>}
      {failure && (
        <p role="alert" className="text-sm text-destructive">
          {t(`billing_renewal.${failure}`)}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {t(`billing_renewal.${notice}`)}
        </p>
      )}
      {data && (
        <>
          {data.exempt && (
            <p role="status" className="text-sm">
              {t("billing_renewal.exempt", {
                reason: reason(data.exempt_reason),
              })}
            </p>
          )}
          {!data.exempt && !preview && data.exempt_reason && (
            <p className="text-sm">{reason(data.exempt_reason)}</p>
          )}
          {data.enrollment && (
            <div className="space-y-1 text-sm">
              <p>
                {t("billing_renewal.enrollment_state", {
                  state: t(`billing_renewal.state.${data.enrollment.state}`, {
                    defaultValue: t("billing_renewal.state.unknown"),
                  }),
                })}
              </p>
              <p>
                {t("billing_renewal.current_binding", {
                  label:
                    data.bindings.find(
                      (binding) => binding.id === data.enrollment?.binding_id,
                    )?.label ?? t("billing_renewal.not_verified"),
                })}
              </p>
              <p>
                {t("billing_renewal.consent_record", {
                  version: data.enrollment.consent_version,
                  at: date(data.enrollment.consented_at),
                })}
              </p>
              {data.enrollment.revoked_at && (
                <p>
                  {t("billing_renewal.revoked_at", {
                    at: date(data.enrollment.revoked_at),
                  })}
                </p>
              )}
            </div>
          )}
          {enabled &&
            (!preview?.eligible ||
              !usableBindings.some(
                (binding) => binding.id === data.enrollment?.binding_id,
              )) && (
              <p role="status" className="max-w-prose text-sm">
                {t("billing_renewal.active_unavailable")}
              </p>
            )}
          {preview && (
            <div className="space-y-1 text-sm">
              <h4 className="font-medium">
                {t("billing_renewal.next_period")}
              </h4>
              <p>
                {formattedAmount ?? t("billing_renewal.amount_unavailable")}
              </p>
              <p>
                {date(preview.period_start)} → {date(preview.period_end)}
              </p>
              <p>
                {t(
                  `page.billing.cycle_${preview.billing_cycle === "yearly" ? "annual" : preview.billing_cycle}`,
                  {
                    defaultValue: preview.billing_cycle,
                  },
                )}
              </p>
              {!preview.eligible && <p>{reason(preview.reason)}</p>}
            </div>
          )}
          <p className="text-xs text-muted-foreground sm:hidden">
            {t("billing_renewal.table_scroll")}
          </p>
          <div
            role="region"
            aria-label={t("billing_renewal.capabilities")}
            tabIndex={0}
            className="max-w-full overflow-x-auto rounded-md border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <table className="w-full text-left text-sm">
              <caption className="p-3 text-left font-medium">
                {t("billing_renewal.capabilities")}
              </caption>
              <thead>
                <tr>
                  {["channel", "support", "conditions", "evidence"].map(
                    (key) => (
                      <th
                        key={key}
                        scope="col"
                        className="px-3 py-2 font-medium"
                      >
                        {t(`billing_renewal.${key}`)}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {data.capabilities.map((capability) => (
                  <tr
                    key={capability.merchant_id}
                    className="border-t align-top"
                  >
                    <th
                      scope="row"
                      className="min-w-32 break-words px-3 py-2 font-normal"
                    >
                      {capability.name}
                      <span className="block text-xs text-muted-foreground">
                        {capability.provider} ·{" "}
                        {t(
                          `billing_renewal.environment.${capability.environment}`,
                          {
                            defaultValue: t(
                              "billing_renewal.environment.unknown",
                            ),
                          },
                        )}
                      </span>
                    </th>
                    <td className="px-3 py-2">
                      {t(`billing_renewal.support.${capability.status}`, {
                        defaultValue: t("billing_renewal.support.unverified"),
                      })}
                    </td>
                    <td className="min-w-48 px-3 py-2">
                      {capability.reason
                        ? reason(capability.reason)
                        : capability.status === "supported" &&
                            capability.adapter_registered &&
                            capability.merchant_approved
                          ? t("billing_renewal.channel_ready")
                          : reason("not_available")}
                      <span className="block text-xs text-muted-foreground">
                        {t(
                          capability.adapter_registered
                            ? "billing_renewal.adapter_ready"
                            : "billing_renewal.adapter_missing",
                        )}{" "}
                        ·{" "}
                        {t(
                          capability.merchant_approved
                            ? "billing_renewal.merchant_approved"
                            : "billing_renewal.merchant_unapproved",
                        )}
                      </span>
                    </td>
                    <td className="min-w-36 px-3 py-2">
                      {date(capability.evidence_at)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {data.capabilities.length === 0 && (
              <p className="p-3 text-sm">{t("billing_renewal.no_channels")}</p>
            )}
          </div>
          <p className="max-w-prose text-xs text-muted-foreground">
            {t("billing_renewal.evidence_boundary")}
          </p>
          {data.can_manage && !data.exempt && (
            <HostedRenewalAuthorization
              merchants={data.capabilities
                .filter(
                  (c) =>
                    c.provider === "stripe" &&
                    c.status === "supported" &&
                    c.adapter_registered &&
                    c.merchant_approved &&
                    !!c.evidence_at,
                )
                .map((c) => ({ merchant_id: c.merchant_id, name: c.name }))}
              quoteDigest={preview?.quote_digest ?? ""}
              canStart={
                !busy &&
                !loading &&
                !failure &&
                preview?.eligible === true &&
                /^[a-f0-9]{64}$/.test(preview.quote_digest)
              }
              onVerified={load}
            />
          )}
          {!enabled && (
            <form
              className="space-y-3"
              onSubmit={(event) => {
                event.preventDefault();
                void act(false);
              }}
            >
              <label className="grid max-w-lg gap-1 text-sm">
                {t("billing_renewal.binding")}
                <select
                  className="h-10 min-w-0 rounded-md border border-input bg-background px-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  value={bindingID}
                  disabled={
                    busy ||
                    loading ||
                    !!failure ||
                    data.can_manage !== true ||
                    data.exempt ||
                    !preview?.eligible
                  }
                  onChange={(event) => {
                    setBindingID(event.target.value);
                    setConsent(false);
                  }}
                >
                  <option value="">
                    {t("billing_renewal.choose_binding")}
                  </option>
                  {usableBindings.map((binding) => (
                    <option key={binding.id} value={binding.id}>
                      {binding.label} · {binding.provider} · {binding.currency}
                    </option>
                  ))}
                </select>
              </label>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t("billing_renewal.verified_only")}
              </p>
              {usableBindings.length === 0 && (
                <p className="text-sm">{t("billing_renewal.no_binding")}</p>
              )}
              {canEnroll ? (
                <>
                  <label className="flex max-w-prose items-start gap-2 text-sm">
                    <input
                      type="checkbox"
                      className="mt-1"
                      checked={consent}
                      disabled={busy || !canEnroll}
                      onChange={(event) => setConsent(event.target.checked)}
                    />
                    {t("billing_renewal.enable_consent", {
                      amount: formattedAmount,
                      at: date(preview?.period_start ?? null),
                    })}
                  </label>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {t("billing_renewal.enable_unavailable")}
                </p>
              )}
              <Button disabled={busy || !consent || !canEnroll}>
                {t("billing_renewal.enable")}
              </Button>
            </form>
          )}
          {enabled && (
            <form
              className="space-y-3"
              onSubmit={(event) => {
                event.preventDefault();
                void act(true);
              }}
            >
              <label className="flex max-w-prose items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={cancelConsent}
                  disabled={
                    busy || loading || !!failure || data.can_manage !== true
                  }
                  onChange={(event) => setCancelConsent(event.target.checked)}
                />
                {t("billing_renewal.disable_consent")}
              </label>
              <Button
                variant="outline"
                disabled={
                  busy ||
                  loading ||
                  !!failure ||
                  !cancelConsent ||
                  data.can_manage !== true
                }
              >
                {t("billing_renewal.disable")}
              </Button>
            </form>
          )}
          <div className="space-y-2">
            <h4 className="font-medium">{t("billing_renewal.attempts")}</h4>
            {inFlight && (
              <p role="status" className="text-sm">
                {t("billing_renewal.in_flight")}
              </p>
            )}
            {data.attempts.length ? (
              <ul className="divide-y text-sm">
                {data.attempts.map((attempt) => (
                  <li key={attempt.id} className="space-y-1 py-2">
                    <p>
                      {date(attempt.period_end)} ·{" "}
                      {t(`billing_renewal.attempt.${attempt.state}`, {
                        defaultValue: t("billing_renewal.state.unknown"),
                      })}
                    </p>
                    {attempt.amount_cents_exact && attempt.currency ? (
                      <p>
                        {amount({
                          amount_cents: Number.NaN,
                          amount_cents_exact: attempt.amount_cents_exact,
                          currency: attempt.currency,
                        }) ?? t("billing_renewal.attempt_amount_unavailable")}
                      </p>
                    ) : (
                      attempt.in_flight && (
                        <p>{t("billing_renewal.attempt_amount_unavailable")}</p>
                      )
                    )}
                    {attempt.last_error && <p>{reason(attempt.last_error)}</p>}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-muted-foreground">
                {t("billing_renewal.no_attempts")}
              </p>
            )}
          </div>
        </>
      )}
      <a
        href="#billing-purchase"
        className="inline-flex min-h-10 items-center text-sm underline underline-offset-4"
      >
        {t("billing_renewal.manual_renewal")}
      </a>
    </section>
  );
}
