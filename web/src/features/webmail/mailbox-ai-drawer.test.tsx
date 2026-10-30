import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MailboxAIDrawer } from "./mailbox-ai-drawer";
import * as api from "./mailbox-api";

vi.mock("react-i18next", () => {
  const t = (key: string) => key;
  return { useTranslation: () => ({ t }) };
});
vi.mock("./mailbox-api", async (original) => ({
  ...(await original<typeof import("./mailbox-api")>()),
  fetchMailboxAITarget: vi.fn(),
  getMailboxSettings: vi.fn(),
  fetchMailboxAIContext: vi.fn(),
  previewMailboxAI: vi.fn(),
  runMailboxAI: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
    enabled: true,
    allowed: true,
    configuration_digest: "cfg",
    provider: "openai",
    model: "test",
  });
  vi.mocked(api.getMailboxSettings).mockResolvedValue({
    preferences: {},
  } as api.MailboxSettingsView);
  vi.mocked(api.fetchMailboxAIContext).mockResolvedValue({
    body: "selected content",
    subject: "A",
    truncated: false,
  });
  vi.mocked(api.previewMailboxAI).mockResolvedValue({
    content_digest: "content-hash",
    messages: [{ role: "user", content: "Exactly selected content" }],
  });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
const message = (uid: number, subject: string) => ({
  uid,
  subject,
  uid_validity: 9,
  from: [{ email: "sender@example.test" }],
  text: "selected content",
  date: "",
  flags: [],
  size: 1,
  attachments: [],
});
const props = () => ({
  open: true,
  onClose: vi.fn(),
  folders: [],
  selectedFolderEnc: "INBOX",
  onCitation: vi.fn(),
  onApplyAIProposal: vi.fn(),
});
async function preview() {
  await waitFor(() => expect(api.fetchMailboxAITarget).toHaveBeenCalled());
  fireEvent.click(screen.getByText("page.webmail.ai_quick_reply"));
  await screen.findByText(/Exactly selected content/);
  expect(api.runMailboxAI).not.toHaveBeenCalled();
}
it("exports only after the exact server preview is explicitly confirmed", async () => {
  vi.mocked(api.runMailboxAI).mockResolvedValue({
    result: { body: "Reply A" },
  });
  render(<MailboxAIDrawer {...props()} detail={message(1, "A")} />);
  await preview();
  fireEvent.click(screen.getByText("page.webmail.ai_confirm_execute"));
  await screen.findByText("Reply A");
  expect(api.runMailboxAI).toHaveBeenCalledWith(
    expect.objectContaining({
      uid: 1,
      operation: "draft_reply",
      content_digest: "content-hash",
    }),
    expect.any(AbortSignal),
  );
});
it("cancels the pending confirmation when its message changes", async () => {
  const p = props();
  const view = render(<MailboxAIDrawer {...p} detail={message(1, "A")} />);
  await preview();
  view.rerender(<MailboxAIDrawer {...p} detail={message(2, "B")} />);
  expect(screen.queryByText("page.webmail.ai_confirm_execute")).toBeNull();
  expect(api.runMailboxAI).not.toHaveBeenCalled();
});
it("never displays or applies a late proposal from another message", async () => {
  let complete!: (v: { result: api.MailboxAIResult }) => void;
  vi.mocked(api.runMailboxAI).mockImplementation(
    () =>
      new Promise((resolve) => {
        complete = resolve;
      }),
  );
  const p = props();
  const view = render(<MailboxAIDrawer {...p} detail={message(1, "A")} />);
  await preview();
  fireEvent.click(screen.getByText("page.webmail.ai_confirm_execute"));
  await waitFor(() => expect(api.runMailboxAI).toHaveBeenCalledTimes(1));
  view.rerender(<MailboxAIDrawer {...p} detail={message(2, "B")} />);
  await act(async () =>
    complete({ result: { body: "Confidential reply for A" } }),
  );
  expect(screen.queryByText("Confidential reply for A")).toBeNull();
  expect(p.onApplyAIProposal).not.toHaveBeenCalled();
});
