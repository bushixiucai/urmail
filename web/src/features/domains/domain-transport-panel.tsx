import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Policy = {
  enabled: boolean;
  version: number;
  mode: "testing" | "enforce" | "none";
  policy_id?: string;
  max_age?: number;
  policy_url?: string;
  policy_body?: string;
  report_uri?: string;
  dns_records?: { name: string; type: string; value: string }[];
  observation_status?: string;
  observation_detail?: Record<string, boolean>;
  cache_safe_after?: string | null;
  testing_since?: string | null;
  dane_enabled: false;
};
type Report = {
  id: number;
  period_start: string;
  period_end: string;
  successful: number;
  failed: number;
  failures: Record<string, number>;
  source_verified: false;
};
export function DomainTransportPanel({
  domain,
}: {
  domain: { id: number; fqdn: string; status: string };
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [snapshot, setPolicy] = useState<(Policy & { source: string }) | null>(
    null,
  );
  const [reportRows, setReports] = useState<Report[]>([]);
  const [mode, setMode] = useState<Policy["mode"]>("testing");
  const [age, setAge] = useState(86400);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const base = `/api/v1/domains/${domain.id}/transport-security`;
  const policy = snapshot?.source === base ? snapshot : null;
  const reports = policy ? reportRows : [];
  useEffect(() => {
    const controller = new AbortController();
    if (domain.status !== "active") {
      setPolicy(null);
      setReports([]);
      return () => controller.abort();
    }
    setError("");
    setConfirm(false);
    void Promise.all([
      request<Policy>(base, { signal: controller.signal }),
      request<{ reports: Report[] }>(base + "/reports", {
        signal: controller.signal,
      }),
    ])
      .then(([p, r]) => {
        if (controller.signal.aborted) return;
        setPolicy({ ...p, source: base });
        setMode(p.mode);
        setAge(p.max_age ?? 86400);
        setReports(r.reports);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(errorMessage(e));
      });
    return () => controller.abort();
  }, [base, revision, errorMessage, domain.status]);
  async function action(
    method: "POST" | "PUT" | "DELETE",
    suffix: string,
    body?: unknown,
  ) {
    if (busy || !policy) return;
    setBusy(true);
    setError("");
    try {
      await request(base + suffix, { method, body });
      setRevision((v) => v + 1);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  if (domain.status !== "active")
    return (
      <section className="rounded-lg border bg-card p-4">
        <h2 className="font-medium">{t("transport.title")}</h2>
        <p className="text-sm text-muted-foreground">
          {t("transport.domain_required")}
        </p>
      </section>
    );
  return (
    <section
      className="rounded-lg border bg-card p-4 space-y-4"
      aria-label={t("transport.title")}
    >
      <header className="space-y-1">
        <h2 className="font-medium">
          {t("transport.title")} · {domain.fqdn}
        </h2>
        <p className="text-sm text-muted-foreground">{t("transport.scope")}</p>
      </header>
      {error && (
        <p role="alert" className="break-words text-sm text-destructive">
          {error}
        </p>
      )}
      {!policy ? (
        <p role="status">{t("state.loading")}</p>
      ) : (
        <>
          <p className="text-sm">
            {t(policy.enabled ? "transport.published" : "transport.disabled")} ·{" "}
            {t(`transport.mode_${policy.mode}`)}
          </p>
          <div className="flex flex-wrap items-end gap-3">
            <label className="grid gap-1 text-sm">
              {t("transport.mode")}
              <select
                className="rounded-md border bg-background p-2"
                value={mode}
                disabled={busy}
                onChange={(event) => {
                  setMode(event.target.value as Policy["mode"]);
                  setConfirm(false);
                }}
              >
                <option value="testing">{t("transport.mode_testing")}</option>
                <option value="enforce" disabled={!policy.enabled}>
                  {t("transport.mode_enforce")}
                </option>
                <option value="none" disabled={!policy.enabled}>
                  {t("transport.mode_none")}
                </option>
              </select>
            </label>
            <label className="grid gap-1 text-sm">
              {t("transport.max_age")}
              <Input
                className="w-40"
                type="number"
                min={300}
                max={604800}
                value={age}
                disabled={busy}
                onChange={(event) => {
                  setAge(Number(event.target.value));
                  setConfirm(false);
                }}
              />
            </label>
          </div>
          <p className="text-sm text-muted-foreground">
            {t("transport.transition_help")}
          </p>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={confirm}
              disabled={busy}
              onChange={(event) => setConfirm(event.target.checked)}
            />
            {t("transport.confirm")}
          </label>
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={
                busy ||
                !confirm ||
                !Number.isInteger(age) ||
                age < 300 ||
                age > 604800
              }
              onClick={() =>
                void action("PUT", "", {
                  mode,
                  max_age: age,
                  version: policy.version,
                  confirm: true,
                })
              }
            >
              {t("transport.publish")}
            </Button>
            <Button
              variant="outline"
              disabled={busy || !policy.enabled}
              onClick={() => void action("POST", "/observe", {})}
            >
              {t("transport.observe")}
            </Button>
            <Button
              variant="outline"
              disabled={busy || !confirm || policy.mode !== "none"}
              onClick={() =>
                void action("DELETE", "", {
                  version: policy.version,
                  confirm: true,
                })
              }
            >
              {t("transport.disable_after_drain")}
            </Button>
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => setRevision((v) => v + 1)}
            >
              {t("transport.refresh")}
            </Button>
          </div>
          {policy.enabled && (
            <div className="space-y-3 text-sm">
              <p className="break-all">{policy.policy_url}</p>
              <pre className="overflow-auto rounded border bg-muted/30 p-3 text-xs">
                {policy.policy_body}
              </pre>
              <p className="break-all">
                {t("transport.report_uri")}: {policy.report_uri}
              </p>
              <p>
                {t("transport.observation")}:{" "}
                {t(
                  `transport.observation_${policy.observation_status ?? "unverified"}`,
                )}
              </p>
              {policy.observation_detail && (
                <ul>
                  {Object.entries(policy.observation_detail).map(
                    ([name, pass]) => (
                      <li key={name}>
                        {t(`transport.check_${name}`)}:{" "}
                        {t(pass ? "transport.pass" : "transport.fail")}
                      </li>
                    ),
                  )}
                </ul>
              )}
              {policy.cache_safe_after && (
                <p>
                  {t("transport.cache_until", {
                    time: new Date(policy.cache_safe_after).toLocaleString(),
                  })}
                </p>
              )}
              <div className="overflow-x-auto">
                <table className="w-full text-left text-xs">
                  <caption className="text-left py-2">
                    {t("transport.dns_help")}
                  </caption>
                  <thead>
                    <tr>
                      <th className="p-2">{t("transport.record_name")}</th>
                      <th className="p-2">{t("transport.record_type")}</th>
                      <th className="p-2">{t("transport.record_value")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {policy.dns_records?.map((record) => (
                      <tr key={record.name}>
                        <td className="p-2">{record.name}</td>
                        <td className="p-2">{record.type}</td>
                        <td className="p-2 break-all min-w-48">
                          {record.value}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
      <div className="border-t pt-3 space-y-2">
        <h3 className="text-sm font-medium">{t("transport.reports")}</h3>
        <p className="text-xs text-muted-foreground">
          {t("transport.reports_unverified")}
        </p>
        {!reports.length ? (
          <p className="text-sm text-muted-foreground">
            {t("transport.reports_empty")}
          </p>
        ) : (
          <ul className="space-y-2">
            {reports.map((report) => (
              <li key={report.id} className="rounded border p-3 text-sm">
                <p>
                  {new Date(report.period_start).toLocaleString()} –{" "}
                  {new Date(report.period_end).toLocaleString()}
                </p>
                <p>
                  {t("transport.report_counts", {
                    success: report.successful,
                    failed: report.failed,
                  })}
                </p>
                <ul className="text-xs">
                  {Object.entries(report.failures).map(([type, count]) => (
                    <li className="break-all" key={type}>
                      {type}: {count}
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
