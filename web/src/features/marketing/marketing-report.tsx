import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  exportMarketingReport,
  loadMarketingReport,
  type MarketingReport,
  type ReportKind,
} from "./marketing-report-api";

const localDate = (date: Date) =>
  new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 19);
export function MarketingReportDialog({
  kind,
  id,
  open,
  onOpenChange,
}: {
  kind: ReportKind;
  id: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const apiError = useApiErrorMessage();
  const [from, setFrom] = useState(() =>
      localDate(new Date(Date.now() - 30 * 86400000)),
    ),
    [until, setUntil] = useState(() => localDate(new Date()));
  const [report, setReport] = useState<MarketingReport | null>(null),
    [offset, setOffset] = useState(0),
    [busy, setBusy] = useState(false),
    [failure, setFailure] = useState(""),
    [confirmed, setConfirmed] = useState(false),
    [dirty, setDirty] = useState(false);
  const controller = useRef<AbortController | null>(null),
    revision = useRef(0);
  const prefix = "marketing.outcomes.";
  const load = async (page = 0) => {
    controller.current?.abort();
    const ac = new AbortController();
    controller.current = ac;
    const epoch = ++revision.current;
    setBusy(true);
    setFailure("");
    setConfirmed(false);
    try {
      const result = await loadMarketingReport(
        kind,
        id,
        page && report ? report.from : new Date(from).toISOString(),
        page && report ? report.until : new Date(until).toISOString(),
        page,
        page ? report?.digest : undefined,
        ac.signal,
      );
      if (ac.signal.aborted || epoch !== revision.current) return;
      setReport(result.report);
      setOffset(page);
      setDirty(false);
    } catch (e) {
      if (!ac.signal.aborted && epoch === revision.current) {
        setFailure(apiError(e));
        setReport(null);
      }
    } finally {
      if (epoch === revision.current) setBusy(false);
    }
  };
  useEffect(() => {
    revision.current++;
    controller.current?.abort();
    setReport(null);
    setFailure("");
    setConfirmed(false);
    setOffset(0);
    setDirty(false);
    setBusy(false);
    if (open) void load();
    return () => {
      controller.current?.abort();
    };
    // Inputs are explicitly applied; changing a date must not export or reset a snapshot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, kind, id]);
  const label = (key: string) => {
    const [category, state] = key.split("/");
    const code = state === "sent" || state === "submitted" ? "accepted" : state;
    return `${t(prefix + "category_" + category)} · ${t(prefix + "state_" + code, { defaultValue: state })}`;
  };
  const download = async () => {
    if (!report || busy || dirty || !confirmed) return;
    controller.current?.abort();
    const ac = new AbortController();
    controller.current = ac;
    const epoch = ++revision.current;
    setBusy(true);
    setFailure("");
    try {
      const bytes = await exportMarketingReport(report, ac.signal);
      if (ac.signal.aborted || epoch !== revision.current) return;
      const url = URL.createObjectURL(
        new Blob([bytes as BlobPart], { type: "text/csv;charset=utf-8" }),
      );
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `marketing-${kind}-${id}-${report.digest.slice(0, 12)}.csv`;
      anchor.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      setConfirmed(false);
    } catch (e) {
      if (!ac.signal.aborted && epoch === revision.current)
        setFailure(apiError(e));
    } finally {
      if (epoch === revision.current) setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!value) {
          revision.current++;
          controller.current?.abort();
        }
        onOpenChange(value);
      }}
    >
      <DialogContent className="max-h-[92dvh] max-w-4xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t(prefix + "title")}</DialogTitle>
        </DialogHeader>
        <p className="max-w-prose text-sm text-muted-foreground">
          {t(prefix + "semantics")}
        </p>
        <form
          className="grid grid-cols-1 items-end gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
          onSubmit={(e) => {
            e.preventDefault();
            void load();
          }}
        >
          <label className="grid min-w-0 gap-1 text-sm">
            {t(prefix + "from")}
            <Input
              className="min-w-0 w-full"
              type="datetime-local"
              step="1"
              value={from}
              disabled={busy}
              required
              onChange={(e) => {
                setFrom(e.target.value);
                setDirty(true);
                setConfirmed(false);
              }}
            />
          </label>
          <label className="grid min-w-0 gap-1 text-sm">
            {t(prefix + "until")}
            <Input
              className="min-w-0 w-full"
              type="datetime-local"
              step="1"
              value={until}
              disabled={busy}
              required
              onChange={(e) => {
                setUntil(e.target.value);
                setDirty(true);
                setConfirmed(false);
              }}
            />
          </label>
          <Button variant="outline" disabled={busy}>
            {t(prefix + "refresh")}
          </Button>
        </form>
        <p className="text-xs text-muted-foreground">{t(prefix + "budget")}</p>
        {busy && <p role="status">{t("state.loading")}</p>}
        {failure && (
          <p role="alert" className="text-sm text-destructive">
            {failure}
          </p>
        )}
        {report && (
          <>
            <h3 className="break-words font-semibold">{report.name}</h3>
            <p className="break-all text-xs text-muted-foreground">
              {t(prefix + "snapshot", {
                digest: report.digest,
                at: new Date(report.observed_at).toLocaleString(),
                count: report.total_facts,
              })}
            </p>
            <dl className="grid grid-cols-1 gap-2 rounded-md border p-3 sm:grid-cols-2">
              {Object.entries(report.counts)
                .sort()
                .map(([key, value]) => (
                  <div key={key} className="flex justify-between gap-3 text-sm">
                    <dt>{label(key)}</dt>
                    <dd className="font-medium tabular-nums">{value}</dd>
                  </div>
                ))}
            </dl>
            {!report.rows.length && <p>{t(prefix + "empty")}</p>}
            <ul className="divide-y rounded-md border">
              {report.rows.map((row) => (
                <li
                  key={row.record_kind + ":" + row.record_id}
                  className="min-w-0 space-y-1 p-3 text-sm"
                >
                  <p className="break-all font-medium">
                    {row.email || t(prefix + "event", { id: row.record_id })}
                  </p>
                  <p>
                    {label(row.record_kind + "/" + row.state)}
                    {row.version
                      ? ` · ${t(prefix + "version", { version: row.version })}`
                      : ""}
                  </p>
                  {row.uncertain && (
                    <p className="text-muted-foreground">
                      {t(prefix + "unknown")}
                    </p>
                  )}
                  {row.suppressed && <p>{t(prefix + "suppressed")}</p>}
                  {row.result_code && (
                    <p className="break-all text-xs text-muted-foreground">
                      {row.result_code}
                    </p>
                  )}
                </li>
              ))}
            </ul>
            <div className="flex flex-wrap items-center gap-3">
              <Button
                variant="outline"
                disabled={busy || dirty || !offset}
                onClick={() => void load(Math.max(0, offset - 50))}
              >
                {t(prefix + "previous")}
              </Button>
              <span className="text-sm">
                {t(prefix + "page", {
                  from: report.total_facts ? offset + 1 : 0,
                  to: offset + report.rows.length,
                  total: report.total_facts,
                })}
              </span>
              <Button
                variant="outline"
                disabled={busy || dirty || !report.next_offset}
                onClick={() => void load(report.next_offset)}
              >
                {t(prefix + "next")}
              </Button>
            </div>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={confirmed}
                disabled={busy || dirty}
                onChange={(e) => setConfirmed(e.target.checked)}
              />
              {t(prefix + "confirm", { count: report.total_facts })}
            </label>
            <Button
              disabled={busy || dirty || !confirmed}
              onClick={() => void download()}
            >
              {t(prefix + "export")}
            </Button>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
