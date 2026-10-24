import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/app/auth-context";
import { useI18nLocale } from "@/app/providers";
import { ApiRequestError, getSessionToken, request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Ticket = {
  id: number;
  tenant_id: number;
  subject: string;
  status: string;
  assigned_to: number | null;
};
type Message = { id: number; body: string; is_staff: boolean };
type Attachment = { id: number; message_id: number; filename: string };
type Detail = {
  ticket: Ticket;
  messages: Message[];
  attachments: Attachment[];
  has_more: boolean;
};
type Announcement = {
  id: number;
  title: Record<string, string>;
  body: Record<string, string>;
  state: string;
  brand_id: number | null;
  published_at: string | null;
  ends_at: string | null;
};
type Topic = { id: string; title_key: string; body_key: string };
const textArea =
  "min-h-28 w-full rounded-md border border-input bg-background px-3 py-2 text-sm";
export function PlatformSupportPage() {
  return <SupportPage platform />;
}
export function SupportPage({ platform = false }: { platform?: boolean }) {
  const { t } = useTranslation();
  const { locale } = useI18nLocale();
  const { isAdmin } = useAuth();
  const errorMessage = useApiErrorMessage();
  const [tickets, setTickets] = useState<Ticket[]>([]);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [topics, setTopics] = useState<Topic[]>([]);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [notice, setNotice] = useState("");
  const [more, setMore] = useState(false);
  const [revision, setRevision] = useState(0);
  const [files, setFiles] = useState<File[]>([]);
  const base = platform ? "/api/v1/platform/support" : "/api/v1/support";
  const ticketBase = (ticket: Ticket) =>
    platform
      ? `${base}/tenants/${ticket.tenant_id}/tickets/${ticket.id}`
      : `${base}/tickets/${ticket.id}`;
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      request<{ tickets: Ticket[]; has_more: boolean }>(`${base}/tickets`, {
        signal: controller.signal,
      }),
      request<{ announcements: Announcement[] }>(`${base}/announcements`, {
        signal: controller.signal,
      }),
      platform
        ? Promise.resolve({ topics: [] })
        : request<{ topics: Topic[] }>(`${base}/help`, {
            signal: controller.signal,
          }),
    ])
      .then(([a, b, c]) => {
        if (controller.signal.aborted) return;
        setTickets(a.tickets ?? []);
        setMore(a.has_more);
        setAnnouncements(b.announcements ?? []);
        setTopics(c.topics);
        setLoading(false);
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) {
          setNotice(errorMessage(e));
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [base, revision, platform, errorMessage]);
  async function action(run: () => Promise<unknown>, refresh = true) {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      await run();
      if (refresh) setRevision((v) => v + 1);
    } catch (e) {
      setNotice(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function open(ticket: Ticket) {
    const row = await request<Detail>(ticketBase(ticket));
    setDetail({
      ...row,
      messages: row.messages ?? [],
      attachments: row.attachments ?? [],
    });
    setFiles([]);
  }
  async function download(attachment: Attachment) {
    if (!detail) return;
    const path = platform
      ? `${base}/tenants/${detail.ticket.tenant_id}/attachments/${attachment.id}`
      : `${base}/attachments/${attachment.id}`;
    const token = getSessionToken();
    const response = await fetch(path, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
      credentials: "same-origin",
    });
    if (!response.ok)
      throw new ApiRequestError(response.status, {
        error: { code: "REQUEST_FAILED" },
      });
    const url = URL.createObjectURL(await response.blob());
    const a = document.createElement("a");
    a.href = url;
    a.download = attachment.filename;
    a.click();
    URL.revokeObjectURL(url);
  }
  const local = (s: Record<string, string>) => s[locale] ?? s.en ?? "";
  if (platform && !isAdmin) return <p role="alert">{t("error.FORBIDDEN")}</p>;
  if (loading) return <p role="status">{t("state.loading")}</p>;
  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold">
          {t(platform ? "nav.platform_support" : "nav.support")}
        </h1>
        <p className="mt-2 text-sm text-muted-foreground">
          {t("support.intro")}
        </p>
      </header>
      {notice && (
        <p role="alert" className="text-sm text-destructive">
          {notice}
        </p>
      )}
      {isAdmin && !platform && (
        <a
          className="text-sm underline underline-offset-4"
          href="/platform/support"
        >
          {t("nav.platform_support")}
        </a>
      )}
      {!platform && (
        <Card>
          <CardHeader>
            <CardTitle>{t("support.create")}</CardTitle>
          </CardHeader>
          <CardContent>
            <form
              className="grid gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                const form = e.currentTarget;
                const fields = Object.fromEntries(new FormData(form));
                void action(async () => {
                  const row = await request<{ ticket: Ticket }>(
                    `${base}/tickets`,
                    { method: "POST", body: fields },
                  );
                  form.reset();
                  await open(row.ticket);
                });
              }}
            >
              <label className="grid gap-1 text-sm">
                {t("support.subject")}
                <Input required name="Subject" maxLength={200} />
              </label>
              <label className="grid gap-1 text-sm">
                {t("support.body")}
                <textarea
                  className={textArea}
                  required
                  name="Body"
                  maxLength={60000}
                />
              </label>
              <label className="grid gap-1 text-sm">
                {t("support.priority")}
                <select
                  name="Priority"
                  className="h-10 rounded-md border bg-background px-3"
                  defaultValue="normal"
                >
                  {["low", "normal", "high"].map((p) => (
                    <option key={p} value={p}>
                      {t(`support.priority.${p}`)}
                    </option>
                  ))}
                </select>
              </label>
              <Button disabled={busy}>{t("support.submit")}</Button>
            </form>
          </CardContent>
        </Card>
      )}
      <section>
        <h2 className="mb-3 text-lg font-medium">{t("support.tickets")}</h2>
        {!tickets.length ? (
          <p className="text-sm text-muted-foreground">{t("support.empty")}</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {tickets.map((ticket) => (
              <li
                key={ticket.id}
                className="flex flex-wrap items-center justify-between gap-3 p-4"
              >
                <div>
                  <p className="font-medium break-words">{ticket.subject}</p>
                  <p className="text-sm text-muted-foreground">
                    {t(`support.status.${ticket.status}`)}
                    {platform ? ` · #${ticket.tenant_id}` : ""}
                  </p>
                </div>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => void action(() => open(ticket))}
                >
                  {t("support.open")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        {more && (
          <Button
            className="mt-3"
            variant="outline"
            disabled={busy}
            onClick={() =>
              void action(async () => {
                const row = await request<{
                  tickets: Ticket[];
                  has_more: boolean;
                }>(`${base}/tickets?before_id=${tickets.at(-1)?.id}`);
                setTickets((current) => [...current, ...(row.tickets ?? [])]);
                setMore(row.has_more);
              }, false)
            }
          >
            {t("support.more")}
          </Button>
        )}
      </section>
      <section>
        <h2 className="mb-3 text-lg font-medium">
          {t("support.announcements")}
        </h2>
        {!announcements.length ? (
          <p className="text-sm text-muted-foreground">
            {t("support.no_announcements")}
          </p>
        ) : (
          <div className="space-y-4">
            {announcements.map((a) => (
              <article className="rounded-lg border p-4" key={a.id}>
                <h3 className="font-medium break-words">{local(a.title)}</h3>
                <p className="mt-2 whitespace-pre-wrap break-words text-sm">
                  {local(a.body)}
                </p>
                {platform && (
                  <div className="mt-3 flex flex-wrap items-center gap-2">
                    <span className="text-sm">
                      {t(`support.announcement.${a.state}`)}
                    </span>
                    {a.state === "draft" && (
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() =>
                          void action(() =>
                            request(`${base}/announcements/${a.id}`, {
                              method: "PATCH",
                              body: {
                                ...a,
                                state: "published",
                                published_at: new Date().toISOString(),
                              },
                            }),
                          )
                        }
                      >
                        {t("support.publish")}
                      </Button>
                    )}
                    {a.state !== "withdrawn" && (
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() =>
                          void action(() =>
                            request(`${base}/announcements/${a.id}`, {
                              method: "PATCH",
                              body: { ...a, state: "withdrawn" },
                            }),
                          )
                        }
                      >
                        {t("support.withdraw")}
                      </Button>
                    )}
                  </div>
                )}
              </article>
            ))}
          </div>
        )}
      </section>
      {platform && (
        <Card>
          <CardHeader>
            <CardTitle>{t("support.announcement.create")}</CardTitle>
          </CardHeader>
          <CardContent>
            <form
              className="grid gap-3 md:grid-cols-2"
              onSubmit={(e) => {
                e.preventDefault();
                const form = e.currentTarget;
                const data = Object.fromEntries(new FormData(form));
                void action(async () => {
                  await request(`${base}/announcements`, {
                    method: "POST",
                    body: {
                      brand_id: data.brand_id ? Number(data.brand_id) : null,
                      title: { "zh-CN": data.title_zh, en: data.title_en },
                      body: { "zh-CN": data.body_zh, en: data.body_en },
                      state: data.state,
                      published_at: data.published_at
                        ? new Date(String(data.published_at)).toISOString()
                        : data.state === "published"
                          ? new Date().toISOString()
                          : null,
                      ends_at: data.ends_at
                        ? new Date(String(data.ends_at)).toISOString()
                        : null,
                    },
                  });
                  form.reset();
                });
              }}
            >
              {["zh", "en"].map((lang) => (
                <div key={lang} className="grid gap-3">
                  <label className="grid gap-1 text-sm">
                    {t(`support.announcement.title_${lang}`)}
                    <Input required name={`title_${lang}`} maxLength={200} />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(`support.announcement.body_${lang}`)}
                    <textarea
                      className={textArea}
                      required
                      name={`body_${lang}`}
                      maxLength={60000}
                    />
                  </label>
                </div>
              ))}
              <label className="grid gap-1 text-sm">
                {t("support.announcement.brand")}
                <Input name="brand_id" type="number" min={1} />
              </label>
              <label className="grid gap-1 text-sm">
                {t("support.announcement.state")}
                <select
                  name="state"
                  className="h-10 rounded-md border bg-background px-3"
                >
                  {["draft", "published"].map((s) => (
                    <option key={s} value={s}>
                      {t(`support.announcement.${s}`)}
                    </option>
                  ))}
                </select>
              </label>
              <label className="grid gap-1 text-sm">
                {t("support.announcement.starts")}
                <Input name="published_at" type="datetime-local" />
              </label>
              <label className="grid gap-1 text-sm">
                {t("support.announcement.ends")}
                <Input name="ends_at" type="datetime-local" />
              </label>
              <Button disabled={busy}>{t("support.announcement.save")}</Button>
            </form>
          </CardContent>
        </Card>
      )}
      {!platform && (
        <section>
          <h2 className="mb-3 text-lg font-medium">{t("support.help")}</h2>
          <div className="divide-y rounded-lg border">
            {topics.map((topic) => (
              <details key={topic.id} className="p-4">
                <summary className="cursor-pointer font-medium">
                  {t(topic.title_key)}
                </summary>
                <p className="mt-3 whitespace-pre-wrap text-sm leading-6">
                  {t(topic.body_key)}
                </p>
              </details>
            ))}
          </div>
        </section>
      )}
      <Dialog
        open={detail !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setDetail(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{detail?.ticket.subject}</DialogTitle>
          </DialogHeader>
          {detail && (
            <div className="max-h-[75vh] space-y-4 overflow-y-auto">
              <ol className="space-y-4">
                {detail.messages.map((m) => (
                  <li key={m.id} className="rounded-md border p-3">
                    <p className="text-xs font-medium text-muted-foreground">
                      {t(m.is_staff ? "support.staff" : "support.author")}
                    </p>
                    <p className="mt-2 whitespace-pre-wrap break-words text-sm">
                      {m.body}
                    </p>
                    {detail.attachments
                      .filter((a) => a.message_id === m.id)
                      .map((a) => (
                        <Button
                          key={a.id}
                          variant="link"
                          disabled={busy}
                          onClick={() => void action(() => download(a))}
                        >
                          {a.filename}
                        </Button>
                      ))}
                  </li>
                ))}
              </ol>
              {detail.has_more && (
                <Button
                  disabled={busy}
                  variant="outline"
                  onClick={() =>
                    void action(async () => {
                      const row = await request<Detail>(
                        `${ticketBase(detail.ticket)}?after_id=${detail.messages.at(-1)?.id ?? 0}`,
                      );
                      setDetail((current) =>
                        current
                          ? {
                              ...row,
                              messages: [
                                ...current.messages,
                                ...(row.messages ?? []),
                              ],
                              attachments: row.attachments ?? [],
                            }
                          : null,
                      );
                    }, false)
                  }
                >
                  {t("support.more_messages")}
                </Button>
              )}
              <form
                className="grid gap-3"
                onSubmit={(e) => {
                  e.preventDefault();
                  const form = e.currentTarget;
                  const body = new FormData(form).get("body");
                  const ticket = detail.ticket;
                  void action(async () => {
                    const attachments = await Promise.all(
                      files.map(async (file) => {
                        const bytes = new Uint8Array(await file.arrayBuffer());
                        let binary = "";
                        for (const byte of bytes)
                          binary += String.fromCharCode(byte);
                        return {
                          filename: file.name,
                          content_type: file.type,
                          content: btoa(binary),
                        };
                      }),
                    );
                    await request(`${ticketBase(ticket)}/messages`, {
                      method: "POST",
                      body: { body, attachments },
                    });
                    form.reset();
                    setFiles([]);
                    await open(ticket);
                  });
                }}
              >
                <label className="grid gap-1 text-sm">
                  {t("support.reply")}
                  <textarea
                    className={textArea}
                    name="body"
                    required
                    maxLength={60000}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("support.attachments")}
                  <input
                    type="file"
                    multiple
                    accept="text/plain,image/png,image/jpeg,application/pdf"
                    onChange={(e) => {
                      const selected = Array.from(e.currentTarget.files ?? []);
                      if (
                        selected.length > 3 ||
                        selected.reduce((sum, file) => sum + file.size, 0) >
                          512000
                      ) {
                        setNotice(t("support.attachment_limit"));
                        e.currentTarget.value = "";
                        setFiles([]);
                      } else {
                        setFiles(selected);
                      }
                    }}
                  />
                </label>
                <p className="text-xs text-muted-foreground">
                  {t("support.attachment_limit")}
                </p>
                <Button
                  disabled={
                    busy ||
                    ["closed", "resolved"].includes(detail.ticket.status)
                  }
                >
                  {t("support.send_reply")}
                </Button>
              </form>
              <form
                className="flex flex-wrap items-end gap-2 border-t pt-4"
                onSubmit={(e) => {
                  e.preventDefault();
                  const data = Object.fromEntries(
                    new FormData(e.currentTarget),
                  );
                  const ticket = detail.ticket;
                  void action(async () => {
                    await request(`${ticketBase(ticket)}/status`, {
                      method: "PATCH",
                      body: {
                        status: data.status,
                        ...(platform
                          ? {
                              assigned_to: data.assigned_to
                                ? Number(data.assigned_to)
                                : null,
                            }
                          : {}),
                      },
                    });
                    await open(ticket);
                  });
                }}
              >
                <label className="grid gap-1 text-sm">
                  {t("support.change_status")}
                  <select
                    className="h-10 rounded-md border bg-background px-3"
                    name="status"
                    defaultValue={detail.ticket.status}
                  >
                    {(platform
                      ? ["open", "pending", "resolved", "closed"]
                      : ["open", "closed"]
                    ).map((s) => (
                      <option key={s} value={s}>
                        {t(`support.status.${s}`)}
                      </option>
                    ))}
                  </select>
                </label>
                {platform && (
                  <label className="grid gap-1 text-sm">
                    {t("support.assign")}
                    <Input
                      type="number"
                      name="assigned_to"
                      min={1}
                      defaultValue={detail.ticket.assigned_to ?? ""}
                    />
                  </label>
                )}
                <Button disabled={busy}>{t("support.save_status")}</Button>
              </form>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
