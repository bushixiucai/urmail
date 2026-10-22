import { ReactNode } from "react";
import { Link, useLocation } from "react-router-dom";
import {
  Globe,
  Mail,
  Share2,
  LayoutDashboard,
  LogOut,
  Building2,
  ShieldCheck,
} from "lucide-react";
import { useAuth } from "@/app/auth-context";
import { Button } from "@/components/ui/button";

interface TenantLayoutProps {
  children: ReactNode;
}

export function TenantLayout({ children }: TenantLayoutProps) {
  const location = useLocation();
  const { user, tenants, currentTenant, switchTenant, logout, isAdmin } =
    useAuth();

  const navigation = [
    { name: "总览", href: "/", icon: LayoutDashboard },
    { name: "域名管理", href: "/domains", icon: Globe },
    { name: "邮箱账号", href: "/mailboxes", icon: Mail },
    { name: "别名与转发", href: "/aliases", icon: Share2 },
  ];

  return (
    <div className="flex min-h-screen bg-muted/20">
      {/* Sidebar */}
      <aside className="w-64 border-r bg-card flex flex-col shrink-0">
        <div className="p-5 border-b flex items-center justify-between">
          <div className="flex items-center gap-2">
            <div className="h-8 w-8 rounded bg-primary flex items-center justify-center text-primary-foreground font-bold">
              U
            </div>
            <div>
              <h1 className="text-base font-semibold leading-tight">
                urMail.cn
              </h1>
              <p className="text-xs text-muted-foreground truncate max-w-[130px]">
                {currentTenant ? currentTenant.name : "多租户控制台"}
              </p>
            </div>
          </div>
        </div>

        <nav className="flex-1 p-3 space-y-1">
          {navigation.map((item) => {
            const Icon = item.icon;
            const isActive = location.pathname === item.href;
            return (
              <Link
                key={item.href}
                to={item.href}
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

        {/* User profile & footer */}
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
            variant="outline"
            size="sm"
            className="w-full justify-start text-xs text-destructive hover:text-destructive"
            onClick={() => logout()}
          >
            <LogOut className="h-3.5 w-3.5 mr-2" />
            退出登录
          </Button>
        </div>
      </aside>

      {/* Main Content Area */}
      <main className="flex-1 flex flex-col min-w-0 overflow-y-auto">
        <header className="h-14 border-b bg-card flex items-center justify-between px-6 shrink-0">
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Building2 className="h-4 w-4" />
            <span>租户:</span>
            {tenants.length > 1 ? (
              <select
                aria-label="选择租户"
                className="h-8 rounded-md border border-input bg-background px-2.5 py-0.5 text-xs font-semibold text-foreground shadow-xs focus:ring-1 focus:ring-ring"
                value={currentTenant?.id ?? ""}
                onChange={(e) => {
                  const selected = tenants.find(
                    (t) => t.id === Number(e.target.value),
                  );
                  if (selected) {
                    switchTenant(selected);
                  }
                }}
              >
                {tenants.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name} ({t.role})
                  </option>
                ))}
              </select>
            ) : (
              <span className="font-semibold text-foreground">
                {currentTenant ? currentTenant.name : "未选择租户"}
              </span>
            )}
          </div>

          {isAdmin && (
            <div className="flex items-center gap-1.5 px-2.5 py-1 bg-amber-500/10 text-amber-600 rounded text-xs font-medium">
              <ShieldCheck className="h-3.5 w-3.5" />
              平台超管
            </div>
          )}
        </header>

        <div className="flex-1 p-6 max-w-6xl w-full mx-auto">{children}</div>
      </main>
    </div>
  );
}
