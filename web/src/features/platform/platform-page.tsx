import { BrandHostLifecycle } from "./brand-host-lifecycle";
import { validNewPassword } from "@/lib/password-policy";
import { BillingCalendarAdmin } from "./billing-calendar-admin";
import { PlatformAIAdmin } from "./platform-ai-admin";
import { BackupOffsiteAdmin } from "./backup-offsite-admin";
import { CouponReissueAdmin } from "./coupon-reissue-admin";
import { FinancialReviewAdmin } from "./financial-review-admin";
import { NotificationAdmin } from "./notification-admin";
import { DocumentProfile } from "@/features/billing/document-profile";
import { PaymentMerchantAdmin } from "./payment-merchant-admin";
import { SettingsEditor } from "@/features/auth/settings-editor";
import { useCallback, useEffect, useState, useMemo, FormEvent } from "react";
import { useTranslation } from "react-i18next";
import {
  Building2,
  Globe,
  Mail,
  Users,
  Layers,
  Receipt,
  Cpu,
  DatabaseBackup,
  Headphones,
  Search,
  Plus,
  ChevronLeft,
  ChevronRight,
  Eye,
  CreditCard,
  Sparkles,
  ArrowRight,
  RefreshCw,
  SlidersHorizontal,
} from "lucide-react";
import { request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { useAssumedTenant } from "@/lib/assumed-tenant";
import { useAuth } from "@/app/auth-context";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NoticeDialog } from "@/components/ui/notice-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

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

export function PlatformPage({ defaultTab }: { defaultTab?: string } = {}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const { assumeTenant } = useAssumedTenant();
  const auth = useAuth();

  const getInitialTab = (): string => {
    if (defaultTab) return defaultTab;
    if (typeof window !== "undefined" && window.location) {
      try {
        const tab = new URLSearchParams(window.location.search).get("tab");
        if (
          tab &&
          [
            "overview",
            "tenants",
            "plans",
            "finance",
            "brands",
            "settings",
            "backup",
          ].includes(tab)
        ) {
          return tab;
        }
      } catch {
        // ignore
      }
    }
    return "overview";
  };

  const [activeTab, setActiveTab] = useState<string>(getInitialTab);

  useEffect(() => {
    const onPopState = () => {
      if (typeof window !== "undefined" && window.location) {
        try {
          const tab = new URLSearchParams(window.location.search).get("tab");
          if (
            tab &&
            [
              "overview",
              "tenants",
              "plans",
              "finance",
              "brands",
              "settings",
              "backup",
            ].includes(tab)
          ) {
            setActiveTab(tab);
          }
        } catch {
          // ignore
        }
      }
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const handleTabChange = (tabId: string) => {
    setActiveTab(tabId);
    if (typeof window !== "undefined" && window.history && window.location) {
      try {
        const url = new URL(window.location.href);
        url.searchParams.set("tab", tabId);
        window.history.pushState(null, "", url.toString());
      } catch {
        // ignore in mock environments
      }
    }
  };

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

  // Tenant creation state & dialog
  const [createTenantOpen, setCreateTenantOpen] = useState(false);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [ownerEmail, setOwnerEmail] = useState("");
  const [ownerPassword, setOwnerPassword] = useState("");
  const [freeUntil, setFreeUntil] = useState("");
  const [freeReason, setFreeReason] = useState("");

  // Tenant search, filter & pagination state
  const [searchQuery, setSearchQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<
    "all" | "active" | "suspended"
  >("all");
  const [tenantPage, setTenantPage] = useState(1);
  const pageSize = 10;

  // Brand and channel state
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
    if (!validNewPassword(ownerPassword)) {
      setNotice({
        variant: "error",
        message: t("error.PASSWORD_POLICY_INVALID"),
      });
      return;
    }
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
      setCreateTenantOpen(false);
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

  // Assume Tenant handler
  const handleAssume = async (tenant: Tenant) => {
    assumeTenant({ id: tenant.id, name: tenant.name, slug: tenant.slug });
    const userTenants = auth?.tenants ?? [];
    const match = userTenants.find((t) => t.id === tenant.id);
    if (match && auth?.switchTenant) {
      await auth.switchTenant(match);
    } else if (typeof window !== "undefined") {
      window.location.href = "/console";
    }
  };

  // Filtered & paginated tenants
  const filteredTenants = useMemo(() => {
    return tenants.filter((tenant) => {
      const matchesStatus =
        statusFilter === "all" || tenant.status === statusFilter;
      const q = searchQuery.toLowerCase().trim();
      const matchesQuery =
        !q ||
        tenant.name.toLowerCase().includes(q) ||
        tenant.slug.toLowerCase().includes(q);
      return matchesStatus && matchesQuery;
    });
  }, [tenants, statusFilter, searchQuery]);

  const totalPages = Math.ceil(filteredTenants.length / pageSize) || 1;
  const pagedTenants = useMemo(() => {
    const start = (tenantPage - 1) * pageSize;
    return filteredTenants.slice(start, start + pageSize);
  }, [filteredTenants, tenantPage, pageSize]);

  const cards = [
    {
      label: t("page.platform.tenants"),
      value: overview?.tenants ?? 0,
      icon: Building2,
      desc:
        t("page.platform.active_tenants", { defaultValue: "活跃租户" }) +
        ": " +
        (overview?.active_tenants ?? 0),
    },
    {
      label: t("page.platform.users"),
      value: overview?.users ?? 0,
      icon: Users,
      desc: "全平台注册用户",
    },
    {
      label: t("page.platform.domains"),
      value: overview?.domains ?? 0,
      icon: Globe,
      desc: "托管企业域名",
    },
    {
      label: t("page.platform.mailboxes"),
      value: overview?.mailboxes ?? 0,
      icon: Mail,
      desc: "已分配工作邮箱",
    },
    {
      label: t("page.platform.active_subscriptions", {
        defaultValue: "有效订阅",
      }),
      value: overview?.active_subscriptions ?? 0,
      icon: CreditCard,
      desc: "进行中付费履约计划",
    },
    {
      label: t("page.platform.brands"),
      value: brandItems.length,
      icon: Sparkles,
      desc: "已配置多品牌矩阵",
    },
  ];

  const tabs = [
    {
      id: "overview",
      label: t("page.platform.tab_overview", { defaultValue: "平台概览" }),
      icon: Building2,
    },
    {
      id: "tenants",
      label: t("page.platform.tab_tenants", { defaultValue: "租户管理" }),
      icon: Users,
    },
    {
      id: "plans",
      label: t("page.platform.tab_plans", { defaultValue: "套餐计划" }),
      icon: Layers,
    },
    {
      id: "finance",
      label: t("page.platform.tab_finance", { defaultValue: "商业化与财务" }),
      icon: Receipt,
    },
    {
      id: "brands",
      label: t("page.platform.tab_brands", { defaultValue: "品牌与主机" }),
      icon: Globe,
    },
    {
      id: "settings",
      label: t("page.platform.tab_settings", { defaultValue: "AI与系统规则" }),
      icon: Cpu,
    },
    {
      id: "backup",
      label: t("page.platform.tab_backup", { defaultValue: "异地备份" }),
      icon: DatabaseBackup,
    },
  ];

  return (
    <div className="space-y-6">
      {/* Platform Page Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">
            {t("page.platform.title")}
          </h2>
          <p className="text-sm text-muted-foreground">
            {t("page.platform.subtitle")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={load}
            className="h-8 text-xs gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>{t("action.refresh", { defaultValue: "刷新数据" })}</span>
          </Button>
        </div>
      </div>

      {/* Tabs Navigation Bar */}
      <div className="border-b">
        <nav
          className="flex flex-wrap gap-2 -mb-px pb-1"
          aria-label="Platform Tabs"
        >
          {tabs.map((tab) => {
            const Icon = tab.icon;
            const isActive = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                type="button"
                onClick={() => handleTabChange(tab.id)}
                className={cn(
                  "flex items-center gap-2 px-3.5 py-2 text-sm font-medium rounded-t-lg border-b-2 transition-all cursor-pointer",
                  isActive
                    ? "border-primary text-primary bg-background shadow-xs font-semibold"
                    : "border-transparent text-muted-foreground hover:text-foreground hover:border-muted-foreground/30",
                )}
              >
                <Icon className="h-4 w-4" />
                <span>{tab.label}</span>
              </button>
            );
          })}
        </nav>
      </div>

      {/* =========================================================================
          Tab 1: 平台概览 (Overview)
          ========================================================================= */}
      <div className={activeTab === "overview" ? "space-y-6" : "hidden"}>
        {/* Core Metric Cards */}
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {cards.map((card) => (
            <Card
              key={card.label}
              className="hover:shadow-xs transition-shadow"
            >
              <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
                <CardTitle className="text-sm font-medium">
                  {card.label}
                </CardTitle>
                <div className="h-8 w-8 rounded-lg bg-primary/10 text-primary flex items-center justify-center">
                  <card.icon className="h-4 w-4" />
                </div>
              </CardHeader>
              <CardContent>
                <div className="text-2xl font-bold">{card.value}</div>
                <p className="text-xs text-muted-foreground mt-1">
                  {card.desc}
                </p>
              </CardContent>
            </Card>
          ))}
        </div>

        {/* Quick Actions Grid */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base font-semibold flex items-center gap-2">
              <SlidersHorizontal className="h-4 w-4 text-primary" />
              <span>
                {t("page.platform.quick_actions", { defaultValue: "快捷入口" })}
              </span>
            </CardTitle>
            <CardDescription>
              平台高频运维操作与系统级配置快捷通道
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              <button
                type="button"
                onClick={() => handleTabChange("tenants")}
                className="flex items-center justify-between p-3.5 rounded-lg border bg-card hover:bg-muted/50 transition-colors text-left group"
              >
                <div className="flex items-center gap-3">
                  <div className="h-8 w-8 rounded-md bg-blue-500/10 text-blue-600 dark:text-blue-400 flex items-center justify-center">
                    <Users className="h-4 w-4" />
                  </div>
                  <div>
                    <div className="text-sm font-medium group-hover:text-primary transition-colors">
                      {t("page.platform.quick_tenants", {
                        defaultValue: "租户全生命周期",
                      })}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      租户管理与代入视察
                    </div>
                  </div>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:translate-x-0.5 transition-transform" />
              </button>

              <button
                type="button"
                onClick={() => handleTabChange("plans")}
                className="flex items-center justify-between p-3.5 rounded-lg border bg-card hover:bg-muted/50 transition-colors text-left group"
              >
                <div className="flex items-center gap-3">
                  <div className="h-8 w-8 rounded-md bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 flex items-center justify-center">
                    <Layers className="h-4 w-4" />
                  </div>
                  <div>
                    <div className="text-sm font-medium group-hover:text-primary transition-colors">
                      {t("page.platform.quick_plans", {
                        defaultValue: "套餐计划定价",
                      })}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      编辑套餐配额与促销
                    </div>
                  </div>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:translate-x-0.5 transition-transform" />
              </button>

              <button
                type="button"
                onClick={() => handleTabChange("finance")}
                className="flex items-center justify-between p-3.5 rounded-lg border bg-card hover:bg-muted/50 transition-colors text-left group"
              >
                <div className="flex items-center gap-3">
                  <div className="h-8 w-8 rounded-md bg-amber-500/10 text-amber-600 dark:text-amber-400 flex items-center justify-center">
                    <Receipt className="h-4 w-4" />
                  </div>
                  <div>
                    <div className="text-sm font-medium group-hover:text-primary transition-colors">
                      {t("page.platform.quick_finance", {
                        defaultValue: "财务与结算",
                      })}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      支付通道与账期日历
                    </div>
                  </div>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:translate-x-0.5 transition-transform" />
              </button>

              <button
                type="button"
                onClick={() => handleTabChange("brands")}
                className="flex items-center justify-between p-3.5 rounded-lg border bg-card hover:bg-muted/50 transition-colors text-left group"
              >
                <div className="flex items-center gap-3">
                  <div className="h-8 w-8 rounded-md bg-indigo-500/10 text-indigo-600 dark:text-indigo-400 flex items-center justify-center">
                    <Globe className="h-4 w-4" />
                  </div>
                  <div>
                    <div className="text-sm font-medium group-hover:text-primary transition-colors">
                      {t("page.platform.quick_brands", {
                        defaultValue: "品牌与主机",
                      })}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      品牌矩阵与域名生命周期
                    </div>
                  </div>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:translate-x-0.5 transition-transform" />
              </button>

              <button
                type="button"
                onClick={() => handleTabChange("settings")}
                className="flex items-center justify-between p-3.5 rounded-lg border bg-card hover:bg-muted/50 transition-colors text-left group"
              >
                <div className="flex items-center gap-3">
                  <div className="h-8 w-8 rounded-md bg-purple-500/10 text-purple-600 dark:text-purple-400 flex items-center justify-center">
                    <Cpu className="h-4 w-4" />
                  </div>
                  <div>
                    <div className="text-sm font-medium group-hover:text-primary transition-colors">
                      {t("page.platform.quick_ai", {
                        defaultValue: "AI模型与规则",
                      })}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      智能额度与系统全局配置
                    </div>
                  </div>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:translate-x-0.5 transition-transform" />
              </button>

              <a
                href="/platform/support"
                className="flex items-center justify-between p-3.5 rounded-lg border bg-card hover:bg-muted/50 transition-colors text-left group"
              >
                <div className="flex items-center gap-3">
                  <div className="h-8 w-8 rounded-md bg-rose-500/10 text-rose-600 dark:text-rose-400 flex items-center justify-center">
                    <Headphones className="h-4 w-4" />
                  </div>
                  <div>
                    <div className="text-sm font-medium group-hover:text-primary transition-colors">
                      {t("page.platform.quick_support", {
                        defaultValue: "平台工单响应",
                      })}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      跨租户技术支持与诊断
                    </div>
                  </div>
                </div>
                <ArrowRight className="h-4 w-4 text-muted-foreground group-hover:translate-x-0.5 transition-transform" />
              </a>
            </div>
          </CardContent>
        </Card>

        {/* Recent Tenants Growth & Audit logs */}
        <div className="grid gap-6 lg:grid-cols-2">
          {/* Recent Tenants */}
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <div>
                <CardTitle className="text-base font-semibold">
                  {t("page.platform.recent_tenants", {
                    defaultValue: "近期新增租户",
                  })}
                </CardTitle>
                <CardDescription>
                  最新入驻平台的租户与其实时规模
                </CardDescription>
              </div>
              <Button
                variant="outline"
                size="sm"
                onClick={() => handleTabChange("tenants")}
                className="text-xs h-7"
              >
                {t("page.platform.view_all_tenants", {
                  defaultValue: "查看全部",
                })}
              </Button>
            </CardHeader>
            <CardContent>
              {tenants.length === 0 ? (
                <p className="text-sm text-muted-foreground py-4 text-center">
                  {t("page.platform.empty")}
                </p>
              ) : (
                <div className="divide-y border rounded-lg overflow-hidden">
                  {tenants.slice(0, 5).map((tenant) => (
                    <div
                      key={tenant.id}
                      className="p-3 flex items-center justify-between gap-3 text-sm hover:bg-muted/30 transition-colors"
                    >
                      <div className="min-w-0">
                        <div className="font-medium truncate flex items-center gap-1.5">
                          <span>{tenant.name}</span>
                          <span className="text-[11px] font-mono text-muted-foreground">
                            ({tenant.slug})
                          </span>
                        </div>
                        <div className="text-xs text-muted-foreground">
                          {tenant.domain_count} {t("page.platform.domains")} ·{" "}
                          {tenant.mailbox_count} {t("page.platform.mailboxes")}
                        </div>
                      </div>
                      <div className="flex items-center gap-2 shrink-0">
                        <span
                          className={cn(
                            "px-2 py-0.5 rounded text-[11px] font-medium",
                            tenant.status === "active"
                              ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                              : "bg-amber-500/10 text-amber-600 dark:text-amber-400",
                          )}
                        >
                          {tenant.status === "active"
                            ? t("page.platform.status_active", {
                                defaultValue: "正常",
                              })
                            : t("page.platform.status_suspended", {
                                defaultValue: "已冻结",
                              })}
                        </span>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 px-2 text-xs text-primary"
                          onClick={() => handleAssume(tenant)}
                        >
                          <Eye className="h-3.5 w-3.5 mr-1" />
                          <span>
                            {t("page.platform.assume_tenant", {
                              defaultValue: "视察",
                            })}
                          </span>
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>

          {/* Audit Logs */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base font-semibold">
                {t("page.platform.recent_audit", {
                  defaultValue: "最新审计动态",
                })}
              </CardTitle>
              <CardDescription>平台级特权与租户状态流转日志</CardDescription>
            </CardHeader>
            <CardContent>
              {auditLogs.length === 0 ? (
                <p className="text-sm text-muted-foreground py-4 text-center">
                  -
                </p>
              ) : (
                <div className="border rounded-lg divide-y max-h-72 overflow-y-auto">
                  {auditLogs.slice(0, 8).map((log) => (
                    <div
                      key={log.id}
                      className="p-2.5 flex items-center justify-between gap-2 text-xs"
                    >
                      <span className="font-mono text-foreground font-medium truncate">
                        {log.action}
                      </span>
                      <span className="text-muted-foreground shrink-0">
                        {log.tenant_id ? `T#${log.tenant_id}` : "平台"} ·{" "}
                        {log.created_at}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        {/* Resources Badges Summary */}
        <div className="grid gap-4 lg:grid-cols-3">
          {[
            { title: t("page.platform.brands"), items: brands },
            { title: t("page.platform.plans"), items: plans },
            { title: t("page.platform.coupons"), items: coupons },
          ].map((group) => (
            <Card key={group.title}>
              <CardHeader className="pb-2">
                <CardTitle className="text-sm font-semibold">
                  {group.title}
                </CardTitle>
              </CardHeader>
              <CardContent className="text-xs text-muted-foreground">
                {group.items.length === 0 ? "-" : group.items.join(", ")}
              </CardContent>
            </Card>
          ))}
        </div>
      </div>

      {/* =========================================================================
          Tab 2: 租户管理 (Tenants)
          ========================================================================= */}
      <div className={activeTab === "tenants" ? "space-y-6" : "hidden"}>
        {/* Header Action Bar */}
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div>
            <h3 className="text-lg font-semibold tracking-tight">
              {t("page.platform.tenant_list")}
            </h3>
            <p className="text-xs text-muted-foreground">
              {t("page.platform.total_tenants", {
                total: tenants.length,
                defaultValue: `共 ${tenants.length} 个租户`,
              })}
            </p>
          </div>

          {/* Create Tenant Dialog */}
          <Dialog open={createTenantOpen} onOpenChange={setCreateTenantOpen}>
            <DialogTrigger asChild>
              <Button size="sm" className="gap-1.5 h-9">
                <Plus className="h-4 w-4" />
                <span>{t("page.platform.create_tenant")}</span>
              </Button>
            </DialogTrigger>
            <DialogContent className="max-w-xl">
              <DialogHeader>
                <DialogTitle>{t("page.platform.create_tenant")}</DialogTitle>
                <DialogDescription>
                  为新企业客户开通独立隔离的多租户组织空间与初始管理员账号。
                </DialogDescription>
              </DialogHeader>

              <form onSubmit={createTenant} className="space-y-3 pt-2">
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-xs font-medium">
                    {t("page.platform.name")} *
                    <Input
                      placeholder={t("page.platform.name")}
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      required
                    />
                  </label>
                  <label className="grid gap-1 text-xs font-medium">
                    {t("page.platform.slug")} *
                    <Input
                      placeholder={t("page.platform.slug")}
                      value={slug}
                      onChange={(e) => setSlug(e.target.value)}
                      required
                    />
                  </label>
                </div>

                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-xs font-medium">
                    {t("page.platform.owner_email")} *
                    <Input
                      type="email"
                      placeholder={t("page.platform.owner_email")}
                      value={ownerEmail}
                      onChange={(e) => setOwnerEmail(e.target.value)}
                      required
                    />
                  </label>
                  <label className="grid gap-1 text-xs font-medium">
                    {t("page.platform.owner_password")} *
                    <Input
                      type="password"
                      placeholder={t("page.platform.owner_password")}
                      value={ownerPassword}
                      onChange={(e) => setOwnerPassword(e.target.value)}
                      minLength={12}
                      maxLength={256}
                      required
                    />
                  </label>
                </div>

                <div className="grid gap-3 sm:grid-cols-2 border-t pt-3">
                  <label className="grid gap-1 text-xs font-medium">
                    {t("page.platform.free_until")}
                    <Input
                      type="datetime-local"
                      value={freeUntil}
                      onChange={(e) => setFreeUntil(e.target.value)}
                      required={freeReason.trim() !== ""}
                    />
                  </label>
                  <label className="grid gap-1 text-xs font-medium">
                    {t("page.platform.free_reason")}
                    <Input
                      value={freeReason}
                      onChange={(e) => setFreeReason(e.target.value)}
                      required={freeUntil !== ""}
                      maxLength={2000}
                    />
                  </label>
                </div>

                <p className="text-[11px] text-muted-foreground">
                  {t("page.platform.free_help")}
                </p>

                <div className="flex justify-end gap-2 pt-3 border-t">
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => setCreateTenantOpen(false)}
                  >
                    {t("action.cancel", { defaultValue: "取消" })}
                  </Button>
                  <Button type="submit">{t("page.platform.create")}</Button>
                </div>
              </form>
            </DialogContent>
          </Dialog>
        </div>

        {/* Filter and Search Bar */}
        <Card>
          <CardContent className="p-3">
            <div className="flex flex-col sm:flex-row gap-2.5 items-center justify-between">
              <div className="relative w-full sm:w-80">
                <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                <Input
                  className="pl-9 h-9 text-xs"
                  placeholder={t("page.platform.search_tenants_placeholder", {
                    defaultValue: "搜索租户名称或标识...",
                  })}
                  value={searchQuery}
                  onChange={(e) => {
                    setSearchQuery(e.target.value);
                    setTenantPage(1);
                  }}
                />
              </div>

              <div className="flex items-center gap-2 w-full sm:w-auto">
                <select
                  aria-label={t("page.platform.filter_status", {
                    defaultValue: "状态筛选",
                  })}
                  className="h-9 rounded-md border border-input bg-background px-3 py-1 text-xs font-medium text-foreground shadow-xs focus:ring-1 focus:ring-ring w-full sm:w-36"
                  value={statusFilter}
                  onChange={(e) => {
                    setStatusFilter(
                      e.target.value as "all" | "active" | "suspended",
                    );
                    setTenantPage(1);
                  }}
                >
                  <option value="all">
                    {t("page.platform.filter_all", {
                      defaultValue: "全部状态",
                    })}
                  </option>
                  <option value="active">
                    {t("page.platform.status_active", {
                      defaultValue: "正常运行",
                    })}
                  </option>
                  <option value="suspended">
                    {t("page.platform.status_suspended", {
                      defaultValue: "已冻结",
                    })}
                  </option>
                </select>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Tenant Data Table */}
        <Card>
          <CardContent className="p-0">
            <div className="overflow-x-auto">
              <table className="w-full text-sm text-left">
                <thead className="bg-muted/50 text-xs text-muted-foreground border-b">
                  <tr>
                    <th className="py-3 px-4 font-medium">ID</th>
                    <th className="py-3 px-4 font-medium">
                      {t("page.platform.tenant_table_tenant", {
                        defaultValue: "租户名称 / 标识",
                      })}
                    </th>
                    <th className="py-3 px-4 font-medium">
                      {t("page.platform.tenant_table_status", {
                        defaultValue: "状态",
                      })}
                    </th>
                    <th className="py-3 px-4 font-medium">
                      {t("page.platform.tenant_table_domains", {
                        defaultValue: "域名数",
                      })}
                    </th>
                    <th className="py-3 px-4 font-medium">
                      {t("page.platform.tenant_table_mailboxes", {
                        defaultValue: "邮箱数",
                      })}
                    </th>
                    <th className="py-3 px-4 font-medium text-right">
                      {t("page.platform.tenant_table_ops", {
                        defaultValue: "管理操作",
                      })}
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {pagedTenants.length === 0 ? (
                    <tr>
                      <td
                        colSpan={6}
                        className="py-8 text-center text-sm text-muted-foreground"
                      >
                        {t("page.platform.empty")}
                      </td>
                    </tr>
                  ) : (
                    pagedTenants.map((tenant) => (
                      <tr
                        key={tenant.id}
                        className="hover:bg-muted/30 transition-colors"
                      >
                        <td className="py-3 px-4 text-xs font-mono text-muted-foreground">
                          #{tenant.id}
                        </td>
                        <td className="py-3 px-4">
                          <div className="font-semibold text-foreground">
                            {tenant.name}
                          </div>
                          <div className="text-xs font-mono text-muted-foreground">
                            {tenant.slug}
                          </div>
                        </td>
                        <td className="py-3 px-4">
                          <span
                            className={cn(
                              "inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium",
                              tenant.status === "active"
                                ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400 border border-emerald-500/20"
                                : "bg-rose-500/10 text-rose-700 dark:text-rose-400 border border-rose-500/20",
                            )}
                          >
                            {tenant.status === "active"
                              ? t("page.platform.status_active", {
                                  defaultValue: "正常",
                                })
                              : t("page.platform.status_suspended", {
                                  defaultValue: "已冻结",
                                })}
                          </span>
                        </td>
                        <td className="py-3 px-4 text-xs font-medium">
                          {tenant.domain_count}
                        </td>
                        <td className="py-3 px-4 text-xs font-medium">
                          {tenant.mailbox_count}
                        </td>
                        <td className="py-3 px-4 text-right">
                          <div className="flex items-center justify-end gap-2">
                            <Button
                              size="sm"
                              variant="outline"
                              className="h-8 text-xs gap-1 border-primary/30 text-primary hover:bg-primary/10"
                              onClick={() => handleAssume(tenant)}
                            >
                              <Eye className="h-3.5 w-3.5" />
                              <span>
                                {t("page.platform.assume_tenant", {
                                  defaultValue: "视察",
                                })}
                              </span>
                            </Button>
                            <Button
                              size="sm"
                              variant={
                                tenant.status === "active"
                                  ? "outline"
                                  : "default"
                              }
                              className="h-8 text-xs"
                              onClick={() =>
                                setStatus(
                                  tenant,
                                  tenant.status === "active"
                                    ? "suspend"
                                    : "activate",
                                )
                              }
                            >
                              {tenant.status === "active"
                                ? t("page.platform.suspend")
                                : t("page.platform.activate")}
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>

            {/* Pagination footer */}
            <div className="p-3 border-t flex flex-col sm:flex-row items-center justify-between gap-3 text-xs text-muted-foreground">
              <div>
                {t("page.platform.page_info", {
                  current: tenantPage,
                  total: totalPages,
                  count: filteredTenants.length,
                  defaultValue: `第 ${tenantPage} / ${totalPages} 页（共 ${filteredTenants.length} 条）`,
                })}
              </div>
              <div className="flex items-center gap-1.5">
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 px-2.5 text-xs"
                  disabled={tenantPage <= 1}
                  onClick={() => setTenantPage((p) => Math.max(1, p - 1))}
                >
                  <ChevronLeft className="h-3.5 w-3.5 mr-1" />
                  <span>上一页</span>
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 px-2.5 text-xs"
                  disabled={tenantPage >= totalPages}
                  onClick={() =>
                    setTenantPage((p) => Math.min(totalPages, p + 1))
                  }
                >
                  <span>下一页</span>
                  <ChevronRight className="h-3.5 w-3.5 ml-1" />
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* =========================================================================
          Tab 3: 套餐计划 (Plans)
          ========================================================================= */}
      <div className={activeTab === "plans" ? "space-y-6" : "hidden"}>
        <Card>
          <CardHeader>
            <div className="flex items-center justify-between">
              <div>
                <CardTitle>{t("page.platform.plan_editor")}</CardTitle>
                <CardDescription>
                  管理全平台订阅套餐计划、功能配额上限与费率
                </CardDescription>
              </div>
              {planEdit.code && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setPlanEdit(emptyPlan)}
                  className="text-xs"
                >
                  {t("page.platform.cancel_edit", { defaultValue: "取消编辑" })}
                </Button>
              )}
            </div>
          </CardHeader>
          <CardContent className="space-y-6">
            <form
              className="grid gap-3 sm:grid-cols-2 p-4 border rounded-lg bg-card/50"
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
                    setNotice({
                      variant: "error",
                      message: errorMessage(err),
                    }),
                  );
              }}
            >
              <label className="grid gap-1 text-xs font-medium">
                {t("page.platform.plan_code")}
                <Input
                  value={planEdit.code}
                  onChange={(e) =>
                    setPlanEdit({ ...planEdit, code: e.target.value })
                  }
                  required
                />
              </label>
              <label className="grid gap-1 text-xs font-medium">
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
                <label key={field} className="grid gap-1 text-xs font-medium">
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
              <div className="flex items-center gap-2 sm:col-span-2 pt-2">
                <input
                  type="checkbox"
                  id="plan-active-check"
                  checked={planEdit.active}
                  onChange={(e) =>
                    setPlanEdit({ ...planEdit, active: e.target.checked })
                  }
                  className="rounded border-input text-primary"
                />
                <label
                  htmlFor="plan-active-check"
                  className="text-xs font-medium cursor-pointer"
                >
                  {t("state.active")}
                </label>
              </div>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit">{t("action.save")}</Button>
              </div>
            </form>

            <div className="border rounded-lg divide-y">
              {planItems.map((plan) => (
                <div
                  key={plan.code}
                  className="p-4 flex flex-wrap items-center justify-between gap-3 hover:bg-muted/30 transition-colors"
                >
                  <div>
                    <div className="font-semibold flex items-center gap-2">
                      <span>
                        {plan.name["zh-CN"] || plan.name.en || plan.code}
                      </span>
                      <span className="text-xs font-mono px-2 py-0.5 rounded bg-muted">
                        {plan.code}
                      </span>
                      {plan.active ? (
                        <span className="text-[11px] text-emerald-600 font-medium">
                          ● 在售
                        </span>
                      ) : (
                        <span className="text-[11px] text-muted-foreground">
                          ○ 下架
                        </span>
                      )}
                    </div>
                    <div className="text-xs text-muted-foreground mt-1">
                      价格: ¥{(plan.price_cents / 100).toFixed(2)} / 月 ·
                      邮箱上限:{" "}
                      {plan.mailbox_limit === -1 ? "不限" : plan.mailbox_limit}{" "}
                      · 域名:{" "}
                      {plan.domain_limit === -1 ? "不限" : plan.domain_limit} ·
                      每日发信:{" "}
                      {plan.daily_send_limit === -1
                        ? "不限"
                        : plan.daily_send_limit}
                    </div>
                  </div>
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
            </div>
          </CardContent>
        </Card>
      </div>

      {/* =========================================================================
          Tab 4: 商业化与财务 (Finance & Commercial)
          ========================================================================= */}
      <div className={activeTab === "finance" ? "space-y-6" : "hidden"}>
        <PaymentMerchantAdmin />
        <FinancialReviewAdmin />
        <CouponReissueAdmin />
        <BillingCalendarAdmin tenants={tenants} />
        <DocumentProfile platform />
      </div>

      {/* =========================================================================
          Tab 5: 品牌与主机 (Brands & Hosts)
          ========================================================================= */}
      <div className={activeTab === "brands" ? "space-y-6" : "hidden"}>
        <Card>
          <CardHeader>
            <CardTitle>{t("page.platform.manage_brands")}</CardTitle>
            <CardDescription>
              平台多品牌白标矩阵、定制外观样式与域名映射
            </CardDescription>
          </CardHeader>
          <CardContent className="divide-y">
            {brandItems.map((brand) => (
              <div
                key={brand.id}
                className="flex flex-wrap items-center justify-between gap-3 py-3"
              >
                <div>
                  <div className="font-semibold flex items-center gap-2">
                    <span>{brand.name}</span>
                    <span className="text-xs font-mono text-muted-foreground">
                      ({brand.slug})
                    </span>
                    <span
                      className={cn(
                        "text-[11px] px-2 py-0.5 rounded",
                        brand.status === "active"
                          ? "bg-emerald-500/10 text-emerald-600"
                          : "bg-muted text-muted-foreground",
                      )}
                    >
                      {t(`status.${brand.status}`, {
                        defaultValue: brand.status,
                      })}
                    </span>
                  </div>
                </div>
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
                                brand.status === "active"
                                  ? "disabled"
                                  : "active",
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

        <div className="grid gap-6 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">
                {t("page.platform.brand_editor")}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <form onSubmit={saveBrand} className="grid gap-2.5">
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
                <div className="flex items-center gap-2">
                  <Input
                    type="color"
                    className="w-14 h-9 p-1 cursor-pointer"
                    value={primaryColor}
                    onChange={(e) => setPrimaryColor(e.target.value)}
                  />
                  <span className="text-xs text-muted-foreground font-mono">
                    {primaryColor}
                  </span>
                </div>
                <label className="grid gap-1 text-xs">
                  {t("page.platform.logo_url")}
                  <Input
                    type="url"
                    value={logoURL}
                    onChange={(e) => setLogoURL(e.target.value)}
                  />
                </label>
                <label className="grid gap-1 text-xs">
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
              <form onSubmit={saveChannel} className="grid gap-2.5">
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

        <Card>
          <CardHeader>
            <CardTitle>{t("page.platform.brand_hosts")}</CardTitle>
            <CardDescription>
              配置与各品牌绑定的专用邮件访问域名
            </CardDescription>
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
                    setNotice({
                      variant: "error",
                      message: errorMessage(err),
                    }),
                  );
              }}
            >
              <Input
                aria-label={t("page.platform.brand_slug")}
                value={brandSlug}
                onChange={(e) => setBrandSlug(e.target.value)}
                required
                className="w-40"
              />
              <Input
                aria-label={t("page.platform.hostname")}
                value={brandHosts}
                onChange={(e) => setBrandHosts(e.target.value)}
                placeholder="mail.example.com"
                required
                className="flex-1 min-w-[200px]"
              />
              <Button type="submit">{t("action.save")}</Button>
            </form>
            <div className="mt-4 divide-y border rounded-lg overflow-hidden">
              {hostItems.map((host) => (
                <div
                  key={host.hostname}
                  className="flex flex-wrap items-center justify-between gap-2 p-3 hover:bg-muted/30 transition-colors"
                >
                  <span className="font-mono text-xs">{host.hostname}</span>
                  <div className="flex items-center gap-2">
                    <BrandHostLifecycle
                      hostname={host.hostname}
                      onChange={load}
                    />
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
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      </div>

      {/* =========================================================================
          Tab 6: AI与系统规则 (AI & System Settings)
          ========================================================================= */}
      <div className={activeTab === "settings" ? "space-y-6" : "hidden"}>
        <PlatformAIAdmin tenants={tenants} />
        <NotificationAdmin />
        <SettingsEditor platform />
      </div>

      <div className={activeTab === "backup" ? "space-y-6" : "hidden"}>
        <BackupOffsiteAdmin />
      </div>

      {/* Global Notice Alert Dialog */}
      <NoticeDialog
        open={notice !== null}
        onOpenChange={(open) => !open && setNotice(null)}
        message={notice?.message ?? ""}
        variant={notice?.variant ?? "error"}
      />
    </div>
  );
}
