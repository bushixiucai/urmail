import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailAdvancedSearchPopover } from "./webmail-advanced-search-popover";
import { WebmailSearchFilterChips } from "./webmail-search-filter-chips";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, defaultOrParams?: unknown) =>
      typeof defaultOrParams === "string" ? defaultOrParams : k,
  }),
}));

describe("WebmailAdvancedSearch", () => {
  afterEach(() => {
    cleanup();
  });

  const mockFolders = [
    { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
    { name: "Archive", name_enc: "Archive", delimiter: "/" },
  ];

  it("submits compound search parameters from popover", () => {
    const onApply = vi.fn();
    const onClose = vi.fn();

    render(
      <WebmailAdvancedSearchPopover
        isOpen={true}
        onClose={onClose}
        folders={mockFolders}
        onApplySearch={onApply}
      />,
    );

    const fromInput = screen.getByTestId("search-input-from");
    const subjectInput = screen.getByTestId("search-input-subject");
    const attachCheckbox = screen.getByTestId("search-checkbox-attachments");
    const submitBtn = screen.getByTestId("btn-submit-advanced-search");

    fireEvent.change(fromInput, { target: { value: "boss@example.com" } });
    fireEvent.change(subjectInput, { target: { value: "Quarterly Review" } });
    fireEvent.click(attachCheckbox);

    fireEvent.click(submitBtn);

    expect(onApply).toHaveBeenCalledWith(
      expect.objectContaining({
        from: "boss@example.com",
        subject: "Quarterly Review",
        hasAttachment: true,
      }),
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("resets all filter parameters when reset clicked", () => {
    render(
      <WebmailAdvancedSearchPopover
        isOpen={true}
        onClose={vi.fn()}
        folders={mockFolders}
        onApplySearch={vi.fn()}
        initialParams={{ from: "test@domain.com", subject: "Hello" }}
      />,
    );

    const fromInput = screen.getByTestId("search-input-from");
    expect(fromInput.getAttribute("value")).toBe("test@domain.com");

    const resetBtn = screen.getByTestId("btn-reset-filters");
    fireEvent.click(resetBtn);

    expect(fromInput.getAttribute("value")).toBe("");
  });

  it("renders active filter chips and handles remove and clear all", () => {
    const onRemove = vi.fn();
    const onClearAll = vi.fn();

    const { rerender } = render(
      <WebmailSearchFilterChips
        params={{
          from: "alice@domain.com",
          hasAttachment: true,
          dateRange: "7d",
        }}
        onRemoveParam={onRemove}
        onClearAll={onClearAll}
      />,
    );

    expect(screen.getByTestId("filter-chip-from")).toBeTruthy();
    expect(screen.getByTestId("filter-chip-hasAttachment")).toBeTruthy();
    expect(screen.getByTestId("filter-chip-dateRange")).toBeTruthy();

    // Click remove from chip
    const removeBtn = screen.getByTestId("btn-remove-chip-from");
    fireEvent.click(removeBtn);
    expect(onRemove).toHaveBeenCalledWith("from");

    // Click clear all
    const clearAllBtn = screen.getByTestId("btn-clear-all-chips");
    fireEvent.click(clearAllBtn);
    expect(onClearAll).toHaveBeenCalled();

    // Empty params renders nothing
    rerender(
      <WebmailSearchFilterChips
        params={{}}
        onRemoveParam={onRemove}
        onClearAll={onClearAll}
      />,
    );
    expect(screen.queryByTestId("search-filter-chips-bar")).toBeNull();
  });

  describe("buildSearchQuery regression tests (D1 - no unsupported filename: token)", () => {
    it("maps filenameExt to has:attachment and never emits filename: token", async () => {
      const { buildSearchQuery } = await import("../../webmail-next-workbench");
      const query = buildSearchQuery("invoice", {
        from: "boss@corp.test",
        subject: "report",
        hasAttachment: true,
        filenameExt: "pdf",
      });

      // Must include valid tokens
      expect(query).toContain("invoice");
      expect(query).toContain("from:boss@corp.test");
      expect(query).toContain("subject:report");
      expect(query).toContain("has:attachment");

      // Critical D1 regression check: MUST NOT contain unsupported "filename:"
      expect(query).not.toContain("filename:");
      expect(query).not.toContain("filename:pdf");

      // Only tokens containing colon must be one of backend supported prefixes
      const tokens = query.split(/\s+/).filter((t) => t.includes(":"));
      const allowedPrefixes = [
        "from:",
        "to:",
        "subject:",
        "body:",
        "has:attachment",
        "since:",
        "before:",
      ];
      for (const tok of tokens) {
        expect(
          allowedPrefixes.some(
            (p) => tok.startsWith(p) || tok === "has:attachment",
          ),
        ).toBe(true);
      }
    });

    it("handles date ranges into since: and before: without emitting unknown tokens", async () => {
      const { buildSearchQuery } = await import("../../webmail-next-workbench");
      const query7d = buildSearchQuery("", { dateRange: "7d" });
      expect(query7d).toMatch(/^since:\d{4}-\d{2}-\d{2}$/);

      const queryCustom = buildSearchQuery("", {
        dateRange: "custom",
        dateFrom: "2026-11-01",
        dateTo: "2026-11-10",
      });
      expect(queryCustom).toContain("since:2026-11-01");
      expect(queryCustom).toContain("before:2026-11-10");
    });
  });
});
