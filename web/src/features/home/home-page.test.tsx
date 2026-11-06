import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { HomePage } from "./home-page";

const authState = { isAuthenticated: false };

vi.mock("@/app/auth-context", () => ({
  useAuth: () => authState,
}));

vi.mock("@/components/brand/public-brand", () => ({
  usePublicBrand: () => ({ name: "urMail", theme: {} }),
  BrandAppearance: () => null,
  safeBrandURL: () => undefined,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

function hrefs(path: string) {
  return screen
    .getAllByRole("link")
    .map((el) => el.getAttribute("href"))
    .filter((href) => href === path);
}

afterEach(() => {
  cleanup();
  authState.isAuthenticated = false;
});

describe("HomePage", () => {
  it("renders public CTAs to login and webmail", () => {
    render(
      <MemoryRouter>
        <HomePage />
      </MemoryRouter>,
    );
    expect(hrefs("/register")).toEqual([]);
    expect(hrefs("/pricing")).toEqual([]);
    expect(hrefs("/login").length).toBeGreaterThan(0);
    expect(hrefs("/webmail").length).toBeGreaterThan(0);
    expect(screen.getByText("home.headline")).toBeTruthy();
  });

  it("points console entry at login when signed out", () => {
    render(
      <MemoryRouter>
        <HomePage />
      </MemoryRouter>,
    );
    const entry = screen.getByText("home.entry_console_cta").closest("a");
    expect(entry?.getAttribute("href")).toBe("/login");
  });

  it("points console entry at /console when signed in", () => {
    authState.isAuthenticated = true;
    render(
      <MemoryRouter>
        <HomePage />
      </MemoryRouter>,
    );
    const entry = screen.getByText("home.entry_console_cta").closest("a");
    expect(entry?.getAttribute("href")).toBe("/console");
    expect(
      within(entry as HTMLElement).getByText("home.entry_console_title"),
    ).toBeTruthy();
  });
});
