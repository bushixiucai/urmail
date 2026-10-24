import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";

/** Edits only this scope; clearing a field restores its inherited value. */
export function SettingsEditor({
  platform = false,
  brandId,
}: {
  platform?: boolean;
  brandId?: number;
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [locale, setLocale] = useState("");
  const [primary, setPrimary] = useState("");
  const [quota, setQuota] = useState("");
  const [timezone, setTimezone] = useState("");
  const [notice, setNotice] = useState("");
  const [pending, setPending] = useState(false);
  const [edited, setEdited] = useState<Set<string>>(new Set());
  const changed = (key: string) => setEdited((prev) => new Set([...prev, key]));
  const path = brandId
    ? `/api/v1/platform/brands/${brandId}/settings`
    : platform
      ? "/api/v1/platform/settings"
      : "/api/v1/tenants/current";
  useEffect(() => {
    const controller = new AbortController();
    request<{ settings: Record<string, unknown> }>(path, {
      signal: controller.signal,
    })
      .then((data) => {
        if (controller.signal.aborted) return;
        const settings = data.settings ?? {};
        setNotice(t("page.settings.inheritance_hint"));
        setLocale(typeof settings.locale === "string" ? settings.locale : "");
        const theme = settings.theme as Record<string, unknown> | undefined;
        setPrimary(typeof theme?.primary === "string" ? theme.primary : "");
        setQuota(
          typeof settings.mailbox_default_quota_bytes === "number"
            ? String(settings.mailbox_default_quota_bytes)
            : "",
        );
        setTimezone(
          typeof settings.business_timezone === "string"
            ? settings.business_timezone
            : "",
        );
        setEdited(new Set());
      })
      .catch(
        (err: unknown) =>
          !controller.signal.aborted && setNotice(errorMessage(err)),
      );
    return () => controller.abort();
  }, [path, t, errorMessage]);
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (pending) return;
    setPending(true);
    try {
      await request(path, {
        method: "PATCH",
        body: {
          settings: Object.fromEntries(
            Object.entries({
              locale: locale || null,
              business_timezone: timezone || null,
              theme: primary ? { primary } : null,
              mailbox_default_quota_bytes: quota === "" ? null : Number(quota),
            }).filter(([key]) => edited.has(key)),
          ),
        },
      });
      setEdited(new Set());
      setNotice(t("page.settings.saved"));
    } catch (err: unknown) {
      setNotice(errorMessage(err));
    } finally {
      setPending(false);
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t(
            brandId
              ? "page.settings.brand"
              : platform
                ? "page.settings.platform"
                : "page.settings.tenant",
          )}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={save} className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t("page.settings.inheritance_hint")}
          </p>
          <label className="grid gap-1">
            {t("page.settings.locale")}
            <select
              value={locale}
              onChange={(e) => {
                setLocale(e.target.value);
                changed("locale");
              }}
              className="rounded-md border bg-background p-2"
            >
              <option value="">{t("page.settings.inherit")}</option>
              <option value="zh-CN">中文</option>
              <option value="en">English</option>
            </select>
          </label>
          <label className="grid gap-1">
            {t("page.settings.primary")}
            <Input
              value={primary}
              onChange={(e) => {
                setPrimary(e.target.value);
                changed("theme");
              }}
              placeholder="#2563eb"
              pattern="#[0-9a-fA-F]{6}"
            />
          </label>
          <label className="grid gap-1">
            {t("page.settings.default_quota")}
            <Input
              type="number"
              min="0"
              step="1"
              value={quota}
              onChange={(e) => {
                setQuota(e.target.value);
                changed("mailbox_default_quota_bytes");
              }}
            />
          </label>
          <label className="grid gap-1">
            {t("page.settings.business_timezone")}
            <Input
              value={timezone}
              onChange={(e) => {
                setTimezone(e.target.value);
                changed("business_timezone");
              }}
              placeholder="Asia/Shanghai"
              maxLength={100}
            />
            <span className="text-xs text-muted-foreground">
              {t("page.settings.timezone_help")}
            </span>
          </label>
          <Button type="submit" disabled={pending || edited.size === 0}>
            {t("action.save")}
          </Button>
          {notice && (
            <p role="status" className="text-sm break-words">
              {notice}
            </p>
          )}
        </form>
      </CardContent>
    </Card>
  );
}
