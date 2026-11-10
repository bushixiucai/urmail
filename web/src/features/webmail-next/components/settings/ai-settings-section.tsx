import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  getMailboxSettings,
  saveMailboxSettings,
  fetchMailboxAIModels,
  fetchMailboxAIBudget,
  type MailboxSettingsView,
  type MailboxUserAIConfig,
  type AIBudgetStatus,
} from "@/features/webmail/mailbox-api";
import {
  Sparkles,
  CheckCircle2,
  AlertCircle,
  Eye,
  EyeOff,
  RefreshCw,
  Zap,
} from "lucide-react";

export function AiSettingsSection() {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<{
    ok: boolean;
    message: string;
  } | null>(null);

  const [settings, setSettings] = useState<MailboxSettingsView | null>(null);
  const [budget, setBudget] = useState<AIBudgetStatus | null>(null);

  // Form state
  const [useCustom, setUseCustom] = useState(false);
  const [provider, setProvider] = useState<"openai" | "ollama" | "azure">(
    "openai",
  );
  const [endpoint, setEndpoint] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [keyConfigured, setKeyConfigured] = useState(false);
  const [clearApiKey, setClearApiKey] = useState(false);
  const [showKey, setShowKey] = useState(false);
  const [model, setModel] = useState("");
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [manualModelInput, setManualModelInput] = useState(false);

  const loadAll = async () => {
    try {
      setLoading(true);
      setError(null);
      setTestResult(null);

      const [settingsData, budgetData] = await Promise.all([
        getMailboxSettings(),
        fetchMailboxAIBudget().catch(() => null),
      ]);

      setSettings(settingsData);
      setBudget(budgetData);

      const aiConfig: MailboxUserAIConfig =
        (settingsData.preferences?.ai as MailboxUserAIConfig) || {};
      setUseCustom(Boolean(aiConfig.use_custom));
      setProvider(aiConfig.provider || "openai");
      setEndpoint(aiConfig.endpoint || "");
      setModel(aiConfig.model || "");
      setKeyConfigured(Boolean(aiConfig.key_configured));
      setApiKey("");
      setClearApiKey(false);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.ai.fetch_error", "Failed to load AI settings"),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadAll();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const isEndpointValid = (url: string) => {
    if (!url.trim()) return true;
    return url.startsWith("http://") || url.startsWith("https://");
  };

  const endpointError =
    endpoint.trim() && !isEndpointValid(endpoint)
      ? t(
          "settings.ai.endpoint_invalid",
          "Endpoint URL must start with http:// or https://",
        )
      : null;

  const handleClearKey = () => {
    setKeyConfigured(false);
    setClearApiKey(true);
    setApiKey("");
  };

  const handleTestConnection = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const res = await fetchMailboxAIModels({
        provider,
        endpoint: endpoint.trim() || undefined,
        model: model.trim() || undefined,
        api_key: apiKey.trim() || undefined,
      });

      const loadedModels = res.models || [];
      setAvailableModels(loadedModels);
      if (loadedModels.length > 0) {
        setManualModelInput(false);
        if (!model || !loadedModels.includes(model)) {
          setModel(loadedModels[0]);
        }
      } else {
        setManualModelInput(true);
      }
      setTestResult({
        ok: true,
        message: t("settings.ai.models_test_success", {
          count: loadedModels.length,
          defaultValue: `Connected successfully, loaded ${loadedModels.length} available models`,
        }),
      });
    } catch {
      setAvailableModels([]);
      setManualModelInput(true);
      setTestResult({
        ok: false,
        message: t(
          "settings.ai.models_test_failed",
          "Connection test failed, please check endpoint and key",
        ),
      });
    } finally {
      setTesting(false);
    }
  };

  const handleSave = async () => {
    if (!settings || saving || loading || endpointError) return;

    try {
      setSaving(true);
      setError(null);
      setSuccess(false);

      const aiPayload: MailboxUserAIConfig = {
        use_custom: useCustom,
        provider,
        endpoint: endpoint.trim(),
        model: model.trim(),
      };

      if (clearApiKey) {
        aiPayload.clear_api_key = true;
      } else if (apiKey.trim()) {
        aiPayload.api_key = apiKey.trim();
      }

      const updatedPreferences = {
        ...(settings.preferences || {}),
        ai: aiPayload,
      };

      const res = await saveMailboxSettings({
        display_name: settings.display_name,
        identity_profiles: settings.identity_profiles,
        signature_text: settings.signature_text,
        signature_html: settings.signature_html,
        preferences: updatedPreferences,
        expected_version: settings.version,
      });

      const nextSettings: MailboxSettingsView = {
        ...settings,
        version: res.version,
        preferences: updatedPreferences,
      };
      setSettings(nextSettings);

      // Reconcile key_configured state based on action
      if (apiKey.trim()) {
        setKeyConfigured(true);
        setApiKey("");
        setClearApiKey(false);
      } else if (clearApiKey) {
        setKeyConfigured(false);
        setClearApiKey(false);
      }

      setSuccess(true);
    } catch (err: unknown) {
      const is409 =
        typeof err === "object" &&
        err !== null &&
        "status" in err &&
        (err as { status: number }).status === 409;
      if (is409) {
        setError(
          t(
            "settings.ai.conflict_error",
            "Settings were modified by another client (version conflict). Please refresh and retry.",
          ),
        );
        try {
          const fresh = await getMailboxSettings();
          setSettings(fresh);
        } catch {
          // ignore refetch failure
        }
      } else {
        const msg = err instanceof Error ? err.message : String(err);
        setError(
          msg || t("settings.ai.save_failed", "Failed to save AI settings"),
        );
      }
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div
        data-testid="ai-settings-loading"
        className="animate-pulse space-y-4"
        aria-busy="true"
      >
        <div className="h-6 w-1/3 rounded bg-[var(--vellum-border)]" />
        <div className="h-4 w-1/2 rounded bg-[var(--vellum-border)]" />
        <div className="h-32 w-full rounded-lg bg-[var(--vellum-border)]" />
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="ai-settings-section">
      {/* Header */}
      <div className="border-b border-[var(--vellum-border)] pb-4 flex items-center justify-between">
        <div>
          <h3 className="text-base font-semibold text-[var(--vellum-ink)] flex items-center gap-2">
            <Sparkles className="w-5 h-5 text-[var(--brand-primary)]" />
            <span>
              {t("settings.ai.title", "AI Control & Model Configuration")}
            </span>
          </h3>
          <p className="text-xs text-[var(--vellum-ink-secondary)] mt-1">
            {t(
              "settings.ai.subtitle",
              "Configure personal BYOK model credentials or review managed AI quota.",
            )}
          </p>
        </div>
        <button
          type="button"
          onClick={loadAll}
          disabled={loading || saving}
          className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-border)] transition-colors"
          title={t("common.refresh", "Refresh")}
          aria-label={t("common.refresh", "Refresh")}
        >
          <RefreshCw className="w-4 h-4" />
        </button>
      </div>

      {/* Alerts */}
      {error && (
        <div
          role="alert"
          className="p-3 rounded-lg border border-[var(--vellum-danger-border-strong)] bg-[var(--vellum-danger-fill-soft)] flex items-start gap-2.5 text-xs text-[var(--vellum-danger-text)]"
        >
          <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
          <span>{error}</span>
        </div>
      )}

      {success && (
        <div
          role="status"
          className="p-3 rounded-lg border border-[var(--vellum-success-border)] bg-[var(--vellum-success-fill)] flex items-center gap-2.5 text-xs text-[var(--vellum-color-success)]"
        >
          <CheckCircle2 className="w-4 h-4 shrink-0" />
          <span>
            {t("settings.ai.save_success", "AI settings saved successfully")}
          </span>
        </div>
      )}

      {/* Billing Attribution Banner */}
      <div className="p-3 rounded-lg bg-[var(--vellum-ground)] border border-[var(--vellum-border)] text-xs space-y-1">
        <p className="font-semibold text-[var(--vellum-ink)] flex items-center justify-between">
          <span>
            {t("page.webmail.ai_service_selection", "Service Selection")}
          </span>
          <span className="font-normal text-[var(--vellum-ink-secondary)]">
            {useCustom
              ? t(
                  "page.webmail.ai_billing_personal",
                  "Billing: Personal BYOK credentials (no platform usage fee)",
                )
              : t(
                  "page.webmail.ai_billing_enterprise",
                  "Billing: Enterprise Account",
                )}
          </span>
        </p>
        <p className="text-[var(--vellum-ink-secondary)]">
          {useCustom
            ? t(
                "page.webmail.ai_quota_personal_unlimited",
                "Personal credentials are not subject to business quota limits.",
              )
            : t(
                "page.webmail.ai_copilot_description",
                "Assisted email analysis, drafting, and summarizing.",
              )}
        </p>
      </div>

      {/* BYOK Toggle */}
      <div className="p-4 rounded-xl border border-[var(--vellum-border)] bg-[var(--vellum-surface)] space-y-4">
        <label className="flex items-center gap-3 cursor-pointer select-none">
          <input
            type="checkbox"
            data-testid="checkbox-use-custom-ai"
            checked={useCustom}
            onChange={(e) => {
              setUseCustom(e.target.checked);
              setSuccess(false);
            }}
            className="h-4 w-4 rounded border-gray-300 text-[var(--brand-primary)] focus:ring-[var(--brand-primary)] accent-[var(--brand-primary)]"
          />
          <div>
            <span className="text-sm font-medium text-[var(--vellum-ink)]">
              {t(
                "page.webmail.ai_use_custom",
                "Use Personal AI Configuration (BYOK - Bring Your Own Key)",
              )}
            </span>
            <p className="text-xs text-[var(--vellum-ink-secondary)] mt-0.5">
              {t(
                "page.webmail.ai_use_custom_desc",
                "Bring your own AI credentials. Usage is billed to your provider account.",
              )}
            </p>
          </div>
        </label>

        {/* BYOK Configuration Form */}
        {useCustom && (
          <div className="pt-4 border-t border-[var(--vellum-border)] space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
              {/* Provider */}
              <div className="space-y-1">
                <label
                  htmlFor="ai-provider-select"
                  className="font-medium text-[var(--vellum-ink)]"
                >
                  {t("page.webmail.ai_provider_label", "Provider")}
                </label>
                <select
                  id="ai-provider-select"
                  data-testid="select-ai-provider"
                  value={provider}
                  onChange={(e) => {
                    setProvider(
                      e.target.value as "openai" | "ollama" | "azure",
                    );
                    setSuccess(false);
                  }}
                  className="h-9 w-full rounded-md border border-[var(--vellum-border)] bg-[var(--vellum-surface)] px-3 text-xs text-[var(--vellum-ink)] focus:outline-hidden focus:ring-1 focus:ring-[var(--brand-primary)]"
                >
                  <option value="openai">
                    {t(
                      "page.webmail.ai_openai_compatible",
                      "OpenAI Compatible Service",
                    )}
                  </option>
                  <option value="ollama">
                    {t(
                      "page.webmail.ai_ollama_local",
                      "Ollama (Approved Local Service)",
                    )}
                  </option>
                  <option value="azure">Azure OpenAI</option>
                </select>
              </div>

              {/* Endpoint */}
              <div className="space-y-1">
                <label
                  htmlFor="ai-endpoint-input"
                  className="font-medium text-[var(--vellum-ink)]"
                >
                  {t("page.webmail.ai_endpoint", "API Endpoint")}
                </label>
                <input
                  id="ai-endpoint-input"
                  data-testid="input-ai-endpoint"
                  type="url"
                  placeholder={
                    provider === "ollama"
                      ? "http://localhost:11434"
                      : "https://api.openai.com/v1"
                  }
                  value={endpoint}
                  onChange={(e) => {
                    setEndpoint(e.target.value);
                    setSuccess(false);
                  }}
                  className={`h-9 w-full rounded-md border px-3 text-xs text-[var(--vellum-ink)] bg-[var(--vellum-surface)] focus:outline-hidden focus:ring-1 ${
                    endpointError
                      ? "border-[var(--vellum-danger-accent)] focus:ring-[var(--vellum-danger-accent)]"
                      : "border-[var(--vellum-border)] focus:ring-[var(--brand-primary)]"
                  }`}
                />
                {endpointError && (
                  <p className="text-[11px] text-[var(--vellum-color-danger)]">
                    {endpointError}
                  </p>
                )}
              </div>

              {/* API Key (Write-Only Security Contract) */}
              <div className="sm:col-span-2 space-y-1">
                <label className="font-medium text-[var(--vellum-ink)]">
                  {t("page.webmail.ai_api_key", "API Key")}
                </label>

                {keyConfigured ? (
                  <div
                    data-testid="key-configured-block"
                    className="flex items-center justify-between p-2.5 rounded-lg border border-[var(--vellum-success-border)] bg-[var(--vellum-success-fill)]"
                  >
                    <div className="flex items-center gap-2 text-xs font-medium text-[var(--vellum-color-success)]">
                      <CheckCircle2 className="w-4 h-4 text-[var(--vellum-success-text)]" />
                      <span data-testid="badge-key-configured">
                        {t(
                          "settings.ai.key_encrypted_badge",
                          "Key encrypted and saved",
                        )}
                      </span>
                    </div>
                    <button
                      type="button"
                      data-testid="btn-clear-ai-key"
                      onClick={handleClearKey}
                      className="px-2.5 py-1 text-xs font-semibold rounded text-[var(--vellum-color-danger)] hover:bg-[var(--vellum-danger-fill)] transition-colors"
                    >
                      {t("settings.ai.clear_key_btn", "Clear Key")}
                    </button>
                  </div>
                ) : (
                  <div className="relative">
                    <input
                      data-testid="input-ai-api-key"
                      type={showKey ? "text" : "password"}
                      autoComplete="new-password"
                      placeholder="sk-..."
                      value={apiKey}
                      onChange={(e) => {
                        setApiKey(e.target.value);
                        setClearApiKey(false);
                        setSuccess(false);
                      }}
                      className="h-9 w-full rounded-md border border-[var(--vellum-border)] bg-[var(--vellum-surface)] pl-3 pr-9 font-mono text-xs text-[var(--vellum-ink)] focus:outline-hidden focus:ring-1 focus:ring-[var(--brand-primary)]"
                    />
                    <button
                      type="button"
                      onClick={() => setShowKey(!showKey)}
                      className="absolute right-2.5 top-2 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)]"
                      aria-label={showKey ? "Hide key" : "Show key"}
                    >
                      {showKey ? (
                        <EyeOff className="w-4 h-4" />
                      ) : (
                        <Eye className="w-4 h-4" />
                      )}
                    </button>
                  </div>
                )}
              </div>

              {/* Model & Test Connection */}
              <div className="sm:col-span-2 space-y-2">
                <div className="flex items-end gap-2">
                  <div className="flex-1 space-y-1">
                    <div className="flex items-center justify-between">
                      <label
                        htmlFor={
                          availableModels.length > 0 && !manualModelInput
                            ? "ai-model-select"
                            : "ai-model-input"
                        }
                        className="font-medium text-[var(--vellum-ink)]"
                      >
                        {t("page.webmail.ai_model_label", "Model Name")}
                      </label>
                      {availableModels.length > 0 && (
                        <button
                          type="button"
                          onClick={() => setManualModelInput(!manualModelInput)}
                          className="text-[11px] text-[var(--brand-primary)] hover:underline"
                        >
                          {manualModelInput
                            ? t(
                                "settings.ai.select_from_list",
                                "Select from list",
                              )
                            : t("settings.ai.manual_input", "Manual input")}
                        </button>
                      )}
                    </div>
                    {availableModels.length > 0 && !manualModelInput ? (
                      <select
                        id="ai-model-select"
                        data-testid="select-ai-model"
                        value={model}
                        onChange={(e) => {
                          setModel(e.target.value);
                          setSuccess(false);
                        }}
                        className="h-9 w-full rounded-md border border-[var(--vellum-border)] bg-[var(--vellum-surface)] px-3 font-mono text-xs text-[var(--vellum-ink)] focus:outline-hidden focus:ring-1 focus:ring-[var(--brand-primary)]"
                      >
                        {model && !availableModels.includes(model) && (
                          <option value={model}>{model}</option>
                        )}
                        {availableModels.map((m) => (
                          <option key={m} value={m}>
                            {m}
                          </option>
                        ))}
                      </select>
                    ) : (
                      <input
                        id="ai-model-input"
                        data-testid="input-ai-model"
                        list="ai-models-datalist"
                        placeholder={t(
                          "page.webmail.ai_model_placeholder",
                          "e.g. gpt-4o or deepseek-chat",
                        )}
                        value={model}
                        onChange={(e) => {
                          setModel(e.target.value);
                          setSuccess(false);
                        }}
                        className="h-9 w-full rounded-md border border-[var(--vellum-border)] bg-[var(--vellum-surface)] px-3 font-mono text-xs text-[var(--vellum-ink)] focus:outline-hidden focus:ring-1 focus:ring-[var(--brand-primary)]"
                      />
                    )}
                    <datalist id="ai-models-datalist">
                      {availableModels.map((m) => (
                        <option key={m} value={m} />
                      ))}
                    </datalist>
                  </div>

                  <button
                    type="button"
                    data-testid="btn-test-ai-models"
                    onClick={handleTestConnection}
                    disabled={testing}
                    className="h-9 px-3 rounded-md bg-[var(--vellum-ground)] border border-[var(--vellum-border)] hover:bg-[var(--vellum-border)] text-xs font-medium text-[var(--vellum-ink)] flex items-center gap-1.5 transition-colors disabled:opacity-50 shrink-0"
                  >
                    <Sparkles className="w-3.5 h-3.5 text-[var(--brand-primary)]" />
                    <span>
                      {testing
                        ? t("settings.ai.testing_connection", "Testing...")
                        : t("settings.ai.test_connection", "Test Connection")}
                    </span>
                  </button>
                </div>

                {testResult && (
                  <p
                    data-testid="test-connection-result"
                    className={`text-xs flex items-center gap-1.5 ${
                      testResult.ok
                        ? "text-[var(--vellum-color-success)]"
                        : "text-[var(--vellum-color-danger)]"
                    }`}
                  >
                    {testResult.ok ? (
                      <CheckCircle2 className="w-3.5 h-3.5 shrink-0" />
                    ) : (
                      <AlertCircle className="w-3.5 h-3.5 shrink-0" />
                    )}
                    <span>{testResult.message}</span>
                  </p>
                )}
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Quota & Budget Info Card */}
      <div
        data-testid="ai-budget-container"
        className="p-4 rounded-xl border border-[var(--vellum-border)] bg-[var(--vellum-ground)] space-y-3"
      >
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Zap className="w-4 h-4 text-[var(--brand-primary)]" />
            <h4 className="text-xs font-semibold text-[var(--vellum-ink)]">
              {t("settings.ai.budget_title", "AI Quota & Budget")}
            </h4>
          </div>
          <span className="text-[11px] px-2 py-0.5 rounded-full bg-[var(--vellum-border)] text-[var(--vellum-ink-secondary)]">
            {budget?.cost_currency || "USD"}
          </span>
        </div>

        {budget?.entitlement ? (
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 text-xs">
            <div className="p-2.5 rounded-lg bg-[var(--vellum-surface)] border border-[var(--vellum-border)]">
              <span className="text-[var(--vellum-ink-secondary)] block text-[11px]">
                {t("page.webmail.ai_quota_status", "Quota Status")}
              </span>
              <span className="font-semibold text-[var(--vellum-ink)]">
                {budget.entitlement.available
                  ? t("page.webmail.ai_quota_normal", "Active")
                  : t("page.webmail.ai_quota_exhausted", "Exhausted")}
              </span>
            </div>

            <div className="p-2.5 rounded-lg bg-[var(--vellum-surface)] border border-[var(--vellum-border)]">
              <span className="text-[var(--vellum-ink-secondary)] block text-[11px]">
                {t("ai.quota.mailbox_calls", "Mailbox Calls")}
              </span>
              <span className="font-semibold text-[var(--vellum-ink)]">
                {budget.entitlement.mailbox_calls_used != null
                  ? `${budget.entitlement.mailbox_calls_used} / ${budget.entitlement.mailbox_call_limit}`
                  : `${budget.entitlement.calls_used} / ${budget.entitlement.call_limit}`}
              </span>
            </div>

            <div className="p-2.5 rounded-lg bg-[var(--vellum-surface)] border border-[var(--vellum-border)] col-span-2 sm:col-span-1">
              <span className="text-[var(--vellum-ink-secondary)] block text-[11px]">
                {t("ai.quota.concurrency", "Concurrency Limit")}
              </span>
              <span className="font-semibold text-[var(--vellum-ink)]">
                {budget.entitlement.concurrent_reserved} /{" "}
                {budget.entitlement.concurrency_limit}
              </span>
            </div>
          </div>
        ) : (
          <p className="text-xs text-[var(--vellum-ink-secondary)]">
            {t(
              "settings.ai.unconfigured_guide",
              "No personal AI key is configured. System will use enterprise managed service (if authorized).",
            )}
          </p>
        )}
      </div>

      {/* Footer Actions */}
      <div className="flex justify-end pt-2">
        <button
          type="button"
          data-testid="btn-save-ai-settings"
          onClick={handleSave}
          disabled={saving || Boolean(endpointError)}
          className="h-9 px-5 bg-[var(--brand-primary)] hover:bg-[var(--vellum-brand-strong)] text-white text-xs font-semibold rounded-md flex items-center gap-1.5 transition-colors disabled:opacity-50"
        >
          {saving ? (
            <>
              <RefreshCw className="w-3.5 h-3.5 animate-spin" />
              <span>{t("settings.privacy.saving", "Saving settings...")}</span>
            </>
          ) : (
            <span>{t("settings.ai.save_btn", "Save Settings")}</span>
          )}
        </button>
      </div>
    </div>
  );
}
