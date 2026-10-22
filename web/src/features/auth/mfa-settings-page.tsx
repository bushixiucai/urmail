import { useEffect, useState, FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, CheckCircle2, ShieldCheck } from "lucide-react";
import { ApiRequestError, getApiErrorKey, request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

interface MFASetupResponse {
  secret: string;
  otpauth_url: string;
  mfa_enabled: boolean;
}

interface MFAEnableResponse {
  status: string;
  mfa_enabled: boolean;
  recovery_codes: string[];
}

interface MFADisableResponse {
  status: string;
  mfa_enabled: boolean;
}

/**
 * MFA (TOTP) management: setup, enable with a first code, and disable.
 * Self-contained page component; wire it into a /settings route (App.tsx is
 * outside this task's file scope).
 */
export function MfaSettingsPage() {
  const { t } = useTranslation();
  const [secret, setSecret] = useState<string | null>(null);
  const [otpauthUrl, setOtpauthUrl] = useState<string | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [code, setCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);

  useEffect(() => {
    request<{ mfa_enabled: boolean; pending: boolean }>(
      "/api/v1/auth/mfa/status",
    )
      .then((data) => setEnabled(data.mfa_enabled))
      .catch(() => {});
  }, []);

  const errorText = (err: unknown, fallback: string): string => {
    const { key } = getApiErrorKey(err, fallback);
    return t(key, { defaultValue: t(fallback) });
  };

  const handleSetup = async () => {
    setError(null);
    setNotice(null);
    setIsLoading(true);
    try {
      const data = await request<MFASetupResponse>("/api/v1/auth/mfa/setup", {
        method: "POST",
        body: {},
      });
      setSecret(data.secret);
      setOtpauthUrl(data.otpauth_url);
    } catch (err: unknown) {
      if (
        err instanceof ApiRequestError &&
        err.code === "MFA_ALREADY_ENABLED"
      ) {
        setEnabled(true);
      }
      setError(errorText(err, "auth.mfa_no_pending_setup"));
    } finally {
      setIsLoading(false);
    }
  };

  const handleEnable = async (e: FormEvent) => {
    e.preventDefault();
    if (!secret) return;
    setError(null);
    setNotice(null);
    setIsLoading(true);
    try {
      const data = await request<MFAEnableResponse>("/api/v1/auth/mfa/enable", {
        method: "POST",
        body: { code },
      });
      setEnabled(true);
      setSecret(null);
      setOtpauthUrl(null);
      setCode("");
      setRecoveryCodes(data.recovery_codes);
      setNotice(t("page.settings.mfa.enable_success"));
    } catch (err: unknown) {
      setError(errorText(err, "auth.mfa_invalid_code"));
    } finally {
      setIsLoading(false);
    }
  };

  const handleDisable = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setNotice(null);
    setIsLoading(true);
    try {
      await request<MFADisableResponse>("/api/v1/auth/mfa/disable", {
        method: "POST",
        body: { code },
      });
      setEnabled(false);
      setSecret(null);
      setOtpauthUrl(null);
      setRecoveryCodes(null);
      setCode("");
      setNotice(t("page.settings.mfa.disable_success"));
    } catch (err: unknown) {
      setError(errorText(err, "auth.mfa_invalid_code"));
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <div className="max-w-xl mx-auto p-4 space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-xl">
            <ShieldCheck className="h-5 w-5" />
            {t("page.settings.mfa.title")}
          </CardTitle>
          <CardDescription>{t("page.settings.mfa.desc")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-sm">
            {t("page.settings.mfa.status_label")}:{" "}
            <span
              className={
                enabled
                  ? "font-medium text-green-600"
                  : "font-medium text-muted-foreground"
              }
            >
              {enabled
                ? t("page.settings.mfa.status_enabled")
                : t("page.settings.mfa.status_disabled")}
            </span>
          </p>

          {error && (
            <div className="p-3 text-sm bg-destructive/10 text-destructive rounded-md flex items-center gap-2">
              <AlertCircle className="h-4 w-4 shrink-0" />
              <span>{error}</span>
            </div>
          )}
          {notice && (
            <div className="p-3 text-sm bg-green-500/10 text-green-700 rounded-md flex items-center gap-2">
              <CheckCircle2 className="h-4 w-4 shrink-0" />
              <span>{notice}</span>
            </div>
          )}

          {recoveryCodes && (
            <div className="p-3 text-sm bg-muted rounded-md space-y-1">
              <p className="font-medium">
                {t("page.settings.mfa.recovery_title")}
              </p>
              <ul className="font-mono text-xs grid grid-cols-2 gap-y-1">
                {recoveryCodes.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            </div>
          )}

          {!enabled && !secret && (
            <Button onClick={handleSetup} disabled={isLoading}>
              {t("page.settings.mfa.start_setup")}
            </Button>
          )}

          {!enabled && secret && (
            <div className="space-y-3">
              <div className="space-y-1">
                <p className="text-sm font-medium">
                  {t("page.settings.mfa.secret_label")}
                </p>
                <code className="block p-2 bg-muted rounded font-mono text-sm break-all">
                  {secret}
                </code>
              </div>
              {otpauthUrl && (
                <div className="space-y-1">
                  <p className="text-sm font-medium">
                    {t("page.settings.mfa.otpauth_label")}
                  </p>
                  <code className="block p-2 bg-muted rounded font-mono text-xs break-all">
                    {otpauthUrl}
                  </code>
                </div>
              )}
              <form onSubmit={handleEnable} className="space-y-2">
                <Input
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder={t("page.settings.mfa.code_placeholder")}
                  autoComplete="one-time-code"
                  required
                />
                <Button type="submit" disabled={isLoading}>
                  {t("page.settings.mfa.enable")}
                </Button>
              </form>
            </div>
          )}

          {enabled && (
            <form onSubmit={handleDisable} className="space-y-2">
              <Input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder={t("page.settings.mfa.code_placeholder")}
                autoComplete="one-time-code"
                required
              />
              <Button type="submit" variant="destructive" disabled={isLoading}>
                {t("page.settings.mfa.disable")}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
