import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useIsMobileViewport } from "./use-is-mobile-viewport";

function stubMatchMedia(matches: boolean) {
  const listeners = new Set<() => void>();
  type FakeMedia = {
    matches: boolean;
    media: string;
    addEventListener: (type: string, fn: () => void) => void;
    removeEventListener: (type: string, fn: () => void) => void;
    dispatchChange: (next: boolean) => void;
  };
  const media: FakeMedia = {
    matches,
    media: "(max-width: 767px)",
    addEventListener: (_type, fn) => {
      listeners.add(fn);
    },
    removeEventListener: (_type, fn) => {
      listeners.delete(fn);
    },
    dispatchChange: (next) => {
      media.matches = next;
      listeners.forEach((fn) => fn());
    },
  };
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => media),
  );
  return media;
}
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useIsMobileViewport", () => {
  it("reports true below the md breakpoint and updates on change", () => {
    const media = stubMatchMedia(true);
    const { result } = renderHook(() => useIsMobileViewport());
    expect(result.current).toBe(true);
    // 视口跨过 768px：change 事件后同步为桌面
    act(() => {
      (
        media as unknown as { dispatchChange(next: boolean): void }
      ).dispatchChange(false);
    });
    expect(result.current).toBe(false);
  });

  it("defaults to desktop when matchMedia is unavailable", () => {
    vi.stubGlobal("matchMedia", undefined);
    const { result } = renderHook(() => useIsMobileViewport());
    expect(result.current).toBe(false);
  });
});
