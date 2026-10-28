import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ApiRequestError } from "@/lib/api/client";
import {
  MailboxInvitationPanel,
  type CalendarMailInvitation,
} from "./mailbox-invitation-panel";
const state = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: "en", exists: () => false },
  }),
}));
vi.mock("./mailbox-api", async (original) => ({
  ...(await original<typeof import("./mailbox-api")>()),
  getMailboxToken: () => "test-mailbox",
  mailboxRequest: state.request,
}));
let invitation: CalendarMailInvitation;
beforeEach(() => {
  vi.clearAllMocks();
  invitation = {
    source_digest: "source-digest",
    verification: "verified",
    verification_reason: "DKIM_VERIFIED",
    method: "REQUEST",
    uid: "meeting@host",
    sequence: 1,
    organizer: "host@example.test",
    from: "host@example.test",
    recipient: "owner@example.test",
    recurrence_id: "",
    event: {
      summary: "Verified calendar meeting",
      description: "",
      location: "",
      starts_at: "2026-11-01T09:00:00Z",
      ends_at: "2026-11-01T10:00:00Z",
      timezone: "UTC",
      all_day: false,
      rrule: "",
    },
    linked_event: null,
    actions: ["accept", "decline", "tentative"],
  };
  state.request.mockImplementation(
    async (_path: string, options?: { method?: string }) =>
      options?.method === "POST"
        ? { applied: true, notification_status: "sent" }
        : invitation,
  );
});
afterEach(cleanup);
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrap = (uid: number) => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <MailboxInvitationPanel
          folderEnc="b64.SU5CT1g"
          uid={uid}
          uidValidity={9}
        />
      </QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(wrap(10));
  return { ...view, source: (uid: number) => view.rerender(wrap(uid)) };
}
it("requires explicit confirmation and submits only the original message identity", async () => {
  const view = mount();
  await view.findByText("Verified calendar meeting");
  expect(
    view.getByRole("button", { name: "invitation.action.accept" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(view.getByRole("checkbox"));
  fireEvent.click(
    view.getByRole("button", { name: "invitation.action.accept" }),
  );
  await view.findByRole("status");
  const call = state.request.mock.calls.find(
    (call) => call[1]?.method === "POST",
  );
  expect(call?.[1].body).toEqual({
    source: { folder_enc: "b64.SU5CT1g", uid: 10, uid_validity: 9 },
    source_digest: "source-digest",
    action: "accept",
    expected_version: 0,
    confirmed: true,
  });
  expect(JSON.stringify(call?.[1].body)).not.toContain("BEGIN:VCALENDAR");
});
it("never presents accept for an unverified source", async () => {
  invitation.verification = "unverified";
  invitation.actions = ["copy_personal"];
  const view = mount();
  await view.findByText("invitation.unverified");
  expect(
    view.queryByRole("button", { name: "invitation.action.accept" }),
  ).toBeNull();
  expect(
    view.getByRole("button", { name: "invitation.action.copy_personal" }),
  ).toBeTruthy();
});
it("resets confirmation for a different message", async () => {
  const view = mount();
  await view.findByRole("checkbox");
  fireEvent.click(view.getByRole("checkbox"));
  view.source(11);
  await waitFor(() =>
    expect(view.getByRole("checkbox")).toHaveProperty("checked", false),
  );
  expect(
    view.getByRole("button", { name: "invitation.action.accept" }),
  ).toHaveProperty("disabled", true);
});
it("keeps an unknown response disabled after reloading its durable state", async () => {
  invitation.notification_status = "uncertain";
  invitation.actions = [];
  const view = mount();
  await view.findByText("invitation.uncertain");
  expect(view.queryByRole("button")).toBeNull();
  expect(view.queryByText("invitation.no_action")).toBeNull();
});
it("does not hide source failures as an empty invitation", async () => {
  state.request.mockRejectedValue(
    new ApiRequestError(409, { error: { code: "CALENDAR_SOURCE_CHANGED" } }),
  );
  const view = mount();
  await view.findByRole("alert");
  expect(view.queryByText("invitation.no_action")).toBeNull();
});
