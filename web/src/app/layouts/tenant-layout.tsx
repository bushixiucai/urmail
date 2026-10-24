import {
  BrandAppearance,
  usePublicBrand,
} from "@/components/brand/public-brand";
import { usePublicStatus } from "@/features/auth/public-status";
import { ReactNode, useEffect, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Globe,
  Mail,
  Share2,
  LayoutDashboard,
  LogOut,
  Building2,
  ShieldCheck,
  Menu,
  Languages,
  Receipt,
  ScrollText,
} from "lucide-react";
import { useAuth } from "@/app/auth-context";
import { useI18nLocale } from "@/app/providers";
import { request } from "@/lib/api/client";
import { BrandTheme, ThemeInjector } from "@/components/brand/theme-injector";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";

interface TenantLayoutProps {
  children: ReactNode;
}

export function TenantLayout({ children }: TenantLayoutProps) {
  const location = useLocation();
  const publicStatus = usePublicStatus();
  const publicBrand = usePublicBrand();
  const { t } = useTranslation();
  const { user, tenants, currentTenant, switchTenant, logout, isAdmin } =
    useAuth();
  const { locale, setLocale } = useI18nLocale();
  const [brand, setBrand] = useState<{
    theme?: BrandTheme;
    logo_url?: string | null;
    primary_color?: string | null;
  } | null>(null);

  useEffect(() => {
    request<{
      theme?: {
        theme?: BrandTheme;
        logo_url?: string | null;
        primary_color?: string | null;
      };
    }>("/api/v1/tenants/current")
      .then((res) => setBrand(res.theme ?? null))
      .catch(() => {});
  }, []);

  const brandLogo =
    brand?.logo_url && /^https?:\/\//.test(brand.logo_url)
      ? brand.logo_url
      : null;
  const brandTheme: BrandTheme | undefined = brand
    ? {
        ...(brand.theme ?? {}),
        ...(brand.primary_color ? { primary: brand.primary_color } : {}),
      }
    : undefined;
  const [webmailEnabled, setWebmailEnabled] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/v1/roundcube/status", { signal: controller.signal })
      .then((res) => (res.ok ? res.json() : null))
      .then((value: { enabled?: boolean } | null) => {
        if (value?.enabled) setWebmailEnabled(true);
      })
      .catch(() => {});
    return () => controller.abort();
  }, []);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const navigation = [
    { name: t("layout.dashboard"), href: "/", icon: LayoutDashboard },
    { name: t("layout.domains"), href: "/domains", icon: Globe },
    { name: t("layout.mailboxes"), href: "/mailboxes", icon: Mail },
    { name: t("layout.aliases"), href: "/aliases", icon: Share2 },
    { name: t("nav.billing"), href: "/billing", icon: Receipt },
    { name: t("nav.audit"), href: "/audit", icon: ScrollText },
    ...(currentTenant?.role === "owner" || currentTenant?.role === "admin"
      ? [{ name: t("nav.ai"), href: "/ai", icon: ShieldCheck }]
      : []),
    { name: t("archive.title"), href: "/archive", icon: Mail },
    { name: t("delivery.title"), href: "/delivery", icon: Mail },
    { name: t("nav.marketing"), href: "/marketing", icon: Mail },
    { name: t("nav.notifications"), href: "/notifications", icon: Mail },
    { name: t("nav.support"), href: "/support", icon: Mail },
    { name: t("layout.settings"), href: "/settings/mfa", icon: ShieldCheck },
    ...(isAdmin
      ? [{ name: t("nav.platform"), href: "/platform", icon: Building2 }]
      : []),
  ];

  const renderNavLinks = (onNavigate?: () => void) => (
    <nav className="flex-1 p-3 space-y-1">
      {webmailEnabled && (
        <a
          href="/mail/"
          className="flex items-center gap-3 px-3 py-2 text-sm font-medium rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
          title={t("page.webmail.independent_identity")}
        >
          <Mail className="h-4 w-4" />
          {t("nav.webmail")}
        </a>
      )}
      {webmailEnabled && (
        <p className="px-3 text-xs text-muted-foreground">
          {t("page.webmail.independent_identity")}
        </p>
      )}
      {navigation.map((item) => {
        const Icon = item.icon;
        const isActive = location.pathname === item.href;
        return (
          <Link
            key={item.href}
            to={item.href}
            onClick={onNavigate}
            className={`flex items-center gap-3 px-3 py-2 text-sm font-medium rounded-md transition-colors ${
              isActive
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:bg-muted hover:text-foreground"
            }`}
          >
            <Icon className="h-4 w-4" />
            {item.name}
          </Link>
        );
      })}
    </nav>
  );

  const renderSidebarBody = (onNavigate?: () => void) => (
    <>
      <div className="p-5 border-b flex items-center justify-between">
        <div className="flex items-center gap-2">
          {brandLogo ? (
            <img
              src={brandLogo}
              alt=""
              className="h-8 max-w-[120px] object-contain"
            />
          ) : (
            <div className="h-8 w-8 rounded bg-primary flex items-center justify-center text-primary-foreground font-bold">
              U
            </div>
          )}
          <div>
            <h1 className="text-base font-semibold leading-tight">
              {publicBrand?.name ?? "urMail.cn"}
            </h1>
            <p className="text-xs text-muted-foreground truncate max-w-[130px]">
              {currentTenant ? currentTenant.name : t("layout.tenant_console")}
            </p>
          </div>
        </div>
      </div>

      {renderNavLinks(onNavigate)}

      <div className="p-4 border-t space-y-3">
        <div className="flex items-center gap-2">
          <div className="h-8 w-8 rounded-full bg-muted flex items-center justify-center font-medium text-xs">
            {user?.email.charAt(0).toUpperCase()}
          </div>
          <div className="overflow-hidden flex-1">
            <p className="text-xs font-medium truncate">
              {user?.display_name || user?.email}
            </p>
            <p className="text-[10px] text-muted-foreground truncate">
              {user?.email}
            </p>
          </div>
        </div>

        <Button
          variant="ghost"
          size="sm"
          className="w-full justify-start text-xs"
          onClick={() => setLocale(locale === "zh-CN" ? "en" : "zh-CN")}
        >
          <Languages className="h-3.5 w-3.5 mr-2" />
          {locale === "zh-CN" ? "English" : "中文"}
        </Button>

        <Button
          variant="outline"
          size="sm"
          className="w-full justify-start text-xs text-destructive hover:text-destructive"
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
    <div className="flex min-h-screen bg-muted/20">
      <BrandAppearance brand={publicBrand} applyTheme={false} />
      <ThemeInjector theme={brandTheme} />
      {/* Mobile drawer */}
      <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
        <SheetTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("layout.open_menu")}
            className="md:hidden fixed top-2.5 left-3 z-40 h-9 w-9"
          >
            <Menu className="h-5 w-5" />
          </Button>
        </SheetTrigger>
        <SheetContent side="left" className="p-0 flex flex-col w-72">
          <SheetHeader className="sr-only">
            <SheetTitle>urMail.cn</SheetTitle>
          </SheetHeader>
          <div className="flex flex-col min-h-0 flex-1">
            {renderSidebarBody(() => setMobileNavOpen(false))}
          </div>
        </SheetContent>
      </Sheet>

      {/* Sidebar (persistent from md up) */}
      <aside className="hidden md:flex w-64 border-r bg-card flex-col shrink-0">
        {renderSidebarBody()}
      </aside>

      {/* Main Content Area */}
      <main className="flex-1 flex flex-col min-w-0 overflow-y-auto">
        <header className="min-h-14 border-b bg-card flex flex-wrap gap-2 items-center justify-between pl-14 pr-3 md:px-6 py-2 shrink-0">
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Building2 className="h-4 w-4" />
            <span>{t("layout.tenant")}</span>
            {tenants.length > 1 ? (
              <select
                aria-label={t("layout.select_tenant")}
                className="h-8 rounded-md border border-input bg-background px-2.5 py-0.5 text-xs font-semibold text-foreground shadow-xs focus:ring-1 focus:ring-ring"
                value={currentTenant?.id ?? ""}
                onChange={(e) => {
                  const selected = tenants.find(
                    (tenant) => tenant.id === Number(e.target.value),
                  );
                  if (selected) {
                    switchTenant(selected);
                  }
                }}
              >
                {tenants.map((tenant) => (
                  <option key={tenant.id} value={tenant.id}>
                    {tenant.name} (
                    {t(`status.${tenant.role}`, { defaultValue: tenant.role })})
                  </option>
                ))}
              </select>
            ) : (
              <span className="font-semibold text-foreground">
                {currentTenant ? currentTenant.name : t("layout.no_tenant")}
              </span>
            )}
          </div>

          {isAdmin && (
            <div className="flex items-center gap-1.5 px-2.5 py-1 bg-amber-500/10 text-amber-600 rounded text-xs font-medium">
              <ShieldCheck className="h-3.5 w-3.5" />
              {t("layout.platform_admin")}
            </div>
          )}
        </header>

        <div className="flex-1 p-3 sm:p-6 max-w-6xl w-full mx-auto">
          {publicStatus.quarantined && (
            <p
              role="alert"
              className="mb-4 rounded-md border border-destructive p-3 text-sm"
            >
              {t("restore.quarantine_banner")}
            </p>
          )}
          {children}
        </div>
      </main>
    </div>
  );
}
