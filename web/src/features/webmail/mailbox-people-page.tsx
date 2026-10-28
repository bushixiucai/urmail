import { useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { CalendarDays, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PeopleContactsPanel } from "./people-contacts-panel";
import { PeopleCalendarPanel } from "./people-calendar-panel";
import { PeopleImportPanel } from "./people-import-panel";

export function MailboxPeoplePage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"contacts" | "calendar">(() =>
    new URLSearchParams(window.location.search).get("tab") === "calendar"
      ? "calendar"
      : "contacts",
  );
  return (
    <main className="mx-auto max-w-6xl space-y-6 p-4 sm:p-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">{t("people.title")}</h1>
        <Button variant="outline" asChild>
          <Link to="/webmail/inbox">{t("people.back_to_mail")}</Link>
        </Button>
      </header>
      <div
        role="tablist"
        aria-label={t("people.title")}
        className="flex gap-1 border-b pb-2"
      >
        {(["contacts", "calendar"] as const).map((value) => (
          <Button
            role="tab"
            id={"people-tab-" + value}
            aria-controls={"people-panel-" + value}
            aria-selected={tab === value}
            tabIndex={tab === value ? 0 : -1}
            onKeyDown={(event) => {
              if (
                !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)
              )
                return;
              event.preventDefault();
              const next =
                event.key === "Home"
                  ? "contacts"
                  : event.key === "End"
                    ? "calendar"
                    : value === "contacts"
                      ? "calendar"
                      : "contacts";
              setTab(next);
              document.getElementById("people-tab-" + next)?.focus();
            }}
            variant={tab === value ? "secondary" : "ghost"}
            key={value}
            onClick={() => setTab(value)}
          >
            {value === "contacts" ? (
              <Users className="mr-2 size-4" aria-hidden />
            ) : (
              <CalendarDays className="mr-2 size-4" aria-hidden />
            )}
            {t(
              "page.webmail." +
                (value === "contacts" ? "contacts_title" : "calendar_title"),
            )}
          </Button>
        ))}
      </div>
      <div
        role="tabpanel"
        id={"people-panel-" + tab}
        aria-labelledby={"people-tab-" + tab}
      >
        {tab === "contacts" ? <PeopleContactsPanel /> : <PeopleCalendarPanel />}
      </div>
      <PeopleImportPanel />
    </main>
  );
}
