import { createRef } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MailboxInlineReply } from "./mailbox-inline-reply";
import type { ComposeControllerHandle } from "./mailbox-compose-dialog";
import * as api from "./mailbox-api";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("./mailbox-api", async (original) => ({
  ...(await original<typeof import("./mailbox-api")>()),
  getMailboxSettings: vi.fn(),
  listMailboxIdentities: vi.fn(),
  getMailboxAddress: vi.fn(() => "alice@example.test"),
  saveDraft: vi.fn(),
  getSendReceiptForDraft: vi.fn(),
  fileToOutgoingAttachment: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(api.getMailboxSettings).mockResolvedValue({
    address: "alice@example.test",
    display_name: "Alice",
    signature_text: "",
    signature_html: "",
    preferences: {},
    version: 1,
    updated_at: "2026-10-30T00:00:00Z",
  });
  vi.mocked(api.listMailboxIdentities).mockResolvedValue({
    identities: [
      { address: "alice@example.test", primary: true },
      { address: "team@example.test", primary: false },
    ],
  });
  vi.mocked(api.saveDraft).mockResolvedValue({
    saved: true,
    uid: 70,
    uid_validity: 9,
    folder: "Drafts",
    name_enc: "drafts",
  });
  vi.mocked(api.fileToOutgoingAttachment).mockResolvedValue({
    filename: "retained.txt",
    content_type: "text/plain",
    content_base64: "YQ==",
  });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
it("shares the same draft, sender, formatted source and attachments when expanding inline reply", async () => {
  const controller = createRef<ComposeControllerHandle>();
  const onOpenFullCompose = vi.fn();
  render(
    <MailboxInlineReply
      ref={controller}
      message={{
        uid: 5,
        uid_validity: 9,
        subject: "Source",
        from: [{ email: "bob@example.test" }],
        to: [{ email: "alice@example.test" }],
        text: "Original text",
        flags: [],
        size: 100,
        date: "2026-10-30T00:00:00Z",
        attachments: [],
      }}
      folderEnc="inbox"
      settings={null}
      identities={[]}
      onOpenFullCompose={onOpenFullCompose}
      onSent={vi.fn()}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_reply" }),
  );
  await waitFor(() =>
    expect(
      (screen.getByLabelText("page.webmail.compose_from") as HTMLSelectElement)
        .disabled,
    ).toBe(false),
  );
  fireEvent.change(screen.getByLabelText("page.webmail.compose_from"), {
    target: { value: "team@example.test" },
  });
  fireEvent.change(screen.getByLabelText("page.webmail.compose_to"), {
    target: { value: "edited@example.test" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_source_code" }),
  );
  const source = screen.getByRole("textbox", {
    name: "page.webmail.compose_source_code",
  });
  fireEvent.change(source, {
    target: { value: "<p><strong>Typed inline</strong></p>" },
  });
  fireEvent.change(screen.getByLabelText("page.webmail.compose_attach"), {
    target: { files: [new File(["a"], "retained.txt")] },
  });
  await screen.findByText("retained.txt");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.compose_maximize" }),
  );
  expect(
    screen.getByRole("textbox", { name: "page.webmail.compose_source_code" }),
  ).toBe(source);
  expect(
    (screen.getByLabelText("page.webmail.compose_from") as HTMLSelectElement)
      .value,
  ).toBe("team@example.test");
  expect(
    (screen.getByLabelText("page.webmail.compose_to") as HTMLInputElement)
      .value,
  ).toBe("edited@example.test");
  expect(onOpenFullCompose).not.toHaveBeenCalled();
  await controller.current?.flush();
  expect(vi.mocked(api.saveDraft).mock.calls.at(-1)?.[0]).toMatchObject({
    from: "team@example.test",
    to: ["edited@example.test"],
    html: "<p><strong>Typed inline</strong></p>",
    attachments: [{ filename: "retained.txt" }],
  });
});
