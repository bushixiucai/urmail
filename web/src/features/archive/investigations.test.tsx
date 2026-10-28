import {
  act,
  cleanup,
  fireEvent,
  render,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ArchiveInvestigations } from "./investigations";
import { ApiRequestError } from "@/lib/api/client";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string) => key,
  i18n: { exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
  getSessionToken: () => "fixture-session",
}));
const row = {
  id: 1,
  title: "Boundary case",
  purpose: "Approved bounded scope",
  version: 7,
  status: "open",
  expires_at: "2099-01-01T00:00:00Z",
  created_by: 2,
};
function fixture() {
  const pending = (id: number, user: number) => ({
    id,
    requester_user_id: user,
    approver_user_id: null,
    state: "pending",
    expires_at: row.expires_at,
    source_digest: "a".repeat(64),
    stale: false,
    manifest_sha256: "",
    decision_reason: "",
  });
  return {
    case: row,
    members: [],
    sources: [
      {
        id: 11,
        subject: "Source",
        size_bytes: 3,
        raw_sha256: "a".repeat(64),
        legal_hold: false,
        deleted: false,
      },
    ],
    approvals: [
      pending(1, 2),
      pending(2, 3),
      {
        ...pending(3, 2),
        state: "approved",
        stale: true,
        manifest_sha256: "b".repeat(64),
      },
    ],
    source_digest: "a".repeat(64),
    actor_id: 2,
    can_manage: true,
    can_export: true,
  };
}
async function settle() {
  await act(async () => {
    for (let i = 0; i < 15; i++) await Promise.resolve();
  });
}
async function setup(detail = fixture()) {
  state.request.mockImplementation(
    async (path: string, options?: { method?: string }) => {
      if (options?.method) return { ok: true };
      if (path.endsWith("eligible-members")) return { members: [] };
      if (path === "/api/v1/archive/cases") return { cases: [row] };
      return detail;
    },
  );
  const view = render(<ArchiveInvestigations />);
  await settle();
  fireEvent.click(view.getByRole("button", { name: /Boundary case/ }));
  await settle();
  return view;
}
afterEach(() => {
  cleanup();
  state.request.mockReset();
  vi.unstubAllGlobals();
});
it("offers independent decision only to another actor and binds the exact approved digest", async () => {
  const view = await setup();
  const own = within(
    view.container.querySelector('[data-approval-id="1"]')! as HTMLElement,
  );
  const other = within(
    view.container.querySelector('[data-approval-id="2"]')! as HTMLElement,
  );
  expect(
    own.queryByRole("button", { name: "archive.case.approve" }),
  ).toBeNull();
  expect(
    own.getByRole("button", { name: "archive.case.withdraw" }),
  ).toHaveProperty("disabled", true);
  const approve = other.getByRole("button", { name: "archive.case.approve" });
  expect(approve).toHaveProperty("disabled", true);
  fireEvent.change(other.getByLabelText("archive.case.reason"), {
    target: { value: "Independent review of exact source set" },
  });
  fireEvent.click(approve);
  await settle();
  expect(state.request).toHaveBeenCalledWith(
    "/api/v1/archive/cases/1/approvals/2/decision",
    {
      method: "POST",
      body: {
        decision: "approved",
        digest: "a".repeat(64),
        reason: "Independent review of exact source set",
      },
    },
  );
});
it("hides preparation and downloads for a stale approved source set", async () => {
  const view = await setup();
  const stale = within(
    view.container.querySelector('[data-approval-id="3"]')! as HTMLElement,
  );
  expect(stale.getByText("archive.case.stale")).toBeTruthy();
  expect(
    stale.queryByRole("button", { name: "archive.case.download" }),
  ).toBeNull();
  expect(
    stale.queryByRole("button", { name: "archive.case.prepare" }),
  ).toBeNull();
});
it("keeps create disabled when independent content authorization is denied", async () => {
  state.request.mockRejectedValue(
    new ApiRequestError(403, {
      error: { code: "ARCHIVE_CASE_GRANT_REQUIRED" },
    }),
  );
  const view = render(<ArchiveInvestigations />);
  await settle();
  fireEvent.change(view.getByLabelText("archive.case.name"), {
    target: { value: "Case without authority" },
  });
  fireEvent.change(view.getByLabelText("archive.case.purpose"), {
    target: { value: "Cannot bypass missing grant" },
  });
  expect(
    view.getByRole("button", { name: "archive.case.create" }),
  ).toHaveProperty("disabled", true);
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
});
it("cancels an active download without saving a partial ZIP", async () => {
  const detail = fixture();
  detail.approvals = [
    {
      ...detail.approvals[0],
      state: "approved",
      stale: false,
      manifest_sha256: "b".repeat(64),
    },
  ];
  const view = await setup(detail);
  let signal: AbortSignal | undefined;
  const fetcher = vi.fn(
    (_url: string, options: RequestInit) =>
      new Promise((_resolve, reject) => {
        signal = options.signal!;
        signal.addEventListener("abort", () =>
          reject(new DOMException("cancelled", "AbortError")),
        );
      }),
  );
  vi.stubGlobal("fetch", fetcher);
  fireEvent.click(view.getByRole("button", { name: "archive.case.download" }));
  await settle();
  fireEvent.click(
    view.getByRole("button", { name: "archive.case.cancel_download" }),
  );
  await settle();
  expect(signal?.aborted).toBe(true);
  expect(view.getByRole("status").textContent).toBe("archive.case.cancelled");
  expect(
    state.request.mock.calls.every(([, options]) => !options?.method),
  ).toBe(true);
});
