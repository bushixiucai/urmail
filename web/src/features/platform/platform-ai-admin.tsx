import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type Configuration = {
  mode: string;
  provider: string;
  endpoint: string;
  model: string;
  enabled: boolean;
  credential_configured: boolean;
  input_micros_per_million: number;
  output_micros_per_million: number;
  cost_currency: string;
};
type Consent = {
  allowed: boolean;
  tenant_local_only: boolean;
  requires_confirmation: boolean;
  configuration_digest: string;
  scope_hash: string;
};
type Usage = {
  id: string | number;
  mode: string;
  provider: string;
  model: string;
  status: string;
  reserved_tokens: number | null;
  reserved_tokens_exact?: string;
  reserved_cost_micros: number;
  actual_tokens: number | null;
  actual_tokens_exact?: string;
  actual_cost_micros: number | null;
  cost_currency: string;
  updated_at: string;
};
type Metadata = {
  configured: boolean;
  configuration: Configuration | null;
  tenant_consent?: Consent;
  cloud_endpoints?: string[];
};
const selectClass = "h-10 rounded-md border bg-background px-3 text-sm";
function explicitInteger(value: FormDataEntryValue | null): number | null {
  const text = String(value ?? "").trim();
  if (!/^[0-9]+$/.test(text)) return null;
  const n = Number(text);
  return Number.isSafeInteger(n) && n >= 0 ? n : null;
}
export function PlatformAIAdmin({
  tenants,
}: {
  tenants: { id: number; name: string }[];
}) {
  const { t } = useTranslation(),
    errorMessage = useApiErrorMessage();
  const [tenant, setTenant] = useState(""),
    [metadata, setMetadata] = useState<Metadata | null>(null),
    [usage, setUsage] = useState<Usage[]>([]),
    [selected, setSelected] = useState<Usage | null>(null),
    [loading, setLoading] = useState(false),
    [busy, setBusy] = useState(false),
    [notice, setNotice] = useState(""),
    [revision, setRevision] = useState(0);
  const base = `/api/v1/platform/tenants/${tenant}/ai`;
  useEffect(() => {
    if (!tenant) return;
    const controller = new AbortController();
    setLoading(true);
    setMetadata(null);
    setUsage([]);
    setSelected(null);
    void Promise.all([
      request<Metadata>(`${base}/configuration`, { signal: controller.signal }),
      request<{ requests: Usage[] }>(`${base}/usage`, {
        signal: controller.signal,
      }),
    ])
      .then(([a, b]) => {
        if (!controller.signal.aborted) {
          setMetadata(a);
          setUsage(b.requests ?? []);
          setLoading(false);
        }
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) {
          setNotice(errorMessage(e));
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [base, tenant, revision, errorMessage]);
  async function act(path: string, body: unknown) {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      await request(path, { method: "POST", body });
      setNotice(t("ai.admin.saved"));
      setRevision((v) => v + 1);
    } catch (e) {
      setNotice(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  const cfg = metadata?.configuration,
    consent = metadata?.tenant_consent;
  return (
    <Card data-testid="platform-ai-admin">
      <CardHeader>
        <CardTitle>{t("ai.admin.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">{t("ai.admin.help")}</p>
        <label className="grid gap-1 text-sm">
          {t("ai.admin.tenant")}
          <select
            className={selectClass}
            value={tenant}
            onChange={(e) => {
              setNotice("");
              setTenant(e.target.value);
            }}
          >
            <option value="">{t("ai.admin.select_tenant")}</option>
            {tenants.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name} · #{item.id}
              </option>
            ))}
          </select>
        </label>
        {notice && <p role="status">{notice}</p>}
        {loading && <p role="status">{t("common.loading")}</p>}
        {tenant && !loading && metadata && (
          <>
            <div className="rounded-md border p-3 text-sm">
              <p>
                {t(
                  consent?.allowed
                    ? "ai.admin.consent_allowed"
                    : "ai.admin.consent_required",
                )}
              </p>
              <p>
                {t(
                  consent?.tenant_local_only
                    ? "ai.admin.local_only"
                    : "ai.admin.cloud_policy",
                )}
              </p>
              <p>
                {t(
                  cfg?.credential_configured
                    ? "ai.admin.credential_present"
                    : "ai.admin.credential_missing",
                )}
              </p>
              <p className="break-all text-xs text-muted-foreground">
                {consent?.configuration_digest}
              </p>
            </div>
            <form
              key={`${tenant}-${revision}`}
              className="grid gap-3 sm:grid-cols-2"
              onSubmit={(e) => {
                e.preventDefault();
                const form = e.currentTarget,
                  data = new FormData(form),
                  input = explicitInteger(data.get("input_rate")),
                  output = explicitInteger(data.get("output_rate")),
                  enabled = data.get("enabled") === "on";
                if (
                  input === null ||
                  output === null ||
                  (enabled && input === 0 && output === 0)
                ) {
                  setNotice(t("ai.admin.rate_invalid"));
                  return;
                }
                const body = {
                  provider: String(data.get("provider")),
                  endpoint: String(data.get("endpoint") ?? "").trim(),
                  model: String(data.get("model") ?? "").trim(),
                  api_key: String(data.get("api_key") ?? ""),
                  enabled,
                  input_micros_per_million: input,
                  output_micros_per_million: output,
                  cost_currency: String(data.get("currency")),
                };
                void act(`${base}/configuration`, body);
              }}
            >
              <label className="grid gap-1 text-sm">
                {t("ai.admin.provider")}
                <select
                  className={selectClass}
                  name="provider"
                  defaultValue={cfg?.provider ?? "openai"}
                >
                  <option value="openai">OpenAI</option>
                  <option value="azure">Azure OpenAI</option>
                </select>
              </label>
              <label className="grid gap-1 text-sm">
                {t("ai.admin.model")}
                <Input required name="model" defaultValue={cfg?.model ?? ""} />
              </label>
              <label className="grid gap-1 text-sm sm:col-span-2">
                {t("ai.admin.endpoint")}
                <Input
                  required
                  type="url"
                  name="endpoint"
                  defaultValue={cfg?.endpoint ?? ""}
                />
                <span className="break-words text-xs text-muted-foreground">
                  {t("ai.admin.allowlist")}{" "}
                  {metadata.cloud_endpoints?.join(" · ")}
                </span>
              </label>
              <label className="grid gap-1 text-sm">
                {t("ai.admin.api_key")}
                <Input name="api_key" type="password" autoComplete="off" />
              </label>
              <label className="grid gap-1 text-sm">
                {t("ai.admin.currency")}
                <select
                  name="currency"
                  required
                  className={selectClass}
                  defaultValue={cfg?.cost_currency ?? ""}
                >
                  <option value="">{t("ai.admin.select_currency")}</option>
                  <option value="CNY">CNY</option>
                  <option value="USD">USD</option>
                </select>
              </label>
              <label className="grid gap-1 text-sm">
                {t("ai.admin.input_rate")}
                <Input
                  required
                  name="input_rate"
                  inputMode="numeric"
                  defaultValue={cfg?.input_micros_per_million ?? ""}
                />
              </label>
              <label className="grid gap-1 text-sm">
                {t("ai.admin.output_rate")}
                <Input
                  required
                  name="output_rate"
                  inputMode="numeric"
                  defaultValue={cfg?.output_micros_per_million ?? ""}
                />
              </label>
              <p className="text-xs text-muted-foreground sm:col-span-2">
                {t("ai.admin.units")}
              </p>
              <label className="flex gap-2 text-sm">
                <input
                  type="checkbox"
                  name="enabled"
                  defaultChecked={cfg?.enabled ?? false}
                />
                {t("ai.admin.enable")}
              </label>
              <label className="flex gap-2 text-sm sm:col-span-2">
                <input type="checkbox" required />
                {t("ai.admin.config_confirm")}
              </label>
              <Button disabled={busy}>{t("ai.admin.save_config")}</Button>
            </form>
            <div className="space-y-2 border-t pt-3">
              <p className="text-sm text-muted-foreground">
                {t("ai.admin.health_help")}
              </p>
              <Button
                variant="outline"
                disabled={
                  busy ||
                  !consent?.allowed ||
                  consent?.tenant_local_only ||
                  !cfg?.enabled
                }
                onClick={() => {
                  if (busy) return;
                  setBusy(true);
                  void request(`${base}/health`, { method: "POST", body: {} })
                    .then(() => setNotice(t("ai.admin.health_checked")))
                    .catch((error: unknown) => setNotice(errorMessage(error)))
                    .finally(() => setBusy(false));
                }}
              >
                {t("ai.admin.health")}
              </Button>
            </div>
            <h3 className="font-medium">{t("ai.admin.pending_usage")}</h3>
            <ul className="divide-y">
              {usage.map((row) => (
                <li
                  key={row.id}
                  className="flex flex-wrap items-center justify-between gap-3 py-3 text-sm"
                >
                  <div>
                    <p className="break-all font-mono">{row.id}</p>
                    <p>
                      {t(`ai.admin.mode.${row.mode}`)} · {row.provider} ·{" "}
                      {row.model} · {t(`ai.admin.status.${row.status}`)}
                    </p>
                    <p>
                      {t("ai.admin.reserved", {
                        tokens:
                          row.reserved_tokens_exact ??
                          row.reserved_tokens ??
                          t("ai.admin.unknown"),
                        cost: row.reserved_cost_micros,
                        currency: row.cost_currency,
                      })}
                    </p>
                    <p>
                      {t("ai.admin.observed_tokens", {
                        tokens:
                          row.actual_tokens_exact ??
                          (row.actual_tokens == null
                            ? t("ai.admin.unknown")
                            : String(row.actual_tokens)),
                      })}
                    </p>
                  </div>
                  <Button variant="outline" onClick={() => setSelected(row)}>
                    {t("ai.admin.reconcile")}
                  </Button>
                </li>
              ))}
            </ul>
            {selected && (
              <form
                key={selected.id}
                className="grid gap-3 rounded-md border p-3 sm:grid-cols-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  const data = new FormData(e.currentTarget),
                    tokens = explicitInteger(data.get("tokens")),
                    cost = explicitInteger(data.get("cost_micros")),
                    status = String(data.get("status")),
                    reason = String(data.get("reason") ?? "").trim();
                  if (
                    tokens === null ||
                    cost === null ||
                    reason.length < 10 ||
                    (status === "released" && (tokens !== 0 || cost !== 0)) ||
                    ((selected.mode === "byo" || selected.mode === "local") &&
                      cost !== 0)
                  ) {
                    setNotice(t("ai.admin.usage_invalid"));
                    return;
                  }
                  void act(`${base}/usage/${selected.id}/resolve`, {
                    status,
                    tokens,
                    cost_micros: cost,
                    cost_currency: String(data.get("currency")),
                    reason,
                  });
                }}
              >
                <p className="text-sm sm:col-span-2">
                  {t("ai.admin.usage_help")}
                </p>
                <label className="grid gap-1 text-sm">
                  {t("ai.admin.decision")}
                  <select
                    className={selectClass}
                    name="status"
                    required
                    defaultValue=""
                  >
                    <option value="">{t("ai.admin.select_decision")}</option>
                    <option value="resolved">{t("ai.admin.resolved")}</option>
                    <option value="released">{t("ai.admin.released")}</option>
                  </select>
                </label>
                <label className="grid gap-1 text-sm">
                  {t("ai.admin.currency")}
                  <select
                    className={selectClass}
                    name="currency"
                    required
                    defaultValue={selected.cost_currency || ""}
                  >
                    <option value="">{t("ai.admin.select_currency")}</option>
                    <option value="CNY">CNY</option>
                    <option value="USD">USD</option>
                  </select>
                </label>
                <label className="grid gap-1 text-sm">
                  {t("ai.admin.actual_tokens")}
                  <Input required name="tokens" inputMode="numeric" />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("ai.admin.actual_cost")}
                  <Input required name="cost_micros" inputMode="numeric" />
                </label>
                <label className="grid gap-1 text-sm sm:col-span-2">
                  {t("ai.admin.reason")}
                  <textarea
                    required
                    name="reason"
                    minLength={10}
                    maxLength={1000}
                    className="min-h-20 rounded-md border bg-background p-3"
                  />
                </label>
                <label className="flex gap-2 text-sm sm:col-span-2">
                  <input required type="checkbox" />
                  {t("ai.admin.usage_confirm")}
                </label>
                <Button disabled={busy}>
                  {t("ai.admin.submit_resolution")}
                </Button>
              </form>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
