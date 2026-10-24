import { act, cleanup, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AuditPage } from "./audit-page";

const state = vi.hoisted(() => ({
  request: vi.fn(),
  tenant: { id: 1, role: "admin" },
  t: (key: string) => key,
  i18n: { exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/app/auth-context", () => ({
  useAuth: () => ({ currentTenant: state.tenant }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
const settle = async () => {
  await act(async () => {
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });
};
beforeEach(() => {
  state.tenant = { id: 1, role: "admin" };
  state.request.mockImplementation((path: string) =>
    Promise.resolve(
      path.includes("compliance/grants")
        ? {
            grants: [],
            recipients: [{ id: 7, email: "owner@example.test", role: "owner" }],
          }
        : {
            logs: [
              {
                id: 9,
                action: "POST /domains",
                created_at: "2026-10-24T00:00:00Z",
                detail: { status: 409, outcome: "failed" },
              },
            ],
            next_cursor: "opaque-next",
          },
    ),
  );
});
afterEach(() => {
  cleanup();
  state.request.mockReset();
});

it("binds filters and page cursors to explicit navigation", async () => {
  const view = render(<AuditPage />);
  await settle();
  expect(state.request).toHaveBeenCalledTimes(1);
  expect(view.getByText(/page.audit.outcome_failed/)).toBeTruthy();
  fireEvent.change(view.getByLabelText("page.audit.filter_action"), {
    target: { value: "POST /domains" },
  });
  fireEvent.click(view.getByRole("button", { name: "page.audit.apply" }));
  await settle();
  const path = state.request.mock.calls.at(-1)?.[0] as string;
  expect(new URL(path, "https://example.test").searchParams.get("action")).toBe(
    "POST /domains",
  );
  fireEvent.click(view.getByRole("button", { name: "page.audit.next" }));
  await settle();
  expect(
    new URL(
      state.request.mock.calls.at(-1)?.[0] as string,
      "https://example.test",
    ).searchParams.get("cursor"),
  ).toBe("opaque-next");
  fireEvent.click(view.getByRole("button", { name: "page.audit.previous" }));
  await settle();
  expect(
    new URL(
      state.request.mock.calls.at(-1)?.[0] as string,
      "https://example.test",
    ).searchParams.has("cursor"),
  ).toBe(false);
  expect(view.queryByText("page.audit.grants_title")).toBeNull();
});

it("only owners see explicit grant creation and its finite scope", async () => {
  state.tenant = { id: 1, role: "owner" };
  const view = render(<AuditPage />);
  await settle();
  expect(
    state.request.mock.calls.filter(
      ([, options]) => options?.method === "POST",
    ),
  ).toHaveLength(0);
  fireEvent.change(view.getByLabelText("page.audit.recipient"), {
    target: { value: "7" },
  });
  fireEvent.change(view.getByLabelText("page.audit.scope"), {
    target: { value: "export" },
  });
  fireEvent.change(view.getByLabelText("page.audit.expires"), {
    target: { value: "2099-01-01T12:00" },
  });
  fireEvent.change(view.getByLabelText("page.audit.reason"), {
    target: { value: "incident review" },
  });
  fireEvent.click(view.getByRole("button", { name: "page.audit.grant" }));
  await settle();
  const mutation = state.request.mock.calls.find(
    ([, options]) => options?.method === "POST",
  );
  expect(mutation?.[0]).toBe("/api/v1/compliance/grants");
  expect(mutation?.[1].body).toMatchObject({
    user_id: 7,
    scope: "export",
    reason: "incident review",
  });
  expect(typeof mutation?.[1].body.expires_at).toBe("string");
});
