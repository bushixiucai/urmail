import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import { MailboxAIDrawer } from "./mailbox-ai-drawer";
import { MailboxAIAsk } from "./mailbox-ai-ask";
import * as api from "./mailbox-api";
const t = (key: string) => key;
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t }) }));
vi.mock("./mailbox-api", async (original) => ({
  ...(await original<typeof import("./mailbox-api")>()),
  fetchMailboxAITarget: vi.fn(),
  fetchMailboxAIBudget: vi.fn(),
  fetchMailboxAIContext: vi.fn(),
  previewMailboxAI: vi.fn(),
  runMailboxAI: vi.fn(),
  askMailboxAI: vi.fn(),
  fetchMailboxAICitation: vi.fn(),
  getMailboxSettings: vi.fn(),
  saveMailboxSettings: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
    enabled: true,
    allowed: true,
    provider: "openai",
    model: "original-model",
    configuration_digest: "target-a",
  });
  vi.mocked(api.fetchMailboxAIBudget).mockRejectedValue(
    new Error("No budget fixture"),
  );
  vi.mocked(api.fetchMailboxAIContext).mockResolvedValue({
    subject: "Subject",
    body: "Original",
    truncated: false,
  });
  vi.mocked(api.previewMailboxAI).mockResolvedValue({
    content_digest: "content-a",
    messages: [{ role: "user", content: "Original" }],
  });
  vi.mocked(api.getMailboxSettings).mockResolvedValue({
    address: "alice@example.test",
    display_name: "Alice",
    signature_html: "",
    signature_text: "",
    preferences: {},
    version: 1,
    updated_at: "2026-10-30T00:00:00Z",
  });
  vi.mocked(api.saveMailboxSettings).mockImplementation(async () => {
    window.dispatchEvent(new Event(api.MAILBOX_AI_CONFIGURATION_CHANGED));
    return { saved: true, version: 2 };
  });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

it("invalidates a translation when its requested language changes during generation", async () => {
  let complete!: (value: { result: api.MailboxAIResult }) => void;
  vi.mocked(api.runMailboxAI).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        complete = resolve;
      }),
  );
  render(
    <MailboxAIPanel
      mode="compose"
      body="Original"
      subject="Subject"
      onApply={vi.fn()}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_assistance" }),
  );
  await waitFor(() => expect(api.fetchMailboxAITarget).toHaveBeenCalled());
  fireEvent.change(screen.getByLabelText("page.webmail.ai_operation"), {
    target: { value: "translate" },
  });
  fireEvent.click(
    screen.getByRole("checkbox", { name: "page.webmail.ai_confirm_content" }),
  );
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.ai_generate",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_generate" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "page.webmail.ai_confirm_execute",
    }),
  );
  await waitFor(() => expect(api.runMailboxAI).toHaveBeenCalledTimes(1));
  fireEvent.change(screen.getByLabelText("page.webmail.ai_language"), {
    target: { value: "ja" },
  });
  await act(async () =>
    complete({ result: { translation: "Old English translation" } }),
  );
  expect(screen.queryByText("Old English translation")).toBeNull();
});

it("removes an existing drawer proposal when its provider configuration is saved", async () => {
  vi.mocked(api.runMailboxAI).mockResolvedValue({
    result: { body: "Proposal from original provider" },
  });
  const detail = {
    uid: 1,
    uid_validity: 9,
    subject: "Subject",
    from: [{ email: "sender@example.test" }],
    text: "Original",
    date: "",
    flags: [],
    size: 1,
    attachments: [],
  };
  render(
    <MailboxAIDrawer
      open
      onClose={vi.fn()}
      folders={[]}
      selectedFolderEnc="INBOX"
      detail={detail}
      onCitation={vi.fn()}
      onApplyAIProposal={vi.fn()}
    />,
  );
  await waitFor(() => expect(api.fetchMailboxAITarget).toHaveBeenCalled());
  fireEvent.click(screen.getByText("page.webmail.ai_quick_reply"));
  fireEvent.click(
    await screen.findByRole("button", {
      name: "page.webmail.ai_confirm_execute",
    }),
  );
  await screen.findByText("Proposal from original provider");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_tab_config" }),
  );
  fireEvent.click(screen.getByLabelText("page.webmail.ai_use_custom"));
  fireEvent.change(screen.getByLabelText("page.webmail.ai_model_label"), {
    target: { value: "new-model" },
  });
  vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
    enabled: true,
    allowed: true,
    provider: "openai",
    model: "new-model",
    configuration_digest: "target-b",
  });
  fireEvent.click(screen.getByRole("button", { name: "action.save" }));
  await waitFor(() =>
    expect(api.fetchMailboxAITarget).toHaveBeenCalledTimes(2),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_tab_current" }),
  );
  expect(screen.queryByText("Proposal from original provider")).toBeNull();
});

it("invalidates a separate compose AI panel when the drawer saves a new target", async () => {
  vi.mocked(api.runMailboxAI).mockResolvedValue({
    result: { draft: "Existing panel proposal" },
  });
  const detail = {
    uid: 1,
    uid_validity: 9,
    subject: "Subject",
    from: [{ email: "sender@example.test" }],
    text: "Original",
    date: "",
    flags: [],
    size: 1,
    attachments: [],
  };
  render(
    <>
      <MailboxAIPanel
        mode="compose"
        body="Original"
        subject="Subject"
        onApply={vi.fn()}
      />
      <MailboxAIDrawer
        open
        onClose={vi.fn()}
        folders={[]}
        selectedFolderEnc="INBOX"
        detail={detail}
        onCitation={vi.fn()}
      />
    </>,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_assistance" }),
  );
  await waitFor(() =>
    expect(api.fetchMailboxAITarget).toHaveBeenCalledTimes(2),
  );
  fireEvent.click(
    screen.getByRole("checkbox", { name: "page.webmail.ai_confirm_content" }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_generate" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "page.webmail.ai_confirm_execute",
    }),
  );
  await screen.findByText("Existing panel proposal");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_tab_config" }),
  );
  fireEvent.click(screen.getByLabelText("page.webmail.ai_use_custom"));
  fireEvent.change(screen.getByLabelText("page.webmail.ai_model_label"), {
    target: { value: "new-model" },
  });
  vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
    enabled: true,
    allowed: true,
    provider: "openai",
    model: "new-model",
    configuration_digest: "target-b",
  });
  fireEvent.click(screen.getByRole("button", { name: "action.save" }));
  await waitFor(() => expect(api.saveMailboxSettings).toHaveBeenCalledTimes(1));
  expect(screen.queryByText("Existing panel proposal")).toBeNull();
});

it("invalidates a separate grounded answer when the drawer saves a new target", async () => {
  vi.mocked(api.askMailboxAI).mockResolvedValue({
    text: "Earlier grounded answer",
    grounded: true,
    citations: ["1"],
  });
  vi.mocked(api.fetchMailboxAICitation).mockResolvedValue({
    id: "1",
    folder_enc: "inbox",
    uid: 1,
    uid_validity: 9,
    quote: "Authorized source",
  });
  const detail = {
    uid: 1,
    uid_validity: 9,
    subject: "Subject",
    from: [{ email: "sender@example.test" }],
    text: "Original",
    date: "",
    flags: [],
    size: 1,
    attachments: [],
  };
  render(
    <>
      <MailboxAIAsk folders={[]} onCitation={vi.fn()} />
      <MailboxAIDrawer
        open
        onClose={vi.fn()}
        folders={[]}
        selectedFolderEnc="INBOX"
        detail={detail}
        onCitation={vi.fn()}
      />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "page.webmail.ai_ask" }));
  await waitFor(() =>
    expect(api.fetchMailboxAITarget).toHaveBeenCalledTimes(2),
  );
  fireEvent.change(screen.getByLabelText("page.webmail.ai_question"), {
    target: { value: "What is the invoice?" },
  });
  fireEvent.click(
    screen.getByRole("checkbox", { name: "page.webmail.ai_confirm_content" }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_ask_submit" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "page.webmail.ai_confirm_execute",
    }),
  );
  await screen.findByText("Earlier grounded answer");
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_tab_config" }),
  );
  fireEvent.click(screen.getByLabelText("page.webmail.ai_use_custom"));
  fireEvent.change(screen.getByLabelText("page.webmail.ai_model_label"), {
    target: { value: "new-model" },
  });
  vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
    enabled: true,
    allowed: true,
    provider: "openai",
    model: "new-model",
    configuration_digest: "target-b",
  });
  fireEvent.click(screen.getByRole("button", { name: "action.save" }));
  await waitFor(() => expect(api.saveMailboxSettings).toHaveBeenCalledTimes(1));
  expect(screen.queryByText("Earlier grounded answer")).toBeNull();
});

it("cancels a pending confirmation across configuration invalidation without exporting content", async () => {
  render(
    <MailboxAIDrawer
      open
      onClose={vi.fn()}
      folders={[]}
      selectedFolderEnc="INBOX"
      detail={null}
      onCitation={vi.fn()}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_tab_ask" }),
  );
  await waitFor(() => expect(api.fetchMailboxAITarget).toHaveBeenCalled());
  fireEvent.change(screen.getByLabelText("page.webmail.ai_question"), {
    target: { value: "What is the invoice?" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_ask_submit" }),
  );
  await screen.findByRole("button", {
    name: "page.webmail.ai_confirm_execute",
  });
  act(() =>
    window.dispatchEvent(new Event(api.MAILBOX_AI_CONFIGURATION_CHANGED)),
  );
  expect(
    screen.queryByRole("button", { name: "page.webmail.ai_confirm_execute" }),
  ).toBeNull();
  expect(api.askMailboxAI).not.toHaveBeenCalled();
});
