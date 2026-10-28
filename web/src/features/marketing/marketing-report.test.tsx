import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { MarketingReportDialog } from "./marketing-report";
const api = vi.hoisted(() => ({ load: vi.fn(), export: vi.fn() }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { exists: () => true },
  }),
}));
vi.mock("./marketing-report-api", () => ({
  loadMarketingReport: api.load,
  exportMarketingReport: api.export,
}));
const report = {
  kind: "campaigns",
  resource_id: 7,
  name: "Fixture",
  from: "2026-10-01T00:00:00Z",
  until: "2026-10-28T00:00:00Z",
  digest: "a".repeat(64),
  total_facts: 55,
  counts: { "recipient/sent": 1 },
  rows: [],
  next_offset: 50,
};
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
it("binds later pages to the exact snapshot and invalidates export after a window edit", async () => {
  api.load.mockResolvedValue({ report });
  const view = render(
    <MarketingReportDialog
      kind="campaigns"
      id={7}
      open
      onOpenChange={() => {}}
    />,
  );
  await waitFor(() => expect(view.getByText("Fixture")).toBeTruthy());
  const download = view.getByRole("button", {
    name: "marketing.outcomes.export",
  }) as HTMLButtonElement;
  expect(download.disabled).toBe(true);
  expect(
    view.getByText(
      "marketing.outcomes.category_recipient · marketing.outcomes.state_accepted",
    ),
  ).toBeTruthy();
  fireEvent.click(
    view.getByRole("button", { name: "marketing.outcomes.next" }),
  );
  await waitFor(() =>
    expect(api.load).toHaveBeenLastCalledWith(
      "campaigns",
      7,
      report.from,
      report.until,
      50,
      report.digest,
      expect.any(AbortSignal),
    ),
  );
  fireEvent.click(view.getByRole("checkbox"));
  expect(download.disabled).toBe(false);
  fireEvent.change(view.getByLabelText("marketing.outcomes.from"), {
    target: { value: "2026-10-02T00:00:00" },
  });
  expect(download.disabled).toBe(true);
  expect(api.export).not.toHaveBeenCalled();
});
