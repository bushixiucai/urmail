import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MarketingPage } from "./marketing-page";
import { MarketingChannelAdmin } from "./channel-admin";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (k: string) => k,
  i18n: { exists: () => true },
}));
vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({ isAdmin: false, currentTenant: { id: 1 } }),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
const settle = async () => {
  await act(async () => {
    for (let i = 0; i < 12; i++) await Promise.resolve();
  });
};
beforeEach(() =>
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("/channels")
        ? { channels: [] }
        : path.endsWith("/templates")
          ? { templates: [] }
          : path.endsWith("/lists")
            ? { lists: [] }
            : path.endsWith("/preview")
              ? {
                  preview: {
                    subject: "Subject",
                    text_body: "<script>unsafe()</script>",
                    opted_in_count: 2,
                    suppressed_count: 1,
                  },
                }
              : path.endsWith("/confirm")
                ? { status: "scheduled" }
                : { campaigns: [{ id: 9, name: "Campaign", status: "draft" }] },
    ),
  ),
);
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
it("loads without sending and requires explicit preview confirmation", async () => {
  const view = render(<MarketingPage />);
  await settle();
  expect(
    state.request.mock.calls.filter(([, o]) => o?.method === "POST"),
  ).toHaveLength(0);
  fireEvent.click(view.getByRole("button", { name: "marketing.preview" }));
  await settle();
  expect(view.getByRole("dialog").textContent).toContain(
    "<script>unsafe()</script>",
  );
  expect(document.querySelector("script")).toBeNull();
  expect(state.request.mock.calls.some(([p]) => p.endsWith("/confirm"))).toBe(
    false,
  );
  const dialog = view.getByRole("dialog");
  fireEvent.click(view.getByLabelText("marketing.send_confirm"));
  fireEvent.submit(dialog.querySelector("form")!);
  await settle();
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/marketing/campaigns/9/confirm",
    { method: "POST", body: { confirm: true } },
  );
});
it("does not interpret a failed request as an empty successful campaign list", async () => {
  state.request.mockRejectedValue(new Error("offline"));
  const view = render(<MarketingPage />);
  await settle();
  expect(view.getByRole("alert").textContent).toBeTruthy();
  expect(view.getByText("marketing.no_channel")).toBeTruthy();
});

it("offers only enabled gateways even when legacy IP metadata is enabled", async () => {
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("/channels")
        ? {
            channels: [
              {
                id: 1,
                name: "Supported gateway",
                kind: "dedicated_gateway",
                enabled: true,
              },
              {
                id: 2,
                name: "Legacy IP metadata",
                kind: "dedicated_ip",
                enabled: true,
              },
              {
                id: 3,
                name: "Disabled gateway",
                kind: "dedicated_gateway",
                enabled: false,
              },
            ],
          }
        : { templates: [], lists: [], campaigns: [] },
    ),
  );
  const view = render(<MarketingPage />);
  await settle();
  const choices = view.container.querySelector<HTMLSelectElement>(
    'select[name="channel_id"]',
  )!;
  expect(
    Array.from(choices.options).map((option) => option.textContent),
  ).toEqual(["marketing.choose", "Supported gateway"]);
  const legacy = view.getByText(/Legacy IP metadata/);
  expect(legacy.textContent).toContain("marketing.channel_unavailable");
  expect(legacy.textContent).not.toContain("marketing.enabled");
});
it("platform channel form exposes gateway configuration without an IP selector", async () => {
  const view = render(<MarketingChannelAdmin tenantID={1} />);
  expect(view.container.querySelector('[name="kind"]')).toBeNull();
  fireEvent.change(view.container.querySelector('input[name="name"]')!, {
    target: { value: "Dedicated gateway" },
  });
  fireEvent.change(view.container.querySelector('input[name="host"]')!, {
    target: { value: "relay.example.test" },
  });
  fireEvent.change(view.container.querySelector('input[name="from"]')!, {
    target: { value: "sender@example.test" },
  });
  fireEvent.submit(view.container.querySelector("form")!);
  await settle();
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/platform/tenants/1/marketing/channel",
    expect.objectContaining({
      body: expect.objectContaining({
        kind: "dedicated_gateway",
        smtp: expect.objectContaining({ dedicated: true }),
      }),
    }),
  );
});
