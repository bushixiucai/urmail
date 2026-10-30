import { useEffect, useRef, useState } from "react";
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
} from "./mailbox-api";
import { ApiRequestError } from "@/lib/api/client";

interface MailboxAIDrawerProps {
  open: boolean;
  onClose: () => void;
  folders: MailboxFolder[];
  selectedFolderEnc?: string;
  detail: MessageDetail | null;
  onCitation: (source: MailboxAICitation) => void;
  onApplyAIProposal?: (proposal: { subject: string; body: string }) => void;
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
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<"current" | "ask" | "settings">(
    "current",
  );

  // Global & User Target State
  const [target, setTarget] = useState<MailboxAITarget | null>(null);

  // Current Message Analysis State
  const [activeOp, setActiveOp] = useState<MailboxAIOperation>("summarize");
  const [customPrompt, setCustomPrompt] = useState("");
  const [targetLang] = useState<"zh-CN" | "en" | "ja">("zh-CN");
  const [generating, setGenerating] = useState(false);
  const [result, setResult] = useState<MailboxAIResult | null>(null);
  const [genError, setGenError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [applied, setApplied] = useState(false);
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

  // Load target configuration and user AI preferences
  const loadTargetAndSettings = () => {
    void fetchMailboxAITarget()
      .then(setTarget)
      .catch(() => setTarget(null));

    void getMailboxSettings()
      .then((s) => {
        const aiPref = s.preferences?.ai as MailboxUserAIConfig | undefined;
        if (aiPref) {
          setUserAI({
            use_custom: Boolean(aiPref.use_custom),
            provider: aiPref.provider || "openai",
            endpoint: aiPref.endpoint || "https://api.deepseek.com/v1",
            model: aiPref.model || "deepseek-chat",
            api_key: aiPref.api_key || "",
          });
        }
      })
      .catch(() => {});
  };

  useEffect(() => {
    if (open) {
      loadTargetAndSettings();
    }
  }, [open]);

  // Load message context when selected mail changes
  useEffect(() => {
    setResult(null);
    setGenError(null);
    setApplied(false);
    setSelectionText("");
    if (detail && selectedFolderEnc && detail.uid_validity) {
      void fetchMailboxAIContext({
        folder_enc: selectedFolderEnc,
        uid: detail.uid,
        uid_validity: detail.uid_validity,
      })
        .then((ctx) => setSelectionText(ctx.body))
        .catch(() => setSelectionText(detail.text ?? ""));
    }
  }, [detail, selectedFolderEnc]);

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
      const response = await runMailboxAI(
        {
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
        },
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
    setAsking(true);
    setAskError(null);
    setAskAnswer(null);

    try {
      const response = await askMailboxAI({
        question: q.trim(),
        configuration_digest: target.configuration_digest,
        confirmed: true,
        folder_enc: askFolder || undefined,
      });
      const citations = await Promise.all(
        response.citations.map((id) => fetchMailboxAICitation(id)),
      );
      setAskAnswer({
        text: response.text,
        grounded: response.grounded,
        citations,
      });
    } catch (err) {
      setAskError(
        err instanceof ApiRequestError
          ? t(`error.${err.code}`, {
              defaultValue: t("page.webmail.ai_failed"),
            })
          : t("page.webmail.ai_failed"),
      );
    } finally {
      setAsking(false);
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
      setSettingsNotice({ text: t("page.webmail.ai_custom_saved") });
      loadTargetAndSettings();
    } catch {
      setSettingsNotice({ text: t("page.ai.save_failed"), error: true });
    } finally {
      setSavingSettings(false);
    }
  };

  const copyResultText = (text: string) => {
    void navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
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
      className="w-84 sm:w-96 border-l bg-card flex flex-col h-full shadow-2xl z-30 shrink-0 select-none"
      aria-label="AI Copilot"
    >
      {/* 1. Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b bg-muted/20 shrink-0">
        <div className="flex items-center gap-2">
          <div className="h-7 w-7 rounded-lg bg-primary/10 text-primary flex items-center justify-center font-bold">
            <Sparkles className="h-4 w-4" />
          </div>
          <div>
            <h3 className="text-sm font-semibold flex items-center gap-1.5">
              <span>AI Copilot</span>
              {target?.is_custom ? (
                <span className="text-[10px] font-normal px-1.5 py-0.2 rounded bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20">
                  BYOK 自定义
                </span>
              ) : target?.enabled ? (
                <span className="text-[10px] font-normal px-1.5 py-0.2 rounded bg-primary/10 text-primary border border-primary/20">
                  企业托管
                </span>
              ) : null}
            </h3>
            <p className="text-[11px] text-muted-foreground truncate max-w-[200px]">
              {target?.model ? `模型: ${target.model}` : "智能邮件生产力助手"}
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
                      {selectionText ? `${selectionText.length} 字符` : ""}
                    </span>
                  </div>
                  <p className="text-[11px] text-muted-foreground truncate">
                    发件人:{" "}
                    {detail.from?.[0]?.name ||
                      detail.from?.[0]?.email ||
                      "未知"}
                  </p>
                </div>

                {/* Quick Action Chips Grid */}
                <div className="space-y-2">
                  <p className="text-[11px] font-medium text-muted-foreground">
                    快捷指令 (Quick Actions):
                  </p>
                  <div className="grid grid-cols-2 gap-1.5">
                    <button
                      type="button"
                      disabled={generating}
                      className="flex items-center gap-1.5 px-2.5 py-2 rounded-lg border border-border/70 bg-card hover:bg-primary/5 hover:border-primary/40 text-xs font-medium text-foreground transition-colors text-left cursor-pointer disabled:opacity-50"
                      onClick={() => handleGenerate("summarize", "")}
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
                      onClick={() =>
                        handleGenerate(
                          "summarize",
                          "请提炼此邮件中的待办事项、需跟进工作、关键截止日期及对接人，以清晰清单呈现",
                        )
                      }
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
                      onClick={() =>
                        handleGenerate(
                          "draft_reply",
                          "请以礼貌、严谨且富有成效的商务口吻生成回复草稿，感谢对方并就核心事宜给出明确响应",
                        )
                      }
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
                      onClick={() => handleGenerate("translate", "")}
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
                    <span>AI 正在深入分析并生成结果...</span>
                  </div>
                )}

                {/* Error Banner */}
                {genError && (
                  <div className="p-3 rounded-lg border border-destructive/40 bg-destructive/5 text-destructive text-xs space-y-1">
                    <div className="flex items-center gap-1.5 font-medium">
                      <AlertCircle className="h-4 w-4 shrink-0" />
                      <span>生成失败</span>
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
                        AI 建议结果
                      </span>
                      {typeof result.score === "number" && (
                        <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-muted">
                          匹配度 {Math.round(result.score * 100)}%
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
                            disabled={applied}
                            className="h-7 text-xs gap-1 cursor-pointer"
                            onClick={() => {
                              onApplyAIProposal({
                                subject: result.subject ?? detail.subject,
                                body: resultDisplayText,
                              });
                              setApplied(true);
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
                        {copied ? "已复制" : t("page.webmail.ai_copy")}
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
                    尚未选择邮件
                  </p>
                  <p className="text-[11px] text-muted-foreground leading-relaxed">
                    在左侧列表中点击选择一封邮件，即可快速进行核心要点提炼、待办事项抽取与商务回复起草。
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
                <Input
                  value={question}
                  disabled={asking}
                  placeholder="提问邮箱内容，如：发票金额、会议安排..."
                  className="h-9 text-xs pr-8 bg-background"
                  onChange={(e) => setQuestion(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && question.trim()) {
                      e.preventDefault();
                      void handleAsk();
                    }
                  }}
                />
                <button
                  type="button"
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
                范围:
              </label>
              <select
                className="h-7 text-xs rounded border bg-background px-2 flex-1"
                value={askFolder}
                onChange={(e) => setAskFolder(e.target.value)}
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
                <span>正在检索全邮箱并归纳回答...</span>
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
                  智能回答
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
                      引用邮件来源 ({askAnswer.citations.length} 处):
                    </p>
                    <ul className="space-y-1.5">
                      {askAnswer.citations.map((cite, idx) => (
                        <li
                          key={cite.uid + "-" + idx}
                          className="p-2 rounded-lg bg-muted/40 border border-border/50 text-xs space-y-1 hover:bg-muted/70 transition-colors cursor-pointer"
                          onClick={() => onCitation(cite)}
                        >
                          <div className="flex items-center justify-between text-[11px] text-primary font-medium">
                            <span className="flex items-center gap-1">
                              <ExternalLink className="h-3 w-3" />
                              来源 {idx + 1}
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
                      服务提供商 (Provider)
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
                        OpenAI 兼容 (DeepSeek / OpenAI / 阿里百炼 / 硅基流动等)
                      </option>
                      <option value="ollama">Ollama (本地私有部署)</option>
                      <option value="azure">Azure OpenAI</option>
                    </select>
                  </label>

                  <label className="block space-y-1">
                    <span className="text-muted-foreground">
                      API 接口端点 (Endpoint)
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
                      API 密钥 (API Key)
                    </span>
                    <Input
                      type="password"
                      autoComplete="new-password"
                      className="h-8 text-xs font-mono"
                      placeholder="sk-..."
                      value={userAI.api_key ?? ""}
                      onChange={(e) =>
                        setUserAI((prev) => ({
                          ...prev,
                          api_key: e.target.value,
                        }))
                      }
                    />
                  </label>

                  <div className="space-y-1.5">
                    <div className="flex items-end gap-1.5">
                      <label className="flex-1 space-y-1">
                        <span className="text-muted-foreground">
                          模型名称 (Model)
                        </span>
                        <Input
                          list="drawer-ai-models-list"
                          className="h-8 text-xs font-mono"
                          placeholder="例如 deepseek-chat 或 gpt-4o"
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
                          ? "拉取中..."
                          : t("page.webmail.ai_test_fetch")}
                      </Button>
                    </div>

                    {availableModels.length > 0 && (
                      <div className="pt-1">
                        <span className="text-[11px] text-muted-foreground">
                          快捷选用:
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
                  当前处于企业全局模式。由企业管理员统一配置的大模型服务承载分析与问答。
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
                  {savingSettings ? "保存中..." : t("action.save")}
                </Button>
              </div>
            </div>
          </div>
        )}
      </div>
    </aside>
  );
}
