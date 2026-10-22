import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Globe, Mail, Shield, CheckCircle2, ArrowUpRight } from "lucide-react";
import { request } from "@/lib/api/client";
import { ThemeInjector, BrandTheme } from "@/components/brand/theme-injector";
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
  };
  subscription: {
    plan_code: string;
    plan_name: Record<string, string>;
    status: string;
  };
}

export function DashboardPage() {
  const [data, setData] = useState<TenantCurrentData | null>(null);

  useEffect(() => {
    request<TenantCurrentData>("/api/v1/tenants/current")
      .then((res) => setData(res))
      .catch(() => {});
  }, []);

  const theme = data?.settings?.theme as BrandTheme | undefined;

  return (
    <div className="space-y-6">
      <ThemeInjector theme={theme} />

      <div>
        <h2 className="text-2xl font-bold tracking-tight">总览仪表盘</h2>
        <p className="text-sm text-muted-foreground">
          查看租户资源用量、套餐状态与快捷入口
        </p>
      </div>

      {/* Metrics Cards */}
      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">托管域名</CardTitle>
            <Globe className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {data?.usage?.domains_used ?? 0}
              <span className="text-sm font-normal text-muted-foreground ml-1">
                / {data?.usage?.domains_limit ? data.usage.domains_limit : "∞"}
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1 flex items-center gap-1">
              <CheckCircle2 className="h-3 w-3 text-emerald-500" />
              解析状态正常
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">邮箱账号</CardTitle>
            <Mail className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold">
              {data?.usage?.mailboxes_used ?? 0}
              <span className="text-sm font-normal text-muted-foreground ml-1">
                /{" "}
                {data?.usage?.mailboxes_limit
                  ? data.usage.mailboxes_limit
                  : "∞"}
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              Dovecot PG 认证就绪
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">当前套餐</CardTitle>
            <Shield className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold uppercase">
              {data?.subscription?.plan_code || "STANDARD"}
            </div>
            <p className="text-xs text-emerald-600 font-medium mt-1">
              状态: {data?.subscription?.status || "active"}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Quick Actions */}
      <Card>
        <CardHeader>
          <CardTitle>快速操作</CardTitle>
          <CardDescription>
            添加新域名并引导配置 DNS 解析，或为租户开通新邮箱账号
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-3">
          <Button asChild>
            <Link to="/domains">
              <Globe className="h-4 w-4 mr-2" />
              添加域名
              <ArrowUpRight className="h-4 w-4 ml-1" />
            </Link>
          </Button>
          <Button asChild variant="outline">
            <Link to="/mailboxes">
              <Mail className="h-4 w-4 mr-2" />
              开通邮箱
              <ArrowUpRight className="h-4 w-4 ml-1" />
            </Link>
          </Button>
          <Button asChild variant="secondary">
            <Link to="/aliases">
              别名与转发规则
              <ArrowUpRight className="h-4 w-4 ml-1" />
            </Link>
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
