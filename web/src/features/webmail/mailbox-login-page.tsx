import { FormEvent, useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { AlertCircle, Building2, Lock, Mail } from "lucide-react";
import { ApiRequestError } from "@/lib/api/client";
import "@/features/webmail-next/theme-vellum.css";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  createMailboxSession,
  hasMailboxSession,
  setMailboxSession,
} from "./mailbox-api";
import { useMailboxNative } from "./use-mailbox-native";
import { WebmailHelpPage } from "./webmail-help-page";

export function MailboxLoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { native, loaded } = useMailboxNative();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Session-expiry recovery (REVIEW-FE-008): honour ?next= so re-login
  // returns the user to the exact webmail route they were on. Only in-app
  // webmail routes are accepted — never an off-origin or login-loop target.
  const nextParam = searchParams.get("next");
  const next =
    nextParam &&
    nextParam.startsWith("/webmail/") &&
    !nextParam.startsWith("/webmail/login")
      ? nextParam
      : "/webmail/inbox";

  if (!loaded) {
    return (
      <div className="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
        {t("state.loading")}
      </div>
    );
  }

  if (!native) {
    return (
      <main className="mx-auto max-w-lg px-4 py-10">
        <WebmailHelpPage />
      </main>
    );
  }

  if (hasMailboxSession()) {
    return <Navigate to={next} replace />;
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const session = await createMailboxSession(
        email.trim(),
        password,
        remember,
      );
      setMailboxSession(session, remember);
      navigate(next, { replace: true });
    } catch (err: unknown) {
      if (err instanceof ApiRequestError) {
        setError(
          t(`error.${err.code}`, {
            defaultValue: t("page.webmail.login_failed"),
          }),
        );
      } else {
        setError(t("page.webmail.login_failed"));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="vellum-shell min-h-screen flex items-center justify-center bg-background px-4 py-10">
      <Card className="w-full max-w-md">
        <CardHeader className="space-y-1 text-center">
          {/* Deterministic default brand mark (REVIEW pm-brand-batch1-1 ⑥):
              always renders the neutral ink glyph when no tenant logo is
              loaded on this surface — no fetch-dependent fallback. */}
          <div
            aria-hidden
            className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-xl bg-primary font-bold text-xl text-primary-foreground select-none"
          >
            u
          </div>
          <CardTitle>{t("page.webmail.login_title")}</CardTitle>
          <CardDescription>
            {t("page.webmail.independent_identity")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={onSubmit}>
            {error && (
              <p
                role="alert"
                className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive"
              >
                <AlertCircle className="h-4 w-4 mt-0.5 shrink-0" />
                {error}
              </p>
            )}
            <div className="space-y-2">
              <label htmlFor="mailbox-email" className="text-sm font-medium">
                {t("page.webmail.field_email")}
              </label>
              <div className="relative">
                <Mail className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  id="mailbox-email"
                  type="email"
                  autoComplete="username"
                  required
                  className="pl-9"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </div>
            </div>
            <div className="space-y-2">
              <label htmlFor="mailbox-password" className="text-sm font-medium">
                {t("page.webmail.field_password")}
              </label>
              <div className="relative">
                <Lock className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  id="mailbox-password"
                  type="password"
                  autoComplete="current-password"
                  required
                  className="pl-9"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </div>
            </div>

            <div className="flex items-center space-x-2">
              <input
                id="mailbox-remember"
                type="checkbox"
                checked={remember}
                onChange={(e) => setRemember(e.target.checked)}
                className="h-4 w-4 rounded border-input text-primary accent-[var(--primary)] focus:ring-ring"
              />
              <label
                htmlFor="mailbox-remember"
                className="text-sm font-medium leading-none cursor-pointer text-muted-foreground select-none"
              >
                {t("login.remember_me", { defaultValue: "记住登录状态" })}
              </label>
            </div>

            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? t("state.loading") : t("page.webmail.login_submit")}
            </Button>
            <div className="pt-2 border-t space-y-2 text-center text-sm">
              <Link
                to="/login"
                className="inline-flex items-center justify-center gap-1.5 font-medium text-primary hover:underline"
              >
                <Building2 className="h-4 w-4" />
                {t("page.webmail.switch_to_admin", {
                  defaultValue: "企业管理后台登录",
                })}
              </Link>
              <p className="text-center text-xs text-muted-foreground">
                <Link to="/webmail/help" className="underline">
                  {t("page.webmail.help_title")}
                </Link>
              </p>
            </div>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
