/**
 * UA-class / viewport-width request headers (v0.6.10 PR #43 contract).
 *
 * The server adapts sanitized mail HTML to the reading surface — breakpoint
 * bucket and fixed-width rewrites are driven by these headers; without them
 * it assumes a wide desktop pane and mobile rendering stays broken.
 *
 * The width snapshot is refreshed on window resize with a small debounce so
 * bursty resize events do not thrash; each request reads the latest snapshot.
 */

export type UAClass = "desktop" | "mobile" | "tablet";

/**
 * Classify the reading surface from the UA string and the viewport width.
 * UA hints win (a phone at 800px is still a phone); width decides only when
 * the UA is desktop-like (desktop browsers, odd webviews).
 */
export function classifyUAClass(userAgent: string, width: number): UAClass {
  const ua = userAgent.toLowerCase();
  if (/ipad|tablet/.test(ua)) return "tablet";
  if (/mobi/.test(ua)) return "mobile";
  if (width > 0 && width <= 640) return "mobile";
  if (width > 640 && width <= 1024) return "tablet";
  return "desktop";
}

const DEBOUNCE_MS = 200;

let snapshotWidth = 0;
let snapshotColorScheme: "light" | "dark" | "" = "";
let resizeListener: (() => void) | null = null;
let trackedTarget: Window | null = null;
let debounceTimer: ReturnType<typeof setTimeout> | undefined;
let colorSchemeMql: MediaQueryList | null = null;
let colorSchemeListener: ((event: MediaQueryListEvent) => void) | null = null;

function readWidth(target: Window): number {
  try {
    const w = target.innerWidth;
    return Number.isFinite(w) && w > 0 ? Math.round(w) : 0;
  } catch {
    return 0;
  }
}

function readColorScheme(target: Window): "light" | "dark" | "" {
  try {
    return target.matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  } catch {
    return "";
  }
}

/**
 * Body text-scale preference (M1 `X-Text-Scale` bucket). The settings UI
 * persists "sm" | "md" | "lg" under this key; "md" (and anything unknown)
 * is omitted so the request stays on the server's default cache bucket.
 */
const TEXT_SCALE_KEY = "urmail.text-scale";

function readTextScale(): "sm" | "lg" | "" {
  if (typeof window === "undefined") return "";
  try {
    const value = window.localStorage.getItem(TEXT_SCALE_KEY);
    return value === "sm" || value === "lg" ? value : "";
  } catch {
    return "";
  }
}

/**
 * Headers to attach to mailbox API requests. Empty-string values are
 * omitted — the server defaults to desktop / wide / light / md.
 */
export function viewportHeaders(
  ua = navigator.userAgent,
): Record<string, string> {
  const headers: Record<string, string> = {
    "X-UA-Class": classifyUAClass(ua, snapshotWidth),
  };
  if (snapshotWidth > 0) headers["X-Viewport-Width"] = String(snapshotWidth);
  if (snapshotColorScheme) headers["X-Color-Scheme"] = snapshotColorScheme;
  const textScale = readTextScale();
  if (textScale) headers["X-Text-Scale"] = textScale;
  return headers;
}

/** Install the debounced resize tracking once; returns the teardown. */
export function initViewportTracking(target: Window = window): () => void {
  snapshotWidth = readWidth(target);
  snapshotColorScheme = readColorScheme(target);
  if (resizeListener) return () => undefined;

  resizeListener = () => {
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      snapshotWidth = readWidth(target);
    }, DEBOUNCE_MS);
  };
  target.addEventListener("resize", resizeListener);
  trackedTarget = target;

  // M1: track the OS color scheme so light/dark buckets stay current
  // without a reload; media-query changes are rare, no debounce needed.
  try {
    colorSchemeMql = target.matchMedia("(prefers-color-scheme: dark)");
    colorSchemeListener = (event) => {
      snapshotColorScheme = event.matches ? "dark" : "light";
    };
    colorSchemeMql.addEventListener?.("change", colorSchemeListener);
  } catch {
    colorSchemeMql = null;
    colorSchemeListener = null;
  }

  return () => {
    if (trackedTarget && resizeListener) {
      trackedTarget.removeEventListener("resize", resizeListener);
    }
    if (colorSchemeMql && colorSchemeListener) {
      colorSchemeMql.removeEventListener?.("change", colorSchemeListener);
    }
    trackedTarget = null;
    resizeListener = null;
    colorSchemeMql = null;
    colorSchemeListener = null;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = undefined;
  };
}

/** Test hook: reset the width snapshot and any installed listener. */
export function resetViewportTrackingForTests(): void {
  if (trackedTarget && resizeListener) {
    trackedTarget.removeEventListener("resize", resizeListener);
  }
  if (colorSchemeMql && colorSchemeListener) {
    colorSchemeMql.removeEventListener?.("change", colorSchemeListener);
  }
  trackedTarget = null;
  snapshotWidth = 0;
  snapshotColorScheme = "";
  resizeListener = null;
  colorSchemeMql = null;
  colorSchemeListener = null;
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = undefined;
}
