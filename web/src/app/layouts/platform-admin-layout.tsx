import { ReactNode, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  LayoutDashboard,
  Building2,
  Layers,
  Receipt,
  Globe,
  Cpu,
  Headphones,
  ShieldCheck,
  AlertTriangle,
  LogOut,
  Languages,
  Menu,
  ArrowRightLeft,
} from "lucide-react";
import { useAuth } from "@/app/auth-context";
import { useI18nLocale } from "@/app/providers";
import { useAssumedTenant } from "@/lib/assumed-tenant";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";

interface PlatformAdminLayoutProps {
  children: ReactNode;
}

export function PlatformAdminLayout({ children }: PlatformAdminLayoutProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const { t } = useTranslation();
  const { user, logout } = useAuth();
  const { locale, setLocale } = useI18nLocale();
  const { assumedTenant, exitAssumeTenant } = useAssumedTenant();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const currentTab =
    new URLSearchParams(location.search).get("tab") || "overview";
  const isSupportPage = location.pathname.startsWith("/platform/support");

  const navigation = [
    {
      id: "overview",
      name: t("page.platform.tab_overview", { defaultValue: "平台概览" }),
      href: "/platform?tab=overview",
      icon: LayoutDashboard,
      active: !isSupportPage && currentTab === "overview",
    },
    {
      id: "tenants",
      name: t("page.platform.tab_tenants", { defaultValue: "租户管理" }),
      href: "/platform?tab=tenants",
      icon: Building2,
      active: !isSupportPage && currentTab === "tenants",
    },
    {
      id: "plans",
      name: t("page.platform.tab_plans", { defaultValue: "套餐与计划" }),
      href: "/platform?tab=plans",
      icon: Layers,
      active: !isSupportPage && currentTab === "plans",
    },
    {
      id: "finance",
      name: t("page.platform.tab_finance", { defaultValue: "财务与结算" }),
      href: "/platform?tab=finance",
      icon: Receipt,
      active: !isSupportPage && currentTab === "finance",
    },
    {
      id: "brands",
      name: t("page.platform.tab_brands", { defaultValue: "品牌与主机" }),
      href: "/platform?tab=brands",
      icon: Globe,
      active: !isSupportPage && currentTab === "brands",
    },
    {
      id: "settings",
      name: t("page.platform.tab_settings", { defaultValue: "AI 与系统配置" }),
      href: "/platform?tab=settings",
      icon: Cpu,
      active: !isSupportPage && currentTab === "settings",
    },
    {
      id: "support",
      name: t("page.platform.tab_support", { defaultValue: "平台工单" }),
      href: "/platform/support",
      icon: Headphones,
      active: isSupportPage,
    },
  ];

  const handleExitAssume = () => {
    exitAssumeTenant();
    navigate("/platform?tab=tenants");
  };

  const renderNavLinks = (onNavigate?: () => void) => (
    <nav
      className="flex-1 p-3 space-y-1.5"
      aria-label="Platform Admin Navigation"
    >
      {navigation.map((item) => {
        const Icon = item.icon;
        return (
          <Link
            key={item.id}
            to={item.href}
            onClick={onNavigate}
            className={`flex items-center gap-3 px-3.5 py-2.5 text-sm font-medium rounded-lg transition-colors ${
              item.active
                ? "bg-primary text-primary-foreground shadow-xs"
                : "text-muted-foreground hover:bg-muted hover:text-foreground"
            }`}
          >
            <Icon className="h-4 w-4 shrink-0" />
            <span className="truncate">{item.name}</span>
          </Link>
        );
      })}
    </nav>
  );

  const renderSidebarBody = (onNavigate?: () => void) => (
    <>
      <div className="p-4 border-b flex items-center justify-between bg-card/60">
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="h-9 w-9 rounded-lg bg-primary/10 text-primary flex items-center justify-center font-bold shrink-0 border border-primary/20">
            <ShieldCheck className="h-5 w-5" />
          </div>
          <div className="min-w-0">
            <h1 className="text-sm font-semibold leading-tight truncate">
              {t("layout.platform_admin", { defaultValue: "平台超管后台" })}
            </h1>
            <p className="text-[11px] text-muted-foreground font-mono truncate">
              urMail Admin Console
            </p>
          </div>
        </div>
      </div>

      {renderNavLinks(onNavigate)}

      <div className="p-4 border-t space-y-2.5 bg-card/40">
        <div className="flex items-center gap-2.5 px-1 py-1">
          <div className="h-8 w-8 rounded-full bg-primary/15 text-primary flex items-center justify-center font-bold text-xs shrink-0">
            {user?.email?.charAt(0).toUpperCase() || "A"}
          </div>
          <div className="overflow-hidden flex-1 min-w-0">
            <p className="text-xs font-semibold truncate text-foreground">
              {user?.display_name || user?.email}
            </p>
            <p className="text-[10px] text-muted-foreground truncate font-mono">
              {user?.email}
            </p>
          </div>
        </div>

        <Link
          to="/console"
          onClick={onNavigate}
          className="flex items-center gap-2 px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:bg-muted rounded-md transition-colors w-full"
        >
          <ArrowRightLeft className="h-3.5 w-3.5" />
          <span>
            {t("page.platform.switch_to_tenant", {
              defaultValue: "进入租户控制台",
            })}
          </span>
        </Link>

        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start text-xs h-8"
          onClick={() => setLocale(locale === "zh-CN" ? "en" : "zh-CN")}
        >
          <Languages className="h-3.5 w-3.5 mr-2" />
          {locale === "zh-CN" ? "English" : "中文"}
        </Button>

        <Button
          variant="outline"
          size="sm"
          className="w-full justify-start text-xs h-8 text-destructive hover:text-destructive"
          onClick={() => {
            onNavigate?.();
            void logout();
          }}
        >
          <LogOut className="h-3.5 w-3.5 mr-2" />
          {t("layout.logout")}
        </Button>
      </div>
    </>
  );

  return (
    <div className="flex min-h-screen bg-muted/20 text-foreground">
      {/* Mobile drawer */}
      <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
        <SheetTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("layout.open_menu")}
            className="md:hidden fixed top-2.5 left-3 z-40 h-9 w-9 bg-card/80 backdrop-blur-xs border shadow-xs"
          >
            <Menu className="h-5 w-5" />
          </Button>
        </SheetTrigger>
        <SheetContent side="left" className="p-0 flex flex-col w-72">
          <SheetHeader className="sr-only">
            <SheetTitle>Platform Admin</SheetTitle>
          </SheetHeader>
          <div className="flex flex-col min-h-0 flex-1">
            {renderSidebarBody(() => setMobileNavOpen(false))}
          </div>
        </SheetContent>
      </Sheet>

      {/* Desktop Persistent Sidebar */}
      <aside className="hidden md:flex w-64 border-r bg-card flex-col shrink-0">
        {renderSidebarBody()}
      </aside>

      {/* Main Content Area */}
      <main className="flex-1 flex flex-col min-w-0 overflow-y-auto">
        {/* Assume Tenant Warning Banner */}
        {assumedTenant && (
          <div
            role="alert"
            className="bg-amber-500/15 border-b border-amber-500/30 text-amber-950 dark:text-amber-200 px-4 py-2.5 flex flex-wrap items-center justify-between gap-2 text-xs sm:text-sm font-medium sticky top-0 z-30 backdrop-blur-xs"
          >
            <div className="flex items-center gap-2 min-w-0">
              <AlertTriangle className="h-4 w-4 text-amber-600 dark:text-amber-400 shrink-0" />
              <span className="truncate">
                {t("page.platform.assume_banner", {
                  defaultValue: `当前处于代入租户视察模式：${assumedTenant.name} (${assumedTenant.slug})。所有操作将作用于该租户数据。`,
                  name: assumedTenant.name,
                  slug: assumedTenant.slug,
                })}
              </span>
            </div>
            <Button
              size="sm"
              variant="outline"
              className="border-amber-500/50 bg-amber-500/10 hover:bg-amber-500/20 text-amber-950 dark:text-amber-200 h-7 text-xs font-semibold shrink-0"
              onClick={handleExitAssume}
            >
              {t("page.platform.exit_assume", {
                defaultValue: "退出视察返回平台",
              })}
            </Button>
          </div>
        )}

        {/* Top Header */}
        <header className="min-h-14 border-b bg-card/60 backdrop-blur-xs flex flex-wrap gap-2 items-center justify-between pl-14 pr-3 md:px-6 py-2 shrink-0">
          <div className="flex items-center gap-2 text-sm">
            <span className="font-semibold text-foreground">
              {t("page.platform.title", { defaultValue: "平台超管工作台" })}
            </span>
            <span className="text-muted-foreground">/</span>
            <span className="text-xs text-muted-foreground">
              {isSupportPage
                ? t("page.platform.tab_support", { defaultValue: "平台工单" })
                : navigation.find((n) => n.id === currentTab)?.name}
            </span>
          </div>

          <div className="flex items-center gap-2">
            <div className="flex items-center gap-1.5 px-2.5 py-1 bg-amber-500/10 text-amber-700 dark:text-amber-400 border border-amber-500/20 rounded-md text-xs font-medium">
              <ShieldCheck className="h-3.5 w-3.5" />
              {t("layout.platform_admin", { defaultValue: "平台超级管理员" })}
            </div>
            <Link to="/console">
              <Button variant="ghost" size="sm" className="text-xs h-8">
                <ArrowRightLeft className="h-3.5 w-3.5 mr-1" />
                <span className="hidden sm:inline">
                  {t("page.platform.switch_to_tenant", {
                    defaultValue: "租户控制台",
                  })}
                </span>
              </Button>
            </Link>
          </div>
        </header>

        {/* Content Container */}
        <div className="flex-1 p-3 sm:p-6 max-w-7xl w-full mx-auto">
          {children}
        </div>
      </main>
    </div>
  );
}
