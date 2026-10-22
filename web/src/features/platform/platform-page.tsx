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
  const [plans, setPlans] = useState<string[]>([]);
  const [coupons, setCoupons] = useState<string[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [ownerEmail, setOwnerEmail] = useState("");
  const [ownerPassword, setOwnerPassword] = useState("");

  const load = useCallback(() => {
    void Promise.all([
      request<{ overview: Overview }>("/api/v1/platform/overview"),
      request<{ tenants: Tenant[] }>("/api/v1/platform/tenants"),
      request<{ brands: { name: string }[] }>("/api/v1/platform/brands"),
      request<{ plans: { code: string }[] }>("/api/v1/platform/plans"),
      request<{ coupons: { code: string }[] }>("/api/v1/platform/coupons"),
    ])
      .then(([o, ts, bs, ps, cs]) => {
        setOverview(o.overview);
        setTenants(ts.tenants ?? []);
        setBrands((bs.brands ?? []).map((b) => b.name));
        setPlans((ps.plans ?? []).map((p) => p.code));
        setCoupons((cs.coupons ?? []).map((c) => c.code));
      })
      .catch((err: unknown) => setNotice(errorMessage(err)));
  }, [errorMessage]);

  useEffect(() => {
    load();
  }, [load]);

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
        },
      });
      setName("");
      setSlug("");
      setOwnerEmail("");
      setOwnerPassword("");
      load();
    } catch (err: unknown) {
      setNotice(errorMessage(err));
    }
  };

  const setStatus = async (tenant: Tenant, action: "suspend" | "activate") => {
    try {
      await request("/api/v1/platform/tenants/" + tenant.id + "/" + action, {
        method: "POST",
      });
      load();
    } catch (err: unknown) {
      setNotice(errorMessage(err));
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

      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice ?? ""}
        variant="error"
      />
    </div>
  );
}
