import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  getMailboxSettings,
  saveMailboxSettings,
  type MailboxSettingsView,
} from "@/features/webmail/mailbox-api";
import {
  ShieldAlert,
  ShieldCheck,
  CheckCircle2,
  AlertCircle,
  Loader2,
  RefreshCw,
} from "lucide-react";

export const REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT =
  "urmail.webmail.remote-images-preference-changed";

interface RemoteImagesSectionProps {
  onPreferenceChanged?: (allowAll: boolean) => void;
}

export function RemoteImagesSection({
  onPreferenceChanged,
}: RemoteImagesSectionProps) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isConflict, setIsConflict] = useState(false);

  const [settings, setSettings] = useState<MailboxSettingsView | null>(null);

  const loadSettings = async () => {
    try {
      setLoading(true);
      setError(null);
      setIsConflict(false);
      const data = await getMailboxSettings();
      setSettings(data);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.privacy.fetch_error", "Failed to load settings."),
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadSettings();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Fail-closed interpretation: true only if explicitly boolean true
  const isEnabled = settings?.preferences?.allow_all_remote_images === true;

  const handleToggle = async () => {
    if (!settings || saving || loading) return;

    const targetValue = !isEnabled;

    try {
      setSaving(true);
      setError(null);
      setIsConflict(false);
      setSuccess(false);

      // Preserve entire preferences map and other DTO fields faithfully
      const updatedPreferences = {
        ...(settings.preferences || {}),
        allow_all_remote_images: targetValue,
      };

      const res = await saveMailboxSettings({
        display_name: settings.display_name,
        identity_profiles: settings.identity_profiles,
        signature_text: settings.signature_text,
        signature_html: settings.signature_html,
        preferences: updatedPreferences,
        expected_version: settings.version,
      });

      // Update local state with new version and preferences
      const nextSettings: MailboxSettingsView = {
        ...settings,
        version: res.version,
        preferences: updatedPreferences,
      };
      setSettings(nextSettings);
      setSuccess(true);

      onPreferenceChanged?.(targetValue);
      if (typeof window !== "undefined") {
        window.dispatchEvent(
          new CustomEvent(REMOTE_IMAGES_PREFERENCE_CHANGED_EVENT, {
            detail: { allowAll: targetValue },
          }),
        );
      }

      setTimeout(() => setSuccess(false), 3000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      const isVersionConflict =
        msg.includes("409") ||
        msg.includes("MAILBOX_SETTINGS_VERSION_CONFLICT") ||
        msg.toLowerCase().includes("conflict");

      if (isVersionConflict) {
        setIsConflict(true);
        setError(
          t(
            "settings.privacy.conflict_error",
            "Settings were modified by another client. Refreshed to latest state. Please verify and retry.",
          ),
        );
        // Refresh to latest snapshot without blind replay
        try {
          const fresh = await getMailboxSettings();
          setSettings(fresh);
        } catch {
          // ignore refetch failure
        }
      } else {
        // Network timeout / unknown outcome: reconcile with GET
        setError(
          msg ||
            t(
              "settings.privacy.save_error",
              "Failed to save privacy settings.",
            ),
        );
        try {
          const fresh = await getMailboxSettings();
          setSettings(fresh);
        } catch {
          // ignore refetch failure
        }
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6" data-testid="remote-images-section">
      <div className="border-b border-[#EBE8E0] pb-4">
        <h3 className="text-base font-semibold text-[#18181B] flex items-center gap-2">
          <ShieldCheck className="w-5 h-5 text-[var(--brand-primary,#2563eb)]" />
          <span>{t("settings.privacy.title", "Privacy & Remote Content")}</span>
        </h3>
        <p className="text-xs text-[#71717A] mt-1">
          {t(
            "settings.privacy.subtitle",
            "Manage loading policies for external images and remote tracking content.",
          )}
        </p>
      </div>

      {/* Privacy Warning Banner */}
      <div
        data-testid="privacy-warning-banner"
        className="p-4 bg-[#FEF3C7]/40 border border-[#FDE68A] rounded-xl flex items-start gap-3 text-xs text-[#92400E]"
      >
        <ShieldAlert className="w-5 h-5 text-[#D97706] shrink-0 mt-0.5" />
        <div className="space-y-1">
          <h4 className="font-semibold text-[#78350F]">
            {t("settings.privacy.warning_title", "Privacy Advisory")}
          </h4>
          <p className="leading-relaxed">
            {t(
              "settings.privacy.remote_images_warning",
              "Privacy Warning: Requesting remote images discloses your email open time, IP address, ISP, and device client metadata to the sender or third parties, which can be used to track reads.",
            )}
          </p>
        </div>
      </div>

      {/* Main Switch Card */}
      <div className="p-4 bg-[#FAF9F5] border border-[#EBE8E0] rounded-xl space-y-3">
        <div className="flex items-center justify-between gap-4">
          <div className="flex-1 space-y-1">
            <span
              id="remote-images-switch-label"
              className="text-sm font-semibold text-[#18181B] block"
            >
              {t(
                "settings.privacy.remote_images_title",
                "Automatically load remote images for all messages",
              )}
            </span>
            <p className="text-xs text-[#71717A] leading-relaxed">
              {t(
                "settings.privacy.remote_images_desc",
                "When enabled, remote images in opened emails will be loaded automatically. When disabled, you can manually load images per message.",
              )}
            </p>
          </div>

          {/* Accessible Switch */}
          <button
            data-testid="switch-allow-remote-images"
            type="button"
            role="switch"
            aria-checked={isEnabled}
            aria-labelledby="remote-images-switch-label"
            disabled={loading || saving}
            onClick={handleToggle}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                handleToggle();
              }
            }}
            className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-hidden focus:ring-2 focus:ring-[var(--brand-primary,#2563eb)] focus:ring-offset-2 ${
              loading || saving ? "opacity-50 cursor-not-allowed" : ""
            } ${
              isEnabled ? "bg-[var(--brand-primary,#2563eb)]" : "bg-[#D4D4D8]"
            }`}
          >
            <span
              className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow-md ring-0 transition duration-200 ease-in-out ${
                isEnabled ? "translate-x-5" : "translate-x-0"
              }`}
            />
          </button>
        </div>

        {/* State feedback indicators */}
        {saving && (
          <div
            data-testid="privacy-status-saving"
            className="flex items-center gap-1.5 text-xs text-[#2563EB] pt-1"
          >
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            <span>{t("settings.privacy.saving", "Saving settings...")}</span>
          </div>
        )}

        {success && (
          <div
            data-testid="privacy-status-success"
            className="flex items-center gap-1.5 text-xs text-[#16A34A] pt-1"
          >
            <CheckCircle2 className="w-3.5 h-3.5" />
            <span>{t("settings.privacy.saved", "Settings saved")}</span>
          </div>
        )}

        {isConflict && (
          <div
            data-testid="privacy-status-conflict"
            className="flex items-center gap-1.5 text-xs text-[#DC2626] pt-1"
          >
            <RefreshCw className="w-3.5 h-3.5" />
            <span>{error}</span>
          </div>
        )}

        {error && !isConflict && (
          <div
            data-testid="privacy-status-error"
            className="flex items-center gap-1.5 text-xs text-[#DC2626] pt-1"
          >
            <AlertCircle className="w-3.5 h-3.5" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </div>
  );
}
