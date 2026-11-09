import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  listMailboxIdentities,
  getMailboxSettings,
  saveMailboxSettings,
  type MailboxIdentity,
  type MailboxIdentityProfile,
  type MailboxSettingsView,
} from "@/features/webmail/mailbox-api";
import { sanitizeHtmlMail } from "@/features/webmail/sanitize-html-mail";
import {
  Users,
  PenTool,
  Save,
  CheckCircle2,
  AlertCircle,
  Eye,
  Type,
  Code,
  Bold,
  Italic,
  Link as LinkIcon,
} from "lucide-react";

export function IdentitiesSignaturesSection() {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [settings, setSettings] = useState<MailboxSettingsView | null>(null);
  const [identities, setIdentities] = useState<MailboxIdentity[]>([]);
  const [currentAddress, setCurrentAddress] = useState<string>("");

  // Editor states
  const [displayName, setDisplayName] = useState("");
  const [signatureText, setSignatureText] = useState("");
  const [signatureHtml, setSignatureHtml] = useState("");
  const [editorMode, setEditorMode] = useState<"visual" | "html">("visual");

  const loadData = async () => {
    try {
      setLoading(true);
      setError(null);
      const [identitiesRes, settingsRes] = await Promise.all([
        listMailboxIdentities(),
        getMailboxSettings(),
      ]);

      setIdentities(identitiesRes.identities || []);
      setSettings(settingsRes);

      // Default to primary or settings address
      const primary =
        identitiesRes.identities.find((id) => id.primary)?.address ||
        settingsRes.address ||
        identitiesRes.identities[0]?.address ||
        "";
      setCurrentAddress(primary);

      // Sync form with initial identity
      syncProfileForAddress(primary, settingsRes);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.identities.err_fetch", "Failed to load identities."),
      );
    } finally {
      setLoading(false);
    }
  };

  const syncProfileForAddress = (
    addr: string,
    currentSettings: MailboxSettingsView | null,
  ) => {
    if (!currentSettings) return;
    const profile = currentSettings.identity_profiles?.[addr];
    if (profile) {
      setDisplayName(profile.display_name || "");
      setSignatureText(profile.signature_text || "");
      setSignatureHtml(profile.signature_html || "");
    } else {
      // Default to top-level
      setDisplayName(currentSettings.display_name || "");
      setSignatureText(currentSettings.signature_text || "");
      setSignatureHtml(currentSettings.signature_html || "");
    }
  };

  useEffect(() => {
    loadData();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSelectIdentity = (addr: string) => {
    // Save draft of current identity before switching
    if (settings && currentAddress) {
      const updatedProfiles: Record<string, MailboxIdentityProfile> = {
        ...(settings.identity_profiles || {}),
        [currentAddress]: {
          display_name: displayName,
          signature_text: signatureText,
          signature_html: signatureHtml,
        },
      };
      setSettings({
        ...settings,
        identity_profiles: updatedProfiles,
      });
    }

    setCurrentAddress(addr);
    syncProfileForAddress(addr, settings);
  };

  const handleFormatText = (prefix: string, suffix: string) => {
    if (editorMode === "visual") {
      setSignatureHtml((prev) => `${prev}${prefix}${suffix}`);
    } else {
      setSignatureHtml((prev) => `${prev}${prefix}${suffix}`);
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!settings) return;

    try {
      setSaving(true);
      setError(null);
      setSuccess(false);

      const updatedProfiles: Record<string, MailboxIdentityProfile> = {
        ...(settings.identity_profiles || {}),
        [currentAddress]: {
          display_name: displayName,
          signature_text: signatureText,
          signature_html: signatureHtml,
        },
      };

      const res = await saveMailboxSettings({
        identity_profiles: updatedProfiles,
        display_name:
          currentAddress === settings.address
            ? displayName
            : settings.display_name,
        signature_text:
          currentAddress === settings.address
            ? signatureText
            : settings.signature_text,
        signature_html:
          currentAddress === settings.address
            ? signatureHtml
            : settings.signature_html,
        preferences: settings.preferences,
        expected_version: settings.version,
      });

      setSettings({
        ...settings,
        version: res.version,
        identity_profiles: updatedProfiles,
      });
      setSuccess(true);
      setTimeout(() => setSuccess(false), 3000);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(
        msg || t("settings.identities.err_save", "Failed to save settings."),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6" data-testid="identities-signatures-section">
      <div className="border-b border-[#EBE8E0] pb-4">
        <h3 className="text-base font-semibold text-[#18181B] flex items-center gap-2">
          <PenTool className="w-5 h-5 text-[var(--brand-primary,#2563eb)]" />
          <span>
            {t("settings.identities.title", "Identities & Signatures")}
          </span>
        </h3>
        <p className="text-xs text-[#52525B] mt-1">
          {t(
            "settings.identities.subtitle",
            "Configure display names, aliases, and tailored HTML/text signatures for each sending address.",
          )}
        </p>
      </div>

      {loading ? (
        <div className="p-6 text-center text-xs text-[var(--vellum-ink-secondary)]">
          {t("common.loading", "Loading identities...")}
        </div>
      ) : (
        <form onSubmit={handleSave} className="space-y-5 max-w-2xl">
          {error && (
            <div className="p-3 bg-[#FEF2F2] border border-[#FECACA] rounded-lg text-[#991B1B] text-xs flex items-center gap-2">
              <AlertCircle className="w-4 h-4 shrink-0" />
              <span>{error}</span>
            </div>
          )}

          {success && (
            <div className="p-3 bg-[#F0FDF4] border border-[#BBF7D0] rounded-lg text-[#166534] text-xs flex items-center gap-2">
              <CheckCircle2 className="w-4 h-4 shrink-0 text-[var(--vellum-success-text)]" />
              <span>
                {t(
                  "settings.identities.saved_success",
                  "Signature and identity saved successfully!",
                )}
              </span>
            </div>
          )}

          {/* Identity Address Dropdown */}
          <div>
            <label className="block text-xs font-semibold text-[#3F3F46] mb-1 flex items-center gap-1.5">
              <Users className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
              <span>
                {t("settings.identities.select_identity", "Sending Identity")}
              </span>
            </label>
            <select
              data-testid="select-identity"
              value={currentAddress}
              onChange={(e) => handleSelectIdentity(e.target.value)}
              className="w-full max-w-sm h-9 px-3 text-xs rounded-md border border-[#D4D4D8] bg-[#FFFFFF] focus:outline-none focus:border-[var(--brand-primary,#2563eb)]"
            >
              {identities.map((id) => (
                <option key={id.address} value={id.address}>
                  {id.address}{" "}
                  {id.primary ? `(${t("common.primary", "Primary")})` : ""}
                </option>
              ))}
            </select>
          </div>

          {/* Display Name */}
          <div>
            <label className="block text-xs font-semibold text-[#3F3F46] mb-1">
              {t("settings.identities.display_name", "Display Name")}
            </label>
            <input
              data-testid="input-display-name"
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="e.g. Tiger Wang"
              className="w-full max-w-sm h-9 px-3 text-xs rounded-md border border-[#D4D4D8] bg-[#FFFFFF] focus:outline-none focus:border-[var(--brand-primary,#2563eb)]"
            />
          </div>

          {/* Signature Editor */}
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label className="text-xs font-semibold text-[#3F3F46]">
                {t("settings.identities.signature_editor", "Email Signature")}
              </label>

              {/* Editor Tabs: Visual / HTML Source */}
              <div className="flex items-center gap-1 text-xs bg-[#EBE8E0] p-0.5 rounded">
                <button
                  type="button"
                  onClick={() => setEditorMode("visual")}
                  className={`px-2 py-0.5 rounded transition-colors ${
                    editorMode === "visual"
                      ? "bg-[#FFFFFF] text-[#18181B] font-semibold shadow-xs"
                      : "text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
                  }`}
                >
                  <Type className="w-3 h-3 inline mr-1" />
                  {t("settings.identities.mode_visual", "Visual / Plain")}
                </button>
                <button
                  type="button"
                  onClick={() => setEditorMode("html")}
                  className={`px-2 py-0.5 rounded transition-colors ${
                    editorMode === "html"
                      ? "bg-[#FFFFFF] text-[#18181B] font-semibold shadow-xs"
                      : "text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
                  }`}
                >
                  <Code className="w-3 h-3 inline mr-1" />
                  {t("settings.identities.mode_html", "HTML Code")}
                </button>
              </div>
            </div>

            {/* Quick formatting toolbar */}
            <div className="flex items-center gap-1 p-1 bg-[#F6F4EE] border border-[#EBE8E0] rounded-t-md text-xs">
              <button
                type="button"
                onClick={() => handleFormatText("<b>", "</b>")}
                className="p-1 hover:bg-[#EBE8E0] rounded text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
                title="Bold"
              >
                <Bold className="w-3.5 h-3.5" />
              </button>
              <button
                type="button"
                onClick={() => handleFormatText("<i>", "</i>")}
                className="p-1 hover:bg-[#EBE8E0] rounded text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
                title="Italic"
              >
                <Italic className="w-3.5 h-3.5" />
              </button>
              <button
                type="button"
                onClick={() => handleFormatText('<a href="https://">', "</a>")}
                className="p-1 hover:bg-[#EBE8E0] rounded text-[var(--vellum-ink-secondary)] hover:text-[#18181B]"
                title="Insert Link"
              >
                <LinkIcon className="w-3.5 h-3.5" />
              </button>
            </div>

            {editorMode === "visual" ? (
              <div className="space-y-3">
                <textarea
                  data-testid="textarea-signature-html"
                  value={signatureHtml}
                  onChange={(e) => {
                    setSignatureHtml(e.target.value);
                    setSignatureText(e.target.value.replace(/<[^>]+>/g, ""));
                  }}
                  rows={4}
                  placeholder={t(
                    "settings.identities.signature_placeholder",
                    "Best regards,\nTiger Wang\nEmail: tiger@urmail.cn",
                  )}
                  className="w-full p-3 text-xs rounded-b-md border border-t-0 border-[#D4D4D8] bg-[#FFFFFF] focus:outline-none focus:border-[var(--brand-primary,#2563eb)] font-sans"
                />
              </div>
            ) : (
              <textarea
                data-testid="textarea-signature-html-raw"
                value={signatureHtml}
                onChange={(e) => setSignatureHtml(e.target.value)}
                rows={5}
                className="w-full p-3 text-xs rounded-b-md border border-t-0 border-[#D4D4D8] bg-[#FFFFFF] font-mono text-[#18181B] focus:outline-none focus:border-[var(--brand-primary,#2563eb)]"
              />
            )}
          </div>

          {/* Live Signature Bottom Preview Box */}
          <div className="p-4 bg-[#FAF9F5] border border-[#EBE8E0] rounded-xl space-y-2">
            <div className="flex items-center gap-1.5 text-xs font-semibold text-[#3F3F46]">
              <Eye className="w-3.5 h-3.5 text-[var(--vellum-ink-secondary)]" />
              <span>
                {t(
                  "settings.identities.live_preview",
                  "Live Email Signature Preview",
                )}
              </span>
            </div>
            <div className="p-3 bg-[#FFFFFF] border border-[#EBE8E0] rounded-lg text-xs">
              <div className="text-[var(--vellum-ink-secondary)] select-none text-xs mb-2">
                ---{" "}
                {t(
                  "settings.identities.email_content_placeholder",
                  "Simulated email body content...",
                )}{" "}
                ---
              </div>
              <div className="text-[var(--vellum-ink-secondary)] text-xs mb-1.5 select-none">
                --{" "}
              </div>
              {signatureHtml ? (
                <div
                  data-testid="signature-rendered-preview"
                  className="text-xs text-[#18181B]"
                  dangerouslySetInnerHTML={{
                    __html: sanitizeHtmlMail(signatureHtml, {
                      allowRemoteImages: true,
                    }),
                  }}
                />
              ) : (
                <div className="text-xs text-[var(--vellum-ink-secondary)] italic">
                  {t(
                    "settings.identities.no_signature",
                    "No signature configured for this identity.",
                  )}
                </div>
              )}
            </div>
          </div>

          {/* Submit */}
          <button
            data-testid="btn-save-identity"
            type="submit"
            disabled={saving}
            className="px-4 py-2 bg-[var(--brand-primary,#2563eb)] text-white text-xs font-semibold rounded-md shadow-sm hover:brightness-105 active:brightness-95 disabled:opacity-50 transition-all flex items-center gap-1.5"
          >
            <Save className="w-3.5 h-3.5" />
            <span>
              {saving
                ? t("common.saving", "Saving...")
                : t("settings.identities.save_btn", "Save Signature")}
            </span>
          </button>
        </form>
      )}
    </div>
  );
}
