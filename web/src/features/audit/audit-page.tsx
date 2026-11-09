import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/app/auth-context";
import {
  ApiRequestError,
  getSessionToken,
  parseApiErrorPayload,
  request,
} from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { NoticeDialog } from "@/components/ui/notice-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ContentGrants } from "./content-grants";

interface AuditLog {
  id: number;
  action: string;
  target_type?: string | null;
  target_id?: string | null;
  actor_user_id?: number | null;
  actor_ip?: string | null;
  created_at: string;
  detail?: { status?: number; outcome?: string; phase?: string };
}
const emptyFilters = {
  actor_user_id: "",
  action: "",
  target_type: "",
  target_id: "",
  from: "",
  to: "",
};

export function AuditPage() {
  const { currentTenant } = useAuth();
  return <AuditWorkspace key={currentTenant?.id ?? 0} />;
}

function AuditWorkspace() {
  const { t } = useTranslation();
  const { currentTenant } = useAuth();
  const errorMessage = useApiErrorMessage();
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [filters, setFilters] = useState(emptyFilters);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  const mayExport =
    currentTenant?.role === "owner" || currentTenant?.role === "admin";

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    const params = new URLSearchParams(query);
    params.set("limit", "100");
    if (cursor) params.set("cursor", cursor);
    void request<{ logs: AuditLog[]; next_cursor?: string }>(
      `/api/v1/audit?${params}`,
      { signal: controller.signal },
    )
      .then((data) => {
        if (!controller.signal.aborted) {
          setLogs(data.logs ?? []);
          setNext(data.next_cursor ?? "");
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [query, cursor, errorMessage]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(filters)) {
      if (value)
        params.set(
          key,
          key === "from" || key === "to"
            ? new Date(value).toISOString()
            : value,
        );
    }
    setCursor("");
    setHistory([]);
    setQuery(params.toString());
  };
  const download = async () => {
    setExporting(true);
    try {
      const token = getSessionToken();
      const response = await fetch(`/api/v1/audit/export?${query}`, {
        credentials: "same-origin",
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      });
      if (!response.ok) {
        const parsed = parseApiErrorPayload(
          await response.json().catch(() => null),
        );
        throw new ApiRequestError(
          response.status,
          parsed ?? { error: { code: "REQUEST_FAILED" } },
        );
      }
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = "audit.csv";
      link.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setExporting(false);
    }
  };
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
          <CardTitle className="text-base">{t("page.audit.filters")}</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={apply}
            className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3"
          >
            {(Object.keys(emptyFilters) as (keyof typeof emptyFilters)[]).map(
              (key) => (
                <label key={key} className="space-y-1 text-sm">
                  {t(`page.audit.filter_${key}`)}
                  <Input
                    className="text-base"
                    type={
                      key === "from" || key === "to"
                        ? "datetime-local"
                        : key === "actor_user_id"
                          ? "number"
                          : "text"
                    }
                    min={key === "actor_user_id" ? 1 : undefined}
                    value={filters[key]}
                    onChange={(event) =>
                      setFilters((value) => ({
                        ...value,
                        [key]: event.target.value,
                      }))
                    }
                  />
                </label>
              ),
            )}
            <div className="flex flex-wrap gap-2 sm:col-span-2 lg:col-span-3">
              <Button disabled={loading} type="submit">
                {t("page.audit.apply")}
              </Button>
              {mayExport && (
                <Button
                  disabled={loading || exporting}
                  type="button"
                  variant="outline"
                  onClick={() => {
                    void download();
                  }}
                >
                  {t("page.audit.export")}
                </Button>
              )}
            </div>
          </form>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("page.audit.recent")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4" aria-busy={loading}>
          {loading && (
            <p role="status" className="text-sm text-muted-foreground">
              {t("page.audit.loading")}
            </p>
          )}
          {!loading && logs.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("page.audit.empty")}
            </p>
          ) : (
            <ul className="divide-y rounded-md border">
              {logs.map((log) => (
                <li
                  key={log.id}
                  className="flex flex-col gap-2 p-3 text-sm sm:flex-row sm:items-start sm:justify-between"
                >
                  <div className="min-w-0 space-y-1">
                    <p className="break-words font-medium">{log.action}</p>
                    <p className="break-words text-muted-foreground">
                      {log.target_type ?? "—"} {log.target_id ?? ""}
                    </p>
                    <p>
                      {log.detail?.outcome
                        ? t(`page.audit.outcome_${log.detail.outcome}`, {
                            defaultValue: log.detail.outcome,
                          })
                        : t("page.audit.outcome_unknown")}{" "}
                      {log.detail?.status ?? ""}
                    </p>
                  </div>
                  <div className="shrink-0 space-y-1 text-xs text-muted-foreground">
                    <p>{new Date(log.created_at).toLocaleString()}</p>
                    <p>
                      {t("page.audit.actor", { id: log.actor_user_id ?? "—" })}{" "}
                      · {log.actor_ip ?? "—"}
                    </p>
                  </div>
                </li>
              ))}
            </ul>
          )}
          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant="outline"
              disabled={loading || history.length === 0}
              onClick={() => {
                setCursor(history[history.length - 1] ?? "");
                setHistory((value) => value.slice(0, -1));
              }}
            >
              {t("page.audit.previous")}
            </Button>
            <span className="text-sm tabular-nums">
              {t("page.audit.page", { number: history.length + 1 })}
            </span>
            <Button
              variant="outline"
              disabled={loading || !next}
              onClick={() => {
                setHistory((value) => [...value, cursor]);
                setCursor(next);
              }}
            >
              {t("page.audit.next")}
            </Button>
          </div>
        </CardContent>
      </Card>
      {currentTenant?.role === "owner" && <ContentGrants />}
      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice ?? ""}
        variant="error"
      />
    </div>
  );
}
