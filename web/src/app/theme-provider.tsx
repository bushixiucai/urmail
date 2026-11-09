import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

export type ThemeMode = "light" | "dark" | "system";
export const THEME_STORAGE_KEY = "urmail.theme";
const mediaQuery = "(prefers-color-scheme: dark)";
// Decision ①b (2026-11-09): v1 ships light only — there is no theme picker,
// so users must never land in the half-dark state. Anything unpersisted,
// invalid or left on "system" clamps to light; the dark machinery below
// stays in place (dormant) for the v2 token-layer dark mode.
const validMode = (value: string | null): ThemeMode =>
  value === "light" || value === "dark" ? value : "light";
function storedMode(): ThemeMode {
  try {
    return validMode(window.localStorage.getItem(THEME_STORAGE_KEY));
  } catch {
    return "light";
  }
}
const ThemeContext = createContext<{
  mode: ThemeMode;
  setMode: (mode: ThemeMode) => void;
} | null>(null);

// Appearance is a device preference; no mailbox or tenant data is persisted.
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(storedMode);
  const setMode = useCallback((next: ThemeMode) => {
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, next);
    } catch {
      // The in-memory preference also works with storage disabled.
    }
    setModeState(next);
  }, []);
  useEffect(() => {
    const media = window.matchMedia?.(mediaQuery);
    const apply = () => {
      // v1: only an explicit "dark" activates dark; "system" behaves as
      // light (decision ①b). The OS listener stays wired for v2.
      const dark = mode === "dark";
      document.documentElement.classList.toggle("dark", dark);
      document.documentElement.style.colorScheme = dark ? "dark" : "light";
    };
    apply();
    media?.addEventListener("change", apply);
    return () => media?.removeEventListener("change", apply);
  }, [mode]);
  useEffect(() => {
    const sync = (event: StorageEvent) => {
      if (event.key === THEME_STORAGE_KEY || event.key === null) {
        setModeState(validMode(event.newValue));
      }
    };
    window.addEventListener("storage", sync);
    return () => window.removeEventListener("storage", sync);
  }, []);
  const value = useMemo(() => ({ mode, setMode }), [mode, setMode]);
  return (
    <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
  );
}
export function useThemeMode() {
  const value = useContext(ThemeContext);
  if (!value) throw new Error("useThemeMode must be inside ThemeProvider");
  return value;
}
