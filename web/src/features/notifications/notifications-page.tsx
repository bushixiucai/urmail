import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type Notice = {
  id: number;
  message_key: string;
  params: Record<string, unknown>;
  channel: string;
  state: string;
  created_at: string;
};
export function NotificationsPage() {
  const { t } = useTranslation(),
    errorMessage = useApiErrorMessage();
  const [rows, setRows] = useState<Notice[]>([]),
    [before, setBefore] = useState(0),
    [more, setMore] = useState(false),
    [busy, setBusy] = useState(true),
    [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    setBusy(true);
    void request<{ notifications: Notice[] }>(
      `/api/v1/notifications${before ? `?before=${before}` : ""}`,
      { signal: controller.signal },
    )
      .then((d) => {
        if (!controller.signal.aborted) {
          setRows(d.notifications ?? []);
          setMore((d.notifications ?? []).length === 50);
          setError("");
          setBusy(false);
        }
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(e));
          setBusy(false);
        }
      });
    return () => controller.abort();
  }, [before, errorMessage]);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("notify.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {error && <p role="alert">{error}</p>}
        {busy ? (
          <p role="status">{t("common.loading")}</p>
        ) : (
          <ul className="divide-y">
            {rows.map((n) => (
              <li key={n.id} className="space-y-1 py-3">
                <p>{t(n.message_key, n.params)}</p>
                <p className="text-xs text-muted-foreground">
                  {t(`notify.channel.${n.channel}`)} ·{" "}
                  {t(`notify.state.${n.state}`)} ·{" "}
                  <time dateTime={n.created_at}>{n.created_at}</time>
                </p>
              </li>
            ))}
          </ul>
        )}
        {!busy && !rows.length && <p>{t("notify.empty")}</p>}
        <div className="flex gap-2">
          {before > 0 && (
            <Button
              variant="outline"
              onClick={() => setBefore(0)}
              disabled={busy}
            >
              {t("notify.latest")}
            </Button>
          )}
          {more && (
            <Button
              variant="outline"
              onClick={() => setBefore(rows[rows.length - 1].id)}
              disabled={busy}
            >
              {t("notify.older")}
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
