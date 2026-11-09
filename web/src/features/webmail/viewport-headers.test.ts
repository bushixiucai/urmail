import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  classifyUAClass,
  initViewportTracking,
  resetViewportTrackingForTests,
  viewportHeaders,
} from "./viewport-headers";

const IPHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1";
const IPAD_UA =
  "Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1";
const DESKTOP_UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36";

function stubColorScheme(dark: boolean) {
  const listeners = new Set<(e: { matches: boolean }) => void>();
  const mql = {
    get matches() {
      return dark;
    },
    media: "(prefers-color-scheme: dark)",
    addEventListener: (_: string, cb: (e: { matches: boolean }) => void) =>
      listeners.add(cb),
    removeEventListener: (_: string, cb: (e: { matches: boolean }) => void) =>
      listeners.delete(cb),
  };
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => mql),
  );
  return listeners;
}

describe("classifyUAClass", () => {
  it("trusts explicit mobile and tablet UA hints over width", () => {
    expect(classifyUAClass(IPHONE_UA, 900)).toBe("mobile");
    expect(classifyUAClass(IPAD_UA, 500)).toBe("tablet");
  });

  it("falls back to width buckets for desktop-like UAs", () => {
    expect(classifyUAClass(DESKTOP_UA, 375)).toBe("mobile");
    expect(classifyUAClass(DESKTOP_UA, 800)).toBe("tablet");
    expect(classifyUAClass(DESKTOP_UA, 1440)).toBe("desktop");
  });

  it("treats unknown width as desktop", () => {
    expect(classifyUAClass(DESKTOP_UA, 0)).toBe("desktop");
  });
});

describe("viewportHeaders", () => {
  // jsdom has no matchMedia; stub a light-mode query by default so the
  // color-scheme snapshot is deterministic. Tests override for dark.
  beforeEach(() => {
    vi.unstubAllGlobals();
    resetViewportTrackingForTests();
    window.innerWidth = 1280;
    stubColorScheme(false);
  });

  afterEach(() => {
    resetViewportTrackingForTests();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("reports the current width and desktop class", () => {
    initViewportTracking(window);
    expect(viewportHeaders(DESKTOP_UA)).toEqual({
      "X-UA-Class": "desktop",
      "X-Viewport-Width": "1280",
      "X-Color-Scheme": "light",
    });
  });

  it("omits the width header when the viewport is unknown", () => {
    // No initViewportTracking: width and color snapshots stay unset.
    expect(viewportHeaders(DESKTOP_UA)).toEqual({ "X-UA-Class": "desktop" });
  });

  it("reports a dark color scheme from the media query", () => {
    stubColorScheme(true);
    initViewportTracking(window);
    expect(viewportHeaders(DESKTOP_UA)["X-Color-Scheme"]).toBe("dark");
  });

  it("omits the color header when matchMedia is unavailable", () => {
    vi.stubGlobal("matchMedia", undefined);
    initViewportTracking(window);
    expect(viewportHeaders(DESKTOP_UA)["X-Color-Scheme"]).toBeUndefined();
  });

  it("tracks color scheme flips through the change listener", () => {
    const listeners = stubColorScheme(false);
    initViewportTracking(window);
    expect(viewportHeaders(DESKTOP_UA)["X-Color-Scheme"]).toBe("light");
    for (const cb of listeners) cb({ matches: true });
    expect(viewportHeaders(DESKTOP_UA)["X-Color-Scheme"]).toBe("dark");
  });

  it("sends X-Text-Scale only for non-default stored preferences", () => {
    window.localStorage.setItem("urmail.text-scale", "lg");
    expect(viewportHeaders(DESKTOP_UA)["X-Text-Scale"]).toBe("lg");
    window.localStorage.setItem("urmail.text-scale", "md");
    expect(viewportHeaders(DESKTOP_UA)["X-Text-Scale"]).toBeUndefined();
    window.localStorage.setItem("urmail.text-scale", "bogus");
    expect(viewportHeaders(DESKTOP_UA)["X-Text-Scale"]).toBeUndefined();
    window.localStorage.removeItem("urmail.text-scale");
  });

  it("debounces resize updates before a request reads the snapshot", () => {
    vi.useFakeTimers();
    initViewportTracking(window);

    window.innerWidth = 480;
    window.dispatchEvent(new Event("resize"));
    // Not yet applied — the debounce window is still open.
    expect(viewportHeaders(IPHONE_UA)["X-Viewport-Width"]).toBe("1280");

    vi.advanceTimersByTime(250);
    expect(viewportHeaders(IPHONE_UA)).toEqual({
      "X-UA-Class": "mobile",
      "X-Viewport-Width": "480",
      "X-Color-Scheme": "light",
    });
  });

  it("coalesces bursty resize events into one update", () => {
    vi.useFakeTimers();
    initViewportTracking(window);

    for (const width of [900, 700, 500]) {
      window.innerWidth = width;
      window.dispatchEvent(new Event("resize"));
      vi.advanceTimersByTime(100);
    }
    vi.advanceTimersByTime(150);
    expect(viewportHeaders(IPHONE_UA)["X-Viewport-Width"]).toBe("500");
  });

  it("stops tracking after teardown", () => {
    vi.useFakeTimers();
    const teardown = initViewportTracking(window);
    teardown();

    window.innerWidth = 900;
    window.dispatchEvent(new Event("resize"));
    vi.advanceTimersByTime(400);
    // Snapshot no longer updates after teardown.
    expect(viewportHeaders(DESKTOP_UA)["X-Viewport-Width"]).toBe("1280");
  });
});
