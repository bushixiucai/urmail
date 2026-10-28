import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
  FinancialReconciliation,
  FinancialFacts,
  FinancialReviewDecision,
} from "./financial-reconciliation";
const state = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { exists: () => true },
  }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
async function settle() {
  await act(async () => {
    for (let i = 0; i < 12; i++) await Promise.resolve();
  });
}
afterEach(() => {
  cleanup();
  state.request.mockReset();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
const facts = {
  id: 1,
  order_no: "FROZEN1",
  status: "pending",
  amount_cents_exact: "9007199254740993",
  currency: "CNY",
  payments: [],
  refunds: [],
  terms: [],
};
it("loads only explicit scoped requests and displays exact page totals", async () => {
  state.request.mockResolvedValue({
    items: [{ facts, discrepancies: ["payment_unknown"] }],
    totals: {
      CNY: {
        ordered_cents_exact: "9007199254740993",
        confirmed_cents_exact: "0",
        refunded_cents_exact: "0",
        net_cents_exact: "0",
      },
    },
    next_cursor: 1,
  });
  const view = render(<FinancialReconciliation platform />);
  expect(state.request).not.toHaveBeenCalled();
  fireEvent.change(view.getByLabelText("finance.tenant"), {
    target: { value: "12" },
  });
  fireEvent.click(
    view.getByRole("button", { name: "finance.reconciliation.load" }),
  );
  await settle();
  expect(state.request.mock.calls[0][0]).toContain("tenant_id=12");
  expect(view.getAllByText(/9007199254740993/).length).toBeGreaterThan(0);
  expect(
    view.getByText("finance.reconciliation.issue.payment_unknown"),
  ).toBeTruthy();
  fireEvent.click(
    view.getByRole("button", { name: "finance.reconciliation.next" }),
  );
  await settle();
  expect(state.request.mock.calls[1][0]).toContain("after=1");
});
it("requires a preview, invalidates it on edits, and never sends an old proof", async () => {
  const done = vi.fn();
  state.request.mockResolvedValue({
    digest: "b".repeat(64),
    expires_at: new Date(Date.now() + 600000).toISOString(),
    facts,
  });
  const view = render(
    <FinancialReviewDecision id={7} onDone={done} onCancel={() => {}} />,
  );
  fireEvent.change(view.getByLabelText("finance.reason"), {
    target: { value: "Independent reason for verified cash" },
  });
  fireEvent.click(
    view.getByRole("button", { name: "finance.reconciliation.preview" }),
  );
  await settle();
  expect(state.request.mock.calls[0][0]).toContain("/preview");
  expect(done).not.toHaveBeenCalled();
  fireEvent.change(view.getByLabelText("finance.reason"), {
    target: { value: "Changed independent audited reason" },
  });
  expect(
    view.queryByRole("button", { name: "finance.reconciliation.confirm" }),
  ).toBeNull();
  fireEvent.click(
    view.getByRole("button", { name: "finance.reconciliation.preview" }),
  );
  await settle();
  fireEvent.click(
    view.getByRole("button", { name: "finance.reconciliation.confirm" }),
  );
  await settle();
  expect(state.request.mock.calls[2][0]).toContain("/resolve");
  expect(state.request.mock.calls[2][1].body).toMatchObject({
    preview_digest: "b".repeat(64),
    reason: "Changed independent audited reason",
  });
  expect(done).toHaveBeenCalledOnce();
});
it("downloads the server blob without rounding nested frozen JSON amounts", async () => {
  const blob = new Blob(['{"nested":{"amount_cents":9007199254740993}}'], {
    type: "application/json",
  });
  const fetch = vi
    .fn()
    .mockResolvedValue({ ok: true, blob: () => Promise.resolve(blob) });
  vi.stubGlobal("fetch", fetch);
  const create = vi.fn().mockReturnValue("blob:financial-exact");
  Object.defineProperty(URL, "createObjectURL", {
    value: create,
    configurable: true,
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    value: vi.fn(),
    configurable: true,
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  const view = render(<FinancialReconciliation />);
  fireEvent.click(
    view.getByRole("button", { name: "finance.reconciliation.export" }),
  );
  await settle();
  expect(fetch.mock.calls[0][0]).toContain(
    "/api/v1/billing/reconciliation/export?",
  );
  expect(create).toHaveBeenCalledWith(blob);
  expect(state.request).not.toHaveBeenCalled();
});

it("labels unrecognized financial states without rendering raw enum text", () => {
  const view = render(
    <FinancialFacts facts={{ ...facts, status: "constructor" }} />,
  );
  expect(view.getByText(/finance.reconciliation.status.unknown/)).toBeTruthy();
  expect(view.queryByText(/constructor/)).toBeNull();
});
