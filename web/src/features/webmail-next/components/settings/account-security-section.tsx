import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  changeMailboxPassword,
  clearMailboxSession,
} from "@/features/webmail/mailbox-api";
import {
  Shield,
  KeyRound,
  AlertCircle,
  CheckCircle2,
  Eye,
  EyeOff,
} from "lucide-react";

export function AccountSecuritySection() {
  const { t } = useTranslation();
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [showOldPassword, setShowOldPassword] = useState(false);
  const [showNewPassword, setShowNewPassword] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);
  const [countdown, setCountdown] = useState(3);

  // Password strength calculation
  const getPasswordStrength = (
    pwd: string,
  ): { score: number; label: string; color: string } => {
    if (!pwd) return { score: 0, label: "", color: "bg-transparent" };
    let score = 0;
    if (pwd.length >= 8) score += 1;
    if (/[A-Z]/.test(pwd)) score += 1;
    if (/[a-z]/.test(pwd)) score += 1;
    if (/[0-9]/.test(pwd)) score += 1;
    if (/[^A-Za-z0-9]/.test(pwd)) score += 1;

    if (score < 3)
      return {
        score: 1,
        label: t("settings.security.strength_weak", "Weak"),
        color: "bg-[var(--vellum-color-danger)]",
      };
    if (score < 5)
      return {
        score: 2,
        label: t("settings.security.strength_medium", "Medium"),
        color: "bg-[var(--vellum-color-warning)]",
      };
    return {
      score: 3,
      label: t("settings.security.strength_strong", "Strong"),
      color: "bg-[var(--vellum-success-text)]",
    };
  };

  const strength = getPasswordStrength(newPassword);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (!oldPassword) {
      setError(
        t(
          "settings.security.err_old_pwd_required",
          "Current password is required.",
        ),
      );
      return;
    }
    if (newPassword.length < 8) {
      setError(
        t(
          "settings.security.err_pwd_length",
          "New password must be at least 8 characters long.",
        ),
      );
      return;
    }
    if (
      !/[A-Z]/.test(newPassword) ||
      !/[a-z]/.test(newPassword) ||
      !/[0-9]/.test(newPassword)
    ) {
      setError(
        t(
          "settings.security.err_pwd_composition",
          "New password must contain uppercase, lowercase letters, and digits.",
        ),
      );
      return;
    }
    if (newPassword !== confirmPassword) {
      setError(
        t("settings.security.err_pwd_mismatch", "Passwords do not match."),
      );
      return;
    }

    try {
      setSubmitting(true);
      await changeMailboxPassword(oldPassword, newPassword);
      setSuccess(true);

      let remain = 3;
      const interval = setInterval(() => {
        remain -= 1;
        setCountdown(remain);
        if (remain <= 0) {
          clearInterval(interval);
          clearMailboxSession();
          window.location.href = "/webmail/login";
        }
      }, 1000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.security.err_generic", "Failed to change password."),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="space-y-6" data-testid="account-security-section">
      <div className="border-b border-[var(--vellum-border)] pb-4">
        <h3 className="text-base font-semibold text-[var(--vellum-ink)] flex items-center gap-2">
          <KeyRound className="w-5 h-5 text-[var(--brand-primary)]" />
          <span>{t("settings.security.title", "Account & Security")}</span>
        </h3>
        <p className="text-xs text-[var(--vellum-ink-secondary)] mt-1">
          {t(
            "settings.security.subtitle",
            "Manage your account authentication credentials and login security.",
          )}
        </p>
      </div>

      {success ? (
        <div className="p-4 bg-[var(--vellum-success-fill)] border border-[var(--vellum-success-border)] rounded-lg text-[var(--vellum-color-success)] text-sm flex items-start gap-3">
          <CheckCircle2 className="w-5 h-5 text-[var(--vellum-success-text)] shrink-0 mt-0.5" />
          <div>
            <p className="font-semibold">
              {t(
                "settings.security.success_title",
                "Password changed successfully!",
              )}
            </p>
            <p className="text-xs text-[var(--vellum-success-text-strong)] mt-1">
              {t(
                "settings.security.redirecting",
                "Please sign in again. Redirecting to login page in {{count}}s...",
                { count: countdown },
              )}
            </p>
          </div>
        </div>
      ) : (
        <form onSubmit={handleSubmit} className="space-y-4 max-w-md">
          {error && (
            <div className="p-3 bg-[var(--vellum-danger-fill-soft)] border border-[var(--vellum-danger-border)] rounded-lg text-[var(--vellum-danger-text)] text-xs flex items-center gap-2">
              <AlertCircle className="w-4 h-4 shrink-0" />
              <span>{error}</span>
            </div>
          )}

          {/* Current Password */}
          <div>
            <label className="block text-xs font-semibold text-[var(--vellum-ink-emphasis)] mb-1">
              {t("settings.security.current_password", "Current Password")}
            </label>
            <div className="relative">
              <input
                data-testid="input-old-password"
                type={showOldPassword ? "text" : "password"}
                value={oldPassword}
                onChange={(e) => setOldPassword(e.target.value)}
                required
                className="w-full h-9 px-3 pr-9 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)] focus:ring-1 focus:ring-[var(--brand-primary)]"
              />
              <button
                type="button"
                onClick={() => setShowOldPassword((prev) => !prev)}
                className="absolute right-2.5 top-2.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)]"
                title="Toggle visibility"
              >
                {showOldPassword ? (
                  <EyeOff className="w-4 h-4" />
                ) : (
                  <Eye className="w-4 h-4" />
                )}
              </button>
            </div>
          </div>

          {/* New Password */}
          <div>
            <label className="block text-xs font-semibold text-[var(--vellum-ink-emphasis)] mb-1">
              {t("settings.security.new_password", "New Password")}
            </label>
            <div className="relative">
              <input
                data-testid="input-new-password"
                type={showNewPassword ? "text" : "password"}
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
                className="w-full h-9 px-3 pr-9 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)] focus:ring-1 focus:ring-[var(--brand-primary)]"
              />
              <button
                type="button"
                onClick={() => setShowNewPassword((prev) => !prev)}
                className="absolute right-2.5 top-2.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)]"
                title="Toggle visibility"
              >
                {showNewPassword ? (
                  <EyeOff className="w-4 h-4" />
                ) : (
                  <Eye className="w-4 h-4" />
                )}
              </button>
            </div>

            {/* Password strength bar */}
            {newPassword && (
              <div className="mt-2 space-y-1">
                <div className="flex justify-between items-center text-xs text-[var(--vellum-ink-secondary)]">
                  <span>
                    {t("settings.security.strength", "Password strength")}:
                  </span>
                  <span className="font-semibold">{strength.label}</span>
                </div>
                <div className="h-1.5 w-full bg-[var(--vellum-border)] rounded-full overflow-hidden flex gap-1">
                  <div
                    className={`h-full flex-1 transition-colors ${
                      strength.score >= 1 ? strength.color : "bg-transparent"
                    }`}
                  />
                  <div
                    className={`h-full flex-1 transition-colors ${
                      strength.score >= 2 ? strength.color : "bg-transparent"
                    }`}
                  />
                  <div
                    className={`h-full flex-1 transition-colors ${
                      strength.score >= 3 ? strength.color : "bg-transparent"
                    }`}
                  />
                </div>
                <p className="text-xs text-[var(--vellum-ink-secondary)]">
                  {t(
                    "settings.security.requirements_hint",
                    "Requires at least 8 characters with uppercase, lowercase, and numbers.",
                  )}
                </p>
              </div>
            )}
          </div>

          {/* Confirm Password */}
          <div>
            <label className="block text-xs font-semibold text-[var(--vellum-ink-emphasis)] mb-1">
              {t("settings.security.confirm_password", "Confirm New Password")}
            </label>
            <input
              data-testid="input-confirm-password"
              type="password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              required
              className="w-full h-9 px-3 text-xs rounded-md border border-[var(--vellum-border-strong)] bg-[var(--vellum-surface)] focus:outline-none focus:border-[var(--brand-primary)] focus:ring-1 focus:ring-[var(--brand-primary)]"
            />
          </div>

          <button
            data-testid="btn-change-password"
            type="submit"
            disabled={submitting}
            className="px-4 py-2 bg-[var(--brand-primary)] text-white text-xs font-semibold rounded-md shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 transition-all flex items-center gap-1.5"
          >
            <Shield className="w-3.5 h-3.5" />
            <span>
              {submitting
                ? t("common.saving", "Saving...")
                : t("settings.security.update_btn", "Update Password")}
            </span>
          </button>
        </form>
      )}
    </div>
  );
}
