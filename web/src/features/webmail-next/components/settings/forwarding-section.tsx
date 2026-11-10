import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  getMailboxRules,
  saveMailboxRules,
  type MailboxRulesView,
  type MailboxForwarding,
} from "@/features/webmail/mailbox-api";
import {
  Forward,
  Save,
  CheckCircle2,
  AlertCircle,
  ShieldAlert,
} from "lucide-react";

export function ForwardingSection() {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [rulesView, setRulesView] = useState<MailboxRulesView | null>(null);

  const [enabled, setEnabled] = useState(false);
  const [target, setTarget] = useState("");
  const [keepCopy, setKeepCopy] = useState(true);
  const [initialTarget, setInitialTarget] = useState("");

  // Confirmation dialog modal state
  const [showConfirmModal, setShowConfirmModal] = useState(false);

  const loadData = async () => {
    try {
      setLoading(true);
      setError(null);
      const res = await getMailboxRules();
      setRulesView(res);
      const fwd = res.rules?.forwarding;
      if (fwd) {
        setEnabled(fwd.enabled || false);
        setTarget(fwd.target || "");
        setKeepCopy(fwd.keep_copy ?? true);
        setInitialTarget(fwd.target || "");
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg ||
          t(
            "settings.forwarding.err_fetch",
            "Failed to load forwarding configuration.",
          ),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadData();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handlePreSave = (e: React.FormEvent) => {
    e.preventDefault();
    if (!rulesView) return;

    if (enabled && !target.trim()) {
      setError(
        t(
          "settings.forwarding.err_target_required",
          "Forwarding address cannot be empty.",
        ),
      );
      return;
    }

    // If forwarding is enabled and target is changed or newly set, require anti-hijack confirmation
    if (enabled && target.trim() !== initialTarget.trim()) {
      setShowConfirmModal(true);
      return;
    }

    executeSave();
  };

  const executeSave = async () => {
    if (!rulesView) return;
    try {
      setSaving(true);
      setError(null);
      setSuccess(false);
      setShowConfirmModal(false);

      const updatedForwarding: MailboxForwarding = {
        enabled,
        target: target.trim(),
        keep_copy: keepCopy,
      };

      const res = await saveMailboxRules({
        rules: {
          ...rulesView.rules,
          forwarding: updatedForwarding,
        },
        confirmed: true,
        expected_version: rulesView.version,
      });

      setRulesView({
        ...rulesView,
        version: res.version,
        state: res.state,
      });

      setInitialTarget(target.trim());
      setSuccess(true);
      setTimeout(() => setSuccess(false), 3000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg ||
          t(
            "settings.forwarding.err_save",
            "Failed to save forwarding configuration.",
          ),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6" data-testid="forwarding-section">
      <div className="border-b border-[var(--vellum-border)] pb-4">
        <h3 className="text-base font-semibold text-[var(--vellum-ink)] flex items-center gap-2">
          <Forward className="w-5 h-5 text-[var(--brand-primary)]" />
          <span>{t("settings.forwarding.title", "Email Auto-Forwarding")}</span>
        </h3>
        <p className="text-xs text-[var(--vellum-ink-secondary)] mt-1">
          {t(
            "settings.forwarding.subtitle",
            "Automatically redirect inbound messages to an external email destination.",
          )}
        </p>
      </div>

      {loading ? (
        <div className="p-6 text-center text-xs text-[var(--vellum-ink-secondary)]">
          {t("common.loading", "Loading forwarding settings...")}
        </div>
      ) : (
        <form onSubmit={handlePreSave} className="space-y-5 max-w-xl">
          {error && (
            <div className="p-3 bg-[var(--vellum-danger-fill-soft)] border border-[var(--vellum-danger-border)] rounded-lg text-[var(--vellum-danger-text)] text-xs flex items-center gap-2">
              <AlertCircle className="w-4 h-4 shrink-0" />
              <span>{error}</span>
            </div>
          )}

          {success && (
            <div className="p-3 bg-[var(--vellum-success-fill)] border border-[var(--vellum-success-border)] rounded-lg text-[var(--vellum-color-success)] text-xs flex items-center gap-2">
              <CheckCircle2 className="w-4 h-4 shrink-0 text-[var(--vellum-success-text)]" />
              <span>
                {t(
                  "settings.forwarding.saved_success",
                  "Forwarding settings updated successfully!",
                )}
              </span>
            </div>
          )}

          {/* Master Toggle */}
          <div className="p-4 bg-[var(--vellum-ground)] border border-[var(--vellum-border)] rounded-xl flex items-center justify-between">
            <div>
              <span className="text-xs font-semibold text-[var(--vellum-ink)]">
                {t(
                  "settings.forwarding.enable_toggle",
                  "Enable Automatic Forwarding",
                )}
              </span>
              <p className="text-xs text-[var(--vellum-ink-secondary)] mt-0.5">
                {enabled
                  ? t(
                      "settings.forwarding.status_active",
                      "Inbound messages will be redirected to destination.",
                    )
                  : t(
                      "settings.forwarding.status_inactive",
                      "Auto-forwarding is disabled.",
                    )}
              </p>
            </div>
            <label className="relative inline-flex items-center cursor-pointer">
              <input
                data-testid="forwarding-switch"
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-[var(--vellum-border)] peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-[var(--brand-primary)]" />
            </label>
          </div>

          <div
            className={`space-y-4 transition-opacity ${
              enabled ? "opacity-100" : "opacity-40 pointer-events-none"
            }`}
          >
            {/* Target Address */}
            <div>
              <label className="block text-xs font-semibold text-[var(--vellum-ink-emphasis)] mb-1">
                {t(
                  "settings.forwarding.target_address",
                  "Forward Inbound Mail To",
                )}
              </label>
              <input
                data-testid="input-forwarding-target"
                type="email"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                placeholder="colleague@domain.com"
                className="w-full h-9 px-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)]"
              />
            </div>

            {/* Keep Copy Checkbox */}
            <div className="flex items-start gap-2 pt-1">
              <input
                data-testid="checkbox-keep-copy"
                type="checkbox"
                id="keep-copy-checkbox"
                checked={keepCopy}
                onChange={(e) => setKeepCopy(e.target.checked)}
                className="mt-0.5 rounded border-[var(--vellum-border-strong)] text-[var(--brand-primary)] focus:ring-[var(--brand-primary)]"
              />
              <label
                htmlFor="keep-copy-checkbox"
                className="text-xs text-[var(--vellum-ink-emphasis)] cursor-pointer"
              >
                <span className="font-semibold">
                  {t(
                    "settings.forwarding.keep_copy_label",
                    "Keep a copy in this mailbox",
                  )}
                </span>
                <p className="text-xs text-[var(--vellum-ink-secondary)] mt-0.5">
                  {t(
                    "settings.forwarding.keep_copy_desc",
                    "A copy will remain in your Inbox when mail is forwarded to the remote address.",
                  )}
                </p>
              </label>
            </div>
          </div>

          <button
            data-testid="btn-save-forwarding"
            type="submit"
            disabled={saving}
            className="px-4 py-2 bg-[var(--brand-primary)] text-white text-xs font-semibold rounded-md shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 transition-all flex items-center gap-1.5"
          >
            <Save className="w-3.5 h-3.5" />
            <span>
              {saving
                ? t("common.saving", "Saving...")
                : t("settings.forwarding.save_btn", "Save Forwarding Rule")}
            </span>
          </button>
        </form>
      )}

      {/* Anti-hijack Safety Confirmation Modal */}
      {showConfirmModal && (
        <div
          data-testid="forwarding-confirm-modal"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-xs p-4"
        >
          <div className="bg-[var(--vellum-surface)] border border-[var(--vellum-border)] rounded-xl max-w-md w-full p-5 space-y-4 shadow-xl">
            <div className="flex items-center gap-2 text-[var(--vellum-color-danger)]">
              <ShieldAlert className="w-5 h-5" />
              <h4 className="text-sm font-bold text-[var(--vellum-ink)]">
                {t(
                  "settings.forwarding.security_alert_title",
                  "Security Confirmation",
                )}
              </h4>
            </div>

            <p className="text-xs text-[var(--vellum-ink-secondary)] leading-relaxed">
              {t(
                "settings.forwarding.security_alert_text",
                "All inbound messages will be automatically forwarded to the specified destination ({{target}}). Please confirm that this address is fully trusted and authorized by you.",
                { target },
              )}
            </p>

            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setShowConfirmModal(false)}
                className="px-3 py-1.5 text-xs text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] rounded border border-[var(--vellum-border)]"
              >
                {t("common.cancel", "Cancel")}
              </button>
              <button
                data-testid="btn-confirm-forwarding-hijack"
                type="button"
                onClick={executeSave}
                className="px-3 py-1.5 text-xs bg-[var(--vellum-danger-strong)] text-white font-semibold rounded shadow-sm hover:bg-[var(--vellum-danger-strong-hover)]"
              >
                {t(
                  "settings.forwarding.confirm_btn",
                  "I Trust This Address & Enable",
                )}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
