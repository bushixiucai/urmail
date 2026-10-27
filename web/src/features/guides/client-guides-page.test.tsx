import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { ClientGuidesPage, allGuideKeys } from "./client-guides-page";

// Same mock pattern as the webmail feature tests.
import { vi } from "vitest";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const localesRoot = resolve(__dirname, "../../../../internal/i18n/locales");

function loadLocale(file: string): Record<string, string> {
  return JSON.parse(readFileSync(resolve(localesRoot, file), "utf8"));
}

afterEach(() => {
  cleanup();
});

describe("client guides page", () => {
  it("renders every guide key in both locales consistently", () => {
    const en = loadLocale("en.json");
    const zh = loadLocale("zh-CN.json");
    for (const key of allGuideKeys()) {
      expect(en[key], `en.json missing ${key}`).toBeTruthy();
      expect(zh[key], `zh-CN.json missing ${key}`).toBeTruthy();
      expect(en[key]).not.toEqual(zh[key]);
    }
  });

  it("renders all client sections and server details", () => {
    const view = render(
      <MemoryRouter>
        <ClientGuidesPage />
      </MemoryRouter>,
    );
    expect(view.getByText("page.guides.title")).toBeTruthy();
    for (const key of allGuideKeys()) {
      expect(view.getAllByText(key).length).toBeGreaterThan(0);
    }
    expect(view.getByText("box.urmail.cn")).toBeTruthy();
    expect(view.getByText("page.guides.ports")).toBeTruthy();
  });
});
