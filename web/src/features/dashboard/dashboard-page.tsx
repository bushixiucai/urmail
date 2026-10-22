import { useTranslation } from "react-i18next";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { useHealth } from "@/lib/query/health";

export function DashboardPage() {
  const { t } = useTranslation();
  const { data, isPending, isError } = useHealth();

  return (
    <main className="mx-auto flex min-h-screen max-w-5xl flex-col gap-6 p-6">
      <header className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight">
          {t("app.name", "urMail.cn")}
        </h1>
        <p className="text-sm text-muted-foreground">
          {t("app.tagline", "多租户邮件 SaaS 平台")}
        </p>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>{t("nav.dashboard", "总览")}</CardTitle>
          <CardDescription>{t("state.loading", "加载中…")}</CardDescription>
        </CardHeader>
        <CardContent>
          {isPending && <p>{t("state.loading", "加载中…")}</p>}
          {isError && <p>{t("state.error", "出错了")}</p>}
          {data && (
            <dl className="grid gap-2 text-sm">
              <div className="flex gap-2">
                <dt className="text-muted-foreground">status</dt>
                <dd>{data.status}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="text-muted-foreground">version</dt>
                <dd>{data.version}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="text-muted-foreground">db</dt>
                <dd>{data.db ?? "-"}</dd>
              </div>
            </dl>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
