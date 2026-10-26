import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "@/lib/api/client";
import {
  composeFromSavedDraft,
  MailboxComposeDialog,
} from "./mailbox-compose-dialog";
import * as mailboxApi from "./mailbox-api";

vi.mock("react-i18next", () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});
vi.mock("./mailbox-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./mailbox-api")>()),
  saveDraft: vi.fn(),
  scheduleSend: vi.fn(),
  sendMessage: vi.fn(),
  deleteDraft: vi.fn(),
}));

beforeEach(() => {
  vi.mocked(mailboxApi.saveDraft).mockResolvedValue({
    saved: true,
    uid: 77,
    uid_validity: 9,
    folder: "Drafts",
    name_enc: "drafts",
  });
  vi.mocked(mailboxApi.sendMessage).mockResolvedValue({ sent: true });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

function compose(
  draft: Parameters<typeof MailboxComposeDialog>[0]["draft"] = { mode: "new" },
) {
  const onOpenChange = vi.fn();
  const onDraftSaved = vi.fn();
  render(
    <MailboxComposeDialog
      open
      draft={draft}
      onOpenChange={onOpenChange}
      onSent={vi.fn()}
      onDraftSaved={onDraftSaved}
    />,
  );
  return { onOpenChange, onDraftSaved };
}
function fillMessage() {
  fireEvent.change(screen.getByLabelText("page.webmail.compose_to"), {
    target: { value: "bob@example.test" },
  });
  fireEvent.change(screen.getByLabelText("page.webmail.compose_subject"), {
    target: { value: "Current subject" },
  });
  fireEvent.change(screen.getByLabelText("page.webmail.compose_body"), {
    target: { value: "Current body" },
  });
}

describe("mailbox compose persistence", () => {
  it("keeps current edits after saving and uses the replacement UID validity", async () => {
    compose();
    fillMessage();
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
    );
    await screen.findByText("page.webmail.compose_draft_saved");
    expect(
      (
        screen.getByLabelText(
          "page.webmail.compose_body",
        ) as HTMLTextAreaElement
      ).value,
    ).toBe("Current body");
    fireEvent.change(screen.getByLabelText("page.webmail.compose_body"), {
      target: { value: "Updated body" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
    );
    await waitFor(() => expect(mailboxApi.saveDraft).toHaveBeenCalledTimes(2));
    expect(vi.mocked(mailboxApi.saveDraft).mock.calls[1][0]).toMatchObject({
      uid: 77,
      uid_validity: 9,
      subject: "Current subject",
      text: "Updated body",
    });
  });

  it("preserves Bcc, HTML-only body and attachments on resume and send", async () => {
    const attachment = {
      filename: "a.txt",
      content_type: "text/plain",
      content_base64: "YQ==",
    };
    compose(
      composeFromSavedDraft(
        {
          uid: 8,
          uid_validity: 4,
          subject: "HTML",
          from: [],
          date: "",
          flags: [],
          size: 1,
          to: [{ email: "bob@example.test" }],
          bcc: [{ email: "hidden@example.test" }],
          html: "<p>Keep me</p>",
          attachments: [],
        },
        [attachment],
      ),
    );
    expect(
      (screen.getByLabelText("page.webmail.compose_bcc") as HTMLInputElement)
        .value,
    ).toBe("hidden@example.test");
    expect(
      (
        screen.getByLabelText(
          "page.webmail.compose_html",
        ) as HTMLTextAreaElement
      ).value,
    ).toBe("<p>Keep me</p>");
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_send" }),
    );
    await waitFor(() =>
      expect(mailboxApi.sendMessage).toHaveBeenCalledWith(
        expect.objectContaining({
          bcc: ["hidden@example.test"],
          html: "<p>Keep me</p>",
          attachments: [attachment],
        }),
      ),
    );
  });

  it("replays an unconfirmed schedule with the identical key and saved draft", async () => {
    vi.mocked(mailboxApi.scheduleSend)
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce({ id: 1 } as mailboxApi.ScheduledSendItem);
    const { onOpenChange } = compose();
    fillMessage();
    fireEvent.change(screen.getByLabelText("page.webmail.compose_send_later"), {
      target: { value: "2099-01-01T12:00" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_schedule" }),
    );
    await screen.findByText("page.webmail.compose_schedule_uncertain");
    expect(
      screen
        .getByLabelText("page.webmail.compose_body")
        .hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_schedule" }),
    );
    await waitFor(() =>
      expect(mailboxApi.scheduleSend).toHaveBeenCalledTimes(2),
    );
    const calls = vi.mocked(mailboxApi.scheduleSend).mock.calls;
    expect(calls[1]).toEqual(calls[0]);
    expect(calls[0][0]).toMatchObject({ draft_uid: 77, draft_uidvalidity: 9 });
    expect(mailboxApi.saveDraft).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("blocks repeated send after an uncertain SMTP result", async () => {
    vi.mocked(mailboxApi.sendMessage).mockRejectedValue(
      new ApiRequestError(409, {
        error: {
          code: "MAILBOX_SEND_UNCERTAIN",
          message: "error.MAILBOX_SEND_UNCERTAIN",
        },
      }),
    );
    compose();
    fillMessage();
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_send" }),
    );
    await screen.findByText("error.MAILBOX_SEND_UNCERTAIN");
    expect(
      screen
        .getByRole("button", { name: "page.webmail.compose_send" })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(mailboxApi.sendMessage).toHaveBeenCalledTimes(1);
  });

  it("reports saved drafts whose previous copy could not be removed", async () => {
    vi.mocked(mailboxApi.saveDraft).mockResolvedValue({
      saved: true,
      cleanup_pending: true,
      uid: 77,
      uid_validity: 9,
      folder: "Drafts",
      name_enc: "drafts",
    });
    compose();
    fillMessage();
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
    );
    await screen.findByText("page.webmail.compose_cleanup_pending");
  });
});
