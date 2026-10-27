import { FormEvent, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

import {
  getMailboxSettings,
  saveMailboxSettings,
  listMailboxIdentities,
  getMailboxQuota,
  getMailboxRules,
  saveMailboxRules,
  MailboxSettingsView,
  MailboxIdentity,
  MailboxRulesView,
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
  const [identities, setIdentities] = useState<MailboxIdentity[]>([]);
  const [quota, setQuota] = useState<{
    used_bytes: number;
    quota_bytes: number;
  } | null>(null);
  const [rules, setRules] = useState<MailboxRulesView | null>(null);
  const [vacationOn, setVacationOn] = useState(false);
  const [vacationSubject, setVacationSubject] = useState("");
  const [vacationDays, setVacationDays] = useState(1);
  const [forwardOn, setForwardOn] = useState(false);
  const [forwardTarget, setForwardTarget] = useState("");
  const [forwardCopy, setForwardCopy] = useState(true);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
        setDisplayName(s.display_name);
        setSignatureText(s.signature_text);
        setSignatureHtml(s.signature_html);
        setIdentities(ids.identities);
        setQuota(q);
        setRules(r);
        setVacationOn(r.rules.vacation.enabled);
        setVacationSubject(r.rules.vacation.subject);
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

  const saveIdentity = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const next = await saveMailboxSettings({
        display_name: displayName,
        signature_text: signatureText,
        signature_html: signatureHtml,
        preferences: settings.preferences,
        expected_version: settings.version,
      });
      setSettings({
        ...settings,
        version: next.version,
        display_name: displayName,
        signature_text: signatureText,
        signature_html: signatureHtml,
      });
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

  const saveRules = async (e: FormEvent) => {
    e.preventDefault();
    if (!rules) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const next = await saveMailboxRules({
        rules: {
          rules: rules.rules.rules,
          vacation: {
            enabled: vacationOn,
            subject: vacationSubject,
            message: rules.rules.vacation.message || vacationSubject,
            days: vacationDays,
          },
          forwarding: forwardOn
            ? { enabled: true, target: forwardTarget, keep_copy: forwardCopy }
            : { enabled: false, target: "", keep_copy: false },
        },
        confirmed: true,
        expected_version: rules.version,
      });
      setRules({ ...rules, version: next.version });
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

  return (
    <main
      className="mx-auto max-w-2xl px-4 py-8 space-y-8"
      aria-label={t("page.webmail.settings_title")}
    >
      <header className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">
          {t("page.webmail.settings_title")}
        </h1>
        <Link to="/webmail/inbox" className="text-sm underline">
          {t("page.webmail.settings_back")}
        </Link>
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
          <fieldset className="space-y-2 border rounded p-3">
            <legend className="text-sm px-1">
              {t("page.webmail.settings_vacation")}
            </legend>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={vacationOn}
                onChange={(e) => setVacationOn(e.target.checked)}
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
                onChange={(e) => setVacationSubject(e.target.value)}
                disabled={!vacationOn}
              />
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
                onChange={(e) => setVacationDays(Number(e.target.value) || 1)}
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
                onChange={(e) => setForwardOn(e.target.checked)}
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
                onChange={(e) => setForwardTarget(e.target.value)}
                disabled={!forwardOn}
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={forwardCopy}
                onChange={(e) => setForwardCopy(e.target.checked)}
                disabled={!forwardOn}
              />
              {t("page.webmail.settings_forwarding_copy")}
            </label>
          </fieldset>
          <Button type="submit" disabled={busy || !rules}>
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
