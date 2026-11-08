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
let resizeListener: (() => void) | null = null;
let trackedTarget: Window | null = null;
let debounceTimer: ReturnType<typeof setTimeout> | undefined;

function readWidth(target: Window): number {
  try {
    const w = target.innerWidth;
    return Number.isFinite(w) && w > 0 ? Math.round(w) : 0;
  } catch {
    return 0;
  }
}

/**
 * Headers to attach to mailbox API requests. Empty-string values are
 * omitted — the server defaults to desktop / wide.
 */
export function viewportHeaders(
  ua = navigator.userAgent,
): Record<string, string> {
  const headers: Record<string, string> = {
    "X-UA-Class": classifyUAClass(ua, snapshotWidth),
  };
  if (snapshotWidth > 0) headers["X-Viewport-Width"] = String(snapshotWidth);
  return headers;
}

/** Install the debounced resize tracking once; returns the teardown. */
export function initViewportTracking(target: Window = window): () => void {
  snapshotWidth = readWidth(target);
  if (resizeListener) return () => undefined;

  resizeListener = () => {
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      snapshotWidth = readWidth(target);
    }, DEBOUNCE_MS);
  };
  target.addEventListener("resize", resizeListener);
  trackedTarget = target;

  return () => {
    if (trackedTarget && resizeListener) {
      trackedTarget.removeEventListener("resize", resizeListener);
    }
    trackedTarget = null;
    resizeListener = null;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = undefined;
  };
}

/** Test hook: reset the width snapshot and any installed listener. */
export function resetViewportTrackingForTests(): void {
  if (trackedTarget && resizeListener) {
    trackedTarget.removeEventListener("resize", resizeListener);
  }
  trackedTarget = null;
  snapshotWidth = 0;
  resizeListener = null;
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = undefined;
}
