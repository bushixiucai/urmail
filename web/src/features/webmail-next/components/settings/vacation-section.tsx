import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  getMailboxRules,
  saveMailboxRules,
  type MailboxRulesView,
  type MailboxVacation,
} from "@/features/webmail/mailbox-api";
import {
  Palmtree,
  Calendar,
  Save,
  CheckCircle2,
  AlertCircle,
  Clock,
} from "lucide-react";

interface VacationSectionProps {
  onVacationUpdated?: () => void;
}

export function VacationSection({ onVacationUpdated }: VacationSectionProps) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [rulesView, setRulesView] = useState<MailboxRulesView | null>(null);

  const [enabled, setEnabled] = useState(false);
  const [from, setFrom] = useState("");
  const [until, setUntil] = useState("");
  const [subject, setSubject] = useState("");
  const [message, setMessage] = useState("");
  const [days, setDays] = useState(1);

  const loadData = async () => {
    try {
      setLoading(true);
      setError(null);
      const res = await getMailboxRules();
      setRulesView(res);
      const vac = res.rules?.vacation;
      if (vac) {
        setEnabled(vac.enabled || false);
        setFrom(vac.from ? vac.from.slice(0, 10) : "");
        setUntil(vac.until ? vac.until.slice(0, 10) : "");
        setSubject(vac.subject || "");
        setMessage(vac.message || "");
        setDays(vac.days || 1);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg ||
          t("settings.vacation.err_fetch", "Failed to load vacation rules."),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadData();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!rulesView) return;

    try {
      setSaving(true);
      setError(null);
      setSuccess(false);

      const updatedVacation: MailboxVacation = {
        enabled,
        from: from ? new Date(from).toISOString() : undefined,
        until: until ? new Date(until).toISOString() : undefined,
        subject,
        message,
        days,
      };

      const res = await saveMailboxRules({
        rules: {
          ...rulesView.rules,
          vacation: updatedVacation,
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
      if (onVacationUpdated) onVacationUpdated();
      setTimeout(() => setSuccess(false), 3000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg ||
          t(
            "settings.vacation.err_save",
            "Failed to save vacation configuration.",
          ),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6" data-testid="vacation-section">
      <div className="border-b border-[var(--vellum-border)] pb-4">
        <h3 className="text-base font-semibold text-[var(--vellum-ink)] flex items-center gap-2">
          <Palmtree className="w-5 h-5 text-[var(--brand-primary)]" />
          <span>{t("settings.vacation.title", "Vacation Autoresponder")}</span>
        </h3>
        <p className="text-xs text-[var(--vellum-ink-secondary)] mt-1">
          {t(
            "settings.vacation.subtitle",
            "Automatically send an out-of-office autoreply during holidays or leave.",
          )}
        </p>
      </div>

      {loading ? (
        <div className="p-6 text-center text-xs text-[var(--vellum-ink-secondary)]">
          {t("common.loading", "Loading autoresponder settings...")}
        </div>
      ) : (
        <form onSubmit={handleSave} className="space-y-5 max-w-xl">
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
                  "settings.vacation.saved_success",
                  "Vacation autoresponder saved successfully!",
                )}
              </span>
            </div>
          )}

          {/* Master Toggle */}
          <div className="p-4 bg-[var(--vellum-ground)] border border-[var(--vellum-border)] rounded-xl flex items-center justify-between">
            <div>
              <span className="text-xs font-semibold text-[var(--vellum-ink)]">
                {t(
                  "settings.vacation.enable_toggle",
                  "Enable Vacation Autoresponder",
                )}
              </span>
              <p className="text-xs text-[var(--vellum-ink-secondary)] mt-0.5">
                {enabled
                  ? t(
                      "settings.vacation.status_active",
                      "Autoresponder is currently armed and answering inbound messages.",
                    )
                  : t(
                      "settings.vacation.status_inactive",
                      "Autoresponder is off.",
                    )}
              </p>
            </div>
            <label className="relative inline-flex items-center cursor-pointer">
              <input
                data-testid="vacation-switch"
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
            {/* Date Range Fields */}
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label className="block text-xs font-semibold text-[var(--vellum-ink-strong)] mb-1 flex items-center gap-1">
                  <Calendar className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
                  <span>{t("settings.vacation.start_date", "Start Date")}</span>
                </label>
                <input
                  data-testid="input-vacation-from"
                  type="date"
                  value={from}
                  onChange={(e) => setFrom(e.target.value)}
                  className="w-full h-9 px-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)]"
                />
              </div>

              <div>
                <label className="block text-xs font-semibold text-[var(--vellum-ink-strong)] mb-1 flex items-center gap-1">
                  <Calendar className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
                  <span>{t("settings.vacation.end_date", "End Date")}</span>
                </label>
                <input
                  data-testid="input-vacation-until"
                  type="date"
                  value={until}
                  onChange={(e) => setUntil(e.target.value)}
                  className="w-full h-9 px-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)]"
                />
              </div>
            </div>

            {/* Response Subject */}
            <div>
              <label className="block text-xs font-semibold text-[var(--vellum-ink-strong)] mb-1">
                {t("settings.vacation.subject", "Response Subject")}
              </label>
              <input
                data-testid="input-vacation-subject"
                type="text"
                value={subject}
                onChange={(e) => setSubject(e.target.value)}
                placeholder={t(
                  "settings.vacation.default_subject",
                  "Out of Office: Thank you for your email",
                )}
                className="w-full h-9 px-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)]"
              />
            </div>

            {/* Message Body */}
            <div>
              <label className="block text-xs font-semibold text-[var(--vellum-ink-strong)] mb-1">
                {t("settings.vacation.message", "Auto-reply Message")}
              </label>
              <textarea
                data-testid="textarea-vacation-message"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                rows={4}
                placeholder={t(
                  "settings.vacation.default_message",
                  "I am out of the office with limited access to email. I will respond to your message upon my return.",
                )}
                className="w-full p-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)]"
              />
            </div>

            {/* Response Frequency (days) */}
            <div>
              <label className="block text-xs font-semibold text-[var(--vellum-ink-strong)] mb-1 flex items-center gap-1">
                <Clock className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
                <span>
                  {t(
                    "settings.vacation.days_interval",
                    "Reply Interval (Days)",
                  )}
                </span>
              </label>
              <input
                data-testid="input-vacation-days"
                type="number"
                min={1}
                max={30}
                value={days}
                onChange={(e) => setDays(Number(e.target.value))}
                className="w-24 h-9 px-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)]"
              />
              <p className="text-xs text-[var(--vellum-ink-secondary)] mt-1">
                {t(
                  "settings.vacation.days_hint",
                  "Number of days before sending another auto-reply to the same sender.",
                )}
              </p>
            </div>
          </div>

          <button
            data-testid="btn-save-vacation"
            type="submit"
            disabled={saving}
            className="px-4 py-2 bg-[var(--brand-primary)] text-white text-xs font-semibold rounded-md shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 transition-all flex items-center gap-1.5"
          >
            <Save className="w-3.5 h-3.5" />
            <span>
              {saving
                ? t("common.saving", "Saving...")
                : t("settings.vacation.save_btn", "Save Vacation Settings")}
            </span>
          </button>
        </form>
      )}
    </div>
  );
}
