import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MailboxAIPanel } from "./mailbox-ai-panel";
import * as api from "./mailbox-api";
const translate = (key: string) => key;
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: translate }) }));
vi.mock("./mailbox-api", async (original) => ({
  ...(await original<typeof import("./mailbox-api")>()),
  fetchMailboxAITarget: vi.fn(),
  fetchMailboxAIContext: vi.fn(),
  fetchMailboxAIBudget: vi.fn(),
  runMailboxAI: vi.fn(),
}));
beforeEach(() => {
  vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
    enabled: true,
    allowed: true,
    provider: "openai",
    configuration_digest: "a".repeat(64),
  });
  vi.mocked(api.fetchMailboxAIContext).mockResolvedValue({
    subject: "Invoice",
    body: "Original invoice text",
    truncated: false,
  });
  vi.mocked(api.fetchMailboxAIBudget).mockResolvedValue({
    enabled: true,
    cost_currency: "CNY",
    monthly: {
      tokens_used: 10,
      tokens_limit: 1000,
      cost_micros_used: 10,
      cost_micros_limit: 1000,
    },
    daily: {
      tokens_used: 10,
      tokens_limit: 100,
      cost_micros_used: 10,
      cost_micros_limit: 100,
    },
  });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
async function start() {
  fireEvent.click(
    screen.getByRole("button", { name: "page.webmail.ai_assistance" }),
  );
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.ai_generate",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true),
  );
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
}

describe("native mailbox AI review boundaries", () => {
  it("ignores a generation that completes after cancellation", async () => {
    let resolve!: (value: { result: api.MailboxAIResult }) => void;
    vi.mocked(api.runMailboxAI).mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const apply = vi.fn();
    render(
      <MailboxAIPanel
        mode="compose"
        subject="Invoice"
        body="Original"
        onApply={apply}
      />,
    );
    await start();
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.ai_cancel" }),
    );
    await act(async () => resolve({ result: { draft: "Late text" } }));
    expect(screen.queryByText("Late text")).toBeNull();
    expect(apply).not.toHaveBeenCalled();
  });
  it("prevents an old suggestion overwriting a manually edited draft", async () => {
    vi.mocked(api.runMailboxAI).mockResolvedValue({
      result: { draft: "Suggested text" },
    });
    const apply = vi.fn();
    const view = render(
      <MailboxAIPanel
        mode="compose"
        subject="Invoice"
        body="Original"
        onApply={apply}
      />,
    );
    await start();
    await screen.findByText("Suggested text");
    view.rerender(
      <MailboxAIPanel
        mode="compose"
        subject="Invoice"
        body="My newer edit"
        onApply={apply}
      />,
    );
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.ai_apply",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(apply).not.toHaveBeenCalled();
    expect(screen.getByText("page.webmail.ai_stale")).toBeTruthy();
  });
  it("renders provider output as text without executing HTML", async () => {
    vi.mocked(api.runMailboxAI).mockResolvedValue({
      result: { draft: '<img src=x onerror="alert(1)">' },
    });
    const view = render(
      <MailboxAIPanel mode="compose" subject="Invoice" body="Original" />,
    );
    await start();
    await screen.findByText('<img src=x onerror="alert(1)">');
    expect(view.container.querySelector("img")).toBeNull();
  });
  it("requires a nonempty selected excerpt instead of silently exporting the entire mail", async () => {
    render(
      <MailboxAIPanel
        mode="read"
        source={{ folder_enc: "SU5CT1g", uid: 5, uid_validity: 9 }}
        subject="Invoice"
        body="Original invoice text"
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.ai_assistance" }),
    );
    await screen.findByDisplayValue("Original invoice text");
    fireEvent.change(screen.getByDisplayValue("Original invoice text"), {
      target: { value: "" },
    });
    fireEvent.click(
      screen.getByRole("checkbox", { name: "page.webmail.ai_confirm_content" }),
    );
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.ai_generate",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(api.runMailboxAI).not.toHaveBeenCalled();
  });
  it("a disabled provider leaves the draft editable and performs no generation", async () => {
    vi.mocked(api.fetchMailboxAITarget).mockResolvedValue({
      enabled: false,
      allowed: false,
    });
    render(<MailboxAIPanel mode="compose" subject="Invoice" body="Original" />);
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.ai_assistance" }),
    );
    await screen.findByText("page.webmail.ai_unavailable");
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.ai_generate",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(api.runMailboxAI).not.toHaveBeenCalled();
  });
});
