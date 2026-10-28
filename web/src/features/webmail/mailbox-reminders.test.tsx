import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { MailboxReminders } from "./mailbox-reminders";
import { PeopleCalendarPanel } from "./people-calendar-panel";
const state = vi.hoisted(() => ({
  list: vi.fn(),
  ack: vi.fn(),
  save: vi.fn(),
  events: vi.fn(),
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: "en", exists: () => false },
  }),
}));
vi.mock("./mailbox-invitation", () => ({
  InvitationStatus: () => null,
  SendInvitationsButton: () => null,
}));
vi.mock("./mailbox-people-api", async (original) => ({
  ...(await original<typeof import("./mailbox-people-api")>()),
  mailboxReminders: state.list,
  acknowledgeMailboxReminder: state.ack,
  peopleEvents: state.events,
  savePeopleEvent: state.save,
}));
let rows: {
  id: number;
  payload: {
    event_id: number;
    summary: string;
    starts_at: string;
    ends_at: string;
    timezone: string;
    recurrence_id: string;
    minutes_before: number;
    fire_at: string;
    acknowledged: boolean;
  };
  created_at: string;
}[];
beforeEach(() => {
  vi.clearAllMocks();
  rows = [
    {
      id: 41,
      payload: {
        event_id: 3,
        summary: "Private event",
        starts_at: "2026-10-28T12:00:00Z",
        ends_at: "2026-10-28T13:00:00Z",
        timezone: "UTC",
        recurrence_id: "",
        minutes_before: 10,
        fire_at: "2026-10-28T11:50:00Z",
        acknowledged: false,
      },
      created_at: "2026-10-28T11:50:00Z",
    },
  ];
  state.list.mockImplementation(async () => ({ reminders: [...rows] }));
  state.ack.mockImplementation(async () => {
    rows = [];
    return { acknowledged: true };
  });
  state.events.mockResolvedValue({ events: [], next_cursor: 0 });
  state.save.mockResolvedValue({});
});
afterEach(cleanup);
function mount(content = <MailboxReminders />) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={client}>{content}</QueryClientProvider>
    </MemoryRouter>,
  );
}
it("reads pending reminders and acknowledges only the selected notification", async () => {
  const view = mount();
  await view.findByText("Private event");
  expect(view.getByRole("link").getAttribute("href")).toBe(
    "/webmail/people?tab=calendar",
  );
  fireEvent.click(view.getByText("reminders.acknowledge"));
  await waitFor(() => expect(view.queryByText("Private event")).toBeNull());
  expect(state.ack).toHaveBeenCalledWith(41);
  expect(view.getByText("reminders.empty")).toBeTruthy();
});
it("does not hide a reminder when acknowledgement fails", async () => {
  state.ack.mockRejectedValue(new Error("offline"));
  const view = mount();
  await view.findByText("Private event");
  fireEvent.click(view.getByText("reminders.acknowledge"));
  await view.findByRole("alert");
  expect(view.getByText("Private event")).toBeTruthy();
});
it("reports list failures without claiming there are no reminders", async () => {
  state.list.mockRejectedValue(new Error("offline"));
  const view = mount();
  await view.findByRole("alert");
  expect(view.queryByText("reminders.empty")).toBeNull();
});
it("creates without reminders by default, bounds entries, and can clear them", async () => {
  const view = mount(<PeopleCalendarPanel />);
  fireEvent.click(view.getByText("page.webmail.event_add"));
  const title = view.getByLabelText("page.webmail.event_summary");
  fireEvent.change(title, { target: { value: "No reminders" } });
  fireEvent.click(view.getByText("action.save"));
  await waitFor(() => expect(state.save).toHaveBeenCalled());
  expect(state.save.mock.calls[0][0].reminders).toEqual([]);
  fireEvent.click(view.getByText("page.webmail.event_add"));
  for (let i = 0; i < 5; i++) fireEvent.click(view.getByText("reminders.add"));
  expect(view.getByText("reminders.add").hasAttribute("disabled")).toBe(true);
  const inputs = view.getAllByRole("spinbutton");
  expect(inputs).toHaveLength(5);
  expect(inputs[0].getAttribute("min")).toBe("0");
  expect(inputs[0].getAttribute("max")).toBe("40320");
  fireEvent.click(view.getByText("reminders.clear"));
  expect(view.queryAllByRole("spinbutton")).toHaveLength(0);
});
it("reads existing reminder settings, saves unique values, and explicitly clears them", async () => {
  const existing = {
    id: 7,
    version: 2,
    uid: "event-seven",
    summary: "Existing event",
    description: "",
    location: "",
    starts_at: new Date(Date.now() + 3600000).toISOString(),
    ends_at: new Date(Date.now() + 7200000).toISOString(),
    all_day: false,
    timezone: "UTC",
    rrule: "",
    status: "confirmed",
    occurrences: [],
    instances: [],
    attendees: [],
    recurrence_overrides: [],
    reminders: [5, 30],
  };
  state.events.mockResolvedValue({ events: [existing], next_cursor: 0 });
  const view = mount(<PeopleCalendarPanel />);
  await view.findByText("Existing event");
  fireEvent.click(view.getByText("action.edit"));
  const inputs = view.getAllByRole("spinbutton") as HTMLInputElement[];
  expect(inputs.map((input) => input.value)).toEqual(["5", "30"]);
  fireEvent.change(inputs[1], { target: { value: "5" } });
  fireEvent.click(view.getByText("action.save"));
  await waitFor(() => expect(state.save).toHaveBeenCalledTimes(1));
  expect(state.save.mock.calls[0][0].reminders).toEqual([5]);
  fireEvent.click(view.getByText("action.edit"));
  fireEvent.click(view.getByText("reminders.clear"));
  fireEvent.click(view.getByText("action.save"));
  await waitFor(() => expect(state.save).toHaveBeenCalledTimes(2));
  expect(state.save.mock.calls[1][0].reminders).toEqual([]);
});
