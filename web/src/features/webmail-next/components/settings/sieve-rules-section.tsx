import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  getMailboxRules,
  saveMailboxRules,
  listFolders,
  type MailboxRulesView,
  type MailboxSieveRule,
  type MailboxFolder,
} from "@/features/webmail/mailbox-api";
import {
  Filter,
  Plus,
  Trash2,
  ArrowUp,
  ArrowDown,
  Save,
  CheckCircle2,
  AlertCircle,
  FolderInput,
  Archive,
  Ban,
} from "lucide-react";

export function SieveRulesSection({
  initialRuleDraft,
}: {
  initialRuleDraft?: { from?: string; folder?: string };
} = {}) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [rulesView, setRulesView] = useState<MailboxRulesView | null>(null);
  const [rules, setRules] = useState<MailboxSieveRule[]>([]);
  const [folders, setFolders] = useState<MailboxFolder[]>([]);

  // Add rule modal / form state
  const [isAdding, setIsAdding] = useState(Boolean(initialRuleDraft));
  const [newHeader, setNewHeader] = useState<"subject" | "from" | "to" | "cc">(
    "from",
  );
  const [newMatch, setNewMatch] = useState<"contains" | "is">("contains");
  const [newValue, setNewValue] = useState(initialRuleDraft?.from || "");
  const [newAction, setNewAction] = useState<"fileinto" | "keep" | "discard">(
    "fileinto",
  );
  const [newFolder, setNewFolder] = useState(
    initialRuleDraft?.folder || "Archive",
  );
  const [newMatchType, setNewMatchType] = useState<"all" | "any">("all");

  const loadData = async () => {
    try {
      setLoading(true);
      setError(null);
      const [rulesRes, foldersRes] = await Promise.all([
        getMailboxRules(),
        listFolders(),
      ]);
      setRulesView(rulesRes);
      setRules(rulesRes.rules?.rules || []);
      setFolders(foldersRes.folders || []);
      if (foldersRes.folders?.length) {
        setNewFolder(foldersRes.folders[0].name_enc);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(msg || t("settings.rules.err_fetch", "Failed to load rules."));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadData();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleToggleRule = (index: number) => {
    setRules((prev) =>
      prev.map((r, i) => (i === index ? { ...r, disabled: !r.disabled } : r)),
    );
  };

  const handleMoveRule = (index: number, direction: "up" | "down") => {
    if (
      (direction === "up" && index === 0) ||
      (direction === "down" && index === rules.length - 1)
    ) {
      return;
    }
    const newIdx = direction === "up" ? index - 1 : index + 1;
    setRules((prev) => {
      const copy = [...prev];
      const item = copy.splice(index, 1)[0];
      copy.splice(newIdx, 0, item);
      return copy;
    });
  };

  const handleDeleteRule = (index: number) => {
    setRules((prev) => prev.filter((_, i) => i !== index));
  };

  const handleCreateRule = (e: React.FormEvent) => {
    e.preventDefault();
    if (!newValue.trim()) return;

    const rule: MailboxSieveRule = {
      header: newHeader,
      match: newMatch,
      value: newValue.trim(),
      match_type: newMatchType,
      action: newAction,
      folder: newAction === "fileinto" ? newFolder : undefined,
      disabled: false,
    };

    setRules((prev) => [...prev, rule]);
    setNewValue("");
    setIsAdding(false);
  };

  const handleSaveAll = async () => {
    if (!rulesView) return;
    try {
      setSaving(true);
      setError(null);
      setSuccess(false);

      const res = await saveMailboxRules({
        rules: {
          ...rulesView.rules,
          rules,
        },
        confirmed: true,
        expected_version: rulesView.version,
      });

      setRulesView({
        ...rulesView,
        version: res.version,
        state: res.state,
      });
      setSuccess(true);
      setTimeout(() => setSuccess(false), 3000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.rules.err_save", "Failed to save sieve rules."),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6" data-testid="sieve-rules-section">
      <div className="border-b border-[#EBE8E0] pb-4 flex items-center justify-between">
        <div>
          <h3 className="text-base font-semibold text-[#18181B] flex items-center gap-2">
            <Filter className="w-5 h-5 text-[var(--brand-primary,#2563eb)]" />
            <span>{t("settings.rules.title", "Filters & Sieve Rules")}</span>
          </h3>
          <p className="text-xs text-[#52525B] mt-1">
            {t(
              "settings.rules.subtitle",
              "Manage incoming email dispatch rules, automated folder sorting, and spam discarding.",
            )}
          </p>
        </div>
        <button
          data-testid="btn-add-rule"
          type="button"
          onClick={() => setIsAdding((prev) => !prev)}
          className="px-3 py-1.5 bg-[#FFFFFF] border border-[#D4D4D8] hover:bg-[#FAF9F5] text-xs font-semibold rounded-md flex items-center gap-1.5 text-[#18181B] transition-colors shadow-xs"
        >
          <Plus className="w-3.5 h-3.5" />
          <span>
            {isAdding
              ? t("common.cancel", "Cancel")
              : t("settings.rules.new_rule", "New Rule")}
          </span>
        </button>
      </div>

      {error && (
        <div className="p-3 bg-[#FEF2F2] border border-[#FECACA] rounded-lg text-[#991B1B] text-xs flex items-center gap-2">
          <AlertCircle className="w-4 h-4 shrink-0" />
          <span>{error}</span>
        </div>
      )}

      {success && (
        <div className="p-3 bg-[#F0FDF4] border border-[#BBF7D0] rounded-lg text-[#166534] text-xs flex items-center gap-2">
          <CheckCircle2 className="w-4 h-4 shrink-0 text-[#16A34A]" />
          <span>
            {t(
              "settings.rules.saved_success",
              "Rules updated and deployed to Sieve engine!",
            )}
          </span>
        </div>
      )}

      {/* New Rule Wizard Drawer / Form */}
      {isAdding && (
        <form
          onSubmit={handleCreateRule}
          data-testid="new-rule-form"
          className="p-4 bg-[#FAF9F5] border border-[#EBE8E0] rounded-xl space-y-4"
        >
          <h4 className="text-xs font-bold text-[#18181B] uppercase tracking-wider">
            {t("settings.rules.wizard_title", "Rule Configuration Wizard")}
          </h4>

          {/* Condition section */}
          <div className="space-y-2">
            <span className="text-xs font-semibold text-[#3F3F46]">
              1.{" "}
              {t(
                "settings.rules.condition_header",
                "If incoming message matches",
              )}
            </span>
            <div className="grid grid-cols-1 sm:grid-cols-4 gap-2">
              <select
                value={newMatchType}
                onChange={(e) =>
                  setNewMatchType(e.target.value as "all" | "any")
                }
                className="h-8 px-2 text-xs rounded border border-[#D4D4D8] bg-[#FFFFFF]"
              >
                <option value="all">
                  {t("settings.rules.match_all", "All conditions (AND)")}
                </option>
                <option value="any">
                  {t("settings.rules.match_any", "Any condition (OR)")}
                </option>
              </select>

              <select
                value={newHeader}
                onChange={(e) =>
                  setNewHeader(
                    e.target.value as "subject" | "from" | "to" | "cc",
                  )
                }
                className="h-8 px-2 text-xs rounded border border-[#D4D4D8] bg-[#FFFFFF]"
              >
                <option value="from">{t("settings.rules.from", "From")}</option>
                <option value="to">{t("settings.rules.to", "To")}</option>
                <option value="cc">{t("settings.rules.cc", "Cc")}</option>
                <option value="subject">
                  {t("settings.rules.subject", "Subject")}
                </option>
              </select>

              <select
                value={newMatch}
                onChange={(e) =>
                  setNewMatch(e.target.value as "contains" | "is")
                }
                className="h-8 px-2 text-xs rounded border border-[#D4D4D8] bg-[#FFFFFF]"
              >
                <option value="contains">
                  {t("settings.rules.contains", "contains")}
                </option>
                <option value="is">
                  {t("settings.rules.exact_is", "is exactly")}
                </option>
              </select>

              <input
                type="text"
                value={newValue}
                onChange={(e) => setNewValue(e.target.value)}
                placeholder={t(
                  "settings.rules.value_placeholder",
                  "e.g. newsletter, alert",
                )}
                required
                className="h-8 px-3 text-xs rounded border border-[#D4D4D8] bg-[#FFFFFF]"
              />
            </div>
          </div>

          {/* Action section */}
          <div className="space-y-2">
            <span className="text-xs font-semibold text-[#3F3F46]">
              2. {t("settings.rules.action_header", "Then perform action")}
            </span>
            <div className="flex flex-wrap items-center gap-3">
              <select
                value={newAction}
                onChange={(e) =>
                  setNewAction(
                    e.target.value as "fileinto" | "keep" | "discard",
                  )
                }
                className="h-8 px-2 text-xs rounded border border-[#D4D4D8] bg-[#FFFFFF]"
              >
                <option value="fileinto">
                  {t("settings.rules.fileinto", "Move to folder (fileinto)")}
                </option>
                <option value="keep">
                  {t("settings.rules.keep", "Keep in Inbox")}
                </option>
                <option value="discard">
                  {t("settings.rules.discard", "Silently Discard")}
                </option>
              </select>

              {newAction === "fileinto" && (
                <select
                  value={newFolder}
                  onChange={(e) => setNewFolder(e.target.value)}
                  className="h-8 px-2 text-xs rounded border border-[#D4D4D8] bg-[#FFFFFF]"
                >
                  {folders.map((f) => (
                    <option key={f.name_enc} value={f.name_enc}>
                      {f.name}
                    </option>
                  ))}
                </select>
              )}
            </div>
          </div>

          <div className="flex items-center gap-2 pt-2">
            <button
              data-testid="btn-create-rule-submit"
              type="submit"
              className="px-3 py-1.5 bg-[var(--brand-primary,#2563eb)] text-white text-xs font-semibold rounded shadow-sm hover:brightness-105"
            >
              {t("settings.rules.add_rule_btn", "Add to Rule Pipeline")}
            </button>
            <button
              type="button"
              onClick={() => setIsAdding(false)}
              className="px-3 py-1.5 text-xs text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
            >
              {t("common.cancel", "Cancel")}
            </button>
          </div>
        </form>
      )}

      {/* Rules List */}
      {loading ? (
        <div className="p-6 text-center text-xs text-[var(--vellum-ink-secondary)]">
          {t("common.loading", "Loading sieve rules...")}
        </div>
      ) : rules.length === 0 ? (
        <div className="p-8 text-center bg-[#FFFFFF] border border-[#EBE8E0] rounded-xl text-[var(--vellum-ink-secondary)] text-xs">
          <Filter className="w-8 h-8 text-[#D4D4D8] mx-auto mb-2" />
          <p>
            {t("settings.rules.no_rules", "No custom Sieve filters defined.")}
          </p>
        </div>
      ) : (
        <div className="space-y-2">
          {rules.map((rule, idx) => {
            const isFirst = idx === 0;
            const isLast = idx === rules.length - 1;

            return (
              <div
                key={idx}
                data-testid={`rule-row-${idx}`}
                className={`p-3 bg-[#FFFFFF] border rounded-lg flex items-center justify-between gap-3 transition-colors ${
                  rule.disabled
                    ? "border-[#EBE8E0] opacity-60 bg-[#FAF9F5]"
                    : "border-[#EBE8E0]"
                }`}
              >
                {/* Priority Handlers */}
                <div className="flex items-center gap-1 shrink-0">
                  <button
                    type="button"
                    onClick={() => handleMoveRule(idx, "up")}
                    disabled={isFirst}
                    className="p-1 text-[var(--vellum-ink-secondary)] hover:text-[#18181B] disabled:opacity-20"
                    title="Move up"
                  >
                    <ArrowUp className="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onClick={() => handleMoveRule(idx, "down")}
                    disabled={isLast}
                    className="p-1 text-[var(--vellum-ink-secondary)] hover:text-[#18181B] disabled:opacity-20"
                    title="Move down"
                  >
                    <ArrowDown className="w-3.5 h-3.5" />
                  </button>
                  <span className="text-xs text-[var(--vellum-ink-secondary)] w-4 text-center font-mono">
                    {idx + 1}
                  </span>
                </div>

                {/* Rule details summary */}
                <div className="flex-1 min-w-0 flex items-center gap-2 text-xs">
                  <span className="font-semibold text-[#18181B] uppercase text-xs bg-[#F6F4EE] px-1.5 py-0.5 rounded">
                    {rule.header}
                  </span>
                  <span className="text-[#52525B]">{rule.match}:</span>
                  <span className="font-mono text-[#18181B] font-medium truncate max-w-xs">
                    "{rule.value}"
                  </span>
                  <span className="text-[var(--vellum-ink-secondary)]">➔</span>
                  <span className="flex items-center gap-1 text-xs font-semibold text-[#2563EB]">
                    {rule.action === "fileinto" ? (
                      <>
                        <FolderInput className="w-3.5 h-3.5" />
                        <span>{rule.folder}</span>
                      </>
                    ) : rule.action === "discard" ? (
                      <>
                        <Ban className="w-3.5 h-3.5 text-[#DC2626]" />
                        <span className="text-[#DC2626]">Discard</span>
                      </>
                    ) : (
                      <>
                        <Archive className="w-3.5 h-3.5 text-[#16A34A]" />
                        <span className="text-[#16A34A]">Keep in Inbox</span>
                      </>
                    )}
                  </span>
                </div>

                {/* Enable toggle & Delete */}
                <div className="flex items-center gap-2 shrink-0">
                  <button
                    type="button"
                    onClick={() => handleToggleRule(idx)}
                    className={`px-2 py-0.5 text-xs font-semibold rounded border transition-colors ${
                      !rule.disabled
                        ? "bg-[#EFF6FF] border-[#BFDBFE] text-[#2563EB]"
                        : "bg-[#F3F4F6] border-[#E5E7EB] text-[#6B7280]"
                    }`}
                  >
                    {!rule.disabled ? "ACTIVE" : "DISABLED"}
                  </button>
                  <button
                    type="button"
                    onClick={() => handleDeleteRule(idx)}
                    className="p-1 text-[var(--vellum-ink-secondary)] hover:text-[#DC2626] rounded transition-colors"
                    title="Delete rule"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {rules.length > 0 && (
        <button
          data-testid="btn-save-rules"
          type="button"
          onClick={handleSaveAll}
          disabled={saving}
          className="px-4 py-2 bg-[var(--brand-primary,#2563eb)] text-white text-xs font-semibold rounded-md shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 transition-all flex items-center gap-1.5"
        >
          <Save className="w-3.5 h-3.5" />
          <span>
            {saving
              ? t("common.saving", "Saving...")
              : t("settings.rules.save_pipeline_btn", "Save & Deploy Rules")}
          </span>
        </button>
      )}
    </div>
  );
}
