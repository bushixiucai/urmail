import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { WebmailHeader } from "./webmail-header";

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
