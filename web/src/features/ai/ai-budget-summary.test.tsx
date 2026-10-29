import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { AIBudgetSummary } from "./ai-budget-summary";
import type { AIBudgetStatus } from "./ai-budget";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, params?: unknown) =>
      key + (params ? JSON.stringify(params) : ""),
    i18n: { language: "zh-CN" },
  }),
}));
afterEach(cleanup);
const budget: AIBudgetStatus = {
  enabled: true,
  cost_currency: "CNY",
  daily: {
    tokens_used: 7,
    tokens_limit: -1,
    cost_micros_used: 0,
    cost_micros_limit: -1,
  },
  monthly: {
    tokens_used: 9,
    tokens_limit: 100,
    cost_micros_used: 0,
    cost_micros_limit: -1,
  },
  entitlement: {
    source: "configured_nonplatform",
    available: true,
    call_limit: 20,
    mailbox_call_limit: 8,
    concurrency_limit: 2,
    period_start: "2026-10-01T00:00:00+08:00",
    period_end: "2026-11-01T00:00:00+08:00",
    timezone: "Asia/Shanghai",
    calls_used: 4,
    calls_remaining: 16,
    mailbox_calls_used: 2,
    mailbox_calls_remaining: 6,
    concurrent_reserved: 1,
  },
};
it("renders resolved caps, server period timezone, remaining and separate provider ceilings", () => {
  const view = render(<AIBudgetSummary budget={budget} />);
  expect(view.getByText("ai.quota.source.configured_nonplatform")).toBeTruthy();
  expect(
    view.getByText(
      'ai.quota.calls_status{"used":"4","limit":"20","remaining":"16"}',
    ),
  ).toBeTruthy();
  expect(
    view.getByText(
      'ai.quota.calls_status{"used":"2","limit":"8","remaining":"6"}',
    ),
  ).toBeTruthy();
  expect(
    view.getByText('ai.quota.concurrent_status{"used":"1","limit":"2"}'),
  ).toBeTruthy();
  expect(view.container.textContent).toContain("Asia/Shanghai");
  expect(view.container.textContent).toContain("ai.quota.no_extra_ceiling");
});
it("does not fabricate per-mailbox usage for panel-wide totals", () => {
  const view = render(
    <AIBudgetSummary
      budget={{
        ...budget,
        entitlement: {
          ...budget.entitlement!,
          source: "plan",
          mailbox_calls_used: null,
          mailbox_calls_remaining: null,
        },
      }}
    />,
  );
  expect(view.getByText("ai.quota.source.plan")).toBeTruthy();
  expect(view.getByText('ai.quota.mailbox_cap{"limit":"8"}')).toBeTruthy();
});
it("keeps zero independent caps unconfigured rather than implying unlimited admission", () => {
  const view = render(
    <AIBudgetSummary
      budget={{
        ...budget,
        entitlement: {
          ...budget.entitlement!,
          source: "unconfigured",
          available: false,
          call_limit: 0,
          mailbox_call_limit: 0,
          concurrency_limit: 0,
        },
      }}
    />,
  );
  expect(view.getByText("ai.quota.source.unconfigured")).toBeTruthy();
  expect(view.getByText("ai.quota.unavailable")).toBeTruthy();
  expect(view.queryByText("ai.quota.concurrent_status")).toBeNull();
});
