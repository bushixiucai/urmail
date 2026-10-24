import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/app/auth-context";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";

interface Health {
  terminal_recipients: number;
  accepted_recipients: number;
  acceptance_rate: number | null;
  recipient_outcomes: Record<string, number>;
  fresh: boolean;
  collection_gap: boolean;
  observed_at: string | null;
}
interface Queue {
  observed_at: string;
  truncated: boolean;
  entries: {
    queue_id: string;
    queue_name: string;
    arrival_time: number;
    sender: string;
    message_size: number;
    recipients: { address: string; delay_reason?: string }[];
  }[];
}

export function DeliveryPage() {
  const { currentTenant } = useAuth();
  return <DeliveryWorkspace key={currentTenant?.id ?? 0} />;
}
function DeliveryWorkspace() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [window, setWindow] = useState("24h");
  const [refresh, setRefresh] = useState(0);
  const [health, setHealth] = useState<Health | null>(null);
  const [queue, setQueue] = useState<Queue | null>(null);
  const [healthError, setHealthError] = useState("");
  const [queueError, setQueueError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const timer = globalThis.setInterval(
      () => setRefresh((value) => value + 1),
      30000,
    );
    return () => globalThis.clearInterval(timer);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    void Promise.allSettled([
      request<Health>(`/api/v1/mail/health?window=${window}`, {
        signal: controller.signal,
      })
        .then((data) => {
          if (!controller.signal.aborted) {
            setHealth(data);
            setHealthError("");
          }
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted) {
            setHealth(null);
            setHealthError(errorMessage(error));
          }
        }),
      request<Queue>("/api/v1/mail/queue", { signal: controller.signal })
        .then((data) => {
          if (!controller.signal.aborted) {
            setQueue(data);
            setQueueError("");
          }
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted) {
            setQueue(null);
            setQueueError(errorMessage(error));
          }
        }),
    ]).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [window, refresh, errorMessage]);
  return (
    <div className="max-w-6xl mx-auto space-y-8">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold">{t("delivery.title")}</h1>
        <p className="text-sm text-muted-foreground max-w-prose">
          {t("delivery.semantics")}
        </p>
        <p className="text-sm text-muted-foreground">
          {t("delivery.auto_refresh")}
        </p>
      </header>
      <div className="flex flex-wrap gap-3 items-center">
        <label htmlFor="delivery-window" className="text-sm">
          {t("delivery.window")}
        </label>
        <select
          id="delivery-window"
          className="border rounded-md bg-background p-2 text-sm"
          value={window}
          onChange={(event) => setWindow(event.target.value)}
        >
          {["1h", "24h", "7d", "30d"].map((value) => (
            <option key={value} value={value}>
              {t(`delivery.window_${value}`)}
            </option>
          ))}
        </select>
        <Button
          variant="outline"
          disabled={loading}
          onClick={() => setRefresh((value) => value + 1)}
        >
          {t("delivery.refresh")}
        </Button>
        {loading && (
          <span role="status" className="text-sm">
            {t("state.loading")}
          </span>
        )}
      </div>
      <section className="space-y-4" aria-label={t("delivery.outcomes")}>
        <h2 className="text-lg font-semibold">{t("delivery.outcomes")}</h2>
        {healthError && (
          <p role="alert" className="text-destructive text-sm">
            {healthError}
          </p>
        )}
        {health && (
          <>
            <p
              role="status"
              className={health.fresh ? "text-sm" : "text-sm text-destructive"}
            >
              {t(
                health.collection_gap
                  ? "delivery.collection_gap"
                  : health.fresh
                    ? "delivery.fresh"
                    : "delivery.stale",
              )}
            </p>
            <p className="text-sm text-muted-foreground">
              {t("delivery.observed", {
                time: health.observed_at
                  ? new Date(health.observed_at).toLocaleString()
                  : t("delivery.never_observed"),
              })}
            </p>
            <dl className="grid grid-cols-1 sm:grid-cols-3 gap-4">
              <div>
                <dt className="text-sm text-muted-foreground">
                  {t("delivery.accepted")}
                </dt>
                <dd className="text-xl tabular-nums">
                  {health.accepted_recipients.toLocaleString()}
                </dd>
              </div>
              <div>
                <dt className="text-sm text-muted-foreground">
                  {t("delivery.denominator")}
                </dt>
                <dd className="text-xl tabular-nums">
                  {health.terminal_recipients.toLocaleString()}
                </dd>
              </div>
              <div>
                <dt className="text-sm text-muted-foreground">
                  {t("delivery.rate")}
                </dt>
                <dd className="text-xl tabular-nums">
                  {health.acceptance_rate === null
                    ? t("delivery.no_terminal")
                    : new Intl.NumberFormat(undefined, {
                        style: "percent",
                        maximumFractionDigits: 2,
                      }).format(health.acceptance_rate)}
                </dd>
              </div>
            </dl>
            <ul className="flex flex-wrap gap-4 text-sm">
              {["sent", "bounced", "expired", "deferred"].map((status) => (
                <li key={status}>
                  {t(`delivery.status_${status}`)}:{" "}
                  {(health.recipient_outcomes[status] ?? 0).toLocaleString()}
                </li>
              ))}
            </ul>
          </>
        )}
      </section>
      <section className="border-t pt-6 space-y-4">
        <h2 className="text-lg font-semibold">{t("delivery.queue")}</h2>
        <p className="text-sm text-muted-foreground max-w-prose">
          {t("delivery.queue_scope")}
        </p>
        {queueError && (
          <p role="alert" className="text-destructive text-sm">
            {queueError}
          </p>
        )}
        {queue && (
          <>
            <p className="text-sm">
              {t("delivery.observed", {
                time: new Date(queue.observed_at).toLocaleString(),
              })}
            </p>
            {queue.truncated && (
              <p role="status" className="text-sm text-destructive">
                {t("delivery.truncated")}
              </p>
            )}
            {queue.entries.length === 0 ? (
              <p className="text-muted-foreground">
                {t("delivery.queue_empty")}
              </p>
            ) : (
              <ul className="divide-y">
                {queue.entries.map((entry) => (
                  <li key={entry.queue_id} className="py-4 space-y-2">
                    <h3 className="font-medium break-words">
                      {entry.queue_id} · {entry.queue_name}
                    </h3>
                    <p className="text-sm break-all">
                      {entry.sender} ·{" "}
                      {new Date(entry.arrival_time * 1000).toLocaleString()}
                    </p>
                    <ul className="space-y-1 text-sm">
                      {entry.recipients.map((recipient, index) => (
                        <li
                          key={`${recipient.address}-${index}`}
                          className="break-words"
                        >
                          {recipient.address}
                          {recipient.delay_reason && (
                            <span className="text-muted-foreground">
                              {" "}
                              — {recipient.delay_reason}
                            </span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </section>
    </div>
  );
}
