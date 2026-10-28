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
  draftFromMessage,
  MailboxComposeDialog,
  chooseMailboxIdentity,
} from "./mailbox-compose-dialog";
import * as mailboxApi from "./mailbox-api";

vi.mock("react-i18next", () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});
vi.mock("./mailbox-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./mailbox-api")>()),
  saveDraft: vi.fn(),
  getMailboxSettings: vi.fn(),
  listMailboxIdentities: vi.fn(),
  getMailboxAddress: vi.fn(() => "alice@example.test"),
  scheduleSend: vi.fn(),
  sendMessage: vi.fn(),
  deleteDraft: vi.fn(),
  fileToOutgoingAttachment: vi.fn(),
}));

beforeEach(() => {
  vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
    address: "alice@example.test",
    display_name: "Alice",
    signature_text: "",
    signature_html: "",
    preferences: {},
    version: 1,
    updated_at: "2026-01-01T00:00:00Z",
  });
  vi.mocked(mailboxApi.listMailboxIdentities).mockResolvedValue({
    identities: [
      { address: "alice@example.test", primary: true },
      { address: "team@example.test", primary: false },
    ],
  });
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

async function compose(
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
  await waitFor(() =>
    expect(
      (screen.getByLabelText("page.webmail.compose_from") as HTMLSelectElement)
        .disabled,
    ).toBe(false),
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
    await compose();
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
    await compose(
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
    const { onOpenChange } = await compose();
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

  it("warns without closing when the Sent copy save is pending", async () => {
    vi.mocked(mailboxApi.sendMessage).mockResolvedValue({
      sent: true,
      sent_copy: "pending",
    });
    const { onOpenChange } = await compose();
    fillMessage();
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_send" }),
    );
    await screen.findByText("page.webmail.compose_sent_copy_pending");
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
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
    await compose();
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
    await compose();
    fillMessage();
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
    );
    await screen.findByText("page.webmail.compose_cleanup_pending");
  });
});

it("enforces the aggregate attachment budget while preserving selected files", async () => {
  await compose();
  const size = mailboxApi.MAX_SEND_ATTACHMENT_BYTES;
  const padding = (3 - (size % 3)) % 3;
  const encoded =
    "A".repeat(Math.ceil(size / 3) * 4 - padding) + "=".repeat(padding);
  vi.mocked(mailboxApi.fileToOutgoingAttachment).mockImplementation(
    async (file) => ({
      filename: file.name,
      content_type: "application/octet-stream",
      content_base64: encoded,
    }),
  );
  const files = ["a.bin", "b.bin", "c.bin"].map((name) => {
    const file = new File([], name);
    Object.defineProperty(file, "size", { value: size });
    return file;
  });
  fireEvent.change(screen.getByLabelText("page.webmail.compose_attach"), {
    target: { files },
  });
  await screen.findByText("page.webmail.compose_attach_total_limit");
  expect(screen.getByText("a.bin")).toBeTruthy();
  expect(screen.getByText("b.bin")).toBeTruthy();
  expect(screen.queryByText("c.bin")).toBeNull();
  expect(mailboxApi.fileToOutgoingAttachment).toHaveBeenCalledTimes(2);
  fireEvent.click(
    screen.getAllByRole("button", {
      name: /page\.webmail\.compose_attach_remove_named/,
    })[0],
  );
  fireEvent.change(screen.getByLabelText("page.webmail.compose_attach"), {
    target: { files: [files[2]] },
  });
  await screen.findByText("c.bin");
  expect(screen.getByText("b.bin")).toBeTruthy();
});

it("chooses a granted recipient identity for replies and keeps revoked drafts visible", () => {
  const settings = {
    address: "alice@example.test",
    display_name: "",
    signature_text: "",
    signature_html: "",
    preferences: {
      default_from: "alice@example.test",
      reply_identity: "recipient",
    },
    version: 1,
    updated_at: "",
  };
  const identities = [
    { address: "alice@example.test", primary: true },
    { address: "team@example.test", primary: false },
  ];
  expect(
    chooseMailboxIdentity(settings, identities, {
      mode: "reply",
      replyRecipients: ["team@example.test"],
    }),
  ).toBe("team@example.test");
  expect(
    chooseMailboxIdentity(settings, identities, {
      mode: "reply",
      replyRecipients: ["outsider@example.test"],
    }),
  ).toBe("alice@example.test");
  expect(
    chooseMailboxIdentity(settings, identities, {
      mode: "draft",
      from: "revoked@example.test",
    }),
  ).toBe("revoked@example.test");
});
it("changes signatures without replacing manually authored text or recipients", async () => {
  vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
    address: "alice@example.test",
    display_name: "Alice",
    signature_text: "Personal signature",
    signature_html: "",
    identity_profiles: {
      "team@example.test": {
        display_name: "Team",
        signature_text: "Team signature",
        signature_html: "<b>Team signature</b>",
      },
    },
    preferences: {},
    version: 1,
    updated_at: "",
  });
  await compose({ mode: "new", to: "bob@example.test", text: "My words" });
  await waitFor(() =>
    expect(
      (
        screen.getByLabelText(
          "page.webmail.compose_body",
        ) as HTMLTextAreaElement
      ).value,
    ).toContain("Personal signature"),
  );
  fireEvent.change(screen.getByLabelText("page.webmail.compose_from"), {
    target: { value: "team@example.test" },
  });
  const body = (
    screen.getByLabelText("page.webmail.compose_body") as HTMLTextAreaElement
  ).value;
  expect(body).toContain("My words");
  expect(body).toContain("Team signature");
  expect(body).not.toContain("Personal signature");
  expect(
    (screen.getByLabelText("page.webmail.compose_to") as HTMLInputElement)
      .value,
  ).toBe("bob@example.test");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_save_draft" }),
  );
  await waitFor(() =>
    expect(mailboxApi.saveDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        from: "team@example.test",
        text: expect.stringContaining("My words"),
        html: expect.stringContaining("My words"),
      }),
    ),
  );
});

it("keeps all valid Reply-To addresses and excludes every granted self alias", async () => {
  const source = {
    uid: 5,
    uid_validity: 9,
    date: "2026-10-27T10:00:00Z",
    flags: [],
    size: 100,
    subject: "Question",
    from: [{ email: "author@example.test" }],
    to: [{ email: "team@example.test" }],
    reply_to: [
      { email: "one@example.test" },
      { email: "two@example.test" },
      { email: "team@example.test" },
    ],
    text: "Original source",
    attachments: [],
  };
  const draft = draftFromMessage("reply", source);
  await compose(draft);
  await waitFor(() =>
    expect(
      (screen.getByLabelText("page.webmail.compose_from") as HTMLSelectElement)
        .value,
    ).toBe("team@example.test"),
  );
  expect(
    (screen.getByLabelText("page.webmail.compose_to") as HTMLInputElement)
      .value,
  ).toBe("one@example.test, two@example.test");
  expect(
    (screen.getByLabelText("page.webmail.compose_bcc") as HTMLInputElement)
      .value,
  ).toBe("");
});
it("loads the quote preference, toggles only the original quote, and preserves manual text", async () => {
  vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
    address: "alice@example.test",
    display_name: "",
    signature_text: "",
    signature_html: "",
    preferences: { quote_original: false },
    version: 1,
    updated_at: "",
  });
  const draft = draftFromMessage("reply", {
    uid: 5,
    date: "2026-10-27T10:00:00Z",
    flags: [],
    size: 100,
    subject: "Hello",
    from: [{ email: "bob@example.test" }],
    text: "Original content",
    attachments: [],
  });
  await compose(draft);
  const body = screen.getByLabelText(
    "page.webmail.compose_body",
  ) as HTMLTextAreaElement;
  await waitFor(() => expect(body.value).toBe(""));
  const checkbox = screen.getByLabelText(
    "page.webmail.compose_quote_original",
  ) as HTMLInputElement;
  expect(checkbox.checked).toBe(false);
  fireEvent.click(checkbox);
  expect(body.value).toContain("Original content");
  fireEvent.change(body, { target: { value: "My reply" + body.value } });
  fireEvent.click(checkbox);
  expect(body.value).toBe("My reply");
});
it("does not rewrite a saved draft when the default quote preference is disabled", async () => {
  vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
    address: "alice@example.test",
    display_name: "",
    signature_text: "",
    signature_html: "",
    preferences: { quote_original: false },
    version: 1,
    updated_at: "",
  });
  await compose({
    mode: "draft",
    uid: 5,
    uidValidity: 9,
    text: "My saved words\n\nOriginal quotation",
  });
  await waitFor(() => expect(mailboxApi.getMailboxSettings).toHaveBeenCalled());
  expect(
    (screen.getByLabelText("page.webmail.compose_body") as HTMLTextAreaElement)
      .value,
  ).toBe("My saved words\n\nOriginal quotation");
  expect(
    screen.queryByLabelText("page.webmail.compose_quote_original"),
  ).toBeNull();
});

it("freezes the selected sender and reply source into a scheduled draft", async () => {
  vi.mocked(mailboxApi.scheduleSend).mockResolvedValue({
    id: 1,
  } as mailboxApi.ScheduledSendItem);
  const source = { folder_enc: "b64.SU5CT1g", uid: 9, uid_validity: 42 };
  await compose({
    mode: "reply",
    from: "team@example.test",
    to: "bob@example.test",
    subject: "Re: thread",
    text: "Reply body",
    inReplyTo: "<original@example.test>",
    aiSource: source,
  });
  await waitFor(() =>
    expect(screen.getByLabelText("page.webmail.compose_from")).toHaveProperty(
      "value",
      "team@example.test",
    ),
  );
  fireEvent.change(screen.getByLabelText("page.webmail.compose_send_later"), {
    target: { value: "2099-01-01T12:00" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_schedule" }),
  );
  await waitFor(() => expect(mailboxApi.scheduleSend).toHaveBeenCalledTimes(1));
  expect(mailboxApi.saveDraft).toHaveBeenCalledWith(
    expect.objectContaining({
      from: "team@example.test",
      in_reply_to: "<original@example.test>",
      reply_source: source,
      text: "Reply body",
    }),
  );
});

it("waits for identity and quote settings before allowing any outbound mutation", async () => {
  let release!: (value: mailboxApi.MailboxSettingsView) => void;
  vi.mocked(mailboxApi.getMailboxSettings).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        release = resolve;
      }),
  );
  render(
    <MailboxComposeDialog
      open
      draft={{ mode: "new" }}
      onOpenChange={vi.fn()}
      onSent={vi.fn()}
    />,
  );
  fillMessage();
  for (const key of [
    "page.webmail.compose_save_draft",
    "page.webmail.compose_send",
    "page.webmail.compose_schedule",
  ]) {
    const button = screen.getByRole("button", { name: key });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(button);
  }
  expect(mailboxApi.sendMessage).not.toHaveBeenCalled();
  expect(mailboxApi.saveDraft).not.toHaveBeenCalled();
  expect(mailboxApi.scheduleSend).not.toHaveBeenCalled();
  release({
    address: "alice@example.test",
    display_name: "Alice",
    signature_text: "",
    signature_html: "",
    preferences: { quote_original: false },
    version: 1,
    updated_at: "2026-01-01T00:00:00Z",
  });
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.compose_send",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_send" }),
  );
  await waitFor(() => expect(mailboxApi.sendMessage).toHaveBeenCalledTimes(1));
});
it("keeps outbound actions disabled when identity settings fail", async () => {
  vi.mocked(mailboxApi.getMailboxSettings).mockRejectedValueOnce(
    new Error("settings unavailable"),
  );
  render(
    <MailboxComposeDialog
      open
      draft={{ mode: "new" }}
      onOpenChange={vi.fn()}
      onSent={vi.fn()}
    />,
  );
  await screen.findByText("page.webmail.settings_load_failed");
  expect(
    (
      screen.getByRole("button", {
        name: "page.webmail.compose_send",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
