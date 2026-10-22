import { useEffect } from "react";

export interface BrandTheme {
  primary?: string;
  background?: string;
  radius?: string;
  [key: string]: string | undefined;
}

interface ThemeInjectorProps {
  theme?: BrandTheme | null;
}

export function ThemeInjector({ theme }: ThemeInjectorProps) {
  useEffect(() => {
    if (!theme || Object.keys(theme).length === 0) return;

    let styleEl = document.getElementById(
      "urmail-brand-theme",
    ) as HTMLStyleElement | null;
    if (!styleEl) {
      styleEl = document.createElement("style");
      styleEl.id = "urmail-brand-theme";
      document.head.appendChild(styleEl);
    }

    const cssRules = Object.entries(theme)
      .filter(([_, val]) => typeof val === "string" && val.trim().length > 0)
      .map(([key, val]) => `--${key}: ${val};`)
      .join(" ");

    styleEl.textContent = `:root { ${cssRules} }`;

    return () => {
      // Keep or cleanup on unmount
    };
  }, [theme]);

  return null;
}
