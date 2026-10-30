import { FormEvent, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ApiRequestError } from "@/lib/api/client";
import { AppearanceControls } from "@/components/appearance-controls";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sparkles } from "lucide-react";
import { formatZonedInput, zonedInputToISO } from "./people-time";

import {
  getMailboxSettings,
  saveMailboxSettings,
  listMailboxIdentities,
  getMailboxQuota,
  getMailboxRules,
  saveMailboxRules,
  MailboxSettingsView,
  MailboxIdentity,
  MailboxIdentityProfile,
  MailboxRulesView,
  MailboxSieveRule,
  MailboxUserAIConfig,
  fetchMailboxAIModels,
} from "./mailbox-api";

function formatBytes(
  n: number,
  t: (k: string, p?: Record<string, unknown>) => string,
): string {
  if (n < 0) return t("page.webmail.settings_quota_unlimited");
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return t("page.webmail.settings_quota_value", {
    value: unit === 0 ? value : Math.round(value * 10) / 10,
    unit: units[unit],
  });
}

export function MailboxSettingsPage() {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<MailboxSettingsView | null>(null);
  const [displayName, setDisplayName] = useState("");
  const [signatureText, setSignatureText] = useState("");
  const [signatureHtml, setSignatureHtml] = useState("");
  const [editorIdentity, setEditorIdentity] = useState("");
  const [profiles, setProfiles] = useState<
    Record<string, MailboxIdentityProfile>
  >({});
  const [defaultFrom, setDefaultFrom] = useState("");
  const [quoteOriginal, setQuoteOriginal] = useState(true);
  const [replyIdentity, setReplyIdentity] = useState("recipient");
  const [timezone, setTimezone] = useState(
    Intl.DateTimeFormat().resolvedOptions().timeZone,
  );
  const [dateFormat, setDateFormat] = useState("locale");
  const [identities, setIdentities] = useState<MailboxIdentity[]>([]);
  const [quota, setQuota] = useState<{
    used_bytes: number;
    quota_bytes: number;
  } | null>(null);
  const [rules, setRules] = useState<MailboxRulesView | null>(null);
  const [vacationOn, setVacationOn] = useState(false);
  const [vacationSubject, setVacationSubject] = useState("");
  const [vacationMessage, setVacationMessage] = useState("");
  const [vacationFrom, setVacationFrom] = useState("");
  const [vacationUntil, setVacationUntil] = useState("");
  const [vacationDST, setVacationDST] = useState<"earlier" | "later">(
    "earlier",
  );
  const originalVacation = useRef<{
    zone: string;
    from: string;
    until: string;
    fromInput: string;
    untilInput: string;
  } | null>(null);
  const changedVacationDST = useRef(false);
  const [headerRules, setHeaderRules] = useState<MailboxSieveRule[]>([]);
  const [rulesConfirmed, setRulesConfirmed] = useState(false);
  const [vacationDays, setVacationDays] = useState(1);
  const [forwardOn, setForwardOn] = useState(false);
  const [forwardTarget, setForwardTarget] = useState("");
  const [forwardCopy, setForwardCopy] = useState(true);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [userAIConfig, setUserAIConfig] = useState<MailboxUserAIConfig>({
    use_custom: false,
    provider: "openai",
    endpoint: "",
    model: "",
    api_key: "",
  });
  const [userAIModels, setUserAIModels] = useState<string[]>([]);
  const [userAIFetching, setUserAIFetching] = useState(false);
  const [userAINotice, setUserAINotice] = useState<{
    text: string;
    error?: boolean;
  } | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const [s, ids, q, r] = await Promise.all([
          getMailboxSettings(),
          listMailboxIdentities(),
          getMailboxQuota(),
          getMailboxRules(),
        ]);
        setSettings(s);
        const aiPref = s.preferences?.ai as MailboxUserAIConfig | undefined;
        if (aiPref) {
          setUserAIConfig({
            use_custom: Boolean(aiPref.use_custom),
            provider: aiPref.provider || "openai",
            endpoint: aiPref.endpoint || "",
            model: aiPref.model || "",
            api_key: aiPref.api_key || "",
          });
        }
        setQuoteOriginal(s.preferences.quote_original !== false);
        setEditorIdentity(s.address);
        setProfiles(s.identity_profiles ?? {});
        setDefaultFrom(
          typeof s.preferences.default_from === "string"
            ? s.preferences.default_from
            : s.address,
        );
        setReplyIdentity(
          s.preferences.reply_identity === "default" ? "default" : "recipient",
        );
        setTimezone(
          typeof s.preferences.timezone === "string"
            ? s.preferences.timezone
            : Intl.DateTimeFormat().resolvedOptions().timeZone,
        );
        setDateFormat(
          typeof s.preferences.date_format === "string"
            ? s.preferences.date_format
            : "locale",
        );
        setDisplayName(s.display_name);
        setSignatureText(s.signature_text);
        setSignatureHtml(s.signature_html);
        setIdentities(ids.identities);
        setQuota(q);
        setRules(r);
        setVacationOn(r.rules.vacation.enabled);
        setVacationSubject(r.rules.vacation.subject);
        setVacationMessage(r.rules.vacation.message);
        setHeaderRules(r.rules.rules ?? []);
        const zone =
          typeof s.preferences.timezone === "string"
            ? s.preferences.timezone
            : Intl.DateTimeFormat().resolvedOptions().timeZone;
        const initialFrom =
          r.rules.vacation.from && !r.rules.vacation.from.startsWith("0001")
            ? r.rules.vacation.from
            : new Date().toISOString();
        const initialUntil =
          r.rules.vacation.until && !r.rules.vacation.until.startsWith("0001")
            ? r.rules.vacation.until
            : new Date(Date.now() + 7 * 86400000).toISOString();
        const fromInput = formatZonedInput(initialFrom, zone),
          untilInput = formatZonedInput(initialUntil, zone);
        setVacationFrom(fromInput);
        setVacationUntil(untilInput);
        originalVacation.current = {
          zone,
          from: initialFrom,
          until: initialUntil,
          fromInput,
          untilInput,
        };
        changedVacationDST.current = false;
        setVacationDays(r.rules.vacation.days || 1);
        setForwardOn(Boolean(r.rules.forwarding?.enabled));
        setForwardTarget(r.rules.forwarding?.target ?? "");
        setForwardCopy(r.rules.forwarding?.keep_copy ?? true);
      } catch (err) {
        setError(
          err instanceof ApiRequestError
            ? t("error." + err.code, {
                defaultValue: t("page.webmail.settings_load_failed"),
              })
            : t("page.webmail.settings_load_failed"),
        );
      }
    })();
  }, [t]);

  const handleFetchUserModels = async () => {
    setUserAIFetching(true);
    setUserAINotice(null);
    try {
      const res = await fetchMailboxAIModels({
        provider: userAIConfig.provider,
        endpoint: userAIConfig.endpoint,
        api_key: userAIConfig.api_key,
        model: userAIConfig.model || "gpt-4o",
      });
      if (res.models && res.models.length > 0) {
        setUserAIModels(res.models);
        setUserAINotice({
          text: t("page.webmail.ai_models_count", { count: res.models.length }),
        });
        if (!userAIConfig.model && res.models[0]) {
          setUserAIConfig((prev) => ({ ...prev, model: res.models[0] }));
        }
      } else {
        setUserAINotice({ text: t("page.ai.reachable") });
      }
    } catch {
      setUserAINotice({ text: t("page.ai.health_failed"), error: true });
    } finally {
      setUserAIFetching(false);
    }
  };

  const saveIdentity = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const valid = new Set(identities.map((value) => value.address));
      const nextProfiles = Object.fromEntries(
        Object.entries({
          ...profiles,
          [editorIdentity]: {
            display_name: displayName,
            signature_text: signatureText,
            signature_html: signatureHtml,
          },
        }).filter(([address]) => valid.has(address)),
      );
      const preferences = {
        ...settings.preferences,
        default_from: valid.has(defaultFrom) ? defaultFrom : settings.address,
        reply_identity: replyIdentity,
        quote_original: quoteOriginal,
        timezone,
        date_format: dateFormat,
        ai: userAIConfig,
      };
      const primary = editorIdentity === settings.address;
      const next = await saveMailboxSettings({
        identity_profiles: nextProfiles,
        display_name: primary ? displayName : settings.display_name,
        signature_text: primary ? signatureText : settings.signature_text,
        signature_html: primary ? signatureHtml : settings.signature_html,
        preferences,
        expected_version: settings.version,
      });
      setSettings({
        ...settings,
        version: next.version,
        identity_profiles: nextProfiles,
        preferences,
        display_name: primary ? displayName : settings.display_name,
        signature_text: primary ? signatureText : settings.signature_text,
        signature_html: primary ? signatureHtml : settings.signature_html,
      });
      setProfiles(nextProfiles);
      setSaved(true);
    } catch (err) {
      setError(
        err instanceof ApiRequestError
          ? t("error." + err.code, {
              defaultValue: t("page.webmail.settings_save_failed"),
            })
          : t("page.webmail.settings_save_failed"),
      );
    } finally {
      setBusy(false);
    }
  };

  const editRule = (index: number, patch: Partial<MailboxSieveRule>) => {
    setRulesConfirmed(false);
    setHeaderRules((values) =>
      values.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)),
    );
  };
  const vacationISO = (which: "from" | "until") => {
    const value = which === "from" ? vacationFrom : vacationUntil,
      original = originalVacation.current;
    if (
      original &&
      original.zone === timezone &&
      !changedVacationDST.current &&
      value === (which === "from" ? original.fromInput : original.untilInput)
    )
      return original[which];
    return zonedInputToISO(value, timezone, vacationDST);
  };
  const saveRules = async (e: FormEvent) => {
    e.preventDefault();
    if (!rules || !rulesConfirmed) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      if (vacationOn) {
        const start = vacationISO("from"),
          end = vacationISO("until");
        if (
          new Date(end) <= new Date(start) ||
          new Date(end).getTime() - new Date(start).getTime() > 366 * 86400000
        )
          throw new Error("INVALID_LOCAL_TIME");
      }
      const next = await saveMailboxRules({
        rules: {
          rules: headerRules,
          vacation: {
            enabled: vacationOn,
            subject: vacationSubject,
            message: vacationMessage,
            from: vacationOn ? vacationISO("from") : undefined,
            until: vacationOn ? vacationISO("until") : undefined,
            days: vacationDays,
          },
          forwarding: forwardOn
            ? { enabled: true, target: forwardTarget, keep_copy: forwardCopy }
            : { enabled: false, target: "", keep_copy: false },
        },
        confirmed: true,
        expected_version: rules.version,
      });
      setRules({ ...rules, version: next.version, state: next.state });
      setRulesConfirmed(false);
      setSaved(true);
    } catch (err) {
      setError(
        err instanceof Error && err.message === "INVALID_LOCAL_TIME"
          ? t("page.webmail.settings_invalid_local_time")
          : err instanceof ApiRequestError
            ? t("error." + err.code, {
                defaultValue: t("page.webmail.settings_save_failed"),
              })
            : t("page.webmail.settings_save_failed"),
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <main
      className="mx-auto max-w-2xl px-4 py-8 space-y-8"
      aria-label={t("page.webmail.settings_title")}
    >
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">
          {t("page.webmail.settings_title")}
        </h1>
        <Link to="/webmail/inbox" className="text-sm underline">
          {t("page.webmail.settings_back")}
        </Link>
        <AppearanceControls className="w-full sm:w-auto" />
      </header>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {saved && (
        <p role="status" className="text-sm text-green-700 dark:text-green-400">
          {t("page.webmail.settings_saved")}
        </p>
      )}

      <section aria-labelledby="settings-identity" className="space-y-3">
        <h2 id="settings-identity" className="font-medium">
          {t("page.webmail.settings_identity")}
        </h2>
        <form onSubmit={(e) => void saveIdentity(e)} className="space-y-3">
          <label className="block space-y-1 text-sm">
            <span>{t("page.webmail.identity_profile")}</span>
            <select
              value={editorIdentity}
              className="h-11 w-full rounded-md border bg-background px-3"
              onChange={(e) => {
                const selected = e.target.value;
                const next = {
                  ...profiles,
                  [editorIdentity]: {
                    display_name: displayName,
                    signature_text: signatureText,
                    signature_html: signatureHtml,
                  },
                };
                setProfiles(next);
                setEditorIdentity(selected);
                const profile =
                  next[selected] ??
                  (selected === settings?.address ? settings : null);
                setDisplayName(profile?.display_name ?? "");
                setSignatureText(profile?.signature_text ?? "");
                setSignatureHtml(profile?.signature_html ?? "");
                setSaved(false);
              }}
            >
              {identities.map((identity) => (
                <option key={identity.address} value={identity.address}>
                  {identity.address}
                </option>
              ))}
            </select>
          </label>
          <p className="text-xs text-muted-foreground">
            {t("page.webmail.identity_signature_scope")}
          </p>
          <div className="space-y-1">
            <label htmlFor="display-name" className="text-sm font-medium">
              {t("page.webmail.settings_display_name")}
            </label>
            <Input
              id="display-name"
              value={displayName}
              maxLength={128}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="signature-text" className="text-sm font-medium">
              {t("page.webmail.settings_signature_text")}
            </label>
            <textarea
              id="signature-text"
              className="min-h-24 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
              value={signatureText}
              onChange={(e) => setSignatureText(e.target.value)}
            />
          </div>
          <div className="space-y-1">
            <label htmlFor="signature-html" className="text-sm font-medium">
              {t("page.webmail.settings_signature_html")}
            </label>
            <textarea
              id="signature-html"
              className="min-h-24 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm"
              value={signatureHtml}
              onChange={(e) => setSignatureHtml(e.target.value)}
            />
          </div>
          <label className="flex min-h-11 items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={quoteOriginal}
              onChange={(e) => setQuoteOriginal(e.target.checked)}
            />
            {t("page.webmail.settings_quote_original")}
          </label>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block space-y-1 text-sm">
              <span>{t("page.webmail.identity_default_from")}</span>
              <select
                className="h-11 w-full rounded-md border bg-background px-3"
                value={defaultFrom}
                onChange={(e) => setDefaultFrom(e.target.value)}
              >
                {identities.map((value) => (
                  <option key={value.address} value={value.address}>
                    {value.address}
                  </option>
                ))}
              </select>
            </label>
            <label className="block space-y-1 text-sm">
              <span>{t("page.webmail.identity_reply_default")}</span>
              <select
                className="h-11 w-full rounded-md border bg-background px-3"
                value={replyIdentity}
                onChange={(e) => setReplyIdentity(e.target.value)}
              >
                <option value="recipient">
                  {t("page.webmail.identity_reply_recipient")}
                </option>
                <option value="default">
                  {t("page.webmail.identity_reply_preferred")}
                </option>
              </select>
            </label>
            <label
              className="block space-y-1 text-sm"
              htmlFor="mailbox-timezone"
            >
              <span>{t("page.webmail.settings_timezone")}</span>
              <Input
                id="mailbox-timezone"
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
                placeholder="Asia/Shanghai"
                required
              />
            </label>
            <label className="block space-y-1 text-sm">
              <span>{t("page.webmail.settings_date_format")}</span>
              <select
                className="h-11 w-full rounded-md border bg-background px-3"
                value={dateFormat}
                onChange={(e) => setDateFormat(e.target.value)}
              >
                {["locale", "iso", "day_first", "month_first"].map((format) => (
                  <option key={format} value={format}>
                    {t("page.webmail.settings_date_" + format)}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <Button type="submit" disabled={busy}>
            {t("page.webmail.settings_save")}
          </Button>
        </form>
      </section>

      <section aria-labelledby="settings-senders" className="space-y-2">
        <h2 id="settings-senders" className="font-medium">
          {t("page.webmail.settings_senders")}
        </h2>
        <ul className="text-sm space-y-1">
          {identities.map((id) => (
            <li key={id.address} className="flex items-center gap-2">
              <span>{id.address}</span>
              {id.primary && (
                <span className="text-xs text-muted-foreground">
                  {t("page.webmail.settings_primary")}
                </span>
              )}
            </li>
          ))}
        </ul>
      </section>

      <section aria-labelledby="settings-rules" className="space-y-3">
        <h2 id="settings-rules" className="font-medium">
          {t("page.webmail.settings_rules")}
        </h2>
        <form onSubmit={(e) => void saveRules(e)} className="space-y-3">
          <fieldset className="space-y-3 rounded border p-3">
            <legend className="px-1 text-sm">
              {t("page.webmail.settings_header_rules")}
            </legend>
            <p className="text-xs text-muted-foreground">
              {t("page.webmail.settings_rule_order")}
            </p>
            {headerRules.map((rule, index) => (
              <div
                key={index}
                className="space-y-2 border-b pb-3 last:border-0"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <label className="flex min-h-11 items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={!rule.disabled}
                      onChange={(e) =>
                        editRule(index, { disabled: !e.target.checked })
                      }
                    />
                    {t("page.webmail.settings_rule_enabled", {
                      number: index + 1,
                    })}
                  </label>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={index === 0 || busy}
                    onClick={() => {
                      setRulesConfirmed(false);
                      setHeaderRules((values) => {
                        const copy = [...values];
                        [copy[index - 1], copy[index]] = [
                          copy[index],
                          copy[index - 1],
                        ];
                        return copy;
                      });
                    }}
                  >
                    {t("page.webmail.settings_rule_up")}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setRulesConfirmed(false);
                      setHeaderRules((values) =>
                        values.filter((_, i) => i !== index),
                      );
                    }}
                  >
                    {t("page.webmail.settings_rule_remove")}
                  </Button>
                </div>
                <div className="grid gap-2 sm:grid-cols-2">
                  <label className="space-y-1 text-sm">
                    <span>{t("page.webmail.settings_rule_header")}</span>
                    <select
                      aria-label={t(
                        "page.webmail.settings_rule_header_number",
                        { number: index + 1 },
                      )}
                      className="h-11 w-full rounded-md border bg-background px-2"
                      value={rule.header}
                      onChange={(e) =>
                        editRule(index, {
                          header: e.target.value as MailboxSieveRule["header"],
                        })
                      }
                    >
                      {["subject", "from", "to", "cc"].map((header) => (
                        <option key={header} value={header}>
                          {t("page.webmail.settings_header_" + header)}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="space-y-1 text-sm">
                    <span>{t("page.webmail.settings_rule_match")}</span>
                    <select
                      className="h-11 w-full rounded-md border bg-background px-2"
                      value={rule.match}
                      onChange={(e) =>
                        editRule(index, {
                          match: e.target.value as MailboxSieveRule["match"],
                        })
                      }
                    >
                      <option value="contains">
                        {t("page.webmail.settings_match_contains")}
                      </option>
                      <option value="is">
                        {t("page.webmail.settings_match_is")}
                      </option>
                    </select>
                  </label>
                  <label className="space-y-1 text-sm">
                    <span>{t("page.webmail.settings_rule_value")}</span>
                    <Input
                      aria-label={t("page.webmail.settings_rule_value_number", {
                        number: index + 1,
                      })}
                      required
                      maxLength={2000}
                      value={rule.value}
                      onChange={(e) =>
                        editRule(index, { value: e.target.value })
                      }
                    />
                  </label>
                  <label className="space-y-1 text-sm">
                    <span>{t("page.webmail.settings_rule_action")}</span>
                    <select
                      className="h-11 w-full rounded-md border bg-background px-2"
                      value={rule.action}
                      onChange={(e) =>
                        editRule(index, {
                          action: e.target.value as MailboxSieveRule["action"],
                        })
                      }
                    >
                      {["keep", "fileinto", "discard"].map((action) => (
                        <option key={action} value={action}>
                          {t("page.webmail.settings_action_" + action)}
                        </option>
                      ))}
                    </select>
                  </label>
                </div>
                {rule.action === "fileinto" && (
                  <label className="block space-y-1 text-sm">
                    <span>{t("page.webmail.settings_rule_folder")}</span>
                    <Input
                      required
                      maxLength={128}
                      value={rule.folder ?? ""}
                      onChange={(e) =>
                        editRule(index, { folder: e.target.value })
                      }
                    />
                  </label>
                )}
              </div>
            ))}
            <Button
              type="button"
              variant="outline"
              disabled={busy || headerRules.length >= 50}
              onClick={() => {
                setRulesConfirmed(false);
                setHeaderRules((values) => [
                  ...values,
                  {
                    header: "subject",
                    match: "contains",
                    value: "",
                    action: "keep",
                  },
                ]);
              }}
            >
              {t("page.webmail.settings_rule_add")}
            </Button>
          </fieldset>

          <fieldset className="space-y-2 border rounded p-3">
            <legend className="text-sm px-1">
              {t("page.webmail.settings_vacation")}
            </legend>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={vacationOn}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setVacationOn(e.target.checked);
                }}
              />
              {t("page.webmail.settings_vacation_enable")}
            </label>
            <div className="space-y-1">
              <label htmlFor="vacation-subject" className="text-sm font-medium">
                {t("page.webmail.settings_vacation_subject")}
              </label>
              <Input
                id="vacation-subject"
                value={vacationSubject}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setVacationSubject(e.target.value);
                }}
                disabled={!vacationOn}
              />
            </div>
            <label
              className="block space-y-1 text-sm"
              htmlFor="vacation-message"
            >
              <span>{t("page.webmail.settings_vacation_message")}</span>
              <textarea
                id="vacation-message"
                className="min-h-24 w-full rounded-md border bg-background p-2"
                maxLength={8192}
                value={vacationMessage}
                disabled={!vacationOn}
                required={vacationOn}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setVacationMessage(e.target.value);
                }}
              />
            </label>
            <label className="block space-y-1 text-sm">
              <span>{t("page.webmail.settings_vacation_dst")}</span>
              <select
                className="h-11 w-full rounded-md border bg-background px-2"
                value={vacationDST}
                disabled={!vacationOn}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  changedVacationDST.current = true;
                  setVacationDST(e.target.value as "earlier" | "later");
                }}
              >
                <option value="earlier">
                  {t("page.webmail.settings_dst_earlier")}
                </option>
                <option value="later">
                  {t("page.webmail.settings_dst_later")}
                </option>
              </select>
            </label>
            <p className="text-xs text-muted-foreground">
              {t("page.webmail.settings_vacation_zone", { zone: timezone })}
            </p>
            <div className="grid gap-2 sm:grid-cols-2">
              {[
                [
                  "vacation-from",
                  vacationFrom,
                  setVacationFrom,
                  "page.webmail.settings_vacation_from",
                ],
                [
                  "vacation-until",
                  vacationUntil,
                  setVacationUntil,
                  "page.webmail.settings_vacation_until",
                ],
              ].map(([id, value, setter, label]) => (
                <label key={id as string} className="space-y-1 text-sm">
                  <span>{t(label as string)}</span>
                  <Input
                    id={id as string}
                    type="datetime-local"
                    disabled={!vacationOn}
                    required={vacationOn}
                    value={value as string}
                    onChange={(e) => {
                      setRulesConfirmed(false);
                      (setter as (value: string) => void)(e.target.value);
                    }}
                  />
                </label>
              ))}
            </div>
            <div className="space-y-1">
              <label htmlFor="vacation-days" className="text-sm font-medium">
                {t("page.webmail.settings_vacation_days")}
              </label>
              <Input
                id="vacation-days"
                type="number"
                min={1}
                max={30}
                value={vacationDays}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setVacationDays(Number(e.target.value) || 1);
                }}
                disabled={!vacationOn}
              />
            </div>
          </fieldset>
          <fieldset className="space-y-2 border rounded p-3">
            <legend className="text-sm px-1">
              {t("page.webmail.settings_forwarding")}
            </legend>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={forwardOn}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setForwardOn(e.target.checked);
                }}
              />
              {t("page.webmail.settings_forwarding_enable")}
            </label>
            <div className="space-y-1">
              <label htmlFor="forward-target" className="text-sm font-medium">
                {t("page.webmail.settings_forwarding_target")}
              </label>
              <Input
                id="forward-target"
                type="email"
                value={forwardTarget}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setForwardTarget(e.target.value);
                }}
                disabled={!forwardOn}
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={forwardCopy}
                onChange={(e) => {
                  setRulesConfirmed(false);
                  setForwardCopy(e.target.checked);
                }}
                disabled={!forwardOn}
              />
              {t("page.webmail.settings_forwarding_copy")}
            </label>
          </fieldset>
          <label className="flex min-h-11 items-start gap-2 py-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={rulesConfirmed}
              onChange={(e) => setRulesConfirmed(e.target.checked)}
            />
            <span>{t("page.webmail.settings_rules_confirm")}</span>
          </label>
          <Button
            type="submit"
            disabled={
              busy ||
              !rules ||
              !rulesConfirmed ||
              rules.state === "legacy_managesieve"
            }
          >
            {t("page.webmail.settings_save")}
          </Button>
        </form>
      </section>

      <section aria-labelledby="settings-quota" className="space-y-2">
        <h2 id="settings-quota" className="font-medium">
          {t("page.webmail.settings_quota")}
        </h2>
        {quota && (
          <p className="text-sm">
            {t("page.webmail.settings_quota_usage", {
              used: formatBytes(quota.used_bytes, t),
              total: formatBytes(quota.quota_bytes, t),
            })}
          </p>
        )}
      </section>

      <section aria-labelledby="settings-ai" className="space-y-4">
        <div>
          <h2 id="settings-ai" className="font-medium text-base">
            {t("page.webmail.ai_settings_title")}
          </h2>
          <p className="text-xs text-muted-foreground mt-0.5">
            {t("page.webmail.ai_use_custom_desc")}
          </p>
        </div>

        <div className="p-4 rounded-xl border border-border/70 bg-card space-y-4 shadow-2xs">
          <label className="flex items-center gap-2.5 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={userAIConfig.use_custom ?? false}
              onChange={(e) => {
                setUserAIConfig((prev) => ({
                  ...prev,
                  use_custom: e.target.checked,
                }));
                setSaved(false);
              }}
              className="h-4 w-4 rounded border-input text-primary accent-primary"
            />
            <span className="text-sm font-medium">
              {t("page.webmail.ai_use_custom")}
            </span>
          </label>

          {userAIConfig.use_custom && (
            <div className="space-y-3 pt-2 border-t border-border/50">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-sm">
                <label className="space-y-1">
                  <span className="text-xs text-muted-foreground">
                    服务提供商 (Provider)
                  </span>
                  <select
                    className="h-9 w-full rounded-md border bg-background px-3 text-sm"
                    value={userAIConfig.provider ?? "openai"}
                    onChange={(e) => {
                      setUserAIConfig((prev) => ({
                        ...prev,
                        provider: e.target.value as
                          "openai" | "ollama" | "azure",
                      }));
                      setSaved(false);
                    }}
                  >
                    <option value="openai">
                      OpenAI 兼容 (DeepSeek / OpenAI / 阿里百炼 / 硅基流动等)
                    </option>
                    <option value="ollama">Ollama (本地私有部署)</option>
                    <option value="azure">Azure OpenAI</option>
                  </select>
                </label>

                <label className="space-y-1">
                  <span className="text-xs text-muted-foreground">
                    API 接口端点 (Endpoint)
                  </span>
                  <Input
                    type="url"
                    className="h-9 text-sm"
                    placeholder="https://api.deepseek.com/v1"
                    value={userAIConfig.endpoint ?? ""}
                    onChange={(e) => {
                      setUserAIConfig((prev) => ({
                        ...prev,
                        endpoint: e.target.value,
                      }));
                      setSaved(false);
                    }}
                  />
                </label>

                <label className="space-y-1 sm:col-span-2">
                  <span className="text-xs text-muted-foreground">
                    API 密钥 (API Key)
                  </span>
                  <Input
                    type="password"
                    autoComplete="new-password"
                    className="h-9 text-sm font-mono"
                    placeholder="sk-..."
                    value={userAIConfig.api_key ?? ""}
                    onChange={(e) => {
                      setUserAIConfig((prev) => ({
                        ...prev,
                        api_key: e.target.value,
                      }));
                      setSaved(false);
                    }}
                  />
                </label>

                <div className="sm:col-span-2 space-y-2">
                  <div className="flex items-end gap-2">
                    <label className="flex-1 space-y-1">
                      <span className="text-xs text-muted-foreground">
                        模型名称 (Model)
                      </span>
                      <Input
                        list="user-ai-models-list"
                        className="h-9 text-sm font-mono"
                        placeholder="例如：deepseek-chat 或 gpt-4o"
                        value={userAIConfig.model ?? ""}
                        onChange={(e) => {
                          setUserAIConfig((prev) => ({
                            ...prev,
                            model: e.target.value,
                          }));
                          setSaved(false);
                        }}
                      />
                      <datalist id="user-ai-models-list">
                        {userAIModels.map((m) => (
                          <option key={m} value={m} />
                        ))}
                      </datalist>
                    </label>

                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      className="h-9 shrink-0 gap-1.5"
                      disabled={userAIFetching || !userAIConfig.endpoint}
                      onClick={handleFetchUserModels}
                    >
                      <Sparkles className="h-3.5 w-3.5" />
                      {userAIFetching
                        ? "拉取中..."
                        : t("page.webmail.ai_test_fetch")}
                    </Button>
                  </div>

                  {userAINotice && (
                    <p
                      className={`text-xs ${
                        userAINotice.error
                          ? "text-destructive"
                          : "text-emerald-600 dark:text-emerald-400"
                      }`}
                    >
                      {userAINotice.text}
                    </p>
                  )}
                </div>
              </div>
            </div>
          )}

          <div className="pt-2 border-t flex justify-end">
            <Button
              type="button"
              size="sm"
              disabled={busy}
              onClick={(e) => void saveIdentity(e as unknown as FormEvent)}
            >
              {t("action.save")}
            </Button>
          </div>
        </div>
      </section>

      <section aria-labelledby="settings-password" className="space-y-2">
        <h2 id="settings-password" className="font-medium">
          {t("page.webmail.password_title")}
        </h2>
        <Link to="/webmail/inbox" className="text-sm underline">
          {t("page.webmail.settings_change_password")}
        </Link>
      </section>
    </main>
  );
}
