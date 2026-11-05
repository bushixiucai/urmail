// web/src/features/webmail-next/use-webmail-preferences.ts
import { useState, useCallback, useEffect } from "react";
import type { DensityMode, SplitViewMode } from "./types";

const DENSITY_KEY = "urmail.webmail.density";
const SPLIT_MODE_KEY = "urmail.webmail.split_mode";

export function useWebmailPreferences() {
  const [density, setDensityState] = useState<DensityMode>(() => {
    if (typeof window === "undefined") return "compact";
    return (
      (localStorage.getItem(DENSITY_KEY) as DensityMode) || "compact"
    );
  });

  const [splitMode, setSplitModeState] = useState<SplitViewMode>(() => {
    if (typeof window === "undefined") return "vertical";
    return (
      (localStorage.getItem(SPLIT_MODE_KEY) as SplitViewMode) || "vertical"
    );
  });

  const setDensity = useCallback((mode: DensityMode) => {
    setDensityState(mode);
    try {
      localStorage.setItem(DENSITY_KEY, mode);
    } catch {
      // ignore
    }
  }, []);

  const setSplitMode = useCallback((mode: SplitViewMode) => {
    setSplitModeState(mode);
    try {
      localStorage.setItem(SPLIT_MODE_KEY, mode);
    } catch {
      // ignore
    }
  }, []);

  useEffect(() => {
    document.documentElement.classList.remove("density-compact", "density-comfortable");
    document.documentElement.classList.add(`density-${density}`);
  }, [density]);

  return {
    density,
    setDensity,
    splitMode,
    setSplitMode,
  };
}
