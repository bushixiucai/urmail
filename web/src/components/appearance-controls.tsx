import { useId } from "react";
import { useTranslation } from "react-i18next";
import { useI18nLocale } from "@/app/providers";
import { useThemeMode, type ThemeMode } from "@/app/theme-provider";
import { cn } from "@/lib/utils";

// Native labelled selects preserve keyboard, touch and screen reader behavior.
export function AppearanceControls({ className }: { className?: string }) {
  const { t } = useTranslation();
  const { locale, setLocale } = useI18nLocale();
  const { mode, setMode } = useThemeMode();
  const id = useId();
  const selectClass =
    "h-10 min-w-0 rounded-md border border-input bg-background px-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
  return (
    <div className={cn("flex flex-wrap items-end gap-3", className)}>
      <label
        htmlFor={`${id}-language`}
        className="grid min-w-0 gap-1 text-xs text-muted-foreground"
      >
        {t("appearance.language")}
        <select
          id={`${id}-language`}
          className={selectClass}
          value={locale}
          onChange={(event) => setLocale(event.target.value)}
        >
          <option value="zh-CN" lang="zh-CN">
            简体中文
          </option>
          <option value="en" lang="en">
            English
          </option>
        </select>
      </label>
      <label
        htmlFor={`${id}-theme`}
        className="grid min-w-0 gap-1 text-xs text-muted-foreground"
      >
        {t("appearance.theme")}
        <select
          id={`${id}-theme`}
          className={selectClass}
          value={mode}
          onChange={(event) => setMode(event.target.value as ThemeMode)}
        >
          <option value="system">{t("appearance.system")}</option>
          <option value="light">{t("appearance.light")}</option>
          <option value="dark">{t("appearance.dark")}</option>
        </select>
      </label>
    </div>
  );
}
