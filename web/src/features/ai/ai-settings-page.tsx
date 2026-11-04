import { CloudConsent, type CloudPolicy } from "./cloud-consent";
import { AIBudgetSummary } from "./ai-budget-summary";
import type { AIBudgetStatus } from "./ai-budget";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
interface Configuration {
  mode: string;
  provider: string;
  endpoint: string;
  model: string;
  enabled: boolean;
  local_only: boolean;
  credential_configured?: boolean;
}
interface Usage {
  id: number;
  mailbox_id: number;
  mode: string;
  status: string;
  cost_currency: string;
  reserved_tokens: number;
  reserved_cost_micros: number;
  actual_tokens: number | null;
  actual_tokens_exact?: string | null;
  reserved_tokens_exact?: string;
  actual_cost_micros: number | null;
}
function formatMicros(value: number): string {
  const amount = BigInt(value);
  return `${amount / 1000000n}.${(amount % 1000000n).toString().padStart(6, "0")}`;
}
export function AISettingsPage() {
  const { t } = useTranslation();
  const [config, setConfig] = useState<Configuration>({
    mode: "local",
    provider: "ollama",
    endpoint: "",
    model: "",
    enabled: false,
    local_only: true,
  });
  const [byokPolicy, setByokPolicy] = useState<
    "disabled" | "approved" | "public"
  >("approved");
  const [policy, setPolicy] = useState<CloudPolicy | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [cloud, setCloud] = useState<string[]>([]);
  const [local, setLocal] = useState<string[]>([]);
  const [key, setKey] = useState("");
  const [usage, setUsage] = useState<Usage[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [budget, setBudget] = useState<AIBudgetStatus | null>(null);
  const [budgetUnavailable, setBudgetUnavailable] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setBudgetUnavailable(false);
    Promise.all([
      request<{
        configuration: Configuration;
        consent?: CloudPolicy;
        cloud_endpoints: string[];
        local_endpoints: string[];
      }>("/api/v1/ai/configuration", { signal: controller.signal }),
      request<AIBudgetStatus>("/api/v1/ai/budget", {
        signal: controller.signal,
      }).catch(() => {
        if (!controller.signal.aborted) setBudgetUnavailable(true);
        return null;
      }),
      request<{ requests: Usage[] }>("/api/v1/ai/usage", {
        signal: controller.signal,
      }),
    ])
      .then(([settings, quota, ledger]) => {
        if (controller.signal.aborted) return;
        setBudget(quota);
        setConfig(settings.configuration);
        setPolicy(settings.consent ?? null);
        setCloud(settings.cloud_endpoints);
        setLocal(settings.local_endpoints);
        setUsage(ledger.requests);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(t("page.ai.load_failed"));
      });
    return () => controller.abort();
  }, [t, refresh]);
  const save = async () => {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await request("/api/v1/ai/configuration", {
        method: "PUT",
        body: {
          mode: config.mode,
          provider: config.provider,
          endpoint: config.endpoint,
          model: config.model,
          enabled: config.enabled,
          local_only: config.local_only,
          api_key: key,
        },
      });
      setKey("");
      setMessage(t("page.ai.saved"));
      setRefresh((value) => value + 1);
    } catch {
      setError(t("page.ai.save_failed"));
    } finally {
      setBusy(false);
    }
  };
  const health = async () => {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const res = await request<{ status: string; models?: string[] }>(
        "/api/v1/ai/health",
        {
          method: "POST",
          body: config.endpoint
            ? {
                provider: config.provider,
                endpoint: config.endpoint,
                model: config.model || "gpt-4o",
                api_key: key,
              }
            : {},
        },
      );
      if (res.models && res.models.length > 0) {
        setModels(res.models);
        setMessage(t("page.ai.models_loaded", { count: res.models.length }));
        if (!config.model && res.models[0]) {
          setConfig((prev) => ({ ...prev, model: res.models![0] }));
        }
      } else {
        setMessage(t("page.ai.reachable"));
      }
    } catch {
      setError(t("page.ai.health_failed"));
    } finally {
      setBusy(false);
    }
  };
  const endpoints = config.mode === "local" ? local : cloud;
  return (
    <main className="mx-auto max-w-4xl space-y-6 p-4 sm:p-6">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold">{t("page.ai.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("page.ai.identity")}</p>
      </header>
      <AIBudgetSummary budget={budget} />
      {budgetUnavailable && (
        <div role="alert" className="space-y-2 text-sm">
          <p>{t("ai.quota.load_failed")}</p>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => setRefresh((value) => value + 1)}
          >
            {t("ai.quota.retry")}
          </Button>
        </div>
      )}
      <CloudConsent
        policy={policy}
        reload={() => setRefresh((value) => value + 1)}
      />
      {error && (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      )}
      {message && <p role="status">{message}</p>}
      <section className="grid gap-4 rounded-md border p-4 sm:grid-cols-2">
        <label>
          {t("page.ai.mode")}
          <select
            className="mt-2 h-10 w-full rounded-md border bg-background px-3"
            value={config.mode}
            onChange={(event) => {
              const mode = event.target.value;
              setConfig({
                ...config,
                mode,
                provider: mode === "local" ? "ollama" : "openai",
                endpoint: "",
                local_only: mode === "local",
                enabled: false,
              });
              setKey("");
            }}
          >
            <option value="local">{t("page.ai.mode_local")}</option>
            <option value="byo">{t("page.ai.mode_byo")}</option>
            {config.mode === "platform" && (
              <option value="platform">{t("page.ai.mode_platform")}</option>
            )}
          </select>
        </label>
        <label>
          {t("page.ai.provider")}
          <select
            disabled={config.mode === "platform" || config.mode === "local"}
            className="mt-2 h-10 w-full rounded-md border bg-background px-3"
            value={config.provider}
            onChange={(event) => {
              setConfig({
                ...config,
                provider: event.target.value,
                enabled: false,
              });
              setKey("");
            }}
          >
            {config.mode === "local" ? (
              <option value="ollama">Ollama</option>
            ) : (
              <>
                <option value="openai">OpenAI-compatible</option>
                <option value="azure">Azure OpenAI v1</option>
              </>
            )}
          </select>
        </label>
        <label className="sm:col-span-2">
          {t("page.ai.endpoint")}
          <select
            disabled={config.mode === "platform"}
            className="mt-2 h-10 w-full rounded-md border bg-background px-3"
            value={config.endpoint ?? ""}
            onChange={(event) => {
              setConfig({
                ...config,
                endpoint: event.target.value,
                enabled: false,
              });
              setKey("");
            }}
          >
            <option value="">{t("page.ai.choose_endpoint")}</option>
            {[
              ...new Set([
                ...endpoints,
                ...(config.mode === "platform" && config.endpoint
                  ? [config.endpoint]
                  : []),
              ]),
            ].map((endpoint) => (
              <option key={endpoint} value={endpoint}>
                {endpoint}
              </option>
            ))}
          </select>
        </label>
        <div>
          <label>
            {t("page.ai.model")}
            <Input
              disabled={config.mode === "platform"}
              list="admin-ai-models-list"
              value={config.model ?? ""}
              maxLength={200}
              placeholder={
                models.length > 0
                  ? t("page.ai.select_model")
                  : "例如：deepseek-chat 或 gpt-4o"
              }
              onChange={(event) =>
                setConfig({ ...config, model: event.target.value })
              }
            />
            <datalist id="admin-ai-models-list">
              {models.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </label>
        </div>
        {config.mode === "byo" && (
          <label>
            {t("page.ai.api_key")}
            <Input
              type="password"
              autoComplete="new-password"
              value={key}
              maxLength={4096}
              placeholder={
                config.credential_configured ? t("page.ai.key_preserved") : ""
              }
              onChange={(event) => setKey(event.target.value)}
            />
          </label>
        )}
        <label className="flex gap-2">
          <input
            type="checkbox"
            disabled={config.mode === "platform"}
            checked={config.enabled}
            onChange={(event) =>
              setConfig({ ...config, enabled: event.target.checked })
            }
          />
          {t("page.ai.enabled")}
        </label>
        <p className="text-sm text-muted-foreground sm:col-span-2">
          {t("page.ai.budget_policy")}
        </p>
        <label className="sm:col-span-2">
          {t("page.ai.byok_policy")}
          <p className="text-xs text-muted-foreground mt-0.5">
            {t("page.ai.byok_policy_desc")}
          </p>
          <select
            className="mt-2 h-10 w-full rounded-md border bg-background px-3 text-sm"
            value={byokPolicy}
            onChange={(e) =>
              setByokPolicy(
                e.target.value as "disabled" | "approved" | "public",
              )
            }
          >
            <option value="approved">{t("page.ai.byok_approved")}</option>
            <option value="public">{t("page.ai.byok_public")}</option>
            <option value="disabled">{t("page.ai.byok_disabled")}</option>
          </select>
        </label>
        <div className="flex flex-wrap gap-2 sm:col-span-2">
          <Button
            disabled={
              busy ||
              config.mode === "platform" ||
              (config.mode !== "local" && config.local_only)
            }
            onClick={() => void save()}
          >
            {t("action.save")}
          </Button>
          <Button
            variant="outline"
            disabled={busy || (!config.enabled && !config.endpoint)}
            onClick={() => void health()}
          >
            {t("page.ai.test_and_fetch_models")}
          </Button>
        </div>
      </section>
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">{t("page.ai.usage")}</h2>
        <p className="text-sm text-muted-foreground">
          {t("page.ai.uncertain")}
        </p>
        {usage.length === 0 ? (
          <p>{t("page.ai.no_usage")}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead>
                <tr>
                  {["mailbox", "status", "tokens", "cost"].map((field) => (
                    <th className="p-2" key={field}>
                      {t(`page.ai.${field}`)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {usage.map((row) => (
                  <tr className="border-t" key={row.id}>
                    <td className="p-2">{row.mailbox_id}</td>
                    <td className="p-2">{t(`page.ai.status_${row.status}`)}</td>
                    <td className="p-2">
                      {row.actual_tokens_exact ??
                        row.actual_tokens ??
                        row.reserved_tokens_exact ??
                        row.reserved_tokens}
                    </td>
                    <td className="p-2">
                      {row.cost_currency}{" "}
                      {formatMicros(
                        row.actual_cost_micros ?? row.reserved_cost_micros,
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </main>
  );
}
