import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

interface Grant {
  id: number;
  user_id: number;
  scope: string;
  reason: string;
  expires_at: string | null;
  revoked: boolean;
  owner_confirmed_at: string | null;
}
interface Recipient {
  id: number;
  email: string;
  role: string;
}

export function ContentGrants() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [grants, setGrants] = useState<Grant[]>([]);
  const [recipients, setRecipients] = useState<Recipient[]>([]);
  const [refresh, setRefresh] = useState(0);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [recipient, setRecipient] = useState("");
  const [scope, setScope] = useState("full_content");
  const [reason, setReason] = useState("");
  const [expires, setExpires] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void request<{ grants: Grant[]; recipients: Recipient[] }>(
      "/api/v1/compliance/grants",
      { signal: controller.signal },
    )
      .then((data) => {
        if (!controller.signal.aborted) {
          setGrants(data.grants);
          setRecipients(data.recipients);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(error));
      });
    return () => controller.abort();
  }, [refresh, errorMessage]);
  const grant = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    if (!expires || new Date(expires).getTime() <= Date.now()) {
      setNotice(t("page.audit.expiry_future"));
      return;
    }
    setBusy(true);
    setNotice("");
    try {
      await request("/api/v1/compliance/grants", {
        method: "POST",
        body: {
          user_id: Number(recipient),
          scope,
          reason,
          expires_at: new Date(expires).toISOString(),
        },
      });
      setReason("");
      setRefresh((value) => value + 1);
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setBusy(false);
    }
  };
  const revoke = async (id: number) => {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      await request(`/api/v1/compliance/grants/${id}/revoke`, {
        method: "POST",
      });
      setRefresh((value) => value + 1);
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">
          {t("page.audit.grants_title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-5">
        <p className="text-sm text-muted-foreground">
          {t("page.audit.grants_description")}
        </p>
        <form
          onSubmit={(event) => {
            void grant(event);
          }}
          className="grid gap-3 sm:grid-cols-2"
        >
          <label className="space-y-1 text-sm">
            {t("page.audit.recipient")}
            <select
              required
              value={recipient}
              onChange={(event) => setRecipient(event.target.value)}
              className="h-10 w-full rounded-md border bg-background px-3 text-base"
            >
              <option value="">{t("page.audit.choose_recipient")}</option>
              {recipients.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.email} ·{" "}
                  {t(`role.${item.role}`, { defaultValue: item.role })}
                </option>
              ))}
            </select>
          </label>
          <label className="space-y-1 text-sm">
            {t("page.audit.scope")}
            <select
              value={scope}
              onChange={(event) => setScope(event.target.value)}
              className="h-10 w-full rounded-md border bg-background px-3 text-base"
            >
              <option value="full_content">
                {t("page.audit.scope_full_content")}
              </option>
              <option value="export">{t("page.audit.scope_export")}</option>
              <option value="retention_manage">
                {t("page.audit.scope_retention_manage")}
              </option>
            </select>
          </label>
          <label className="space-y-1 text-sm">
            {t("page.audit.expires")}
            <Input
              required
              className="text-base"
              type="datetime-local"
              value={expires}
              onChange={(event) => setExpires(event.target.value)}
            />
          </label>
          <label className="space-y-1 text-sm">
            {t("page.audit.reason")}
            <Input
              required
              className="text-base"
              maxLength={2000}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <div className="sm:col-span-2">
            <Button disabled={busy || recipients.length === 0} type="submit">
              {t("page.audit.grant")}
            </Button>
          </div>
        </form>
        {notice && (
          <p role="alert" className="break-words text-sm text-destructive">
            {notice}
          </p>
        )}
        {grants.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("page.audit.no_grants")}
          </p>
        ) : (
          <ul className="divide-y rounded-md border">
            {grants.map((item) => (
              <li
                key={item.id}
                className="flex flex-col gap-3 p-3 sm:flex-row sm:items-start sm:justify-between"
              >
                <div className="min-w-0 space-y-1 text-sm">
                  <p className="break-words font-medium">
                    {recipients.find((person) => person.id === item.user_id)
                      ?.email ?? `#${item.user_id}`}{" "}
                    ·{" "}
                    {t(`page.audit.scope_${item.scope}`, {
                      defaultValue: item.scope,
                    })}
                  </p>
                  <p className="break-words text-muted-foreground">
                    {item.reason}
                  </p>
                  <p className="text-muted-foreground">
                    {item.expires_at
                      ? new Date(item.expires_at).toLocaleString()
                      : t("page.audit.expiry_unset")}
                  </p>
                  <p>
                    {item.revoked
                      ? t("page.audit.revoked")
                      : !item.owner_confirmed_at
                        ? t("page.audit.unconfirmed")
                        : item.expires_at &&
                            new Date(item.expires_at).getTime() <= Date.now()
                          ? t("page.audit.expired")
                          : t("page.audit.authorized")}
                  </p>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy || item.revoked}
                  onClick={() => {
                    void revoke(item.id);
                  }}
                >
                  {t("page.audit.revoke")}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
