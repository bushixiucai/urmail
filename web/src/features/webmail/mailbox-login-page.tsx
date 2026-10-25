import { FormEvent, useState } from "react";
import { Link, Navigate, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { AlertCircle, Lock, Mail } from "lucide-react";
import { ApiRequestError } from "@/lib/api/client";
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
  const { native, loaded } = useMailboxNative();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
    return <Navigate to="/webmail/inbox" replace />;
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const session = await createMailboxSession(email.trim(), password);
      setMailboxSession(session);
      navigate("/webmail/inbox", { replace: true });
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
    <main className="min-h-screen flex items-center justify-center bg-muted/30 px-4 py-10">
      <Card className="w-full max-w-md">
        <CardHeader>
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
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? t("state.loading") : t("page.webmail.login_submit")}
            </Button>
            <p className="text-center text-xs text-muted-foreground">
              <Link to="/webmail/help" className="underline">
                {t("page.webmail.help_title")}
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
