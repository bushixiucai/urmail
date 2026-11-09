// web/src/features/webmail-next/use-webmail-preferences.ts
import { useState, useCallback, useEffect } from "react";
import type { DensityMode, SplitViewMode } from "./types";

const DENSITY_KEY = "urmail.webmail.density";
const SPLIT_MODE_KEY = "urmail.webmail.split_mode";

export function useWebmailPreferences() {
  const [density, setDensityState] = useState<DensityMode>(() => {
    if (typeof window === "undefined") return "compact";
    return (localStorage.getItem(DENSITY_KEY) as DensityMode) || "compact";
  });
  // B2-3：用户是否显式选过密度（localStorage 有值）。移动端只对
  // 「从未显式设置」的用户把默认密度抬到 comfortable（触控 ≥44px），
  // 显式设置优先且跨视口一致。
  const [hasExplicitDensity, setHasExplicitDensity] = useState<boolean>(() => {
    if (typeof window === "undefined") return false;
    try {
      return localStorage.getItem(DENSITY_KEY) != null;
    } catch {
      return false;
    }
  });

  const [splitMode, setSplitModeState] = useState<SplitViewMode>(() => {
    if (typeof window === "undefined") return "vertical";
    return (
      (localStorage.getItem(SPLIT_MODE_KEY) as SplitViewMode) || "vertical"
    );
  });

  const setDensity = useCallback((mode: DensityMode) => {
    setDensityState(mode);
    setHasExplicitDensity(true);
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
    document.documentElement.classList.remove(
      "density-compact",
      "density-comfortable",
      "density-list_only",
    );
    document.documentElement.classList.add(`density-${density}`);
  }, [density]);

  return {
    density,
    setDensity,
    hasExplicitDensity,
    splitMode,
    setSplitMode,
  };
}
