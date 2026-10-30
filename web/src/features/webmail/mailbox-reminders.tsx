import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Bell, Calendar, RefreshCw } from "lucide-react";
import { getMailboxToken } from "./mailbox-api";
import { Button } from "@/components/ui/button";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import {
  acknowledgeMailboxReminder,
  mailboxReminders,
} from "./mailbox-people-api";

export function MailboxReminders({
  hideWhenEmpty = false,
}: {
  hideWhenEmpty?: boolean;
} = {}) {
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

  if (
    hideWhenEmpty &&
    !error &&
    !reminders.error &&
    rows.length === 0 &&
    !reminders.isPending
  ) {
    return null;
  }

  // Standalone full view or when error exists
  return (
    <section
      aria-label={t("reminders.title")}
      className={`space-y-2.5 rounded-lg border p-3 text-sm transition-all shadow-2xs ${
        rows.length > 0
          ? "border-amber-500/40 bg-amber-500/5 dark:bg-amber-500/10"
          : "border-border/60 bg-card/60"
      }`}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          {rows.length > 0 ? (
            <span className="flex h-6 w-6 items-center justify-center rounded-md bg-amber-500/15 text-amber-600 dark:text-amber-400">
              <Bell className="h-3.5 w-3.5 animate-pulse" />
            </span>
          ) : (
            <span className="flex h-6 w-6 items-center justify-center rounded-md bg-muted text-muted-foreground">
              <Calendar className="h-3.5 w-3.5" />
            </span>
          )}
          <h2 className="font-semibold text-sm">{t("reminders.title")}</h2>
          {rows.length > 0 && (
            <span className="rounded-full bg-amber-500/20 px-2 py-0.5 text-xs font-semibold text-amber-700 dark:text-amber-300">
              {rows.length}
            </span>
          )}
        </div>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
          disabled={reminders.isFetching}
          onClick={() => void reminders.refetch()}
        >
          <RefreshCw
            className={`h-3 w-3 mr-1 ${reminders.isFetching ? "animate-spin" : ""}`}
          />
          {t("reminders.refresh")}
        </Button>
      </div>

      <p className="text-xs text-muted-foreground">
        {t("reminders.display_only")}
      </p>

      {(error || reminders.error) && (
        <p role="alert" className="text-xs text-destructive">
          {error || apiError(reminders.error)}
        </p>
      )}

      {reminders.isPending ? (
        <p role="status" className="text-xs text-muted-foreground">
          {t("state.loading")}
        </p>
      ) : !reminders.error && rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("reminders.empty")}</p>
      ) : null}

      {rows.length > 0 && (
        <ul className="divide-y divide-border/40">
          {rows.map((item) => (
            <li key={item.id} className="space-y-1.5 py-2 first:pt-1 last:pb-0">
              <div className="flex items-start justify-between gap-2">
                <div>
                  <p className="break-words font-medium text-xs sm:text-sm text-foreground">
                    {item.payload.summary}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {formatTime(item.payload.starts_at, item.payload.timezone)}{" "}
                    · {item.payload.timezone}
                  </p>
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2 pt-1">
                <Button
                  type="button"
                  size="sm"
                  className="h-7 px-2.5 text-xs font-medium"
                  disabled={pending.includes(item.id)}
                  onClick={() => void acknowledge(item.id)}
                >
                  {t("reminders.acknowledge")}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2.5 text-xs"
                  asChild
                >
                  <Link to="/webmail/people?tab=calendar">
                    {t("reminders.open_calendar")}
                  </Link>
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
