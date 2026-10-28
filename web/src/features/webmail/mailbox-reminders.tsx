import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { getMailboxToken } from "./mailbox-api";
import { Button } from "@/components/ui/button";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import {
  acknowledgeMailboxReminder,
  mailboxReminders,
} from "./mailbox-people-api";

export function MailboxReminders() {
  const { t, i18n } = useTranslation();
  const cache = useQueryClient();
  const apiError = useApiErrorMessage();
  const queryKey = ["mailbox-reminders", getMailboxToken()] as const;
  const [pending, setPending] = useState<number[]>([]);
  const [error, setError] = useState("");
  const reminders = useQuery({
    queryKey,
    queryFn: mailboxReminders,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
    retry: false,
  });
  const acknowledge = async (id: number) => {
    if (pending.includes(id)) return;
    setPending((current) => [...current, id]);
    setError("");
    try {
      const result = await acknowledgeMailboxReminder(id);
      if (!result.acknowledged) throw new Error("REQUEST_FAILED");
      cache.setQueryData<Awaited<ReturnType<typeof mailboxReminders>>>(
        queryKey,
        (current) =>
          current && {
            reminders: current.reminders.filter((item) => item.id !== id),
          },
      );
      await cache.invalidateQueries({ queryKey });
    } catch (failure) {
      setError(apiError(failure));
    } finally {
      setPending((current) => current.filter((value) => value !== id));
    }
  };
  const formatTime = (instant: string, timezone: string) => {
    try {
      return new Intl.DateTimeFormat(i18n.language, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: timezone || "UTC",
      }).format(new Date(instant));
    } catch {
      return t("reminders.time_unavailable");
    }
  };
  const rows = reminders.error
    ? []
    : (reminders.data?.reminders.filter((item) => !item.payload.acknowledged) ??
      []);
  return (
    <section
      aria-label={t("reminders.title")}
      className="space-y-3 rounded-md border p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-semibold">{t("reminders.title")}</h2>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={reminders.isFetching}
          onClick={() => void reminders.refetch()}
        >
          {t("reminders.refresh")}
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        {t("reminders.display_only")}
      </p>
      {(error || reminders.error) && (
        <p role="alert" className="text-sm text-destructive">
          {error || apiError(reminders.error)}
        </p>
      )}
      {reminders.isPending ? (
        <p role="status" className="text-sm">
          {t("state.loading")}
        </p>
      ) : !reminders.error && rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("reminders.empty")}</p>
      ) : null}
      <ul className="divide-y">
        {rows.map((item) => (
          <li key={item.id} className="space-y-2 py-3">
            <p className="break-words font-medium">{item.payload.summary}</p>
            <p className="text-sm text-muted-foreground">
              {formatTime(item.payload.starts_at, item.payload.timezone)} ·{" "}
              {item.payload.timezone}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                size="sm"
                disabled={pending.includes(item.id)}
                onClick={() => void acknowledge(item.id)}
              >
                {t("reminders.acknowledge")}
              </Button>
              <Button variant="outline" size="sm" asChild>
                <Link to="/webmail/people?tab=calendar">
                  {t("reminders.open_calendar")}
                </Link>
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
