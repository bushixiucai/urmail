import { useTranslation } from "react-i18next";

export function NotFoundPage() {
  const { t } = useTranslation();

  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-2 p-6">
      <h1 className="text-2xl font-semibold">404</h1>
      <p className="text-sm text-muted-foreground">
        {t("error.NOT_FOUND", "找不到该资源")}
      </p>
    </main>
  );
}
