import { useEffect, useState } from "react";
import { useAuth } from "@/app/auth-context";
import { MarketingChannelAdmin } from "./channel-admin";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
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

type Named = { id: number; name: string };
type Channel = Named & { enabled: boolean; kind: string };
type Campaign = Named & { status: string };
type Recipient = {
  id: number;
  email: string;
  status: string;
  result_code: string | null;
};
type Preview = {
  subject: string;
  text_body: string;
  opted_in_count: number;
  suppressed_count: number;
};
const area =
  "min-h-28 w-full rounded-md border border-input bg-background px-3 py-2 text-sm";

export function MarketingPage() {
  const { t } = useTranslation();
  const { isAdmin, currentTenant } = useAuth();
  const errorMessage = useApiErrorMessage();
  const [channels, setChannels] = useState<Channel[]>([]);
  const [templates, setTemplates] = useState<Named[]>([]);
  const [lists, setLists] = useState<Named[]>([]);
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [preview, setPreview] = useState<{
    campaign: Campaign;
    detail: Preview;
  } | null>(null);
  const [recipients, setRecipients] = useState<Recipient[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      request<{ channels: Channel[] }>("/api/v1/marketing/channels", {
        signal: controller.signal,
      }),
      request<{ templates: Named[] }>("/api/v1/marketing/templates", {
        signal: controller.signal,
      }),
      request<{ lists: Named[] }>("/api/v1/marketing/lists", {
        signal: controller.signal,
      }),
      request<{ campaigns: Campaign[] }>("/api/v1/marketing/campaigns", {
        signal: controller.signal,
      }),
    ])
      .then(([c, t, l, p]) => {
        if (controller.signal.aborted) return;
        setChannels(c.channels ?? []);
        setTemplates(t.templates ?? []);
        setLists(l.lists ?? []);
        setCampaigns(p.campaigns ?? []);
        setLoading(false);
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) {
          setNotice(errorMessage(e));
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [revision, errorMessage]);
  async function action(run: () => Promise<unknown>) {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      await run();
      setRevision((v) => v + 1);
    } catch (e) {
      setNotice(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  function fields(form: HTMLFormElement) {
    return Object.fromEntries(new FormData(form).entries());
  }
  const select = (name: string, label: string, items: Named[]) => (
    <label className="grid gap-1 text-sm">
      {t(label)}
      <select
        name={name}
        required
        className="h-10 rounded-md border border-input bg-background px-3"
      >
        <option value="">{t("marketing.choose")}</option>
        {items.map((x) => (
          <option key={x.id} value={x.id}>
            {x.name}
          </option>
        ))}
      </select>
    </label>
  );
  if (loading) return <p role="status">{t("state.loading")}</p>;
  return (
    <div className="space-y-6">
      {isAdmin && currentTenant && (
        <MarketingChannelAdmin tenantID={currentTenant.id} />
      )}
      <header>
        <h1 className="text-2xl font-semibold">{t("nav.marketing")}</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          {t("marketing.intro")}
        </p>
      </header>
      {notice && (
        <p role="alert" className="text-sm text-destructive">
          {notice}
        </p>
      )}
      <section
        aria-label={t("marketing.channels")}
        className="rounded-lg border p-4"
      >
        <h2 className="font-medium">{t("marketing.channels")}</h2>
        {channels.length ? (
          <ul className="mt-2 space-y-1 text-sm">
            {channels.map((c) => (
              <li key={c.id}>
                {c.name} ·{" "}
                {t(
                  c.kind !== "dedicated_gateway"
                    ? "marketing.channel_unavailable"
                    : c.enabled
                      ? "marketing.enabled"
                      : "marketing.disabled",
                )}{" "}
                · {t(`marketing.kind.${c.kind}`)}
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-2 text-sm text-muted-foreground">
            {t("marketing.no_channel")}
          </p>
        )}
      </section>
      <div className="grid gap-5 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>{t("marketing.template")}</CardTitle>
          </CardHeader>
          <CardContent>
            <form
              className="grid gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                const form = e.currentTarget;
                const data = fields(form);
                void action(async () => {
                  await request("/api/v1/marketing/templates", {
                    method: "POST",
                    body: data,
                  });
                  form.reset();
                });
              }}
            >
              <label className="grid gap-1 text-sm">
                {t("marketing.name")}
                <Input name="name" required maxLength={200} />
              </label>
              <label className="grid gap-1 text-sm">
                {t("marketing.subject")}
                <Input name="subject" required maxLength={998} />
              </label>
              <label className="grid gap-1 text-sm">
                {t("marketing.body")}
                <textarea
                  className={area}
                  name="text_body"
                  required
                  maxLength={1000000}
                />
              </label>
              <Button disabled={busy}>{t("marketing.save_template")}</Button>
            </form>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t("marketing.list")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            <form
              className="grid gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                const form = e.currentTarget;
                const data = fields(form);
                void action(async () => {
                  await request("/api/v1/marketing/lists", {
                    method: "POST",
                    body: data,
                  });
                  form.reset();
                });
              }}
            >
              <label className="grid gap-1 text-sm">
                {t("marketing.name")}
                <Input name="name" required maxLength={200} />
              </label>
              <Button disabled={busy}>{t("marketing.create_list")}</Button>
            </form>
            <form
              className="grid gap-3 border-t pt-4"
              onSubmit={(e) => {
                e.preventDefault();
                const form = e.currentTarget;
                const data = fields(form);
                const emails = String(data.emails)
                  .split(/\r?\n/)
                  .map((s) => s.trim())
                  .filter(Boolean);
                void action(async () => {
                  await request(
                    `/api/v1/marketing/lists/${data.list_id}/contacts`,
                    {
                      method: "POST",
                      body: {
                        contacts: emails.map((email) => ({
                          email,
                          consent_source: data.consent_source,
                          opted_in: true,
                        })),
                      },
                    },
                  );
                  form.reset();
                });
              }}
            >
              {select("list_id", "marketing.list", lists)}
              <label className="grid gap-1 text-sm">
                {t("marketing.emails")}
                <textarea
                  className={area}
                  name="emails"
                  required
                  maxLength={250000}
                />
              </label>
              <label className="grid gap-1 text-sm">
                {t("marketing.consent_source")}
                <Input name="consent_source" required maxLength={256} />
              </label>
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" required className="mt-1" />
                {t("marketing.consent_confirm")}
              </label>
              <Button disabled={busy}>{t("marketing.import")}</Button>
            </form>
          </CardContent>
        </Card>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>{t("marketing.new_campaign")}</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            className="grid gap-3 md:grid-cols-2"
            onSubmit={(e) => {
              e.preventDefault();
              const form = e.currentTarget;
              const data = fields(form);
              void action(async () => {
                await request("/api/v1/marketing/campaigns", {
                  method: "POST",
                  body: {
                    name: data.name,
                    channel_id: Number(data.channel_id),
                    template_id: Number(data.template_id),
                    list_id: Number(data.list_id),
                  },
                });
                form.reset();
              });
            }}
          >
            <label className="grid gap-1 text-sm">
              {t("marketing.name")}
              <Input name="name" required maxLength={200} />
            </label>
            {select(
              "channel_id",
              "marketing.channels",
              channels.filter(
                (c) => c.enabled && c.kind === "dedicated_gateway",
              ),
            )}
            {select("template_id", "marketing.template", templates)}
            {select("list_id", "marketing.list", lists)}
            <Button disabled={busy}>{t("marketing.create_draft")}</Button>
          </form>
        </CardContent>
      </Card>
      <section>
        <h2 className="mb-3 text-lg font-medium">{t("marketing.campaigns")}</h2>
        {campaigns.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("marketing.empty")}
          </p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {campaigns.map((c) => (
              <li
                key={c.id}
                className="flex flex-wrap items-center justify-between gap-3 p-4"
              >
                <div>
                  <p className="font-medium">{c.name}</p>
                  <p className="text-sm text-muted-foreground">
                    {t(`marketing.status.${c.status}`)}
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    variant="outline"
                    disabled={busy}
                    onClick={() =>
                      void action(async () => {
                        const d = await request<{ recipients: Recipient[] }>(
                          `/api/v1/marketing/campaigns/${c.id}/recipients`,
                        );
                        setRecipients(d.recipients ?? []);
                      })
                    }
                  >
                    {t("marketing.results")}
                  </Button>
                  {c.status === "draft" && (
                    <Button
                      disabled={busy}
                      onClick={() =>
                        void action(async () => {
                          const d = await request<{ preview: Preview }>(
                            `/api/v1/marketing/campaigns/${c.id}/preview`,
                          );
                          setPreview({ campaign: c, detail: d.preview });
                        })
                      }
                    >
                      {t("marketing.preview")}
                    </Button>
                  )}
                  {["scheduled", "running", "paused"].includes(c.status) && (
                    <>
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() =>
                          void action(() =>
                            request(`/api/v1/marketing/campaigns/${c.id}`, {
                              method: "PATCH",
                              body: {
                                status:
                                  c.status === "paused"
                                    ? "scheduled"
                                    : "paused",
                              },
                            }),
                          )
                        }
                      >
                        {t(
                          c.status === "paused"
                            ? "marketing.resume"
                            : "marketing.pause",
                        )}
                      </Button>
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() =>
                          void action(() =>
                            request(`/api/v1/marketing/campaigns/${c.id}`, {
                              method: "PATCH",
                              body: { status: "canceled" },
                            }),
                          )
                        }
                      >
                        {t("marketing.cancel")}
                      </Button>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
      <Dialog
        open={preview !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setPreview(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("marketing.confirm_title")}</DialogTitle>
          </DialogHeader>
          {preview && (
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                const current = preview;
                const date = new FormData(e.currentTarget).get("scheduled_at");
                void action(async () => {
                  await request(
                    `/api/v1/marketing/campaigns/${current.campaign.id}/confirm`,
                    {
                      method: "POST",
                      body: {
                        confirm: true,
                        ...(date
                          ? {
                              scheduled_at: new Date(
                                String(date),
                              ).toISOString(),
                            }
                          : {}),
                      },
                    },
                  );
                  setPreview(null);
                });
              }}
            >
              <h3 className="font-medium break-words">
                {preview.detail.subject}
              </h3>
              <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words font-sans text-sm">
                {preview.detail.text_body}
              </pre>
              <p className="text-sm">
                {t("marketing.preview_counts", {
                  count: preview.detail.opted_in_count,
                  suppressed: preview.detail.suppressed_count,
                })}
              </p>
              <label className="grid gap-1 text-sm">
                {t("marketing.schedule")}
                <Input type="datetime-local" name="scheduled_at" required />
              </label>
              <label className="flex items-start gap-2 text-sm">
                <input required type="checkbox" className="mt-1" />
                {t("marketing.send_confirm")}
              </label>
              <Button disabled={busy}>{t("marketing.confirm_send")}</Button>
            </form>
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={recipients !== null}
        onOpenChange={(open) => {
          if (!open) setRecipients(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("marketing.results")}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            {t("marketing.results_limit")}
          </p>
          <ul className="max-h-96 overflow-auto divide-y">
            {recipients?.map((r) => (
              <li className="py-2 text-sm break-all" key={r.id}>
                {r.email} · {t(`marketing.recipient.${r.status}`)}
              </li>
            ))}
          </ul>
        </DialogContent>
      </Dialog>
    </div>
  );
}
