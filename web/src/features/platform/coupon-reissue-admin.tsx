import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
type Issued = {
  code: string;
  kind: string;
  value: number;
  bound_tenant_id: number;
  source_coupon_code: string;
  reissue_reason: string;
  valid_to: string;
  active: boolean;
};
export function CouponReissueAdmin() {
  const { t } = useTranslation(),
    errorMessage = useApiErrorMessage();
  const [rows, setRows] = useState<Issued[]>([]),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false),
    [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void request<{ coupons: Issued[] }>("/api/v1/platform/coupon-reissues", {
      signal: controller.signal,
    })
      .then((d) => {
        if (!controller.signal.aborted) setRows(d.coupons ?? []);
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(e));
      });
    return () => controller.abort();
  }, [revision, errorMessage]);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("coupon.reissue.title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {t("coupon.reissue.help")}
        </p>
        {notice && <p role="status">{notice}</p>}
        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (busy) return;
            const form = e.currentTarget,
              data = new FormData(form),
              tenant = Number(data.get("tenant_id")),
              source = String(data.get("source_code") ?? "").trim(),
              reason = String(data.get("reason") ?? "").trim(),
              until = String(data.get("valid_until") ?? "");
            if (
              !Number.isSafeInteger(tenant) ||
              tenant < 1 ||
              !source ||
              reason.length < 10 ||
              !until ||
              !Number.isFinite(new Date(until).getTime()) ||
              new Date(until).getTime() <= Date.now()
            ) {
              setNotice(t("coupon.reissue.invalid"));
              return;
            }
            setBusy(true);
            void request(`/api/v1/platform/tenants/${tenant}/coupon-reissues`, {
              method: "POST",
              body: {
                source_code: source,
                reason,
                valid_until: new Date(until).toISOString(),
              },
            })
              .then(() => {
                setNotice(t("coupon.reissue.saved"));
                setRevision((v) => v + 1);
                form.reset();
              })
              .catch((err: unknown) => setNotice(errorMessage(err)))
              .finally(() => setBusy(false));
          }}
        >
          <label className="grid gap-1 text-sm">
            {t("coupon.reissue.tenant")}
            <Input name="tenant_id" type="number" min={1} step={1} required />
          </label>
          <label className="grid gap-1 text-sm">
            {t("coupon.reissue.source")}
            <Input name="source_code" maxLength={200} required />
          </label>
          <label className="grid gap-1 text-sm">
            {t("coupon.reissue.until")}
            <Input name="valid_until" type="datetime-local" required />
          </label>
          <label className="grid gap-1 text-sm sm:col-span-2">
            {t("coupon.reissue.reason")}
            <textarea
              className="min-h-20 rounded-md border bg-background p-3"
              name="reason"
              minLength={10}
              maxLength={1000}
              required
            />
          </label>
          <label className="flex gap-2 text-sm sm:col-span-2">
            <input type="checkbox" required />
            {t("coupon.reissue.confirm")}
          </label>
          <Button className="sm:col-span-2" disabled={busy}>
            {t("coupon.reissue.issue")}
          </Button>
        </form>
        <ul className="divide-y">
          {rows.map((r) => (
            <li key={r.code} className="space-y-1 py-3 text-sm">
              <p className="break-all font-mono">{r.code}</p>
              <p>
                {t("coupon.reissue.tenant")} {r.bound_tenant_id} ·{" "}
                {t("coupon.reissue.source")} {r.source_coupon_code} ·{" "}
                {t(`coupon.reissue.kind.${r.kind}`)} {r.value} · {r.valid_to}
              </p>
              <p className="break-words">{r.reissue_reason}</p>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}
