import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  ThemeProvider,
  THEME_STORAGE_KEY,
  useThemeMode,
} from "./theme-provider";

let dark = false;
let media: MediaQueryList;
function Control() {
  const { mode, setMode } = useThemeMode();
  return (
    <select
      aria-label="Theme mode"
      value={mode}
      onChange={(event) =>
        setMode(event.target.value as "light" | "dark" | "system")
      }
    >
      <option>system</option>
      <option>light</option>
      <option>dark</option>
    </select>
  );
}
function mount() {
  return render(
    <ThemeProvider>
      <Control />
    </ThemeProvider>,
  );
}
beforeEach(() => {
  localStorage.clear();
  dark = false;
  const target = new EventTarget();
  media = Object.assign(target, {
    get matches() {
      return dark;
    },
    media: "(prefers-color-scheme: dark)",
  }) as MediaQueryList;
  Object.defineProperty(media, "matches", { get: () => dark });
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => media),
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.documentElement.classList.remove("dark");
  document.documentElement.style.colorScheme = "";
});
it("defaults to light on a dark OS; only an explicit dark activates dark", () => {
  const view = mount();
  expect(document.documentElement.classList.contains("dark")).toBe(false);
  // Decision ①b: v1 is light-only — a dark OS must not flip the app via
  // "system"; the OS change event is ignored while no explicit choice exists.
  dark = true;
  media.dispatchEvent(new Event("change"));
  expect(document.documentElement.classList.contains("dark")).toBe(false);
  expect(document.documentElement.style.colorScheme).toBe("light");
  // Explicitly choosing "system" also stays light under the dark OS.
  fireEvent.change(view.getByLabelText("Theme mode"), {
    target: { value: "system" },
  });
  expect(document.documentElement.classList.contains("dark")).toBe(false);
  // Only an explicit "dark" activates dark, and OS flips then stop mattering.
  fireEvent.change(view.getByLabelText("Theme mode"), {
    target: { value: "dark" },
  });
  expect(document.documentElement.classList.contains("dark")).toBe(true);
  expect(document.documentElement.style.colorScheme).toBe("dark");
  dark = false;
  media.dispatchEvent(new Event("change"));
  expect(document.documentElement.classList.contains("dark")).toBe(true);
});
it("restores a persisted explicit mode on remount and synchronizes other tabs", () => {
  const view = mount();
  fireEvent.change(view.getByLabelText("Theme mode"), {
    target: { value: "dark" },
  });
  expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
  view.unmount();
  const second = mount();
  expect((second.getByLabelText("Theme mode") as HTMLSelectElement).value).toBe(
    "dark",
  );
  expect(document.documentElement.classList.contains("dark")).toBe(true);
  fireEvent(
    window,
    new StorageEvent("storage", { key: THEME_STORAGE_KEY, newValue: "light" }),
  );
  expect(document.documentElement.classList.contains("dark")).toBe(false);
});
it("keeps in-memory controls working when persistence is blocked", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("blocked");
  });
  const view = mount();
  fireEvent.change(view.getByLabelText("Theme mode"), {
    target: { value: "dark" },
  });
  expect(document.documentElement.classList.contains("dark")).toBe(true);
});
it("rejects invalid persisted modes and releases the media listener", () => {
  localStorage.setItem(THEME_STORAGE_KEY, "invalid");
  const release = vi.spyOn(media, "removeEventListener");
  const view = mount();
  // Invalid persisted values clamp to "light" (decision ①b), not "system".
  expect((view.getByLabelText("Theme mode") as HTMLSelectElement).value).toBe(
    "light",
  );
  view.unmount();
  expect(release).toHaveBeenCalledWith("change", expect.any(Function));
});
