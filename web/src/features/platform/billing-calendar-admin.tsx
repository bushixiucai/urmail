import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

type Calendar = {
  timezone: string;
  first_paid_at: string;
  anchor_at: string;
  version: number;
  source: string;
};
type CalendarState = {
  tenant_id: number;
  calendar: Calendar | null;
  review_required: boolean;
  current_business_timezone: string;
};

// Existing paid dates and the first timezone cannot be changed from this panel.
export function BillingCalendarAdmin({
  tenants,
}: {
  tenants: { id: number; name: string }[];
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [tenantID, setTenantID] = useState("");
  const [data, setData] = useState<CalendarState | null>(null);
  const [loading, setLoading] = useState(false),
    [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  useEffect(() => {
    setData(null);
    setNotice("");
    if (!tenantID) {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    void request<CalendarState>(
      `/api/v1/platform/tenants/${tenantID}/billing-calendar`,
      { signal: controller.signal },
    )
      .then((value) => {
        if (!controller.signal.aborted) setData(value);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(err));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [tenantID, errorMessage]);
  return (
    <Card data-testid="billing-calendar-admin">
      <CardHeader>
        <CardTitle>{t("calendar.admin.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {t("calendar.admin.intro")}
        </p>
        <label className="grid gap-1 text-sm">
          {t("calendar.admin.tenant")}
          <select
            className="h-10 rounded-md border bg-background px-3"
            value={tenantID}
            disabled={busy}
            onChange={(e) => setTenantID(e.target.value)}
          >
            <option value="">{t("calendar.admin.choose")}</option>
            {tenants.map((tenant) => (
              <option key={tenant.id} value={tenant.id}>
                {tenant.name}
              </option>
            ))}
          </select>
        </label>
        {loading && <p role="status">{t("common.loading")}</p>}
        {notice && (
          <p role="status" className="break-words text-sm">
            {notice}
          </p>
        )}
        {data && (
          <>
            <p className="text-sm text-muted-foreground">
              {t("calendar.admin.current_timezone", {
                timezone: data.current_business_timezone,
              })}
            </p>
            {data.calendar ? (
              <div className="space-y-3">
                <p className="text-sm">{t("calendar.admin.readonly")}</p>
                <dl className="grid gap-2 text-sm sm:grid-cols-[auto_1fr]">
                  {(
                    [
                      ["timezone", data.calendar.timezone],
                      ["first_paid_at", data.calendar.first_paid_at],
                      ["anchor_at", data.calendar.anchor_at],
                      ["version", String(data.calendar.version)],
                      ["source", data.calendar.source],
                    ] as const
                  ).map(([key, value]) => (
                    <div key={key} className="contents">
                      <dt className="text-muted-foreground">
                        {t("calendar.admin." + key)}
                      </dt>
                      <dd className="break-all">{value}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            ) : data.review_required ? (
              <form
                key={data.tenant_id}
                className="grid gap-3 sm:grid-cols-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (busy) return;
                  const form = e.currentTarget,
                    values = new FormData(form);
                  setBusy(true);
                  setNotice("");
                  void request<CalendarState>(
                    `/api/v1/platform/tenants/${data.tenant_id}/billing-calendar`,
                    {
                      method: "PUT",
                      body: {
                        timezone: values.get("timezone"),
                        first_paid_at: values.get("first_paid_at"),
                        reason: values.get("reason"),
                      },
                    },
                  )
                    .then((value) => {
                      setData(value);
                      setNotice(t("calendar.admin.saved"));
                    })
                    .catch((err: unknown) => setNotice(errorMessage(err)))
                    .finally(() => setBusy(false));
                }}
              >
                <p className="text-sm sm:col-span-2">
                  {t("calendar.admin.legacy")}
                </p>
                <label className="grid gap-1 text-sm">
                  {t("calendar.admin.timezone")}
                  <Input
                    name="timezone"
                    required
                    maxLength={64}
                    placeholder="Asia/Shanghai"
                    disabled={busy}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("calendar.admin.first_paid_at")}
                  <Input
                    name="first_paid_at"
                    required
                    maxLength={64}
                    placeholder="2024-01-31T09:00:00+08:00"
                    disabled={busy}
                  />
                </label>
                <p className="text-sm text-muted-foreground sm:col-span-2">
                  {t("calendar.admin.timestamp_help")}
                </p>
                <label className="grid gap-1 text-sm sm:col-span-2">
                  {t("calendar.admin.reason")}
                  <textarea
                    name="reason"
                    required
                    minLength={20}
                    maxLength={1000}
                    disabled={busy}
                    className="min-h-24 rounded-md border bg-background px-3 py-2"
                  />
                </label>
                <label className="flex items-start gap-2 text-sm sm:col-span-2">
                  <input
                    type="checkbox"
                    required
                    disabled={busy}
                    className="mt-1"
                  />
                  {t("calendar.admin.confirm")}
                </label>
                <Button disabled={busy}>{t("calendar.admin.save")}</Button>
              </form>
            ) : (
              <p className="text-sm">{t("calendar.admin.automatic")}</p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
