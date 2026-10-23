import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Globe, Mail, Shield, CheckCircle2, ArrowUpRight } from "lucide-react";
import { request } from "@/lib/api/client";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";

interface TenantCurrentData {
  tenant: {
    id: number;
    name: string;
    slug: string;
    status: string;
    created_at: string;
  };
  settings: Record<string, unknown>;
  usage: {
    domains_used: number;
    domains_limit: number;
    mailboxes_used: number;
    mailboxes_limit: number;
    storage_bytes_limit: number;
    storage_bytes_used?: number | null;
    storage_bytes_allocated?: number;
    storage_measured_at?: string | null;
    storage_error?: string | null;
  };
  subscription: {
    plan_code: string;
    plan_name: Record<string, string>;
    status: string;
  };
}

export function DashboardPage() {
  const { t } = useTranslation();
  const [data, setData] = useState<TenantCurrentData | null>(null);

  useEffect(() => {
    request<TenantCurrentData>("/api/v1/tenants/current")
      .then((res) => setData(res))
      .catch(() => {});
  }, []);

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">
          {t("page.dashboard.title")}
        </h2>
        <p className="text-sm text-muted-foreground">
          {t("page.dashboard.subtitle")}
        </p>
      </div>

      {/* Metrics Cards */}
      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">
              {t("page.dashboard.domains")}
            </CardTitle>
            <Globe className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {data?.usage?.domains_used ?? "—"}
              <span className="text-sm font-normal text-muted-foreground ml-1">
                /{" "}
                {data
                  ? data.usage.domains_limit === -1
                    ? "∞"
                    : data.usage.domains_limit
                  : "—"}
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1 flex items-center gap-1">
              <CheckCircle2 className="h-3 w-3 text-emerald-500" />
              {t("page.dashboard.domains_ok")}
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">
              {t("page.dashboard.mailboxes")}
            </CardTitle>
            <Mail className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {data?.usage?.mailboxes_used ?? "—"}
              <span className="text-sm font-normal text-muted-foreground ml-1">
                /{" "}
                {data
                  ? data.usage.mailboxes_limit === -1
                    ? "∞"
                    : data.usage.mailboxes_limit
                  : "—"}
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              {t("page.dashboard.mailboxes_ok")}
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">
              {t("page.dashboard.plan")}
            </CardTitle>
            <Shield className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold uppercase">
              {data?.subscription?.plan_code || "—"}
            </div>
            <p className="text-xs text-emerald-600 font-medium mt-1">
              {t("page.dashboard.status")}:{" "}
              {t(`status.${data?.subscription?.status}`, { defaultValue: "—" })}
            </p>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("page.usage.storage")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <p>
            {t("page.usage.used")}:{" "}
            {data?.usage.storage_bytes_used == null
              ? t("page.usage.unknown")
              : data.usage.storage_bytes_used.toLocaleString()}{" "}
            {t("page.usage.bytes")}
          </p>
          <p>
            {t("page.usage.allocated")}:{" "}
            {data?.usage.storage_bytes_allocated?.toLocaleString() ??
              t("page.usage.unknown")}{" "}
            /{" "}
            {data?.usage.storage_bytes_limit === -1
              ? "∞"
              : (data?.usage.storage_bytes_limit?.toLocaleString() ?? "—")}{" "}
            {t("page.usage.bytes")}
          </p>
          <p className="text-sm text-muted-foreground">
            {t("page.usage.measured")}:{" "}
            {data?.usage.storage_measured_at ?? t("page.usage.unknown")}
          </p>
          {data?.usage.storage_error && (
            <p role="alert">{t("page.usage.unavailable")}</p>
          )}
        </CardContent>
      </Card>
      {/* Quick Actions */}
      <Card>
        <CardHeader>
          <CardTitle>{t("page.dashboard.quick_actions")}</CardTitle>
          <CardDescription>
            {t("page.dashboard.quick_actions_desc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-3">
          <Button asChild>
            <Link to="/domains">
              <Globe className="h-4 w-4 mr-2" />
              {t("page.dashboard.add_domain")}
              <ArrowUpRight className="h-4 w-4 ml-1" />
            </Link>
          </Button>
          <Button asChild variant="outline">
            <Link to="/mailboxes">
              <Mail className="h-4 w-4 mr-2" />
              {t("page.dashboard.create_mailbox")}
              <ArrowUpRight className="h-4 w-4 ml-1" />
            </Link>
          </Button>
          <Button asChild variant="secondary">
            <Link to="/aliases">
              {t("page.dashboard.aliases")}
              <ArrowUpRight className="h-4 w-4 ml-1" />
            </Link>
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
