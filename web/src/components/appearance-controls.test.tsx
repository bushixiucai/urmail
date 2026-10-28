import { cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ThemeProvider } from "@/app/theme-provider";
import { AppearanceControls } from "./appearance-controls";
const state = vi.hoisted(() => ({ setLocale: vi.fn() }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("@/app/providers", () => ({
  useI18nLocale: () => ({ locale: "en", setLocale: state.setLocale }),
}));
afterEach(() => {
  cleanup();
  state.setLocale.mockClear();
  localStorage.clear();
  document.documentElement.classList.remove("dark");
});
it("exposes native named controls for both languages and all appearance modes", () => {
  const view = render(
    <ThemeProvider>
      <AppearanceControls />
    </ThemeProvider>,
  );
  const language = view.getByRole("combobox", { name: "appearance.language" });
  fireEvent.change(language, { target: { value: "zh-CN" } });
  expect(state.setLocale).toHaveBeenCalledWith("zh-CN");
  const theme = view.getByRole("combobox", { name: "appearance.theme" });
  expect(
    Array.from((theme as HTMLSelectElement).options, (option) => option.value),
  ).toEqual(["system", "light", "dark"]);
  fireEvent.change(theme, { target: { value: "dark" } });
  expect(document.documentElement.classList.contains("dark")).toBe(true);
});
