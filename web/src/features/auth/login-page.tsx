import {
  BrandAppearance,
  safeBrandURL,
  usePublicBrand,
} from "@/components/brand/public-brand";
import { useState, FormEvent } from "react";
import { usePublicStatus } from "./public-status";
import { Link, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Mail, Lock, AlertCircle, ArrowRight } from "lucide-react";
import { useAuth, User, TenantInfo } from "@/app/auth-context";
import { useI18nLocale } from "@/app/providers";
import { ApiRequestError, request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

interface LoginResponse {
  token: string;
  csrf_token: string;
  user: User;
  tenants: TenantInfo[];
  is_admin: boolean;
  current_tenant_id?: number | null;
  mfa_required?: boolean;
  mfa_token?: string;
}

export function LoginPage() {
  const navigate = useNavigate();
  const publicStatus = usePublicStatus();
  const brand = usePublicBrand();
  const { t } = useTranslation();
  const { login } = useAuth();
  const { setLocale } = useI18nLocale();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [remember, setRemember] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  // Second MFA login leg: set once the first leg returns mfa_required.
  const [mfaToken, setMfaToken] = useState<string | null>(null);
  const [mfaCode, setMfaCode] = useState("");

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setIsLoading(true);

    try {
      const data = await request<LoginResponse>("/api/v1/auth/login", {
        method: "POST",
        body: { email, password },
      });

      if (data.mfa_required && data.mfa_token) {
        setMfaToken(data.mfa_token);
        return;
      }

      login(
        data.token,
        data.csrf_token,
        data.user,
        data.tenants,
        data.is_admin,
        data.current_tenant_id,
        remember,
      );
      setLocale(data.user.locale || "zh-CN");
      navigate("/console");
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        setError(
          t(`error.${err.code}`, { defaultValue: t("error.LOGIN_FAILED") }),
        );
      } else {
        setError(t("error.LOGIN_FAILED"));
      }
    } finally {
      setIsLoading(false);
    }
  };

  const handleMfaVerify = async (e: FormEvent) => {
    e.preventDefault();
    if (!mfaToken) return;
    setError(null);
    setIsLoading(true);

    try {
      const data = await request<LoginResponse>("/api/v1/auth/mfa/verify", {
        method: "POST",
        body: { mfa_token: mfaToken, code: mfaCode },
      });
      login(
        data.token,
        data.csrf_token,
        data.user,
        data.tenants,
        data.is_admin,
        data.current_tenant_id,
        remember,
      );
      setLocale(data.user.locale || "zh-CN");
      navigate("/console");
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        setError(
          t("error." + err.code, {
            defaultValue: t("auth.mfa_invalid_code"),
          }),
        );
      } else {
        setError(t("auth.mfa_invalid_code"));
      }
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <div className="min-h-screen bg-muted/40 flex items-center justify-center p-4">
      <BrandAppearance brand={brand} />
      <Card className="w-full max-w-md shadow-lg border-muted">
        <CardHeader className="space-y-1 text-center">
          {safeBrandURL(brand?.logo_url) ? (
            <img
              src={safeBrandURL(brand?.logo_url)}
              alt={brand?.name ?? ""}
              className="mx-auto mb-2 h-12 max-w-full object-contain"
            />
          ) : (
            <div className="h-12 w-12 rounded-xl bg-primary text-primary-foreground mx-auto flex items-center justify-center font-bold text-xl mb-2">
              {brand?.name?.charAt(0) ?? "U"}
            </div>
          )}
          <CardTitle className="text-2xl font-bold">
            {brand?.name ?? "urMail.cn"}
          </CardTitle>
          <CardDescription>{t("login.subtitle")}</CardDescription>
        </CardHeader>
        <CardContent>
          {publicStatus.quarantined && (
            <p role="alert" className="mb-3 text-sm">
              {t("restore.quarantine_banner")}
            </p>
          )}
          {mfaToken ? (
            <form onSubmit={handleMfaVerify} className="space-y-4">
              {error && (
                <div className="p-3 text-sm bg-destructive/10 text-destructive rounded-md flex items-center gap-2">
                  <AlertCircle className="h-4 w-4 shrink-0" />
                  <span>{error}</span>
                </div>
              )}
              <div className="space-y-2">
                <label
                  className="text-sm font-medium leading-none"
                  htmlFor="mfa_code"
                >
                  {t("login.mfa_code_label")}
                </label>
                <Input
                  id="mfa_code"
                  autoComplete="one-time-code"
                  placeholder="123456"
                  value={mfaCode}
                  onChange={(e) => setMfaCode(e.target.value)}
                  required
                  autoFocus
                />
                <p className="text-xs text-muted-foreground">
                  {t("login.mfa_code_hint")}
                </p>
              </div>
              <Button type="submit" className="w-full" disabled={isLoading}>
                {isLoading ? t("state.loading") : t("login.mfa_verify")}
              </Button>
            </form>
          ) : (
            <form onSubmit={handleSubmit} className="space-y-4">
              {error && (
                <div className="p-3 text-sm bg-destructive/10 text-destructive rounded-md flex items-center gap-2">
                  <AlertCircle className="h-4 w-4 shrink-0" />
                  <span>{error}</span>
                </div>
              )}

              <div className="space-y-2">
                <label
                  className="text-sm font-medium leading-none"
                  htmlFor="email"
                >
                  {t("login.email_label")}
                </label>
                <div className="relative">
                  <Mail className="absolute left-3 top-3 h-4 w-4 text-muted-foreground" />
                  <Input
                    id="email"
                    type="email"
                    placeholder="admin@example.com"
                    className="pl-9"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    required
                  />
                </div>
              </div>

              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <label
                    className="text-sm font-medium leading-none"
                    htmlFor="password"
                  >
                    {t("login.password_label")}
                  </label>
                </div>
                <div className="relative">
                  <Lock className="absolute left-3 top-3 h-4 w-4 text-muted-foreground" />
                  <Input
                    id="password"
                    type="password"
                    placeholder="••••••••"
                    className="pl-9"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                  />
                </div>
              </div>

              <div className="flex items-center space-x-2">
                <input
                  id="remember"
                  type="checkbox"
                  checked={remember}
                  onChange={(e) => setRemember(e.target.checked)}
                  className="h-4 w-4 rounded border-input text-primary focus:ring-ring"
                />
                <label
                  htmlFor="remember"
                  className="text-sm font-medium leading-none cursor-pointer text-muted-foreground select-none"
                >
                  {t("login.remember_me", { defaultValue: "记住登录状态" })}
                </label>
              </div>

              <Button type="submit" className="w-full" disabled={isLoading}>
                {isLoading ? t("login.signing_in") : t("login.submit")}
                {!isLoading && <ArrowRight className="h-4 w-4 ml-2" />}
              </Button>
            </form>
          )}

          <div className="mt-4 pt-4 border-t space-y-2 text-center text-sm">
            <Link
              to="/webmail/login"
              className="inline-flex items-center justify-center gap-1.5 font-medium text-primary hover:underline"
            >
              <Mail className="h-4 w-4" />
              {t("login.switch_to_webmail", { defaultValue: "员工邮箱登录" })}
            </Link>
            <div className="flex items-center justify-center gap-4 text-xs text-muted-foreground pt-1">
              <Link className="hover:underline" to="/pricing">
                {t("pricing.title")}
              </Link>
              {publicStatus.registration && (
                <Link className="hover:underline" to="/register">
                  {t("registration.action")}
                </Link>
              )}
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
