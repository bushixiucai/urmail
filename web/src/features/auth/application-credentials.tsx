import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Card, CardHeader, CardContent, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

interface Credential {
  id: number;
  name: string;
  scopes: string[];
  expires_at: string;
  revoked_at?: string | null;
}

export function ApplicationCredentials({ canCreate }: { canCreate: boolean }) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [items, setItems] = useState<Credential[]>([]);
  const [name, setName] = useState("");
  const [days, setDays] = useState(30);
  const [code, setCode] = useState("");
  const [write, setWrite] = useState(false);
  const [token, setToken] = useState("");
  const [legacy, setLegacy] = useState<{
    token: string;
    csrf_token: string;
    expires_at: string;
  } | null>(null);
  const [notice, setNotice] = useState("");
  const [pending, setPending] = useState(false);
  const path = "/api/v1/auth/application-credentials";
  useEffect(() => {
    const controller = new AbortController();
    request<{ credentials: Credential[] }>(path, { signal: controller.signal })
      .then(
        (data) =>
          !controller.signal.aborted &&
          setItems((data.credentials ?? []).filter((item) => !item.revoked_at)),
      )
      .catch(
        (err: unknown) =>
          !controller.signal.aborted && setNotice(errorMessage(err)),
      );
    return () => controller.abort();
  }, [errorMessage]);
  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (pending) return;
    setPending(true);
    setToken("");
    setNotice("");
    try {
      const data = await request<{ token: string; credential: Credential }>(
        path,
        {
          method: "POST",
          body: {
            name,
            code,
            scopes: write ? ["legacy:read", "legacy:write"] : ["legacy:read"],
            expires_at: new Date(Date.now() + days * 86400000).toISOString(),
          },
        },
      );
      setToken(data.token);
      setItems((prev) => [...prev, data.credential]);
      setName("");
      setCode("");
    } catch (err: unknown) {
      setNotice(errorMessage(err));
    } finally {
      setPending(false);
    }
  };
  const revoke = async (id: number) => {
    try {
      await request(`${path}/${id}`, { method: "DELETE" });
      setItems((prev) => prev.filter((item) => item.id !== id));
      setToken("");
    } catch (err: unknown) {
      setNotice(errorMessage(err));
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("page.credentials.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {t("page.credentials.independent")}
        </p>
        {canCreate && (
          <form onSubmit={create} className="grid gap-3">
            <label className="grid gap-1">
              {t("page.credentials.name")}
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                maxLength={100}
                required
              />
            </label>
            <label className="grid gap-1">
              {t("page.credentials.days")}
              <Input
                type="number"
                value={days}
                onChange={(e) => setDays(Number(e.target.value))}
                min={1}
                max={365}
                required
              />
            </label>
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={write}
                onChange={(e) => setWrite(e.target.checked)}
              />
              {t("page.credentials.write")}
            </label>
            <label className="grid gap-1">
              {t("page.credentials.code")}
              <Input
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoComplete="one-time-code"
                required
              />
            </label>
            <Button type="submit" disabled={pending}>
              {t("page.credentials.create")}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={pending || !code}
              onClick={() => {
                setPending(true);
                setLegacy(null);
                void request<{
                  token: string;
                  csrf_token: string;
                  expires_at: string;
                }>("/api/v1/auth/legacy-session", {
                  method: "POST",
                  body: { code },
                })
                  .then((data) => {
                    setLegacy(data);
                    setCode("");
                  })
                  .catch((err: unknown) => setNotice(errorMessage(err)))
                  .finally(() => setPending(false));
              }}
            >
              {t("page.credentials.legacy_session")}
            </Button>
          </form>
        )}
        {legacy && (
          <div role="status" className="space-y-2">
            <p>
              {t("page.credentials.legacy_hint", {
                expires: new Date(legacy.expires_at).toLocaleString(),
              })}
            </p>
            <label className="grid gap-1">
              {t("page.credentials.session_token")}
              <code className="break-all bg-muted p-2">{legacy.token}</code>
            </label>
            <label className="grid gap-1">
              {t("page.credentials.csrf")}
              <code className="break-all bg-muted p-2">
                {legacy.csrf_token}
              </code>
            </label>
            <Button variant="outline" onClick={() => setLegacy(null)}>
              {t("action.close")}
            </Button>
          </div>
        )}
        {token && (
          <div role="status" className="space-y-2">
            <p>{t("page.credentials.once")}</p>
            <code className="block break-all rounded-md bg-muted p-3">
              {token}
            </code>
            <Button variant="outline" onClick={() => setToken("")}>
              {t("action.close")}
            </Button>
          </div>
        )}
        {notice && (
          <p role="alert" className="break-words">
            {notice}
          </p>
        )}
        {items.length === 0 ? (
          <p>{t("page.credentials.empty")}</p>
        ) : (
          items.map((item) => (
            <div
              key={item.id}
              className="flex flex-wrap items-center justify-between gap-2 border-t py-3"
            >
              <div className="min-w-0">
                <p className="break-words">{item.name}</p>
                <p className="text-sm text-muted-foreground">
                  {new Date(item.expires_at).toLocaleString()} ·{" "}
                  {t(
                    item.scopes.includes("legacy:write")
                      ? "page.credentials.write"
                      : "page.credentials.read",
                  )}
                </p>
              </div>
              <Button variant="outline" onClick={() => revoke(item.id)}>
                {t("page.credentials.revoke")}
              </Button>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}
