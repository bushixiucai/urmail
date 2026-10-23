import { useEffect } from "react";

export interface BrandTheme {
  primary?: string;
  secondary?: string;
  background?: string;
  foreground?: string;
  accent?: string;
  ring?: string;
  radius?: string;
  [key: string]: string | undefined;
}

/** Theme variables that may be injected into :root — everything else is dropped. */
const ALLOWED_KEYS = new Set([
  "primary",
  "secondary",
  "background",
  "foreground",
  "accent",
  "ring",
  "radius",
]);

const COLOR_KEYS = new Set([
  "primary",
  "secondary",
  "background",
  "foreground",
  "accent",
  "ring",
]);

/** Length cap for any theme value before it may enter a CSS custom property. */
const MAX_VALUE_LENGTH = 32;

const HEX_COLOR_PATTERN = /^#[0-9a-fA-F]{3,8}$/;
const NAMED_COLORS = new Set([
  "transparent",
  "currentcolor",
  "white",
  "black",
  "red",
  "green",
  "blue",
  "orange",
  "yellow",
  "purple",
  "gray",
  "grey",
]);
const RADIUS_PATTERN = /^\d{1,4}(\.\d{1,3})?(px|rem|em|%)?$/;

export function isValidThemeValue(
  key: string,
  value: unknown,
): value is string {
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  if (trimmed.length === 0 || trimmed.length > MAX_VALUE_LENGTH) return false;

  if (COLOR_KEYS.has(key)) {
    if (HEX_COLOR_PATTERN.test(trimmed)) return true;
    return NAMED_COLORS.has(trimmed.toLowerCase());
  }
  if (key === "radius") {
    return RADIUS_PATTERN.test(trimmed);
  }
  return false;
}

/** Keep only whitelisted keys with well-formed values; drop everything else. */
export function sanitizeTheme(
  theme: Record<string, unknown>,
): Record<string, string> {
  const result: Record<string, string> = {};
  for (const [key, value] of Object.entries(theme)) {
    if (!ALLOWED_KEYS.has(key)) continue;
    if (!isValidThemeValue(key, value)) continue;
    result[key] = (value as string).trim();
  }
  return result;
}

interface ThemeInjectorProps {
  theme?: BrandTheme | null;
}

export function ThemeInjector({ theme }: ThemeInjectorProps) {
  useEffect(() => {
    if (!theme) return;

    const safe = sanitizeTheme(theme);
    if (Object.keys(safe).length === 0) return;

    let styleEl = document.getElementById(
      "urmail-brand-theme",
    ) as HTMLStyleElement | null;
    if (!styleEl) {
      styleEl = document.createElement("style");
      styleEl.id = "urmail-brand-theme";
      document.head.appendChild(styleEl);
    }

    const cssRules = Object.entries(safe)
      .map(([key, value]) => `--${key}: ${value};`)
      .join(" ");

    styleEl.textContent = `:root { ${cssRules} }`;
    return () => styleEl?.remove();
  }, [theme]);

  return null;
}
