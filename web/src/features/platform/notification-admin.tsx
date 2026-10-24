import { ReminderPolicy } from "./reminder-policy";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
type Alert = {
  id: number;
  alert_key: string;
  tenant_id: number | null;
  kind: string;
  state: string;
  last_seen_at: string;
};
export function NotificationAdmin() {
  const { t } = useTranslation(),
    errorMessage = useApiErrorMessage();
  const [alerts, setAlerts] = useState<Alert[]>([]),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void request<{ alerts: Alert[] }>("/api/v1/platform/alerts", {
      signal: controller.signal,
    })
      .then((d) => {
        if (!controller.signal.aborted) setAlerts(d.alerts ?? []);
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(e));
      });
    return () => controller.abort();
  }, [errorMessage]);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("notify.admin.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {t("notify.admin.help")}
        </p>
        {notice && <p role="status">{notice}</p>}
        <ul className="divide-y">
          {alerts.map((a) => (
            <li key={a.alert_key} className="break-words py-2 text-sm">
              {t(`notify.alert.${a.kind}`)} · {a.alert_key} ·{" "}
              {t(`notify.alert_state.${a.state}`)} · {a.last_seen_at}
            </li>
          ))}
        </ul>
        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (busy) return;
            const form = e.currentTarget,
              data = new FormData(form);
            const body = {
              enabled: data.get("enabled") === "on",
              host: String(data.get("host") ?? ""),
              port: Number(data.get("port")),
              from: String(data.get("from") ?? ""),
              username: String(data.get("username") ?? ""),
              password: String(data.get("password") ?? ""),
              implicit_tls: data.get("implicit_tls") === "on",
            };
            setBusy(true);
            void request("/api/v1/platform/notification-channel", {
              method: "PUT",
              body,
            })
              .then(() => {
                setNotice(t("notify.admin.saved"));
                form.reset();
              })
              .catch((err: unknown) => setNotice(errorMessage(err)))
              .finally(() => setBusy(false));
          }}
        >
          {(["host", "port", "from", "username", "password"] as const).map(
            (f) => (
              <label key={f} className="grid gap-1 text-sm">
                {t(`notify.admin.${f}`)}
                <Input
                  name={f}
                  type={
                    f === "password"
                      ? "password"
                      : f === "port"
                        ? "number"
                        : f === "from"
                          ? "email"
                          : "text"
                  }
                  min={f === "port" ? 1 : undefined}
                  max={f === "port" ? 65535 : undefined}
                  autoComplete="off"
                  required={["host", "port", "from"].includes(f)}
                />
              </label>
            ),
          )}
          <label className="flex items-center gap-2 text-sm">
            <input name="implicit_tls" type="checkbox" />
            {t("notify.admin.implicit_tls")}
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input name="enabled" type="checkbox" />
            {t("notify.admin.enable")}
          </label>
          <label className="flex items-center gap-2 text-sm sm:col-span-2">
            <input required type="checkbox" />
            {t("notify.admin.confirm")}
          </label>
          <Button className="sm:col-span-2" disabled={busy}>
            {t("notify.admin.save")}
          </Button>
        </form>
        <ReminderPolicy />
      </CardContent>
    </Card>
  );
}
