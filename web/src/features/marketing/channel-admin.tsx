import { useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

// Credentials are write-only: the response exposes safe channel metadata only.
export function MarketingChannelAdmin({ tenantID }: { tenantID: number }) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("marketing.channel_config")}</CardTitle>
      </CardHeader>
      <CardContent>
        <p className="mb-4 text-sm text-muted-foreground">
          {t("marketing.channel_config_help")}
        </p>
        {notice && (
          <p role="status" className="mb-3 text-sm">
            {notice}
          </p>
        )}
        <form
          className="grid gap-3 md:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (busy) return;
            const form = e.currentTarget;
            const d = Object.fromEntries(new FormData(form));
            setBusy(true);
            setNotice("");
            void request(
              d.channel_id
                ? `/api/v1/platform/tenants/${tenantID}/marketing/channels/${d.channel_id}`
                : `/api/v1/platform/tenants/${tenantID}/marketing/channel`,
              {
                method: d.channel_id ? "PATCH" : "POST",
                body: {
                  name: d.name,
                  kind: "dedicated_gateway",
                  enabled: d.enabled === "on",
                  smtp: {
                    host: d.host,
                    port: Number(d.port),
                    from: d.from,
                    username: d.username,
                    password: d.password,
                    feedback_key: d.feedback_key,
                    dedicated: true,
                    implicit_tls: d.implicit_tls === "on",
                  },
                },
              },
            )
              .then(() => {
                form.reset();
                setNotice(t("marketing.channel_saved"));
              })
              .catch((err: unknown) => setNotice(errorMessage(err)))
              .finally(() => setBusy(false));
          }}
        >
          <label className="grid gap-1 text-sm">
            {t("marketing.channel_id")}
            <Input type="number" min={1} name="channel_id" />
          </label>
          <label className="grid gap-1 text-sm">
            {t("marketing.name")}
            <Input name="name" required maxLength={200} />
          </label>
          {["host", "from", "username", "password", "feedback_key"].map(
            (name) => (
              <label key={name} className="grid gap-1 text-sm">
                {t(`marketing.channel.${name}`)}
                <Input
                  name={name}
                  type={
                    name === "password" || name === "feedback_key"
                      ? "password"
                      : name === "from"
                        ? "email"
                        : "text"
                  }
                  required={name === "host" || name === "from"}
                  autoComplete="off"
                  maxLength={500}
                />
              </label>
            ),
          )}
          <label className="grid gap-1 text-sm">
            {t("marketing.channel.port")}
            <Input
              type="number"
              min={1}
              max={65535}
              name="port"
              defaultValue={587}
              required
            />
          </label>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" name="implicit_tls" className="mt-1" />
            {t("marketing.channel.implicit_tls")}
          </label>
          <label className="flex items-start gap-2 text-sm">
            <input type="checkbox" name="enabled" className="mt-1" />
            {t("marketing.channel.enable")}
          </label>
          <label className="flex items-start gap-2 text-sm md:col-span-2">
            <input type="checkbox" required className="mt-1" />
            {t("marketing.channel.dedicated_confirm")}
          </label>
          <Button disabled={busy}>{t("marketing.channel.save")}</Button>
        </form>
      </CardContent>
    </Card>
  );
}
