import { BillingCalendarAdmin } from "./billing-calendar-admin";
import { PlatformAIAdmin } from "./platform-ai-admin";
import { CouponReissueAdmin } from "./coupon-reissue-admin";
import { FinancialReviewAdmin } from "./financial-review-admin";
import { NotificationAdmin } from "./notification-admin";
import { DocumentProfile } from "@/features/billing/document-profile";
import { PaymentMerchantAdmin } from "./payment-merchant-admin";
import { SettingsEditor } from "@/features/auth/settings-editor";
import { useCallback, useEffect, useState, FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Building2, Globe, Mail, Users } from "lucide-react";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NoticeDialog } from "@/components/ui/notice-dialog";

interface Overview {
  tenants: number;
  active_tenants: number;
  users: number;
  domains: number;
  mailboxes: number;
  active_subscriptions: number;
}

interface ManagedBrand {
  id: number;
  slug: string;
  name: string;
  status: string;
  logo_url?: string | null;
  favicon_url?: string | null;
  primary_color?: string | null;
}
interface ManagedPlan {
  code: string;
  name: Record<string, string>;
  price_cents: number;
  currency: string;
  quota_bytes: number;
  mailbox_limit: number;
  domain_limit: number;
  daily_send_limit: number;
  active: boolean;
}
const emptyPlan: ManagedPlan = {
  code: "",
  name: { en: "", "zh-CN": "" },
  price_cents: 0,
  currency: "CNY",
  quota_bytes: 1073741824,
  mailbox_limit: 5,
  domain_limit: 1,
  daily_send_limit: 100,
  active: true,
};
interface Tenant {
  id: number;
  name: string;
  slug: string;
  status: string;
  domain_count: number;
  mailbox_count: number;
}

export function PlatformPage() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [overview, setOverview] = useState<Overview | null>(null);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [brands, setBrands] = useState<string[]>([]);
  const [brandItems, setBrandItems] = useState<ManagedBrand[]>([]);
  const [settingsBrand, setSettingsBrand] = useState<{
    id: number;
    name: string;
  } | null>(null);
  const [planItems, setPlanItems] = useState<ManagedPlan[]>([]);
  const [planEdit, setPlanEdit] = useState<ManagedPlan>(emptyPlan);
  const [plans, setPlans] = useState<string[]>([]);
  const [coupons, setCoupons] = useState<string[]>([]);
  const [auditLogs, setAuditLogs] = useState<
    {
      id: number;
      action: string;
      tenant_id?: number | null;
      created_at: string;
    }[]
  >([]);
  const [notice, setNotice] = useState<{
    variant: "error" | "success";
    message: string;
  } | null>(null);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [ownerEmail, setOwnerEmail] = useState("");
  const [ownerPassword, setOwnerPassword] = useState("");
  const [freeUntil, setFreeUntil] = useState("");
  const [freeReason, setFreeReason] = useState("");
  const [brandSlug, setBrandSlug] = useState("default");
  const [brandName, setBrandName] = useState("");
  const [logoURL, setLogoURL] = useState("");
  const [faviconURL, setFaviconURL] = useState("");
  const [brandHosts, setBrandHosts] = useState("");
  const [hostItems, setHostItems] = useState<
    { hostname: string; brand_id: number }[]
  >([]);
  const [primaryColor, setPrimaryColor] = useState("#2563eb");
  const [channelCode, setChannelCode] = useState("");
  const [channelName, setChannelName] = useState("");

  const load = useCallback(() => {
    void Promise.all([
      request<{ overview: Overview }>("/api/v1/platform/overview"),
      request<{ tenants: Tenant[] }>("/api/v1/platform/tenants"),
      request<{ brands: ManagedBrand[] }>("/api/v1/platform/brands"),
      request<{ plans: ManagedPlan[] }>("/api/v1/platform/plans"),
      request<{ coupons: { code: string }[] }>("/api/v1/platform/coupons"),
      request<{
        logs: {
          id: number;
          action: string;
          tenant_id?: number | null;
          created_at: string;
        }[];
      }>("/api/v1/platform/audit?limit=20"),
      request<{ hosts: { hostname: string; brand_id: number }[] }>(
        "/api/v1/platform/brand-hosts",
      ),
    ])
      .then(([o, ts, bs, ps, cs, logs, hs]) => {
        setOverview(o.overview);
        setHostItems(hs.hosts ?? []);
        setTenants(ts.tenants ?? []);
        setBrandItems(bs.brands ?? []);
        setPlanItems(ps.plans ?? []);
        setBrands((bs.brands ?? []).map((b) => b.name));
        setPlans((ps.plans ?? []).map((p) => p.code));
        setCoupons((cs.coupons ?? []).map((c) => c.code));
        setAuditLogs(logs.logs ?? []);
      })
      .catch((err: unknown) =>
        setNotice({ variant: "error", message: errorMessage(err) }),
      );
  }, [errorMessage]);

  useEffect(() => {
    load();
  }, [load]);

  const saveBrand = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await request("/api/v1/platform/brands", {
        method: "POST",
        body: {
          slug: brandSlug,
          name: brandName || brandSlug,
          status:
            brandItems.find((brand) => brand.slug === brandSlug)?.status ??
            "active",
          primary_color: primaryColor,
          logo_url: logoURL || null,
          favicon_url: faviconURL || null,
          theme: { primary: primaryColor },
        },
      });
      load();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const saveChannel = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await request("/api/v1/platform/channels", {
        method: "POST",
        body: {
          code: channelCode,
          name: channelName || channelCode,
          kind: "agent",
          commission_bps: 0,
        },
      });
      setChannelCode("");
      setChannelName("");
      load();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const createTenant = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await request("/api/v1/platform/tenants", {
        method: "POST",
        body: {
          name,
          slug,
          owner_email: ownerEmail,
          owner_password: ownerPassword,
          ...(freeUntil
            ? {
                free_until: new Date(freeUntil).toISOString(),
                free_reason: freeReason.trim(),
              }
            : {}),
        },
      });
      setName("");
      setSlug("");
      setOwnerEmail("");
      setOwnerPassword("");
      setFreeUntil("");
      setFreeReason("");
      load();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const setStatus = async (tenant: Tenant, action: "suspend" | "activate") => {
    try {
      await request("/api/v1/platform/tenants/" + tenant.id + "/" + action, {
        method: "POST",
      });
      load();
    } catch (err: unknown) {
      setNotice({ variant: "error", message: errorMessage(err) });
    }
  };

  const cards = [
    {
      label: t("page.platform.tenants"),
      value: overview?.tenants ?? 0,
      icon: Building2,
    },
    {
      label: t("page.platform.users"),
      value: overview?.users ?? 0,
      icon: Users,
    },
    {
      label: t("page.platform.domains"),
      value: overview?.domains ?? 0,
      icon: Globe,
    },
    {
      label: t("page.platform.mailboxes"),
      value: overview?.mailboxes ?? 0,
      icon: Mail,
    },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          {t("page.platform.title")}
        </h2>
        <p className="text-sm text-muted-foreground">
          {t("page.platform.subtitle")}
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {cards.map((card) => (
          <Card key={card.label}>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle className="text-sm font-medium">
                {card.label}
              </CardTitle>
              <card.icon className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-2xl font-bold">{card.value}</div>
            </CardContent>
          </Card>
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">
              {t("page.platform.brand_editor")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={saveBrand} className="grid gap-2">
              <Input
                placeholder={t("page.platform.brand_slug")}
                aria-label={t("page.platform.brand_slug")}
                value={brandSlug}
                onChange={(e) => setBrandSlug(e.target.value)}
                required
              />
              <Input
                placeholder={t("page.platform.name")}
                value={brandName}
                onChange={(e) => setBrandName(e.target.value)}
              />
              <Input
                type="color"
                value={primaryColor}
                onChange={(e) => setPrimaryColor(e.target.value)}
              />
              <label className="grid gap-1 text-sm">
                {t("page.platform.logo_url")}
                <Input
                  type="url"
                  value={logoURL}
                  onChange={(e) => setLogoURL(e.target.value)}
                />
              </label>
              <label className="grid gap-1 text-sm">
                {t("page.platform.favicon_url")}
                <Input
                  type="url"
                  value={faviconURL}
                  onChange={(e) => setFaviconURL(e.target.value)}
                />
              </label>
              <Button type="submit">{t("page.platform.save_brand")}</Button>
            </form>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">
              {t("page.platform.channel_editor")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={saveChannel} className="grid gap-2">
              <Input
                placeholder="code"
                value={channelCode}
                onChange={(e) => setChannelCode(e.target.value)}
                required
              />
              <Input
                placeholder={t("page.platform.name")}
                value={channelName}
                onChange={(e) => setChannelName(e.target.value)}
              />
              <Button type="submit">{t("page.platform.save_channel")}</Button>
            </form>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.platform.create_tenant")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={createTenant}
            className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5"
          >
            <Input
              placeholder={t("page.platform.name")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
            <Input
              placeholder={t("page.platform.slug")}
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              required
            />
            <Input
              type="email"
              placeholder={t("page.platform.owner_email")}
              value={ownerEmail}
              onChange={(e) => setOwnerEmail(e.target.value)}
              required
            />
            <Input
              type="password"
              placeholder={t("page.platform.owner_password")}
              value={ownerPassword}
              onChange={(e) => setOwnerPassword(e.target.value)}
              minLength={12}
              required
            />
            <label className="grid gap-1 text-sm sm:col-span-2">
              {t("page.platform.free_until")}
              <Input
                type="datetime-local"
                value={freeUntil}
                onChange={(e) => setFreeUntil(e.target.value)}
                required={freeReason.trim() !== ""}
              />
            </label>
            <label className="grid gap-1 text-sm sm:col-span-2">
              {t("page.platform.free_reason")}
              <Input
                value={freeReason}
                onChange={(e) => setFreeReason(e.target.value)}
                required={freeUntil !== ""}
                maxLength={2000}
              />
            </label>
            <p className="text-xs text-muted-foreground sm:col-span-2 lg:col-span-5">
              {t("page.platform.free_help")}
            </p>
            <Button type="submit">{t("page.platform.create")}</Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.platform.tenant_list")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="border rounded-md divide-y">
            {tenants.length === 0 ? (
              <div className="p-6 text-sm text-muted-foreground">
                {t("page.platform.empty")}
              </div>
            ) : (
              tenants.map((tenant) => (
                <div
                  key={tenant.id}
                  className="p-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"
                >
                  <div>
                    <div className="font-medium">{tenant.name}</div>
                    <div className="text-xs text-muted-foreground">
                      {tenant.slug} · {tenant.status} · {tenant.domain_count}{" "}
                      {t("page.platform.domains")} · {tenant.mailbox_count}{" "}
                      {t("page.platform.mailboxes")}
                    </div>
                  </div>
                  <Button
                    size="sm"
                    variant={tenant.status === "active" ? "outline" : "default"}
                    onClick={() =>
                      setStatus(
                        tenant,
                        tenant.status === "active" ? "suspend" : "activate",
                      )
                    }
                  >
                    {tenant.status === "active"
                      ? t("page.platform.suspend")
                      : t("page.platform.activate")}
                  </Button>
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>

      <div className="grid gap-4 lg:grid-cols-3">
        {[
          { title: t("page.platform.brands"), items: brands },
          { title: t("page.platform.plans"), items: plans },
          { title: t("page.platform.coupons"), items: coupons },
        ].map((group) => (
          <Card key={group.title}>
            <CardHeader>
              <CardTitle className="text-base">{group.title}</CardTitle>
            </CardHeader>
            <CardContent className="text-sm text-muted-foreground">
              {group.items.length === 0 ? "-" : group.items.join(", ")}
            </CardContent>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">
            {t("page.platform.audit")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {auditLogs.length === 0 ? (
            <p className="text-sm text-muted-foreground">-</p>
          ) : (
            <div className="border rounded-md divide-y">
              {auditLogs.map((log) => (
                <div
                  key={log.id}
                  className="p-3 flex items-center justify-between gap-3 text-sm"
                >
                  <span>{log.action}</span>
                  <span className="text-xs text-muted-foreground">
                    {log.tenant_id ?? "-"} · {log.created_at}
                  </span>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t("page.platform.manage_brands")}</CardTitle>
        </CardHeader>
        <CardContent className="divide-y">
          {brandItems.map((brand) => (
            <div
              key={brand.id}
              className="flex flex-wrap justify-between gap-2 py-3"
            >
              <span className="break-all">
                {brand.name} ·{" "}
                {t(`status.${brand.status}`, { defaultValue: brand.status })}
              </span>
              <div className="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    setSettingsBrand({ id: brand.id, name: brand.name })
                  }
                >
                  {t("page.settings.brand")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setBrandSlug(brand.slug);
                    setBrandName(brand.name);
                    setLogoURL(brand.logo_url ?? "");
                    setFaviconURL(brand.favicon_url ?? "");
                    setPrimaryColor(brand.primary_color ?? "#2563eb");
                    window.scrollTo({ top: 0 });
                  }}
                >
                  {t("action.edit")}
                </Button>
                {brand.slug !== "default" && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      void request(
                        `/api/v1/platform/brands/${brand.id}/status`,
                        {
                          method: "PATCH",
                          body: {
                            status:
                              brand.status === "active" ? "disabled" : "active",
                          },
                        },
                      )
                        .then(load)
                        .catch((err: unknown) =>
                          setNotice({
                            variant: "error",
                            message: errorMessage(err),
                          }),
                        );
                    }}
                  >
                    {t(
                      brand.status === "active"
                        ? "action.disable"
                        : "action.enable",
                    )}
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    void request(
                      `/api/v1/platform/brands/${brand.id}/reset-theme`,
                      { method: "POST" },
                    )
                      .then(load)
                      .catch((err: unknown) =>
                        setNotice({
                          variant: "error",
                          message: errorMessage(err),
                        }),
                      );
                  }}
                >
                  {t("page.settings.reset_theme")}
                </Button>
                {brand.slug !== "default" && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      void request(`/api/v1/platform/brands/${brand.id}`, {
                        method: "DELETE",
                      })
                        .then(load)
                        .catch((err: unknown) =>
                          setNotice({
                            variant: "error",
                            message: errorMessage(err),
                          }),
                        );
                    }}
                  >
                    {t("action.delete")}
                  </Button>
                )}
              </div>
            </div>
          ))}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t("page.platform.plan_editor")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <form
            className="grid gap-3 sm:grid-cols-2"
            onSubmit={(e) => {
              e.preventDefault();
              void request("/api/v1/platform/plans", {
                method: "POST",
                body: planEdit,
              })
                .then(() => {
                  setPlanEdit(emptyPlan);
                  load();
                })
                .catch((err: unknown) =>
                  setNotice({ variant: "error", message: errorMessage(err) }),
                );
            }}
          >
            <label className="grid gap-1">
              {t("page.platform.plan_code")}
              <Input
                value={planEdit.code}
                onChange={(e) =>
                  setPlanEdit({ ...planEdit, code: e.target.value })
                }
                required
              />
            </label>
            <label className="grid gap-1">
              {t("page.platform.name")}
              <Input
                value={planEdit.name["zh-CN"] ?? planEdit.name.en ?? ""}
                onChange={(e) =>
                  setPlanEdit({
                    ...planEdit,
                    name: { en: e.target.value, "zh-CN": e.target.value },
                  })
                }
                required
              />
            </label>
            {(
              [
                "price_cents",
                "quota_bytes",
                "mailbox_limit",
                "domain_limit",
                "daily_send_limit",
              ] as const
            ).map((field) => (
              <label key={field} className="grid gap-1">
                {t(`page.platform.${field}`)}
                <Input
                  type="number"
                  min={field === "price_cents" ? 0 : -1}
                  step="1"
                  value={planEdit[field]}
                  onChange={(e) =>
                    setPlanEdit({
                      ...planEdit,
                      [field]: Number(e.target.value),
                    })
                  }
                  required
                />
              </label>
            ))}
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={planEdit.active}
                onChange={(e) =>
                  setPlanEdit({ ...planEdit, active: e.target.checked })
                }
              />
              {t("state.active")}
            </label>
            <Button type="submit">{t("action.save")}</Button>
          </form>
          {planItems.map((plan) => (
            <div
              key={plan.code}
              className="flex flex-wrap items-center justify-between gap-2 border-t py-3"
            >
              <span>{plan.code}</span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setPlanEdit(plan)}
                >
                  {t("action.edit")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={plan.code === "free"}
                  onClick={() => {
                    void request(
                      `/api/v1/platform/plans/${encodeURIComponent(plan.code)}`,
                      { method: "DELETE" },
                    )
                      .then(load)
                      .catch((err: unknown) =>
                        setNotice({
                          variant: "error",
                          message: errorMessage(err),
                        }),
                      );
                  }}
                >
                  {t("action.delete")}
                </Button>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>
      {settingsBrand && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="font-medium break-all">{settingsBrand.name}</p>
            <Button variant="outline" onClick={() => setSettingsBrand(null)}>
              {t("action.close")}
            </Button>
          </div>
          <SettingsEditor
            key={settingsBrand.id}
            platform
            brandId={settingsBrand.id}
          />
        </div>
      )}
      <SettingsEditor platform />
      <Card>
        <CardHeader>
          <CardTitle>{t("page.platform.brand_hosts")}</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            className="flex flex-wrap gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void request("/api/v1/platform/brand-hosts", {
                method: "POST",
                body: {
                  brand_id: brandItems.find((b) => b.slug === brandSlug)?.id,
                  hostname: brandHosts,
                },
              })
                .then(() => {
                  setNotice({
                    variant: "success",
                    message: t("page.settings.saved"),
                  });
                  load();
                })
                .catch((err: unknown) =>
                  setNotice({ variant: "error", message: errorMessage(err) }),
                );
            }}
          >
            <Input
              aria-label={t("page.platform.brand_slug")}
              value={brandSlug}
              onChange={(e) => setBrandSlug(e.target.value)}
              required
            />
            <Input
              aria-label={t("page.platform.hostname")}
              value={brandHosts}
              onChange={(e) => setBrandHosts(e.target.value)}
              placeholder="mail.example.com"
              required
            />
            <Button type="submit">{t("action.save")}</Button>
          </form>
          <div className="mt-3 divide-y">
            {hostItems.map((host) => (
              <div
                key={host.hostname}
                className="flex flex-wrap items-center justify-between gap-2 py-2"
              >
                <span className="break-all">{host.hostname}</span>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    void request(
                      `/api/v1/platform/brand-hosts/${encodeURIComponent(host.hostname)}`,
                      { method: "DELETE" },
                    )
                      .then(load)
                      .catch((err: unknown) =>
                        setNotice({
                          variant: "error",
                          message: errorMessage(err),
                        }),
                      );
                  }}
                >
                  {t("page.platform.unbind")}
                </Button>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
      <PaymentMerchantAdmin />
      <FinancialReviewAdmin />
      <CouponReissueAdmin />
      <PlatformAIAdmin tenants={tenants} />
      <BillingCalendarAdmin tenants={tenants} />
      <NotificationAdmin />
      <DocumentProfile platform />
      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice?.message ?? ""}
        variant={notice?.variant ?? "error"}
      />
    </div>
  );
}
