import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useMailboxNative } from "./use-mailbox-native";

const MAIL_HOST = "box.urmail.cn";

export function WebmailHelpPage() {
  const { t } = useTranslation();
  const { native, loaded } = useMailboxNative();

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("page.webmail.help_title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        <p className="text-muted-foreground">{t("page.webmail.help_intro")}</p>
        <dl className="space-y-2">
          <div className="flex flex-wrap gap-x-2 gap-y-1">
            <dt className="font-medium text-foreground">
              {t("page.webmail.help_host")}
            </dt>
            <dd>
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                {MAIL_HOST}
              </code>
            </dd>
          </div>
          <div className="flex flex-wrap gap-x-2 gap-y-1">
            <dt className="font-medium text-foreground">
              {t("page.webmail.help_imaps")}
            </dt>
            <dd>
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                993
              </code>
            </dd>
          </div>
          <div className="flex flex-wrap gap-x-2 gap-y-1">
            <dt className="font-medium text-foreground">
              {t("page.webmail.help_pop3s")}
            </dt>
            <dd>
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                995
              </code>
            </dd>
          </div>
          <div className="flex flex-wrap gap-x-2 gap-y-1">
            <dt className="font-medium text-foreground">
              {t("page.webmail.help_submission")}
            </dt>
            <dd>
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                465 / 587
              </code>
            </dd>
          </div>
        </dl>
        <div className="space-y-1 border-t pt-4">
          <p className="font-medium text-foreground">
            {t("page.webmail.help_migration_title")}
          </p>
          <p className="text-muted-foreground">
            {t("page.webmail.help_migration_export")}
          </p>
          <p className="text-muted-foreground">
            {t("page.webmail.help_migration_import")}
          </p>
          <p className="text-muted-foreground">
            {t("page.webmail.help_migration_mail")}
          </p>
        </div>
        {loaded && native ? (
          <p>
            <Link to="/webmail/inbox" className="underline text-primary">
              {t("page.webmail.open_inbox")}
            </Link>
          </p>
        ) : (
          <p className="text-muted-foreground">
            {t("page.webmail.help_coming_soon")}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
