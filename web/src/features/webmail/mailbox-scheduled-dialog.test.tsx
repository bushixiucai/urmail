import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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
  cancelScheduledSend: vi.fn(),
}));
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
