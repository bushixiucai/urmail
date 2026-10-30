import { useTranslation } from "react-i18next";
import {
  Mail,
  Calendar,
  Users,
  Sparkles,
  LogOut,
  HelpCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";

export type MailboxAppId = "mail" | "calendar" | "contacts" | "settings";

interface MailboxAppRailProps {
  activeApp: MailboxAppId;
  onSelectApp: (app: MailboxAppId) => void;
  aiOpen: boolean;
  onToggleAi: () => void;
  address: string;
  onLogout: () => void;
  onOpenHelp: () => void;
}

export function MailboxAppRail({
  activeApp,
  onSelectApp,
  aiOpen,
  onToggleAi,
  address,
  onLogout,
  onOpenHelp,
}: MailboxAppRailProps) {
  const { t } = useTranslation();

  const userInitial = address ? address.charAt(0).toUpperCase() : "U";

  return (
    <>
      <nav
        className="hidden md:flex flex-col items-center justify-between w-14 border-r bg-card/80 backdrop-blur-xs py-3 select-none z-20 shrink-0"
        aria-label={t("page.webmail.app_navigation")}
      >
        {/* Top Brand & Apps */}
        <div className="flex flex-col items-center gap-4 w-full">
          {/* Brand Logo */}
          <button
            type="button"
            className="h-9 w-9 rounded-xl bg-primary text-primary-foreground flex items-center justify-center font-bold text-xs shadow-sm cursor-pointer hover:opacity-90 transition-opacity"
            title="urMail"
            onClick={() => onSelectApp("mail")}
          >
            uM
          </button>

          {/* Primary App Switcher */}
          <div className="flex flex-col items-center gap-1.5 w-full px-2">
            <Button
              type="button"
              variant={activeApp === "mail" ? "secondary" : "ghost"}
              size="sm"
              className={
                "h-10 w-10 p-0 rounded-lg cursor-pointer transition-colors relative " +
                (activeApp === "mail"
                  ? "bg-primary/10 text-primary font-semibold"
                  : "text-muted-foreground hover:text-foreground")
              }
              title={t("page.webmail.app_mail", { defaultValue: "邮件" })}
              onClick={() => onSelectApp("mail")}
            >
              <Mail className="h-4.5 w-4.5" />
              {activeApp === "mail" && (
                <span className="absolute left-0 top-2 bottom-2 w-0.5 bg-primary rounded-r" />
              )}
            </Button>

            <Button
              type="button"
              variant={activeApp === "calendar" ? "secondary" : "ghost"}
              size="sm"
              className={
                "h-10 w-10 p-0 rounded-lg cursor-pointer transition-colors relative " +
                (activeApp === "calendar"
                  ? "bg-primary/10 text-primary font-semibold"
                  : "text-muted-foreground hover:text-foreground")
              }
              title={t("page.webmail.calendar_title", { defaultValue: "日历" })}
              onClick={() => onSelectApp("calendar")}
            >
              <Calendar className="h-4.5 w-4.5" />
              {activeApp === "calendar" && (
                <span className="absolute left-0 top-2 bottom-2 w-0.5 bg-primary rounded-r" />
              )}
            </Button>

            <Button
              type="button"
              variant={activeApp === "contacts" ? "secondary" : "ghost"}
              size="sm"
              className={
                "h-10 w-10 p-0 rounded-lg cursor-pointer transition-colors relative " +
                (activeApp === "contacts"
                  ? "bg-primary/10 text-primary font-semibold"
                  : "text-muted-foreground hover:text-foreground")
              }
              title={t("page.webmail.contacts_title", {
                defaultValue: "通讯录",
              })}
              onClick={() => onSelectApp("contacts")}
            >
              <Users className="h-4.5 w-4.5" />
              {activeApp === "contacts" && (
                <span className="absolute left-0 top-2 bottom-2 w-0.5 bg-primary rounded-r" />
              )}
            </Button>

            <div className="w-6 h-px bg-border/60 my-1" />

            {/* AI Copilot Toggle Button */}
            <Button
              type="button"
              variant={aiOpen ? "default" : "ghost"}
              size="sm"
              className={
                "h-10 w-10 p-0 rounded-lg cursor-pointer transition-all " +
                (aiOpen
                  ? "bg-primary text-primary-foreground shadow-sm"
                  : "text-amber-500 hover:text-amber-600 hover:bg-amber-500/10")
              }
              title={t("page.webmail.ai_copilot", {
                defaultValue: "AI 助手 (Copilot)",
              })}
              onClick={onToggleAi}
            >
              <Sparkles className="h-4.5 w-4.5" />
            </Button>
          </div>
        </div>

        {/* Bottom Utilities */}
        <div className="flex flex-col items-center gap-2 w-full px-2">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-9 w-9 p-0 rounded-lg text-muted-foreground hover:text-foreground cursor-pointer"
            title={t("page.webmail.help_title", { defaultValue: "帮助" })}
            onClick={onOpenHelp}
          >
            <HelpCircle className="h-4 w-4" />
          </Button>

          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-9 w-9 p-0 rounded-lg text-muted-foreground hover:text-destructive hover:bg-destructive/10 cursor-pointer"
            title={t("page.webmail.logout", { defaultValue: "退出登录" })}
            onClick={onLogout}
          >
            <LogOut className="h-4 w-4" />
          </Button>

          <div
            className="h-8 w-8 rounded-full bg-primary/15 border border-primary/20 text-primary flex items-center justify-center font-bold text-xs select-none"
            title={address}
          >
            {userInitial}
          </div>
        </div>
      </nav>
      <nav
        aria-label={t("page.webmail.mobile_navigation")}
        className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-4 border-t bg-card md:hidden"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        {(
          [
            ["mail", Mail, "page.webmail.app_mail"],
            ["contacts", Users, "page.webmail.contacts_title"],
            ["calendar", Calendar, "page.webmail.calendar_title"],
            ["ai", Sparkles, "page.webmail.ai_copilot"],
          ] as const
        ).map(([app, Icon, label]) => (
          <button
            key={app}
            type="button"
            className="min-h-16 min-w-0 flex flex-col items-center justify-center gap-1 px-1 text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
            aria-label={t(label)}
            aria-current={
              app !== "ai" && activeApp === app && !aiOpen ? "page" : undefined
            }
            aria-pressed={app === "ai" ? aiOpen : undefined}
            onClick={() => (app === "ai" ? onToggleAi() : onSelectApp(app))}
          >
            <Icon
              className={
                (app === "ai" ? aiOpen : activeApp === app && !aiOpen)
                  ? "h-5 w-5 text-primary"
                  : "h-5 w-5 text-muted-foreground"
              }
              aria-hidden
            />
            <span className="max-w-full truncate">{t(label)}</span>
          </button>
        ))}
      </nav>
    </>
  );
}
