import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WebmailHeader } from "./webmail-header";

function stubMatchMedia(matches: boolean) {
  const media = {
    matches,
    media: "(max-width: 767px)",
    addEventListener: () => {},
    removeEventListener: () => {},
  };
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => media),
  );
}
afterEach(() => vi.unstubAllGlobals());

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/components/brand/public-brand", () => ({
  usePublicBrand: () => ({ name: "urMail" }),
}));

describe("WebmailHeader icon buttons", () => {
  it("names the icon buttons with i18n keys", () => {
    render(
      <WebmailHeader
        onToggleRail={vi.fn()}
        onRefresh={vi.fn()}
        searchQuery=""
        onSearchChange={vi.fn()}
        density="comfortable"
        onSetDensity={vi.fn()}
        onOpenSettings={vi.fn()}
        onOpenContacts={vi.fn()}
        onOpenAdvancedSearch={vi.fn()}
      />,
    );

    expect(
      screen.getByTestId("rail-toggle-button").getAttribute("aria-label"),
    ).toBe("webmail_next.toggle_navigation");
    expect(
      screen.getByTestId("btn-open-advanced-search").getAttribute("aria-label"),
    ).toBe("webmail_next.advanced_filter");
    expect(
      screen.getByTestId("webmail-refresh-btn").getAttribute("aria-label"),
    ).toBe("webmail_next.refresh");
    expect(
      screen.getByTestId("webmail-settings-btn").getAttribute("aria-label"),
    ).toBe("webmail_next.settings");
    expect(
      screen.getByTestId("webmail-contacts-btn").getAttribute("aria-label"),
    ).toBe("contacts.title");
  });
});

describe("WebmailHeader density toggle (B2-3)", () => {
  it("keeps the label on desktop (≥md)", () => {
    stubMatchMedia(false);
    render(
      <WebmailHeader
        onToggleRail={vi.fn()}
        searchQuery=""
        onSearchChange={vi.fn()}
        density="comfortable"
        onSetDensity={vi.fn()}
        onOpenSettings={vi.fn()}
      />,
    );
    expect(screen.getByTestId("webmail-density-toggle").textContent).toContain(
      "webmail_next.density_comfortable",
    );
  });

  it("goes icon-only with a title below md (no wrapping label)", () => {
    stubMatchMedia(true);
    render(
      <WebmailHeader
        onToggleRail={vi.fn()}
        searchQuery=""
        onSearchChange={vi.fn()}
        density="comfortable"
        onSetDensity={vi.fn()}
        onOpenSettings={vi.fn()}
      />,
    );
    const toggle = screen.getByTestId("webmail-density-toggle");
    expect(toggle.textContent).not.toContain(
      "webmail_next.density_comfortable",
    );
    expect(toggle.getAttribute("title")).toBe(
      "webmail_next.density_comfortable",
    );
    expect(toggle.getAttribute("aria-label")).toBe("webmail_next.density_menu");
  });
});
