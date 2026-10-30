import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowRight, Mail, Settings2, Tags } from "lucide-react";
import {
  BrandAppearance,
  safeBrandURL,
  usePublicBrand,
} from "@/components/brand/public-brand";
import { useAuth } from "@/app/auth-context";
import { Button } from "@/components/ui/button";

export function HomePage() {
  const { t } = useTranslation();
  const brand = usePublicBrand();
  const { isAuthenticated } = useAuth();
  const brandName = brand?.name?.trim() || t("home.brand_fallback");
  const logo = safeBrandURL(brand?.logo_url);
  const consoleHref = isAuthenticated ? "/console" : "/login";

  return (
    <div className="home-surface min-h-screen bg-[var(--home-paper)] text-[var(--home-ink)]">
      <BrandAppearance brand={brand} />
      <link
        rel="stylesheet"
        href="https://fonts.googleapis.com/css2?family=Syne:wght@600;700;800&display=swap"
      />
      <style>{`
        .home-surface {
          --home-paper: oklch(0.985 0.004 95);
          --home-ink: oklch(0.18 0.02 255);
          --home-mute: oklch(0.42 0.02 255);
          --home-line: oklch(0.88 0.01 95);
          --home-accent: var(--primary);
          --home-accent-fg: var(--primary-foreground);
          color-scheme: light;
        }
        .home-display {
          font-family: Syne, "PingFang SC", "Noto Sans SC", system-ui, sans-serif;
        }
        .home-hero-field {
          background:
            radial-gradient(120% 80% at 85% 15%, color-mix(in oklab, var(--home-accent) 22%, transparent), transparent 55%),
            radial-gradient(90% 70% at 10% 90%, oklch(0.92 0.02 240 / 0.7), transparent 50%),
            linear-gradient(165deg, oklch(0.97 0.01 95), oklch(0.94 0.015 240));
        }
        .home-hero-field::after {
          content: "";
          position: absolute;
          inset: 0;
          background-image:
            linear-gradient(105deg, transparent 40%, oklch(0.2 0.02 255 / 0.04) 40.5%, transparent 41%),
            linear-gradient(105deg, transparent 62%, oklch(0.2 0.02 255 / 0.05) 62.5%, transparent 63%),
            linear-gradient(105deg, transparent 78%, oklch(0.2 0.02 255 / 0.04) 78.5%, transparent 79%);
          pointer-events: none;
        }
        @keyframes home-rise {
          from { opacity: 0; transform: translateY(12px); filter: blur(4px); }
          to { opacity: 1; transform: translateY(0); filter: blur(0); }
        }
        .home-rise {
          animation: home-rise 700ms cubic-bezier(0.16, 1, 0.3, 1) both;
        }
        .home-rise-delay { animation-delay: 90ms; }
        .home-rise-delay-2 { animation-delay: 160ms; }
        @media (prefers-reduced-motion: reduce) {
          .home-rise { animation: none; }
        }
        .home-surface ::selection {
          background: color-mix(in oklab, var(--home-accent) 35%, white);
          color: var(--home-ink);
        }
      `}</style>

      <header className="relative z-10 flex items-center justify-between gap-4 px-5 py-5 sm:px-10">
        <Link to="/" className="home-display flex items-center gap-3 text-lg font-bold tracking-tight">
          {logo ? (
            <img src={logo} alt="" className="h-8 max-w-[140px] object-contain" />
          ) : null}
          <span>{brandName}</span>
        </Link>
        <nav className="flex flex-wrap items-center gap-2 text-sm">
          <Link className="px-2 py-1 text-[var(--home-mute)] hover:text-[var(--home-ink)]" to="/pricing">
            {t("home.nav_pricing")}
          </Link>
          <Link className="px-2 py-1 text-[var(--home-mute)] hover:text-[var(--home-ink)]" to={consoleHref}>
            {t("home.nav_login")}
          </Link>
          <Button asChild size="sm" className="bg-[var(--home-accent)] text-[var(--home-accent-fg)] hover:opacity-90">
            <Link to="/register">{t("home.cta_register")}</Link>
          </Button>
        </nav>
      </header>

      <section className="home-hero-field relative isolate min-h-[min(92vh,920px)] overflow-hidden border-b border-[var(--home-line)]">
        <div className="relative z-10 mx-auto flex min-h-[min(92vh,920px)] max-w-6xl flex-col justify-end gap-8 px-5 pb-16 pt-24 sm:px-10 sm:pb-24">
          <p className="home-display home-rise text-[clamp(3rem,12vw,6rem)] font-extrabold leading-[0.92] tracking-[-0.035em]">
            {brandName}
          </p>
          <div className="home-rise home-rise-delay max-w-2xl space-y-4">
            <h1 className="home-display text-[clamp(1.75rem,4vw,2.75rem)] font-semibold leading-tight tracking-[-0.02em]">
              {t("home.headline")}
            </h1>
            <p className="max-w-[42rem] text-base leading-relaxed text-[var(--home-mute)] sm:text-lg">
              {t("home.support")}
            </p>
          </div>
          <div className="home-rise home-rise-delay-2 flex flex-wrap gap-3">
            <Button
              asChild
              size="lg"
              className="h-12 rounded-md bg-[var(--home-accent)] px-6 text-[var(--home-accent-fg)] hover:opacity-90"
            >
              <Link to="/register">
                {t("home.cta_register")}
                <ArrowRight className="ml-2 h-4 w-4" />
              </Link>
            </Button>
            <Button
              asChild
              size="lg"
              variant="outline"
              className="h-12 rounded-md border-[var(--home-ink)]/25 bg-transparent px-6 text-[var(--home-ink)] hover:bg-[var(--home-ink)]/5"
            >
              <Link to="/webmail">{t("home.cta_webmail")}</Link>
            </Button>
          </div>
        </div>
      </section>

      <section className="mx-auto max-w-6xl space-y-10 px-5 py-20 sm:px-10">
        <div className="max-w-2xl space-y-3">
          <h2 className="home-display text-3xl font-semibold tracking-[-0.02em]">
            {t("home.entries_title")}
          </h2>
          <p className="text-[var(--home-mute)]">{t("home.entries_support")}</p>
        </div>
        <div className="grid gap-0 border-y border-[var(--home-line)] md:grid-cols-3">
          {[
            {
              icon: Mail,
              title: t("home.entry_webmail_title"),
              body: t("home.entry_webmail_body"),
              cta: t("home.entry_webmail_cta"),
              to: "/webmail",
            },
            {
              icon: Settings2,
              title: t("home.entry_console_title"),
              body: t("home.entry_console_body"),
              cta: t("home.entry_console_cta"),
              to: consoleHref,
            },
            {
              icon: Tags,
              title: t("home.entry_pricing_title"),
              body: t("home.entry_pricing_body"),
              cta: t("home.entry_pricing_cta"),
              to: "/pricing",
            },
          ].map((entry, index) => {
            const Icon = entry.icon;
            return (
              <Link
                key={entry.to + entry.title}
                to={entry.to}
                className={`group flex flex-col gap-5 px-0 py-8 transition-colors hover:bg-[var(--home-ink)]/[0.03] md:px-8 ${
                  index > 0 ? "md:border-l md:border-[var(--home-line)]" : ""
                } ${index > 0 ? "border-t border-[var(--home-line)] md:border-t-0" : ""}`}
              >
                <Icon className="h-5 w-5 text-[var(--home-ink)]" strokeWidth={1.75} />
                <div className="space-y-2">
                  <h3 className="home-display text-xl font-semibold tracking-tight">
                    {entry.title}
                  </h3>
                  <p className="text-sm leading-relaxed text-[var(--home-mute)]">
                    {entry.body}
                  </p>
                </div>
                <span className="mt-auto inline-flex items-center gap-2 text-sm font-medium text-[var(--home-ink)]">
                  {entry.cta}
                  <ArrowRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" />
                </span>
              </Link>
            );
          })}
        </div>
      </section>

      <section className="border-t border-[var(--home-line)] bg-[oklch(0.97_0.006_95)]">
        <div className="mx-auto max-w-6xl space-y-10 px-5 py-20 sm:px-10">
          <div className="max-w-2xl space-y-3">
            <h2 className="home-display text-3xl font-semibold tracking-[-0.02em]">
              {t("home.capabilities_title")}
            </h2>
            <p className="text-[var(--home-mute)]">{t("home.capabilities_support")}</p>
          </div>
          <ul className="grid gap-10 md:grid-cols-3">
            {[
              {
                title: t("home.cap_domain_title"),
                body: t("home.cap_domain_body"),
              },
              {
                title: t("home.cap_tenant_title"),
                body: t("home.cap_tenant_body"),
              },
              {
                title: t("home.cap_webmail_title"),
                body: t("home.cap_webmail_body"),
              },
            ].map((item) => (
              <li key={item.title} className="space-y-3 border-t border-[var(--home-line)] pt-6">
                <h3 className="home-display text-lg font-semibold tracking-tight">
                  {item.title}
                </h3>
                <p className="text-sm leading-relaxed text-[var(--home-mute)]">{item.body}</p>
              </li>
            ))}
          </ul>
        </div>
      </section>

      <footer className="border-t border-[var(--home-line)]">
        <div className="mx-auto flex max-w-6xl flex-col gap-6 px-5 py-10 sm:flex-row sm:items-center sm:justify-between sm:px-10">
          <p className="home-display text-sm font-semibold">{brandName}</p>
          <div className="flex flex-wrap gap-x-5 gap-y-2 text-sm text-[var(--home-mute)]">
            <Link to="/pricing" className="hover:text-[var(--home-ink)]">
              {t("home.nav_pricing")}
            </Link>
            <Link to="/login" className="hover:text-[var(--home-ink)]">
              {t("home.nav_login")}
            </Link>
            <Link to="/guides/clients" className="hover:text-[var(--home-ink)]">
              {t("home.footer_guides")}
            </Link>
            <Link to="/support" className="hover:text-[var(--home-ink)]">
              {t("home.footer_support")}
            </Link>
          </div>
          <p className="text-xs text-[var(--home-mute)]">{t("home.footer_note")}</p>
        </div>
      </footer>
    </div>
  );
}
