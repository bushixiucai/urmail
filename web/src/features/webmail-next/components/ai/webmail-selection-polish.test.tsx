import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailSelectionPolish } from "./webmail-selection-polish";
import * as mailboxApi from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("@/features/webmail/mailbox-api", async () => {
  const actual = await vi.importActual<
    typeof import("@/features/webmail/mailbox-api")
  >("@/features/webmail/mailbox-api");
  return {
    ...actual,
    fetchMailboxAITarget: vi.fn(),
    previewMailboxAI: vi.fn().mockResolvedValue({
      content_digest: "test-content-digest",
      messages: [],
    }),
    runMailboxAI: vi.fn(),
  };
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("WebmailSelectionPolish", () => {
  const defaultProps = {
    selectedText: "找对方退回了",
    draftBody:
      "王经理您好：\n\n关于上次对账的差异，我们找对方退回了。\n\n顺颂商祺",
    position: { top: 100, left: 200 },
    onReplace: vi.fn(),
    onClose: vi.fn(),
  };

  it("renders floating capsule with 28px height button", () => {
    render(<WebmailSelectionPolish {...defaultProps} />);

    expect(screen.getByTestId("selection-polish-container")).toBeTruthy();
    expect(screen.getByTestId("selection-polish-capsule")).toBeTruthy();
    expect(screen.getByTestId("btn-selection-polish")).toBeTruthy();
  });

  it("performs polish on click and renders diff view and rationale list", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI).mockResolvedValueOnce({
      result: {
        revised_text: "已获供货方全额对账冲销",
        changes: [
          { type: "tone", note: "Enhance formal tone" },
          { type: "clarity", note: "Specify accounting offset" },
        ],
      },
    });

    render(<WebmailSelectionPolish {...defaultProps} />);

    fireEvent.click(screen.getByTestId("btn-selection-polish"));

    await waitFor(() => {
      expect(mailboxApi.runMailboxAI).toHaveBeenCalledWith(
        expect.objectContaining({
          operation: "polish",
          selection: "找对方退回了",
          draft: "找对方退回了",
          body: defaultProps.draftBody,
        }),
      );
    });

    // The server rejects polish requests with an empty body, and the preview
    // must carry exactly the same input as the confirmed run.
    const previewInput = vi.mocked(mailboxApi.previewMailboxAI).mock
      .calls[0][1] as {
      body?: string;
      draft?: string;
      selection?: string;
    };
    expect(previewInput.body).toBe(defaultProps.draftBody);
    expect(previewInput.draft).toBe("找对方退回了");
    expect(previewInput.selection).toBe("找对方退回了");

    await waitFor(() => {
      expect(screen.getByTestId("selection-diff-popover")).toBeTruthy();
    });

    expect(screen.getByText("找对方退回了")).toBeTruthy();
    expect(screen.getByText("已获供货方全额对账冲销")).toBeTruthy();
    expect(screen.getByText("Enhance formal tone")).toBeTruthy();
    expect(screen.getByText("Specify accounting offset")).toBeTruthy();
  });

  it("calls onReplace with revised text and closes on clicking apply replacement", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI).mockResolvedValueOnce({
      result: {
        revised_text: "已获供货方全额对账冲销",
        changes: [],
      },
    });

    const onReplace = vi.fn();
    const onClose = vi.fn();

    render(
      <WebmailSelectionPolish
        {...defaultProps}
        onReplace={onReplace}
        onClose={onClose}
      />,
    );

    fireEvent.click(screen.getByTestId("btn-selection-polish"));

    await waitFor(() => {
      expect(screen.getByTestId("btn-apply-polish")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("btn-apply-polish"));

    expect(onReplace).toHaveBeenCalledWith("已获供货方全额对账冲销");
    expect(onClose).toHaveBeenCalled();
  });

  it("closes on Escape key press", () => {
    const onClose = vi.fn();
    render(<WebmailSelectionPolish {...defaultProps} onClose={onClose} />);

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("silently retries on first 409 error and recovers without error state", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI)
      .mockRejectedValueOnce({
        status: 409,
        code: "AI_CONTENT_CONSENT_STALE",
      })
      .mockResolvedValueOnce({
        result: {
          revised_text: "已获供货方全额对账冲销",
          changes: [],
        },
      });

    render(<WebmailSelectionPolish {...defaultProps} />);

    fireEvent.click(screen.getByTestId("btn-selection-polish"));

    await waitFor(() => {
      expect(screen.getByTestId("selection-diff-popover")).toBeTruthy();
    });

    expect(screen.getByText("已获供货方全额对账冲销")).toBeTruthy();
    expect(mailboxApi.previewMailboxAI).toHaveBeenCalledTimes(2);
    expect(mailboxApi.runMailboxAI).toHaveBeenCalledTimes(2);
  });

  it("falls back to the selection as body when the draft body is unavailable", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);
    vi.mocked(mailboxApi.runMailboxAI).mockResolvedValueOnce({
      result: { revised_text: "已获供货方全额对账冲销", changes: [] },
    });

    render(<WebmailSelectionPolish {...defaultProps} draftBody="" />);

    fireEvent.click(screen.getByTestId("btn-selection-polish"));

    await waitFor(() => {
      expect(mailboxApi.runMailboxAI).toHaveBeenCalledWith(
        expect.objectContaining({
          operation: "polish",
          draft: "找对方退回了",
          body: "找对方退回了",
        }),
      );
    });
  });

  it("shows the concrete server reason and a retry action instead of a generic failure", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI)
      .mockRejectedValueOnce({
        status: 400,
        code: "INVALID_PARAMS",
      })
      .mockResolvedValueOnce({
        result: { revised_text: "已获供货方全额对账冲销", changes: [] },
      });

    render(<WebmailSelectionPolish {...defaultProps} />);

    fireEvent.click(screen.getByTestId("btn-selection-polish"));

    const errorCard = await screen.findByTestId("selection-polish-error");
    expect(errorCard).toBeTruthy();
    expect(screen.getByText("error.INVALID_PARAMS")).toBeTruthy();
    expect(screen.getByTestId("btn-polish-retry")).toBeTruthy();

    fireEvent.click(screen.getByTestId("btn-polish-retry"));

    await waitFor(() => {
      expect(screen.getByText("已获供货方全额对账冲销")).toBeTruthy();
    });
    expect(mailboxApi.runMailboxAI).toHaveBeenCalledTimes(2);
  });

  it("shows an actionable consent card only after the silent 409 retry also fails", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI).mockRejectedValue({
      status: 409,
      code: "AI_CONTENT_CONSENT_STALE",
    });

    render(<WebmailSelectionPolish {...defaultProps} />);

    fireEvent.click(screen.getByTestId("btn-selection-polish"));

    const staleCard = await screen.findByTestId(
      "selection-polish-consent-stale-card",
    );
    expect(staleCard).toBeTruthy();
    expect(screen.getByTestId("btn-polish-consent-retry")).toBeTruthy();
    expect(screen.queryByTestId("selection-polish-error")).toBeNull();
    expect(mailboxApi.runMailboxAI).toHaveBeenCalledTimes(2);
  });
});
