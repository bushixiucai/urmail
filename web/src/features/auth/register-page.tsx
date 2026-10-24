import { usePublicStatus } from "./public-status";
import { useState, type FormEvent } from "react";
import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { usePublicBrand } from "@/components/brand/public-brand";
import { ApiRequestError, request } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function RegisterPage() {
  const { t } = useTranslation();
  const publicStatus = usePublicStatus();
  const brand = usePublicBrand();
  const location = useLocation();
  const verification = location.pathname.endsWith("/verify");
  const [token] = useState(() => {
    const value =
      new URLSearchParams(location.hash.slice(1)).get("token") || "";
    if (value) window.history.replaceState(null, "", location.pathname);
    return value;
  });
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy || !publicStatus.registration) return;
    setBusy(true);
    setError("");
    try {
      await request(
        verification ? "/api/v1/auth/register/verify" : "/api/v1/auth/register",
        {
          method: "POST",
          body: verification
            ? { token }
            : { email, password, name, brand: brand?.slug ?? "default" },
        },
      );
      setPassword("");
      setDone(true);
    } catch (e) {
      setError(
        t(
          e instanceof ApiRequestError
            ? `error.${e.code}`
            : "error.NETWORK_ERROR",
        ),
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="mx-auto flex min-h-screen max-w-md items-center px-4 py-8">
      <Card className="w-full">
        <CardHeader>
          <CardTitle>
            {t(
              verification ? "registration.verify_title" : "registration.title",
            )}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-sm text-muted-foreground">
            {t("registration.restricted_help")}
          </p>
          {!publicStatus.registration && (
            <p role="status">
              {t(
                publicStatus.quarantined
                  ? "restore.quarantine_banner"
                  : publicStatus.loaded
                    ? "registration.unavailable"
                    : "state.loading",
              )}
            </p>
          )}
          {done ? (
            <p role="status">
              {t(
                verification
                  ? "registration.contact_verified"
                  : "registration.verification_pending",
              )}
            </p>
          ) : (
            <form onSubmit={submit} className="space-y-4">
              {!verification && (
                <>
                  <label className="block space-y-1 text-sm">
                    <span>{t("registration.name")}</span>
                    <Input
                      value={name}
                      maxLength={128}
                      required
                      onChange={(e) => setName(e.target.value)}
                      autoComplete="organization"
                    />
                  </label>
                  <label className="block space-y-1 text-sm">
                    <span>{t("login.email_label")}</span>
                    <Input
                      type="email"
                      value={email}
                      required
                      maxLength={254}
                      onChange={(e) => setEmail(e.target.value)}
                      autoComplete="email"
                    />
                  </label>
                  <label className="block space-y-1 text-sm">
                    <span>{t("login.password_label")}</span>
                    <Input
                      type="password"
                      value={password}
                      required
                      minLength={12}
                      maxLength={256}
                      onChange={(e) => setPassword(e.target.value)}
                      autoComplete="new-password"
                    />
                  </label>
                </>
              )}
              {verification && !token && (
                <p role="alert">{t("error.VERIFICATION_INVALID")}</p>
              )}
              {error && (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
              <Button
                type="submit"
                disabled={
                  busy || !publicStatus.registration || (verification && !token)
                }
                className="w-full"
              >
                {t(
                  busy
                    ? "state.loading"
                    : verification
                      ? "registration.verify_action"
                      : "registration.action",
                )}
              </Button>
            </form>
          )}
          <Link className="inline-block text-sm underline" to="/login">
            {t("registration.sign_in")}
          </Link>
        </CardContent>
      </Card>
    </main>
  );
}
