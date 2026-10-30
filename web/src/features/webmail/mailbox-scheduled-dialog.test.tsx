import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "@/lib/api/client";
import { MailboxScheduledDialog } from "./mailbox-scheduled-dialog";
import * as api from "./mailbox-api";

vi.mock("react-i18next", () => {
  const t = (key: string, params?: { status?: string }) =>
    params?.status ? `${key}:${params.status}` : key;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});
vi.mock("./mailbox-api", () => ({
  listScheduledSends: vi.fn(),
  listSendReceipts: vi.fn(),
  repairSentCopy: vi.fn(),
  cancelScheduledSend: vi.fn(),
  repairScheduledSentCopy: vi.fn(),
  acknowledgeUncertainScheduledSend: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(api.listSendReceipts).mockResolvedValue({ items: [] });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

function item(status: string, id = 1): api.ScheduledSendItem {
  return {
    id,
    status,
    draft_folder: "Drafts",
    draft_uid: 2,
    draft_uidvalidity: 4,
    send_at: "2099-01-01T01:00:00Z",
    attempts: 0,
    created_at: "",
    updated_at: "",
  };
}

describe("scheduled mail results", () => {
  it("shows all terminal results and only offers cancellation before submitting", async () => {
    vi.mocked(api.listScheduledSends).mockResolvedValue({
      items: [
        "pending",
        "preparing",
        "submitting",
        "sent",
        "uncertain",
        "failed",
        "cancelled",
        "held",
      ].map((status, i) => item(status, i + 1)),
    });
    render(<MailboxScheduledDialog open onOpenChange={vi.fn()} />);
    await screen.findByText(
      "page.webmail.scheduled_item:page.webmail.scheduled_status_sent",
    );
    const buttons = screen.getAllByRole("button", {
      name: "page.webmail.scheduled_cancel",
    });
    expect(buttons.map((button) => button.hasAttribute("disabled"))).toEqual([
      false,
      false,
      true,
      true,
      true,
      true,
    ]);
    expect(
      screen.getAllByText("page.webmail.scheduled_review_required"),
    ).toHaveLength(2);
  });

  it("refreshes an outdated pending row without claiming successful cancellation", async () => {
    vi.mocked(api.listScheduledSends)
      .mockResolvedValueOnce({ items: [item("pending")] })
      .mockResolvedValue({ items: [item("submitting")] });
    vi.mocked(api.cancelScheduledSend).mockRejectedValue(
      new ApiRequestError(409, {
        error: {
          code: "SCHEDULE_CANNOT_CANCEL",
          message: "error.SCHEDULE_CANNOT_CANCEL",
        },
      }),
    );
    render(<MailboxScheduledDialog open onOpenChange={vi.fn()} />);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "page.webmail.scheduled_cancel",
      }),
    );
    await screen.findByText("error.SCHEDULE_CANNOT_CANCEL");
    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "page.webmail.scheduled_cancel" })
          .hasAttribute("disabled"),
      ).toBe(true),
    );
    expect(api.cancelScheduledSend).toHaveBeenCalledTimes(1);
  });

  it("shows a retry time separately from the original appointment", async () => {
    vi.mocked(api.listScheduledSends).mockResolvedValue({
      items: [
        {
          ...item("pending"),
          attempts: 1,
          next_attempt_at: "2099-01-01T02:00:00Z",
          last_error: "SCHEDULE_RETRY",
        },
      ],
    });
    render(<MailboxScheduledDialog open onOpenChange={vi.fn()} />);
    await screen.findByText("page.webmail.scheduled_retry_at");
    expect(screen.getByText("error.SCHEDULE_RETRY")).toBeTruthy();
  });
});

it("repairs an accepted copy without exposing a resend action", async () => {
  const pending = {
    ...item("sent", 41),
    sent_copy_state: "held" as const,
    last_error: "RESTORE_RECONCILIATION_REQUIRED",
  };
  vi.mocked(api.listScheduledSends)
    .mockResolvedValueOnce({ items: [pending] })
    .mockResolvedValue({
      items: [
        { ...pending, sent_copy_state: "complete", last_error: undefined },
      ],
    });
  vi.mocked(api.repairScheduledSentCopy).mockResolvedValue({
    item: { ...pending, sent_copy_state: "complete" },
  });
  render(<MailboxScheduledDialog open onOpenChange={vi.fn()} />);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "page.webmail.repair_sent_copy",
    }),
  );
  await waitFor(() =>
    expect(api.repairScheduledSentCopy).toHaveBeenCalledWith(41),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "page.webmail.repair_sent_copy" }),
    ).toBeNull(),
  );
  expect(api.cancelScheduledSend).not.toHaveBeenCalled();
});

it.each(["uncertain", "held"])(
  "requires a checked review before releasing a %s attempt and never sends it",
  async (status) => {
    vi.mocked(api.listScheduledSends)
      .mockResolvedValueOnce({ items: [item(status, 91)] })
      .mockResolvedValue({ items: [item("cancelled", 91)] });
    vi.mocked(api.acknowledgeUncertainScheduledSend).mockResolvedValue(
      item("cancelled", 91),
    );
    render(<MailboxScheduledDialog open onOpenChange={vi.fn()} />);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "page.webmail.scheduled_uncertain_review_action",
      }),
    );
    const release = screen.getByRole("button", {
      name: "page.webmail.scheduled_uncertain_release",
    });
    expect(release.hasAttribute("disabled")).toBe(true);
    fireEvent.click(
      screen.getByRole("checkbox", {
        name: "page.webmail.scheduled_uncertain_confirm",
      }),
    );
    fireEvent.click(release);
    await waitFor(() =>
      expect(api.acknowledgeUncertainScheduledSend).toHaveBeenCalledWith(91),
    );
    expect(api.cancelScheduledSend).not.toHaveBeenCalled();
    await screen.findByText("page.webmail.scheduled_uncertain_released");
  },
);

it("finds a retained immediate-send copy after reopening and repairs without SMTP", async () => {
  const receipt: api.SendReceipt = {
    id: 77,
    request_key: "reopened-send-0001",
    status: "accepted",
    sent_copy_state: "held",
    message_id: "<reopened@fixture>",
    created_at: "2026-10-30T00:00:00Z",
  };
  vi.mocked(api.listScheduledSends).mockResolvedValue({ items: [] });
  vi.mocked(api.listSendReceipts)
    .mockResolvedValueOnce({ items: [receipt] })
    .mockResolvedValue({
      items: [{ ...receipt, sent_copy_state: "complete" }],
    });
  vi.mocked(api.repairSentCopy).mockResolvedValue({
    receipt: { ...receipt, sent_copy_state: "complete" },
  });
  render(<MailboxScheduledDialog open onOpenChange={vi.fn()} />);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "page.webmail.repair_sent_copy",
    }),
  );
  await waitFor(() => expect(api.repairSentCopy).toHaveBeenCalledWith(77));
  expect(api.cancelScheduledSend).not.toHaveBeenCalled();
  expect(api.acknowledgeUncertainScheduledSend).not.toHaveBeenCalled();
  await screen.findByText("page.webmail.receipt_copy_repaired");
});
