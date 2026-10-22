import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { NoticeDialog } from "@/components/ui/notice-dialog";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

interface AuditLog {
  id: number;
  action: string;
  target_type?: string | null;
  target_id?: string | null;
  actor_ip?: string | null;
  created_at: string;
}

export function AuditPage() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    request<{ logs: AuditLog[] }>("/api/v1/audit?limit=200")
      .then((res) => setLogs(res.logs ?? []))
      .catch((err: unknown) => setNotice(errorMessage(err)));
  }, [errorMessage]);

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          {t("page.audit.title")}
        </h2>
        <p className="text-sm text-muted-foreground">
          {t("page.audit.subtitle")}
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("page.audit.recent")}</CardTitle>
        </CardHeader>
        <CardContent>
          {logs.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("page.audit.empty")}
            </p>
          ) : (
            <div className="border rounded-md divide-y">
              {logs.map((log) => (
                <div
                  key={log.id}
                  className="p-3 flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between"
                >
                  <div>
                    <div className="text-sm font-medium">{log.action}</div>
                    <div className="text-xs text-muted-foreground">
                      {log.target_type ?? "-"} {log.target_id ?? ""}
                    </div>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {log.actor_ip ?? "-"} · {log.created_at}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice ?? ""}
        variant="error"
      />
    </div>
  );
}
