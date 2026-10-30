import { useMailboxAIConsent } from "./mailbox-ai-consent";
import {
  MAILBOX_AI_CONFIGURATION_CHANGED,
  previewMailboxAI,
} from "./mailbox-api";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Sparkles,
  X,
  Bot,
  FileText,
  Search,
  Settings,
  Send,
  Copy,
  Check,
  RefreshCw,
  MailCheck,
  ShieldCheck,
  Languages,
  ListTodo,
  ExternalLink,
  ChevronRight,
  AlertCircle,
  Loader2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  MailboxFolder,
  MailboxAICitation,
  MessageDetail,
  MailboxAITarget,
  MailboxAIResult,
  MailboxAIOperation,
  MailboxUserAIConfig,
  fetchMailboxAITarget,
  fetchMailboxAIContext,
  runMailboxAI,
  askMailboxAI,
  fetchMailboxAICitation,
  fetchMailboxAIModels,
  getMailboxSettings,
  saveMailboxSettings,
  fetchMailboxAIBudget,
} from "./mailbox-api";
import type { AIBudgetStatus } from "@/features/ai/ai-budget";
import { ApiRequestError } from "@/lib/api/client";

interface MailboxAIDrawerProps {
  open: boolean;
  onClose: () => void;
  folders: MailboxFolder[];
  selectedFolderEnc?: string;
  detail: MessageDetail | null;
  onCitation: (source: MailboxAICitation) => void;
  onApplyAIProposal?: (proposal: {
    subject: string;
    body: string;
  }) => void | Promise<void>;
}

function getUILanguage(i18nLang?: string): "zh-CN" | "en" | "ja" {
  const l = (i18nLang || "").toLowerCase();
  if (l.startsWith("zh")) return "zh-CN";
  if (l.startsWith("ja")) return "ja";
  return "en";
}

function detectMailLanguage(text?: string, subject?: string): "zh-CN" | "en" | "ja" {
  const sample = (subject || "") + " " + (text || "");
  if (/[\u3040-\u30ff]/.test(sample)) return "ja";
  if (/[\u4e00-\u9fa5]/.test(sample)) return "zh-CN";
  return "en";
}

function getInitialTargetLang(
  op: MailboxAIOperation,
  uiLang: string,
  detail: MessageDetail | null,
): "zh-CN" | "en" | "ja" {
  if (op === "draft_reply") {
    return detectMailLanguage(detail?.text, detail?.subject);
  }
  return getUILanguage(uiLang);
}

export function MailboxAIDrawer({
  open,
  onClose,
  folders,
  selectedFolderEnc,
  detail,
  onCitation,
  onApplyAIProposal,
}: MailboxAIDrawerProps) {
  const { t, i18n } = useTranslation();
  const {
    confirm: confirmConsent,
    cancel: cancelConsent,
    dialog: consentDialog,
  } = useMailboxAIConsent();
  const [activeTab, setActiveTab] = useState<"current" | "ask" | "settings">(
    "current",
  );

  // Global & User Target State
  const [target, setTarget] = useState<MailboxAITarget | null>(null);
  const [budget, setBudget] = useState<AIBudgetStatus | null>(null);

  // Current Message Analysis State
  const [activeOp, setActiveOp] = useState<MailboxAIOperation>("summarize");
  const [customPrompt, setCustomPrompt] = useState("");
  const [targetLang, setTargetLang] = useState<"zh-CN" | "en" | "ja">(() =>
    getInitialTargetLang("summarize", i18n?.language ?? "zh-CN", detail),
  );
  const confirmedScopes = useRef<Set<string>>(new Set());
  const [generating, setGenerating] = useState(false);
  const [result, setResult] = useState<MailboxAIResult | null>(null);
  const [genError, setGenError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [applied, setApplied] = useState(false);
  const [applying, setApplying] = useState(false);
  const [selectionText, setSelectionText] = useState("");

  // Ask / Q&A State
  const [question, setQuestion] = useState("");
  const [askFolder, setAskFolder] = useState("");
  const [asking, setAsking] = useState(false);
  const [askAnswer, setAskAnswer] = useState<{
    text: string;
    grounded: boolean;
    citations: MailboxAICitation[];
  } | null>(null);
  const [askError, setAskError] = useState<string | null>(null);

  // Settings / BYOK State
  const [userAI, setUserAI] = useState<MailboxUserAIConfig>({
    use_custom: false,
    provider: "openai",
    endpoint: "https://api.deepseek.com/v1",
    model: "deepseek-chat",
    api_key: "",
  });
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [fetchingModels, setFetchingModels] = useState(false);
  const [settingsNotice, setSettingsNotice] = useState<{
    text: string;
    error?: boolean;
  } | null>(null);
  const [savingSettings, setSavingSettings] = useState(false);

  const abortControllerRef = useRef<AbortController | null>(null);
  const askControllerRef = useRef<AbortController | null>(null);
  const targetRevision = useRef(0);
  const visible = useRef(open);
  visible.current = open;
  const invalidateTargetResults = useCallback(() => {
    abortControllerRef.current?.abort();
    askControllerRef.current?.abort();
    cancelConsent();
    confirmedScopes.current.clear();
    setGenerating(false);
    setAsking(false);
    setResult(null);
    setAskAnswer(null);
    setGenError(null);
    setAskError(null);
    setApplied(false);
    setCopied(false);
  }, [cancelConsent]);
  useEffect(() => {
    invalidateTargetResults();
  }, [target?.configuration_digest, invalidateTargetResults]);

  useEffect(
    () => () => {
      askControllerRef.current?.abort();
    },
    [],
  );
  useEffect(() => {
    if (!open) {
      targetRevision.current++;
      invalidateTargetResults();
    }
  }, [open, invalidateTargetResults]);

  // Load target configuration and user AI preferences
  const loadTargetAndSettings = useCallback(() => {
    const generation = ++targetRevision.current;
    void fetchMailboxAITarget()
      .then((next) => {
        if (visible.current && generation === targetRevision.current)
          setTarget(next);
      })
      .catch(() => {
        if (visible.current && generation === targetRevision.current)
          setTarget(null);
      });

    void getMailboxSettings()
      .then((s) => {
        if (!visible.current || generation !== targetRevision.current) return;
        const aiPref = s.preferences?.ai as MailboxUserAIConfig | undefined;
        if (aiPref) {
          setUserAI({
            use_custom: Boolean(aiPref.use_custom),
            provider: aiPref.provider || "openai",
            endpoint: aiPref.endpoint || "https://api.deepseek.com/v1",
            model: aiPref.model || "deepseek-chat",
            api_key: "",
            key_configured: Boolean(aiPref.key_configured),
            clear_api_key: false,
          });
        }
      })
      .catch(() => {});

    void fetchMailboxAIBudget()
      .then((b) => {
        if (visible.current && generation === targetRevision.current)
          setBudget(b);
      })
      .catch(() => {
        if (visible.current && generation === targetRevision.current)
          setBudget(null);
      });
  }, []);

  useEffect(() => {
    const invalidate = () => {
      targetRevision.current++;
      invalidateTargetResults();
      setTarget(null);
      if (visible.current) loadTargetAndSettings();
    };
    window.addEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, invalidate);
    return () =>
      window.removeEventListener(MAILBOX_AI_CONFIGURATION_CHANGED, invalidate);
  }, [invalidateTargetResults, loadTargetAndSettings]);

  useEffect(() => {
    if (open) {
      loadTargetAndSettings();
    }
  }, [open, loadTargetAndSettings]);

  // Load message context when selected mail changes
  useEffect(() => {
    abortControllerRef.current?.abort();
    cancelConsent();
    setGenerating(false);
    setResult(null);
    setGenError(null);
    setApplied(false);
    setSelectionText("");
    let active = true;
    if (open && detail && selectedFolderEnc && detail.uid_validity) {
      void fetchMailboxAIContext({
        folder_enc: selectedFolderEnc,
        uid: detail.uid,
        uid_validity: detail.uid_validity,
      })
        .then((ctx) => {
          if (active) setSelectionText(ctx.body);
        })
        .catch(() => {
          if (active) setSelectionText(detail.text ?? "");
        });
    }
    return () => {
      active = false;
      abortControllerRef.current?.abort();
      cancelConsent();
    };
  }, [detail, selectedFolderEnc, open, cancelConsent]);

  if (!open) return null;

  // Execute Analysis on Current Message
  const handleGenerate = async (
    op: MailboxAIOperation = activeOp,
    instruction: string = customPrompt,
  ) => {
    if (!detail || !target?.configuration_digest) return;
    abortControllerRef.current?.abort();
    const controller = new AbortController();
    abortControllerRef.current = controller;

    setGenerating(true);
    setGenError(null);
    setResult(null);
    setApplied(false);
    setActiveOp(op);

    try {
      const input = {
        operation: op,
        configuration_digest: target.configuration_digest,
        confirmed: true,
        request_key: `native-${crypto.randomUUID()}`,
        body: detail.text ?? "",
        subject: detail.subject ?? "",
        instruction: instruction.trim(),
        target_language: targetLang,
        folder_enc: selectedFolderEnc,
        uid: detail.uid,
        uid_validity: detail.uid_validity,
        selection: selectionText || (detail.text ?? ""),
      };
      const preview = await previewMailboxAI(
        "assist",
        input,
        controller.signal,
      );
      const scopeKey = `assist:${target.configuration_digest}:${preview.content_digest}`;
      if (!confirmedScopes.current.has(scopeKey)) {
        const approved = await confirmConsent(
          { ...target, ...preview },
          controller.signal,
        );
        if (!approved) return;
        confirmedScopes.current.add(scopeKey);
      }
      const response = await runMailboxAI(
        { ...input, content_digest: preview.content_digest },
        controller.signal,
      );
      if (!controller.signal.aborted) {
        setResult(response.result);
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        setGenError(
          err instanceof ApiRequestError
            ? t(`error.${err.code}`, {
                defaultValue: t("page.webmail.ai_failed"),
              })
            : t("page.webmail.ai_failed"),
        );
      }
    } finally {
      if (!controller.signal.aborted) {
        setGenerating(false);
      }
    }
  };

  // Execute Mailbox Q&A Search
  const handleAsk = async (q: string = question) => {
    if (!q.trim() || !target?.configuration_digest) return;
    askControllerRef.current?.abort();
    const controller = new AbortController();
    askControllerRef.current = controller;
    setAsking(true);
    setAskError(null);
    setAskAnswer(null);

    try {
      const input = {
        question: q.trim(),
        configuration_digest: target.configuration_digest,
        confirmed: true,
        folder_enc: askFolder || undefined,
      };
      const preview = await previewMailboxAI(
        "search-ask",
        input,
        controller.signal,
      );
      const scopeKey = `ask:${target.configuration_digest}:${preview.content_digest}`;
      if (!confirmedScopes.current.has(scopeKey)) {
        const approved = await confirmConsent(
          { ...target, ...preview },
          controller.signal,
        );
        if (!approved) return;
        confirmedScopes.current.add(scopeKey);
      }
      const response = await askMailboxAI(
        {
          ...input,
          content_digest: preview.content_digest,
        },
        controller.signal,
      );
      const citations = await Promise.all(
        response.citations.map((id) =>
          fetchMailboxAICitation(id, controller.signal),
        ),
      );
      if (controller.signal.aborted) return;
      setAskAnswer({
        text: response.text,
        grounded: response.grounded,
        citations,
      });
    } catch (err) {
      if (controller.signal.aborted) return;
      setAskError(
        err instanceof ApiRequestError
          ? t(`error.${err.code}`, {
              defaultValue: t("page.webmail.ai_failed"),
            })
          : t("page.webmail.ai_failed"),
      );
    } finally {
      if (!controller.signal.aborted) setAsking(false);
    }
  };

  // Test and Fetch Models in Settings
  const handleFetchModels = async () => {
    setFetchingModels(true);
    setSettingsNotice(null);
    try {
      const res = await fetchMailboxAIModels({
        provider: userAI.provider,
        endpoint: userAI.endpoint,
        api_key: userAI.api_key,
        model: userAI.model || "gpt-4o",
      });
      if (res.models && res.models.length > 0) {
        setAvailableModels(res.models);
        setSettingsNotice({
          text: t("page.webmail.ai_models_count", { count: res.models.length }),
        });
        if (!userAI.model || !res.models.includes(userAI.model)) {
          setUserAI((prev) => ({ ...prev, model: res.models[0] }));
        }
      } else {
        setSettingsNotice({ text: t("page.ai.reachable") });
      }
    } catch {
      setSettingsNotice({ text: t("page.ai.health_failed"), error: true });
    } finally {
      setFetchingModels(false);
    }
  };

  // Save Settings
  const handleSaveSettings = async () => {
    setSavingSettings(true);
    setSettingsNotice(null);
    try {
      const current = await getMailboxSettings();
      const updatedPrefs = {
        ...current.preferences,
        ai: userAI,
      };
      await saveMailboxSettings({
        display_name: current.display_name,
        signature_text: current.signature_text,
        signature_html: current.signature_html,
        identity_profiles: current.identity_profiles,
        preferences: updatedPrefs,
        expected_version: current.version,
      });
      invalidateTargetResults();
      setSettingsNotice({ text: t("page.webmail.ai_custom_saved") });
    } catch {
      setSettingsNotice({ text: t("page.ai.save_failed"), error: true });
    } finally {
      setSavingSettings(false);
    }
  };

  const copyResultText = async (text: string) => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const el = document.createElement("textarea");
        el.value = text;
        document.body.appendChild(el);
        el.select();
        document.execCommand("copy");
        document.body.removeChild(el);
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Graceful fallback on clipboard permission denial
    }
  };

  const resultDisplayText =
    result?.body ??
    result?.draft ??
    result?.translation ??
    result?.summary ??
    (result?.category
      ? t(`page.webmail.ai_category_${result.category}`)
      : (result?.reasons?.join("\n") ?? ""));

  return (
    <aside
      className="fixed inset-x-0 top-0 bottom-[calc(4rem+env(safe-area-inset-bottom))] w-full max-w-full border-l bg-card flex flex-col min-h-0 shadow-xl z-30 select-none md:static md:w-84 lg:w-96 md:h-full md:shrink-0"
      aria-label={t("page.webmail.ai_copilot")}
    >
      {consentDialog}
      {/* 1. Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b bg-muted/20 shrink-0">
        <div className="flex items-center gap-2">
          <div className="h-7 w-7 rounded-lg bg-primary/10 text-primary flex items-center justify-center font-bold">
            <Sparkles className="h-4 w-4" />
          </div>
          <div>
            <h3 className="text-sm font-semibold flex items-center gap-1.5">
              <span>{t("page.webmail.ai_copilot")}</span>
              {target?.is_custom ? (
                <span className="text-[10px] font-normal px-1.5 py-0.2 rounded bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20">
                  {t("page.webmail.ai_custom_badge")}
                </span>
              ) : target?.enabled ? (
                <span className="text-[10px] font-normal px-1.5 py-0.2 rounded bg-primary/10 text-primary border border-primary/20">
                  {t("page.webmail.ai_managed_badge")}
                </span>
              ) : null}
            </h3>
            <p className="text-[11px] text-muted-foreground truncate max-w-[200px]">
              {target?.model
                ? t("page.webmail.ai_model_current", { model: target.model })
                : t("page.webmail.ai_copilot_description")}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-1">
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-7 w-7 p-0 text-muted-foreground hover:text-foreground cursor-pointer"
            onClick={onClose}
            aria-label={t("action.close")}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {/* 2. Top Segmented Tabs */}
      <div className="flex border-b px-3 pt-2 gap-1 shrink-0 bg-muted/10">
        <Button
          type="button"
          size="sm"
          variant={activeTab === "current" ? "secondary" : "ghost"}
          className={
            "h-8 text-xs gap-1.5 flex-1 " +
            (activeTab === "current"
              ? "font-semibold bg-background shadow-xs text-foreground"
              : "text-muted-foreground")
          }
          onClick={() => setActiveTab("current")}
        >
          <FileText className="h-3.5 w-3.5" />
          {t("page.webmail.ai_tab_current")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant={activeTab === "ask" ? "secondary" : "ghost"}
          className={
            "h-8 text-xs gap-1.5 flex-1 " +
            (activeTab === "ask"
              ? "font-semibold bg-background shadow-xs text-foreground"
              : "text-muted-foreground")
          }
          onClick={() => setActiveTab("ask")}
        >
          <Search className="h-3.5 w-3.5" />
          {t("page.webmail.ai_tab_ask")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant={activeTab === "settings" ? "secondary" : "ghost"}
          className={
            "h-8 text-xs px-2.5 " +
            (activeTab === "settings"
              ? "font-semibold bg-background shadow-xs text-foreground"
              : "text-muted-foreground")
          }
          title={t("page.webmail.ai_tab_config")}
          onClick={() => setActiveTab("settings")}
        >
          <Settings className="h-3.5 w-3.5" />
        </Button>
      </div>

      {/* Service / Quota / Billing Attribution Banner */}
      <div className="px-3.5 py-2 border-b bg-muted/15 text-xs space-y-1 shrink-0">
        {(() => {
          const isExhausted =
            budget?.entitlement != null &&
            (!budget.entitlement.available ||
              (budget.entitlement.calls_remaining <= 0 &&
                budget.entitlement.call_limit > 0));
          const isEnterprise =
            target?.provider === "ollama" || budget?.mode === "byo";

          return (
            <>
              <div className="flex items-center justify-between">
                <span className="font-medium text-foreground flex items-center gap-1.5">
                  <span
                    className={
                      "inline-block h-2 w-2 rounded-full " +
                      (target?.is_custom
                        ? "bg-amber-500"
                        : isExhausted
                          ? "bg-rose-500"
                          : "bg-emerald-500")
                    }
                  />
                  {target?.is_custom
                    ? t("page.webmail.ai_service_personal")
                    : isEnterprise
                      ? t("page.webmail.ai_service_enterprise")
                      : t("page.webmail.ai_service_platform")}
                </span>
                <span className="text-[11px] text-muted-foreground">
                  {target?.is_custom
                    ? t("page.webmail.ai_billing_personal")
                    : isEnterprise
                      ? t("page.webmail.ai_billing_enterprise")
                      : t("page.webmail.ai_billing_platform")}
                </span>
              </div>
              {!target?.is_custom && isExhausted ? (
                <div className="p-2 rounded bg-destructive/10 text-destructive text-[11px] flex flex-col gap-1">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold">
                      {t("page.webmail.ai_quota_exhausted")}
                    </span>
                    <button
                      type="button"
                      className="text-primary underline hover:no-underline font-medium cursor-pointer"
                      onClick={() => setActiveTab("settings")}
                    >
                      {t("page.webmail.ai_switch_to_personal")}
                    </button>
                  </div>
                  <p className="text-muted-foreground">
                    {budget?.entitlement?.period_end
                      ? t("page.webmail.ai_quota_recovery", {
                          time: new Date(
                            budget.entitlement.period_end,
                          ).toLocaleTimeString([], {
                            hour: "2-digit",
                            minute: "2-digit",
                          }),
                        })
                      : t("page.webmail.ai_quota_recovery_tomorrow")}
                  </p>
                </div>
              ) : !target?.is_custom && budget ? (
                <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                  <span>{t("page.webmail.ai_quota_normal")}</span>
                  {budget.entitlement?.period_start &&
                  budget.entitlement?.period_end ? (
                    <span>
                      {t("page.webmail.ai_quota_period", {
                        start: new Date(
                          budget.entitlement.period_start,
                        ).toLocaleDateString(),
                        end: new Date(
                          budget.entitlement.period_end,
                        ).toLocaleDateString(),
                      })}
                    </span>
                  ) : null}
                </div>
              ) : null}
            </>
          );
        })()}
      </div>

      {/* 3. Main Content Body */}
      <div className="flex-1 overflow-y-auto p-3.5 space-y-4">
        {/* --- TAB 1: CURRENT MAIL ANALYSIS --- */}
        {activeTab === "current" && (
          <>
            {detail ? (
              <div className="space-y-3.5">
                {/* Target Mail Card */}
                <div className="p-3 rounded-xl bg-muted/30 border border-border/60 space-y-1 shadow-2xs">
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-xs font-semibold text-foreground truncate">
                      {detail.subject || t("page.webmail.no_subject")}
                    </p>
                    <span className="text-[10px] text-muted-foreground shrink-0 font-mono">
                      {selectionText
                        ? t("page.webmail.ai_characters", {
                            count: selectionText.length,
                          })
                        : ""}
                    </span>
                  </div>
                  <p className="text-[11px] text-muted-foreground truncate">
                    {t("page.webmail.from")}{" "}
                    {detail.from?.[0]?.name ||
                      detail.from?.[0]?.email ||
                      t("page.webmail.unknown_sender")}
                  </p>
                </div>

                {/* Quick Action Chips Grid */}
                <div className="space-y-2">
                  <p className="text-[11px] font-medium text-muted-foreground">
                    {t("page.webmail.ai_quick_actions")}
                  </p>
                  <div className="grid grid-cols-2 gap-1.5">
                    <button
                      type="button"
                      disabled={generating}
                      className="flex items-center gap-1.5 px-2.5 py-2 rounded-lg border border-border/70 bg-card hover:bg-primary/5 hover:border-primary/40 text-xs font-medium text-foreground transition-colors text-left cursor-pointer disabled:opacity-50"
                      onClick={() => {
                        setTargetLang(getInitialTargetLang("summarize", i18n?.language ?? "zh-CN", detail));
                        void handleGenerate("summarize", "");
                      }}
                    >
                      <Sparkles className="h-3.5 w-3.5 text-amber-500 shrink-0" />
                      <span className="truncate">
                        {t("page.webmail.ai_quick_summarize")}
                      </span>
                    </button>

                    <button
                      type="button"
                      disabled={generating}
                      className="flex items-center gap-1.5 px-2.5 py-2 rounded-lg border border-border/70 bg-card hover:bg-primary/5 hover:border-primary/40 text-xs font-medium text-foreground transition-colors text-left cursor-pointer disabled:opacity-50"
                      onClick={() => {
                        setTargetLang(getInitialTargetLang("summarize", i18n?.language ?? "zh-CN", detail));
                        void handleGenerate(
                          "summarize",
                          t("page.webmail.ai_action_items_prompt"),
                        );
                      }}
                    >
                      <ListTodo className="h-3.5 w-3.5 text-blue-500 shrink-0" />
                      <span className="truncate">
                        {t("page.webmail.ai_quick_action_items")}
                      </span>
                    </button>

                    <button
                      type="button"
                      disabled={generating}
                      className="flex items-center gap-1.5 px-2.5 py-2 rounded-lg border border-border/70 bg-card hover:bg-primary/5 hover:border-primary/40 text-xs font-medium text-foreground transition-colors text-left cursor-pointer disabled:opacity-50"
                      onClick={() => {
                        setTargetLang(getInitialTargetLang("draft_reply", i18n?.language ?? "zh-CN", detail));
                        void handleGenerate(
                          "draft_reply",
                          t("page.webmail.ai_reply_prompt"),
                        );
                      }}
                    >
                      <MailCheck className="h-3.5 w-3.5 text-emerald-500 shrink-0" />
                      <span className="truncate">
                        {t("page.webmail.ai_quick_reply")}
                      </span>
                    </button>

                    <button
                      type="button"
                      disabled={generating}
                      className="flex items-center gap-1.5 px-2.5 py-2 rounded-lg border border-border/70 bg-card hover:bg-primary/5 hover:border-primary/40 text-xs font-medium text-foreground transition-colors text-left cursor-pointer disabled:opacity-50"
                      onClick={() => {
                        setTargetLang(getInitialTargetLang("translate", i18n?.language ?? "zh-CN", detail));
                        void handleGenerate("translate", "");
                      }}
                    >
                      <Languages className="h-3.5 w-3.5 text-violet-500 shrink-0" />
                      <span className="truncate">
                        {t("page.webmail.ai_quick_translate")}
                      </span>
                    </button>

                    <button
                      type="button"
                      disabled={generating}
                      className="col-span-2 flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg border border-border/70 bg-card hover:bg-primary/5 hover:border-primary/40 text-xs font-medium text-foreground transition-colors text-left cursor-pointer disabled:opacity-50"
                      onClick={() => handleGenerate("phishing", "")}
                    >
                      <ShieldCheck className="h-3.5 w-3.5 text-rose-500 shrink-0" />
                      <span className="truncate">
                        {t("page.webmail.ai_quick_security")}
                      </span>
                    </button>
                  </div>
                </div>

                {/* Target Language Selector */}
                <div className="flex items-center justify-between gap-2 px-1 text-xs">
                  <label htmlFor="mailbox-ai-lang-select" className="text-muted-foreground font-medium flex items-center gap-1.5">
                    <Languages className="h-3.5 w-3.5" />
                    <span>{t("page.webmail.ai_language")}</span>
                  </label>
                  <select
                    id="mailbox-ai-lang-select"
                    aria-label={t("page.webmail.ai_language")}
                    value={targetLang}
                    disabled={generating}
                    onChange={(e) =>
                      setTargetLang(e.target.value as "zh-CN" | "en" | "ja")
                    }
                    className="h-7 text-xs rounded border bg-background px-2 font-sans"
                  >
                    <option value="zh-CN">简体中文 (zh-CN)</option>
                    <option value="en">English (en)</option>
                    <option value="ja">日本語 (ja)</option>
                  </select>
                </div>

                {/* Custom Prompt Input */}
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (customPrompt.trim()) {
                      handleGenerate("draft_reply", customPrompt.trim());
                    }
                  }}
                  className="space-y-1.5"
                >
                  <div className="relative">
                    <Input
                      value={customPrompt}
                      disabled={generating}
                      placeholder={t("page.webmail.ai_custom_prompt")}
                      className="h-9 text-xs pr-8 bg-background"
                      onChange={(e) => setCustomPrompt(e.target.value)}
                    />
                    <button
                      type="submit"
                      disabled={generating || !customPrompt.trim()}
                      className="absolute right-1 top-1 bottom-1 px-2 flex items-center justify-center text-primary disabled:text-muted-foreground cursor-pointer disabled:cursor-not-allowed hover:bg-primary/10 rounded"
                    >
                      <Send className="h-3.5 w-3.5" />
                    </button>
                  </div>
                </form>

                {/* Loading Indicator */}
                {generating && (
                  <div className="p-4 rounded-xl border border-primary/20 bg-primary/5 flex items-center justify-center gap-2.5 text-primary text-xs font-medium">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    <span>{t("page.webmail.ai_generating")}</span>
                  </div>
                )}

                {/* Error Banner */}
                {genError && (
                  <div className="p-3 rounded-lg border border-destructive/40 bg-destructive/5 text-destructive text-xs space-y-1">
                    <div className="flex items-center gap-1.5 font-medium">
                      <AlertCircle className="h-4 w-4 shrink-0" />
                      <span>{t("page.webmail.ai_generation_failed")}</span>
                    </div>
                    <p className="pl-5.5 text-[11px] leading-relaxed">
                      {genError}
                    </p>
                  </div>
                )}

                {/* Result Card */}
                {result && !generating && (
                  <div className="p-3.5 rounded-xl border border-border/80 bg-card space-y-3 shadow-xs">
                    <div className="flex items-center justify-between pb-2 border-b">
                      <span className="text-xs font-semibold text-foreground flex items-center gap-1.5">
                        <Sparkles className="h-3.5 w-3.5 text-primary" />
                        {t("page.webmail.ai_result_title")}
                      </span>
                      {typeof result.score === "number" && (
                        <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-muted">
                          {t("page.webmail.ai_match_score", {
                            score: Math.round(result.score * 100),
                          })}
                        </span>
                      )}
                    </div>

                    <div className="text-xs text-foreground/90 leading-relaxed whitespace-pre-wrap break-words max-h-80 overflow-y-auto select-text font-sans">
                      {resultDisplayText}
                    </div>

                    {applied && (
                      <p className="text-[11px] text-emerald-600 dark:text-emerald-400 font-medium">
                        ✓ {t("page.webmail.ai_applied")}
                      </p>
                    )}

                    {/* Result Action Buttons */}
                    <div className="pt-2 border-t flex flex-wrap items-center gap-2">
                      {onApplyAIProposal &&
                        (result.body || result.draft || result.translation) && (
                          <Button
                            type="button"
                            size="sm"
                            disabled={applied || applying}
                            className="h-7 text-xs gap-1 cursor-pointer"
                            onClick={async () => {
                              const sourceRequest = abortControllerRef.current;
                              setApplying(true);
                              try {
                                await onApplyAIProposal({
                                  subject: result.subject ?? detail.subject,
                                  body: resultDisplayText,
                                });
                                if (!sourceRequest?.signal.aborted)
                                  setApplied(true);
                              } catch {
                                if (!sourceRequest?.signal.aborted)
                                  setGenError(
                                    t("page.webmail.compose_draft_failed"),
                                  );
                              } finally {
                                setApplying(false);
                              }
                            }}
                          >
                            <MailCheck className="h-3 w-3" />
                            {t("page.webmail.ai_use_reply")}
                          </Button>
                        )}

                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        className="h-7 text-xs gap-1 cursor-pointer"
                        onClick={() => copyResultText(resultDisplayText)}
                      >
                        {copied ? (
                          <Check className="h-3 w-3 text-emerald-500" />
                        ) : (
                          <Copy className="h-3 w-3" />
                        )}
                        {copied
                          ? t("page.webmail.copied")
                          : t("page.webmail.ai_copy")}
                      </Button>

                      <Button
                        type="button"
                        size="sm"
                        variant="ghost"
                        className="h-7 text-xs text-muted-foreground hover:text-foreground cursor-pointer"
                        onClick={() => handleGenerate(activeOp, customPrompt)}
                      >
                        <RefreshCw className="h-3 w-3" />
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            ) : (
              <div className="h-64 flex flex-col items-center justify-center text-center p-6 text-muted-foreground space-y-3">
                <div className="h-12 w-12 rounded-2xl bg-muted/40 border border-border/70 flex items-center justify-center">
                  <Bot className="h-6 w-6 stroke-[1.5] text-muted-foreground/60" />
                </div>
                <div className="space-y-1">
                  <p className="text-xs font-medium text-foreground">
                    {t("page.webmail.ai_no_message")}
                  </p>
                  <p className="text-[11px] text-muted-foreground leading-relaxed">
                    {t("page.webmail.ai_select_message")}
                  </p>
                </div>
              </div>
            )}
          </>
        )}

        {/* --- TAB 2: MAILBOX Q&A SEARCH --- */}
        {activeTab === "ask" && (
          <div className="space-y-3.5">
            <div className="space-y-1.5">
              <div className="relative">
                <label className="block space-y-1">
                  <span className="sr-only">{t("page.webmail.ai_question")}</span>
                  <Input
                    aria-label={t("page.webmail.ai_question")}
                    value={question}
                    disabled={asking}
                    placeholder={t("page.webmail.ai_question_placeholder")}
                    className="h-9 text-xs pr-8 bg-background"
                    onChange={(e) => {
                      askControllerRef.current?.abort();
                      cancelConsent();
                      setAsking(false);
                      setAskAnswer(null);
                      setQuestion(e.target.value);
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && question.trim()) {
                        e.preventDefault();
                        void handleAsk();
                      }
                    }}
                  />
                </label>
                <button
                  type="button"
                  aria-label={t("page.webmail.ai_ask_submit")}
                  disabled={asking || !question.trim()}
                  className="absolute right-1 top-1 bottom-1 px-2 flex items-center justify-center text-primary disabled:text-muted-foreground cursor-pointer disabled:cursor-not-allowed hover:bg-primary/10 rounded"
                  onClick={() => void handleAsk()}
                >
                  <Search className="h-3.5 w-3.5" />
                </button>
              </div>

              {/* Preset Query Bubbles */}
              <div className="flex flex-wrap gap-1.5 pt-1">
                {[
                  t("page.webmail.ai_preset_invoices"),
                  t("page.webmail.ai_preset_tasks"),
                  t("page.webmail.ai_preset_meetings"),
                ].map((preset) => (
                  <button
                    key={preset}
                    type="button"
                    disabled={asking}
                    className="text-[11px] px-2 py-0.5 rounded-full border border-border/70 bg-muted/30 hover:bg-muted text-muted-foreground hover:text-foreground cursor-pointer transition-colors"
                    onClick={() => {
                      setQuestion(preset);
                      void handleAsk(preset);
                    }}
                  >
                    {preset}
                  </button>
                ))}
              </div>
            </div>

            {/* Folder Scope Filter */}
            <div className="flex items-center gap-2">
              <label className="text-xs text-muted-foreground shrink-0">
                {t("page.webmail.ai_scope_label")}
              </label>
              <select
                className="h-7 text-xs rounded border bg-background px-2 flex-1"
                value={askFolder}
                onChange={(e) => {
                  askControllerRef.current?.abort();
                  cancelConsent();
                  setAsking(false);
                  setAskAnswer(null);
                  setAskFolder(e.target.value);
                }}
              >
                <option value="">{t("page.webmail.ai_all_folders")}</option>
                {folders.map((f) => (
                  <option key={f.name_enc} value={f.name_enc}>
                    {f.name}
                  </option>
                ))}
              </select>
            </div>

            {/* Asking Loading */}
            {asking && (
              <div className="p-4 rounded-xl border border-primary/20 bg-primary/5 flex items-center justify-center gap-2 text-primary text-xs font-medium">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span>{t("page.webmail.ai_searching")}</span>
              </div>
            )}

            {/* Ask Error */}
            {askError && (
              <div className="p-3 rounded-lg border border-destructive/40 bg-destructive/5 text-destructive text-xs">
                {askError}
              </div>
            )}

            {/* Answer Display */}
            {askAnswer && !asking && (
              <div className="p-3.5 rounded-xl border border-border/80 bg-card space-y-3 shadow-xs">
                <p className="text-xs font-semibold text-foreground flex items-center gap-1.5 border-b pb-2">
                  <Bot className="h-3.5 w-3.5 text-primary" />
                  {t("page.webmail.ai_answer_title")}
                </p>
                <p className="text-xs text-foreground/90 leading-relaxed whitespace-pre-wrap break-words select-text">
                  {askAnswer.grounded
                    ? askAnswer.text
                    : t("page.webmail.ai_no_support")}
                </p>

                {/* Citations List */}
                {askAnswer.citations.length > 0 && (
                  <div className="pt-2 border-t space-y-2">
                    <p className="text-[11px] font-semibold text-muted-foreground">
                      {t("page.webmail.ai_citation_count", {
                        count: askAnswer.citations.length,
                      })}
                    </p>
                    <ul className="space-y-1.5">
                      {askAnswer.citations.map((cite, idx) => (
                        <li
                          key={cite.uid + "-" + idx}
                          className="p-2 rounded-lg bg-muted/40 border border-border/50 text-xs space-y-1 hover:bg-muted/70 transition-colors cursor-pointer"
                          onClick={() => {
                            try {
                              onCitation(cite);
                            } catch {
                              setAskError(t("page.webmail.ai_citation_invalid"));
                            }
                          }}
                        >
                          <div className="flex items-center justify-between text-[11px] text-primary font-medium">
                            <span className="flex items-center gap-1">
                              <ExternalLink className="h-3 w-3" />
                              {t("page.webmail.ai_source_number", {
                                number: idx + 1,
                              })}
                            </span>
                            <ChevronRight className="h-3 w-3 text-muted-foreground" />
                          </div>
                          <p className="text-[11px] text-muted-foreground line-clamp-2 leading-relaxed">
                            "{cite.quote}"
                          </p>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* --- TAB 3: AI SETTINGS & BYOK --- */}
        {activeTab === "settings" && (
          <div className="space-y-4 text-xs">
            <div>
              <h4 className="font-semibold text-sm text-foreground">
                {t("page.webmail.ai_settings_title")}
              </h4>
              <p className="text-[11px] text-muted-foreground mt-0.5 leading-relaxed">
                {t("page.webmail.ai_use_custom_desc")}
              </p>
            </div>

            <div className="p-3.5 rounded-xl border border-border/80 bg-card space-y-3.5 shadow-2xs">
              <label className="flex items-center gap-2 cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={userAI.use_custom ?? false}
                  className="h-4 w-4 rounded border-input text-primary accent-primary"
                  onChange={(e) =>
                    setUserAI((prev) => ({
                      ...prev,
                      use_custom: e.target.checked,
                    }))
                  }
                />
                <span className="font-semibold text-foreground">
                  {t("page.webmail.ai_use_custom")}
                </span>
              </label>

              {userAI.use_custom ? (
                <div className="space-y-3 pt-2 border-t border-border/60">
                  <label className="block space-y-1">
                    <span className="text-muted-foreground">
                      {t("page.webmail.ai_provider_label")}
                    </span>
                    <select
                      className="h-8 w-full rounded border bg-background px-2 text-xs"
                      value={userAI.provider ?? "openai"}
                      onChange={(e) =>
                        setUserAI((prev) => ({
                          ...prev,
                          provider: e.target.value as
                            "openai" | "ollama" | "azure",
                        }))
                      }
                    >
                      <option value="openai">
                        {t("page.webmail.ai_openai_compatible")}
                      </option>
                      <option value="ollama">
                        {t("page.webmail.ai_ollama_local")}
                      </option>
                      <option value="azure">Azure OpenAI</option>
                    </select>
                  </label>

                  <label className="block space-y-1">
                    <span className="text-muted-foreground">
                      {t("page.webmail.ai_endpoint")}
                    </span>
                    <Input
                      type="url"
                      className="h-8 text-xs font-mono"
                      placeholder="https://api.deepseek.com/v1"
                      value={userAI.endpoint ?? ""}
                      onChange={(e) =>
                        setUserAI((prev) => ({
                          ...prev,
                          endpoint: e.target.value,
                        }))
                      }
                    />
                  </label>

                  <label className="block space-y-1">
                    <span className="text-muted-foreground">
                      {t("page.webmail.ai_api_key")}
                    </span>
                    <Input
                      type="password"
                      autoComplete="new-password"
                      className="h-8 text-xs font-mono"
                      placeholder="sk-..."
                      value={userAI.api_key ?? ""}
                      aria-label={t("page.webmail.ai_api_key")}
                      onChange={(e) =>
                        setUserAI((prev) => ({
                          ...prev,
                          api_key: e.target.value,
                          clear_api_key: false,
                        }))
                      }
                    />
                  </label>
                  {userAI.key_configured && (
                    <p className="text-xs text-muted-foreground">
                      {t("page.webmail.ai_key_saved")}
                    </p>
                  )}
                  <label className="flex min-h-11 items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={userAI.clear_api_key ?? false}
                      onChange={(e) =>
                        setUserAI((prev) => ({
                          ...prev,
                          api_key: "",
                          clear_api_key: e.target.checked,
                          use_custom: e.target.checked
                            ? false
                            : prev.use_custom,
                        }))
                      }
                    />
                    {t("page.webmail.ai_clear_key")}
                  </label>
                  <div className="space-y-1.5">
                    <div className="flex items-end gap-1.5">
                      <label className="flex-1 space-y-1">
                        <span className="text-muted-foreground">
                          {t("page.webmail.ai_model_label")}
                        </span>
                        <Input
                          list="drawer-ai-models-list"
                          className="h-8 text-xs font-mono"
                          placeholder={t("page.webmail.ai_model_placeholder")}
                          value={userAI.model ?? ""}
                          onChange={(e) =>
                            setUserAI((prev) => ({
                              ...prev,
                              model: e.target.value,
                            }))
                          }
                        />
                        <datalist id="drawer-ai-models-list">
                          {availableModels.map((m) => (
                            <option key={m} value={m} />
                          ))}
                        </datalist>
                      </label>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        className="h-8 text-xs gap-1 shrink-0 cursor-pointer"
                        disabled={fetchingModels || !userAI.endpoint}
                        onClick={handleFetchModels}
                      >
                        <Sparkles className="h-3 w-3" />
                        {fetchingModels
                          ? t("state.loading")
                          : t("page.webmail.ai_test_fetch")}
                      </Button>
                    </div>

                    {availableModels.length > 0 && (
                      <div className="pt-1">
                        <span className="text-[11px] text-muted-foreground">
                          {t("page.webmail.ai_model_presets")}
                        </span>
                        <div className="flex flex-wrap gap-1 mt-1">
                          {availableModels.slice(0, 6).map((m) => (
                            <button
                              key={m}
                              type="button"
                              className="text-[10px] px-1.5 py-0.5 rounded border border-border/70 bg-muted/40 hover:bg-muted font-mono"
                              onClick={() =>
                                setUserAI((prev) => ({ ...prev, model: m }))
                              }
                            >
                              {m}
                            </button>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              ) : (
                <div className="p-2.5 rounded-lg bg-muted/20 border border-border/50 text-[11px] text-muted-foreground leading-relaxed">
                  {t("page.webmail.ai_managed_notice")}
                </div>
              )}

              {settingsNotice && (
                <p
                  className={`text-xs ${
                    settingsNotice.error
                      ? "text-destructive"
                      : "text-emerald-600 dark:text-emerald-400 font-medium"
                  }`}
                >
                  {settingsNotice.text}
                </p>
              )}

              <div className="pt-2 border-t flex justify-end">
                <Button
                  type="button"
                  size="sm"
                  disabled={savingSettings}
                  className="h-7 text-xs cursor-pointer"
                  onClick={handleSaveSettings}
                >
                  {savingSettings ? t("state.loading") : t("action.save")}
                </Button>
              </div>
            </div>
          </div>
        )}
      </div>
    </aside>
  );
}
