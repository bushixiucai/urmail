import { FormEvent, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { ApiRequestError } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import {
  deletePeopleEvent,
  downloadPeopleFile,
  peopleEvents,
  savePeopleEvent,
  type OccurrenceOverride,
  type PeopleEvent,
} from "./mailbox-people-api";
import { InvitationStatus, SendInvitationsButton } from "./mailbox-invitation";
import { formatZonedInput, zonedInputToISO } from "./people-time";

const area =
  "block min-h-24 w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
const day = (date: Date) => date.toISOString().slice(0, 10);
type Editor = {
  summary: string;
  description: string;
  location: string;
  start: string;
  end: string;
  allDay: boolean;
  timezone: string;
  frequency: string;
  interval: number;
  count: number;
  attendees: string;
  status: string;
  disambiguation: "earlier" | "later";
  originalRule: string;
  reminders: number[];
};
function newEditor(): Editor {
  const start = new Date(Math.ceil(Date.now() / 3600000) * 3600000);
  const timezone = zone();
  return {
    summary: "",
    description: "",
    location: "",
    start: formatZonedInput(start.toISOString(), timezone),
    end: formatZonedInput(
      new Date(start.getTime() + 3600000).toISOString(),
      timezone,
    ),
    allDay: false,
    timezone,
    frequency: "",
    interval: 1,
    count: 10,
    attendees: "",
    status: "confirmed",
    disambiguation: "earlier",
    originalRule: "",
    reminders: [],
  };
}
function ruleParts(rule: string) {
  return Object.fromEntries(rule.split(";").map((part) => part.split("=")));
}

export function PeopleCalendarPanel() {
  const { t, i18n } = useTranslation();
  const apiError = useApiErrorMessage();
  const cache = useQueryClient();
  const [rangeStart, setRangeStart] = useState(() => day(new Date()));
  const [rangeEnd, setRangeEnd] = useState(() =>
    day(new Date(Date.now() + 31 * 86400000)),
  );
  const [editor, setEditor] = useState<Editor>(newEditor);
  const [selected, setSelected] = useState<PeopleEvent>();
  const [occurrence, setOccurrence] = useState<string>();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [notify, setNotify] = useState(false);
  const [clearExceptions, setClearExceptions] = useState(false);
  const validRange =
    /^\d{4}-\d{2}-\d{2}$/.test(rangeStart) &&
    /^\d{4}-\d{2}-\d{2}$/.test(rangeEnd) &&
    rangeEnd > rangeStart;
  const range = useMemo(
    () =>
      validRange
        ? {
            start: zonedInputToISO(rangeStart, zone()),
            end: zonedInputToISO(rangeEnd, zone()),
          }
        : { start: "", end: "" },
    [rangeStart, rangeEnd, validRange],
  );
  const events = useInfiniteQuery({
    queryKey: ["people-events", range],
    queryFn: ({ pageParam }) => peopleEvents(range.start, range.end, pageParam),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: validRange,
  });
  const rows = events.data?.pages.flatMap((page) => page.events) ?? [];
  const run = async (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
      await cache.invalidateQueries({ queryKey: ["people-events"] });
      setNotice(t("people.saved"));
    } catch (e) {
      await cache.invalidateQueries({ queryKey: ["people-events"] });
      if (e instanceof ApiRequestError && e.code === "MAILBOX_SEND_UNCERTAIN") {
        setOpen(false);
        setDeleting(false);
        setSelected(undefined);
      }
      setError(
        e instanceof Error && e.message === "INVALID_LOCAL_TIME"
          ? t("people.invalid_time")
          : apiError(e),
      );
    } finally {
      setBusy(false);
    }
  };
  const edit = (event?: PeopleEvent, instance?: OccurrenceOverride) => {
    setSelected(event);
    setOccurrence(instance?.recurrence_id);
    setOpen(true);
    setError("");
    setNotice("");
    setNotify(false);
    setClearExceptions(false);
    if (!event) {
      setEditor(newEditor());
      return;
    }
    const item = instance ?? event;
    const timezone = event.timezone || "UTC";
    const parts = ruleParts(event.rrule ?? "");
    setEditor({
      summary: item.summary,
      description: item.description ?? "",
      location: item.location ?? "",
      start: formatZonedInput(item.starts_at, timezone, item.all_day),
      end: formatZonedInput(item.ends_at, timezone, item.all_day),
      timezone,
      allDay: item.all_day,
      frequency: instance
        ? ""
        : event.rrule &&
            (!parts.COUNT ||
              Object.keys(parts).some(
                (key) => !["FREQ", "INTERVAL", "COUNT"].includes(key),
              ))
          ? "CUSTOM"
          : (parts.FREQ ?? ""),
      interval: Number(parts.INTERVAL) || 1,
      count: Number(parts.COUNT) || 10,
      attendees: (event.attendees ?? [])
        .map((person) => person.email)
        .join("\n"),
      status: item.status || "confirmed",
      disambiguation: "earlier",
      originalRule: instance ? "" : (event.rrule ?? ""),
      reminders: event.reminders ?? [],
    });
  };
  const format = (instant: string, timezone: string, allDay = false) =>
    new Intl.DateTimeFormat(i18n.language, {
      timeZone: timezone,
      ...(allDay
        ? { dateStyle: "medium" as const }
        : { dateStyle: "medium" as const, timeStyle: "short" as const }),
    }).format(new Date(instant));
  const change = <K extends keyof Editor>(key: K, value: Editor[K]) =>
    setEditor({ ...editor, [key]: value });
  const save = (event: FormEvent) => {
    event.preventDefault();
    void run(async () => {
      const starts = zonedInputToISO(
        editor.start,
        editor.timezone,
        editor.disambiguation,
      );
      const ends = zonedInputToISO(
        editor.end,
        editor.timezone,
        editor.disambiguation,
      );
      if (ends <= starts) throw new Error("INVALID_LOCAL_TIME");
      await savePeopleEvent(
        {
          summary: editor.summary,
          description: editor.description,
          location: editor.location,
          starts_at: starts,
          ends_at: ends,
          all_day: editor.allDay,
          timezone: editor.timezone,
          status: editor.status,
          rrule: occurrence
            ? ""
            : editor.frequency === "CUSTOM"
              ? editor.originalRule
              : editor.frequency
                ? `FREQ=${editor.frequency};INTERVAL=${editor.interval};COUNT=${editor.count}`
                : "",
          ...(!occurrence ? { reminders: [...new Set(editor.reminders)] } : {}),
          attendees: editor.attendees
            .split(/[\n,;]/)
            .map((s) => s.trim())
            .filter(Boolean),
          ...(occurrence
            ? { recurrence_id: occurrence }
            : selected?.recurrence_overrides
              ? {
                  recurrence_overrides: clearExceptions
                    ? []
                    : selected.recurrence_overrides,
                }
              : {}),
        },
        selected,
      );
      setOpen(false);
      setSelected(undefined);
      setOccurrence(undefined);
    });
  };
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">
            {t("page.webmail.calendar_title")}
          </h2>
          <p className="text-sm text-muted-foreground">
            {t("people.calendar_help")}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button disabled={busy} onClick={() => edit()}>
            {t("page.webmail.event_add")}
          </Button>
          <Button
            variant="outline"
            disabled={busy || !validRange}
            onClick={() =>
              void run(() =>
                downloadPeopleFile(
                  `/calendars/export.ics?${new URLSearchParams({ from: range.start, to: range.end })}`,
                  "calendar.ics",
                ),
              )
            }
          >
            {t("people.export_calendar")}
          </Button>
        </div>
      </div>
      <div className="flex flex-wrap gap-3">
        <label className="grid gap-1 text-sm">
          {t("people.range_start")}
          <Input
            type="date"
            value={rangeStart}
            onChange={(event) => setRangeStart(event.target.value)}
            required
          />
        </label>
        <label className="grid gap-1 text-sm">
          {t("people.range_end")}
          <Input
            type="date"
            value={rangeEnd}
            onChange={(event) => setRangeEnd(event.target.value)}
            required
          />
        </label>
      </div>
      {!validRange && (
        <p role="alert" className="text-sm text-destructive">
          {t("people.invalid_range")}
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      {events.error && <p role="alert">{apiError(events.error)}</p>}
      {open && (
        <section
          className="space-y-4 rounded-md border p-4"
          aria-label={t(
            occurrence
              ? "people.edit_occurrence"
              : selected
                ? "people.edit_event"
                : "page.webmail.event_add",
          )}
        >
          <div className="flex items-center justify-between gap-3">
            <h3 className="font-semibold">
              {t(
                occurrence
                  ? "people.edit_occurrence"
                  : selected
                    ? "people.edit_event"
                    : "page.webmail.event_add",
              )}
            </h3>
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              {t("action.close")}
            </Button>
          </div>
          {occurrence && (
            <p className="text-sm text-muted-foreground">
              {t("people.occurrence_help", {
                time: format(occurrence, editor.timezone),
              })}
            </p>
          )}
          <form className="grid gap-4 md:grid-cols-2" onSubmit={save}>
            <label className="grid gap-1 text-sm md:col-span-2">
              {t("page.webmail.event_summary")}
              <Input
                value={editor.summary}
                required
                maxLength={500}
                onChange={(event) => change("summary", event.target.value)}
              />
            </label>
            <label className="flex items-center gap-2 text-sm md:col-span-2">
              <input
                type="checkbox"
                checked={editor.allDay}
                onChange={(event) => {
                  const allDay = event.target.checked;
                  setEditor({
                    ...editor,
                    allDay,
                    start: allDay
                      ? editor.start.slice(0, 10)
                      : editor.start.slice(0, 10) + "T09:00",
                    end: allDay
                      ? day(
                          new Date(
                            new Date(
                              editor.start.slice(0, 10) + "T00:00:00Z",
                            ).getTime() + 86400000,
                          ),
                        )
                      : editor.end.slice(0, 10) + "T10:00",
                  });
                }}
              />
              {t("people.all_day")}
            </label>
            <label className="grid gap-1 text-sm">
              {t("people.starts")}
              <Input
                type={editor.allDay ? "date" : "datetime-local"}
                value={editor.start}
                required
                onChange={(event) => change("start", event.target.value)}
              />
            </label>
            <label className="grid gap-1 text-sm">
              {t(editor.allDay ? "people.end_exclusive" : "people.ends")}
              <Input
                type={editor.allDay ? "date" : "datetime-local"}
                value={editor.end}
                required
                onChange={(event) => change("end", event.target.value)}
              />
            </label>
            <label className="grid gap-1 text-sm">
              {t("people.timezone")}
              <Input
                value={editor.timezone}
                list="people-timezones"
                required
                maxLength={100}
                onChange={(event) => change("timezone", event.target.value)}
              />
              <datalist id="people-timezones">
                {[
                  "UTC",
                  "Asia/Shanghai",
                  "Asia/Tokyo",
                  "America/New_York",
                  "America/Los_Angeles",
                  "Europe/London",
                  "Europe/Paris",
                  "Australia/Sydney",
                ].map((value) => (
                  <option key={value} value={value} />
                ))}
              </datalist>
            </label>
            <label className="grid gap-1 text-sm">
              {t("people.repeated_time")}
              <select
                className="h-9 rounded-md border bg-background px-3"
                aria-label={t("people.repeated_time")}
                value={editor.disambiguation}
                onChange={(event) =>
                  change(
                    "disambiguation",
                    event.target.value as "earlier" | "later",
                  )
                }
              >
                <option value="earlier">{t("people.earlier")}</option>
                <option value="later">{t("people.later")}</option>
              </select>
            </label>
            {!occurrence && (
              <>
                {!!selected?.recurrence_overrides?.length && (
                  <label className="flex items-start gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={clearExceptions}
                      onChange={(event) =>
                        setClearExceptions(event.target.checked)
                      }
                    />
                    <span>{t("people.clear_exceptions")}</span>
                  </label>
                )}
                <label className="grid gap-1 text-sm">
                  {t("people.repeat")}
                  <select
                    className="h-9 rounded-md border bg-background px-3"
                    aria-label={t("people.repeat")}
                    value={editor.frequency}
                    onChange={(event) =>
                      change("frequency", event.target.value)
                    }
                  >
                    <option value="">{t("people.no_repeat")}</option>
                    {["DAILY", "WEEKLY", "MONTHLY", "YEARLY"].map((value) => (
                      <option key={value} value={value}>
                        {t("people.repeat_" + value.toLowerCase())}
                      </option>
                    ))}
                    {editor.frequency === "CUSTOM" && (
                      <option value="CUSTOM">
                        {t("people.custom_repeat")}
                      </option>
                    )}
                  </select>
                </label>
                {editor.frequency && editor.frequency !== "CUSTOM" && (
                  <div className="grid grid-cols-2 gap-3">
                    <label className="grid gap-1 text-sm">
                      {t("people.interval")}
                      <Input
                        type="number"
                        min={1}
                        max={366}
                        required
                        value={editor.interval}
                        onChange={(event) =>
                          change("interval", Number(event.target.value))
                        }
                      />
                    </label>
                    <label className="grid gap-1 text-sm">
                      {t("people.count")}
                      <Input
                        type="number"
                        min={1}
                        max={400}
                        required
                        value={editor.count}
                        onChange={(event) =>
                          change("count", Number(event.target.value))
                        }
                      />
                    </label>
                  </div>
                )}
              </>
            )}
            <label className="grid gap-1 text-sm">
              {t("people.location")}
              <Input
                maxLength={500}
                value={editor.location}
                onChange={(event) => change("location", event.target.value)}
              />
            </label>
            <label className="grid gap-1 text-sm">
              {t("people.event_status")}
              <select
                className="h-9 rounded-md border bg-background px-3"
                aria-label={t("people.event_status")}
                value={editor.status}
                onChange={(event) => change("status", event.target.value)}
              >
                {["confirmed", "tentative", "cancelled"].map((value) => (
                  <option key={value} value={value}>
                    {t("people.status_" + value)}
                  </option>
                ))}
              </select>
            </label>
            {!occurrence && (
              <fieldset className="space-y-2 md:col-span-2" disabled={busy}>
                <legend className="text-sm font-medium">
                  {t("reminders.settings")}
                </legend>
                <p className="text-sm text-muted-foreground">
                  {t("reminders.help")}
                </p>
                {editor.reminders.map((minutes, index) => (
                  <div key={index} className="flex items-center gap-2">
                    <label className="grid gap-1 text-sm">
                      {t("reminders.minutes", { number: index + 1 })}
                      <Input
                        type="number"
                        min={0}
                        max={40320}
                        step={1}
                        required
                        value={minutes}
                        onChange={(event) =>
                          change(
                            "reminders",
                            editor.reminders.map((value, position) =>
                              position === index
                                ? Number(event.target.value)
                                : value,
                            ),
                          )
                        }
                      />
                    </label>
                    <Button
                      type="button"
                      variant="ghost"
                      aria-label={t("reminders.remove", { number: index + 1 })}
                      onClick={() =>
                        change(
                          "reminders",
                          editor.reminders.filter(
                            (_, position) => position !== index,
                          ),
                        )
                      }
                    >
                      {t("action.delete")}
                    </Button>
                  </div>
                ))}
                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    disabled={editor.reminders.length >= 5}
                    onClick={() =>
                      change("reminders", [...editor.reminders, 10])
                    }
                  >
                    {t("reminders.add")}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    disabled={editor.reminders.length === 0}
                    onClick={() => change("reminders", [])}
                  >
                    {t("reminders.clear")}
                  </Button>
                </div>
              </fieldset>
            )}
            <label className="grid gap-1 text-sm md:col-span-2">
              {t("people.description")}
              <textarea
                className={area}
                maxLength={8000}
                value={editor.description}
                onChange={(event) => change("description", event.target.value)}
              />
            </label>
            <label className="grid gap-1 text-sm md:col-span-2">
              {t("people.attendees")}
              <textarea
                className={area}
                value={editor.attendees}
                disabled={!!occurrence}
                onChange={(event) => change("attendees", event.target.value)}
              />
            </label>
            <p className="text-sm text-muted-foreground md:col-span-2">
              {t("people.invitation_help")}
            </p>
            <div className="flex flex-wrap gap-2 md:col-span-2">
              <Button type="submit" disabled={busy}>
                {t("action.save")}
              </Button>
              <Button
                variant="outline"
                type="button"
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                {t("action.cancel")}
              </Button>
              {selected && (
                <Button
                  type="button"
                  variant="destructive"
                  disabled={busy}
                  onClick={() => setDeleting(true)}
                >
                  {t(occurrence ? "people.cancel_occurrence" : "action.delete")}
                </Button>
              )}
            </div>
          </form>
        </section>
      )}
      {events.isLoading && (
        <div
          aria-label={t("state.loading")}
          className="h-28 animate-pulse rounded-md bg-muted"
        />
      )}
      {!events.isLoading && rows.length === 0 && validRange && (
        <p className="py-8 text-sm text-muted-foreground">
          {t("people.events_empty")}
        </p>
      )}
      <ul className="divide-y">
        {rows.map((event) => (
          <li key={event.id} className="space-y-3 py-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <h3 className="break-words font-medium">{event.summary}</h3>
                <p className="text-sm text-muted-foreground">
                  {format(event.starts_at, event.timezone, event.all_day)} –{" "}
                  {format(event.ends_at, event.timezone, event.all_day)} ·{" "}
                  {event.timezone}
                </p>
                {event.location && (
                  <p className="break-words text-sm">{event.location}</p>
                )}
              </div>
              <Button variant="outline" size="sm" onClick={() => edit(event)}>
                {t(event.rrule ? "people.edit_series" : "action.edit")}
              </Button>
            </div>
            <InvitationStatus attendees={event.attendees} />
            {(event.attendees?.length ?? 0) > 0 && (
              <SendInvitationsButton
                eventId={event.id}
                method={event.status === "cancelled" ? "cancel" : "request"}
              />
            )}
            {event.rrule && (
              <details className="text-sm">
                <summary className="cursor-pointer py-1">
                  {t("people.series_instances", {
                    count: event.instances?.length ?? 0,
                  })}
                </summary>
                <ul className="mt-2 divide-y">
                  {[
                    ...(event.instances ?? []),
                    ...(event.recurrence_overrides ?? []).filter(
                      (item) => item.status === "cancelled",
                    ),
                  ].map((instance) => (
                    <li
                      key={instance.recurrence_id}
                      className="flex flex-wrap items-center justify-between gap-2 py-2"
                    >
                      <span>
                        {format(
                          instance.starts_at,
                          event.timezone,
                          instance.all_day,
                        )}{" "}
                        · {instance.summary}
                        {instance.status === "cancelled"
                          ? " · " + t("people.status_cancelled")
                          : ""}
                      </span>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => edit(event, instance)}
                      >
                        {t("people.edit_occurrence")}
                      </Button>
                    </li>
                  ))}
                </ul>
              </details>
            )}
          </li>
        ))}
      </ul>
      {events.hasNextPage && (
        <Button
          variant="outline"
          disabled={events.isFetchingNextPage}
          onClick={() => void events.fetchNextPage()}
        >
          {t("people.load_more")}
        </Button>
      )}
      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t(
                occurrence ? "people.cancel_occurrence" : "people.delete_event",
              )}
            </DialogTitle>
            <DialogDescription>
              {t(
                occurrence
                  ? "people.cancel_occurrence_help"
                  : "people.delete_event_help",
              )}
            </DialogDescription>
          </DialogHeader>
          {!occurrence && (selected?.attendees?.length ?? 0) > 0 && (
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={notify}
                onChange={(event) => setNotify(event.target.checked)}
              />
              {t("people.notify_cancel")}
            </label>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setDeleting(false)}
            >
              {t("action.cancel")}
            </Button>
            <Button
              variant="destructive"
              disabled={busy || !selected}
              onClick={() =>
                void run(async () => {
                  if (selected)
                    await deletePeopleEvent(selected, occurrence, notify);
                  setDeleting(false);
                  setOpen(false);
                  setSelected(undefined);
                  setOccurrence(undefined);
                })
              }
            >
              {t(occurrence ? "people.cancel_occurrence" : "action.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
