import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import { request } from "@/lib/api/client";

/**
 * The message catalogue is served by the backend so that there is exactly one
 * copy of every translation. The frontend fetches it at start-up and falls
 * back to the key when a message is missing.
 */
export const FALLBACK_LOCALE = "zh-CN";
export const LOCALE_STORAGE_KEY = "urmail.locale";

interface I18nContextValue {
  locale: string;
  setLocale: (locale: string) => void;
}

function preferredLocale(): string {
  try {
    const stored = window.localStorage.getItem(LOCALE_STORAGE_KEY);
    if (stored) return stored;
    if (window.navigator.language.toLowerCase().startsWith("en")) return "en";
  } catch {
    // Ignore storage/security errors and use the default.
  }
  return FALLBACK_LOCALE;
}

const I18nContext = createContext<I18nContextValue | null>(null);

async function loadCatalogue(locale: string): Promise<Record<string, string>> {
  try {
    const response = await request<Record<string, string>>(
      `/api/v1/i18n/${encodeURIComponent(locale)}`,
    );
    return response;
  } catch {
    return {};
  }
}

export function I18nProvider({
  children,
  initialLocale = FALLBACK_LOCALE,
}: {
  children: ReactNode;
  initialLocale?: string;
}) {
  const [locale, setLocaleState] = useState(() =>
    initialLocale === FALLBACK_LOCALE ? preferredLocale() : initialLocale,
  );
  const setLocale = (next: string) => {
    try {
      window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      // Storage is optional.
    }
    setLocaleState(next);
  };

  useEffect(() => {
    let cancelled = false;

    async function load() {
      const catalogue = await loadCatalogue(locale);
      if (cancelled) return;
      if (Object.keys(catalogue).length === 0) return;

      if (!i18n.isInitialized) {
        await i18n.use(initReactI18next).init({
          lng: locale,
          fallbackLng: FALLBACK_LOCALE,
          resources: { [locale]: { translation: catalogue } },
          interpolation: { escapeValue: false },
        });
        return;
      }
      i18n.addResourceBundle(locale, "translation", catalogue, true, true);
      await i18n.changeLanguage(locale);
    }

    void load();
    return () => {
      cancelled = true;
    };
  }, [locale]);

  const value = useMemo(() => ({ locale, setLocale }), [locale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18nLocale(): I18nContextValue {
  const context = useContext(I18nContext);
  if (!context) {
    throw new Error("useI18nLocale must be used inside I18nProvider");
  }
  return context;
}
