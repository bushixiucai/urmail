import { useEffect, useState } from "react";
import { request } from "@/lib/api/client";
import { ThemeInjector, type BrandTheme } from "./theme-injector";

export interface PublicBrand {
  slug: string;
  name: string;
  theme: BrandTheme;
  primary_color?: string | null;
  logo_url?: string | null;
  favicon_url?: string | null;
}

export function safeBrandURL(
  value: string | null | undefined,
): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value, window.location.origin);
    if (
      url.protocol === "https:" ||
      (url.origin === window.location.origin && url.protocol === "http:")
    )
      return url.href;
  } catch {
    /* Invalid operator configuration must not become a navigable URL. */
  }
  return undefined;
}

export function usePublicBrand() {
  const [brand, setBrand] = useState<PublicBrand | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    const slug = new URLSearchParams(window.location.search).get("brand");
    request<{ brand: PublicBrand }>(
      "/api/v1/branding" + (slug ? `?brand=${encodeURIComponent(slug)}` : ""),
      { signal: controller.signal },
    )
      .then((data) => !controller.signal.aborted && setBrand(data.brand))
      .catch(() => {});
    return () => controller.abort();
  }, []);
  return brand;
}

export function BrandAppearance({
  brand,
  applyTheme = true,
}: {
  brand: PublicBrand | null;
  applyTheme?: boolean;
}) {
  useEffect(() => {
    const previousTitle = document.title;
    const icon = document.createElement("link");
    icon.rel = "icon";
    const favicon = safeBrandURL(brand?.favicon_url);
    if (favicon) {
      icon.href = favicon;
      document.head.appendChild(icon);
    }
    if (brand?.name) document.title = brand.name;
    return () => {
      icon.remove();
      document.title = previousTitle;
    };
  }, [brand]);
  return applyTheme ? (
    <ThemeInjector
      theme={
        brand
          ? {
              ...brand.theme,
              ...(brand.primary_color ? { primary: brand.primary_color } : {}),
            }
          : null
      }
    />
  ) : null;
}
