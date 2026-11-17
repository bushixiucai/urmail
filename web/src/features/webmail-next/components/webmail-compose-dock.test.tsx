import {
  render,
  screen,
  fireEvent,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailComposeDock } from "./webmail-compose-dock";
import { sendMessage } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("@/features/webmail/mailbox-api", async () => {
  const actual = await vi.importActual<
    typeof import("@/features/webmail/mailbox-api")
  >("@/features/webmail/mailbox-api");
  return {
    ...actual,
    saveDraft: vi
      .fn()
      .mockResolvedValue({ saved: true, uid: 999, uid_validity: 1 }),
    sendMessage: vi.fn().mockResolvedValue({ sent: true }),
  };
});

afterEach(() => {
  cleanup();
});

describe("WebmailComposeDock", () => {
  it("converts entered email into recipient chip on Enter", () => {
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);

    const toInput = screen.getByTestId("compose-to-input");
    fireEvent.change(toInput, { target: { value: "test@example.com" } });
    fireEvent.keyDown(toInput, { key: "Enter", code: "Enter" });

    expect(screen.getByTestId("recipient-pill-test@example.com")).toBeTruthy();
  });

  it("blocks attachments exceeding 5MB single file limit", () => {
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);

    const fileInput = screen.getByTestId("compose-file-input");
    const oversizedFile = new File(["dummy".repeat(1024 * 1024)], "huge.pdf", {
      type: "application/pdf",
    });
    Object.defineProperty(oversizedFile, "size", { value: 6 * 1024 * 1024 });

    fireEvent.change(fileInput, { target: { files: [oversizedFile] } });

    expect(screen.getByTestId("attachment-error-notice")).toBeTruthy();
    const sendBtn = screen.getByTestId("compose-send-button");
    expect(sendBtn.hasAttribute("disabled")).toBe(true);
  });

  it("shows undo toast after sending", () => {
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["client@example.com"]}
        initialSubject="Contract Attached"
      />,
    );

    const sendBtn = screen.getByTestId("compose-send-button");
    fireEvent.click(sendBtn);

    expect(screen.getByTestId("undo-send-toast")).toBeTruthy();
    expect(screen.getByTestId("undo-send-button")).toBeTruthy();
  });

  it("sends in_reply_to when the dock is opened for a reply/forward", async () => {
    vi.useFakeTimers();
    try {
      render(
        <WebmailComposeDock
          isOpen={true}
          onClose={vi.fn()}
          initialTo={["alice@example.com"]}
          initialSubject="Re: Hello"
          inReplyTo="<original@example.test>"
        />,
      );

      fireEvent.change(screen.getByTestId("compose-to-input"), {
        target: { value: "alice@example.com" },
      });
      fireEvent.click(screen.getByTestId("compose-send-button"));

      // The 10-second undo buffer gates the real send call.
      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBe("<original@example.test>");
    } finally {
      vi.useRealTimers();
    }
  });

  it("omits in_reply_to when no prop is passed (fresh compose; forward also opens without it)", async () => {
    vi.useFakeTimers();
    try {
      render(
        <WebmailComposeDock
          isOpen={true}
          onClose={vi.fn()}
          initialTo={["bob@example.com"]}
        />,
      );

      fireEvent.click(screen.getByTestId("compose-send-button"));

      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("renders quoted content with blockquote when initialHtml is provided (DEF-5)", () => {
    const quotedHtml = `<p><br></p><div class="urmail-quoted-message"><p>On Nov 8, 2026, Alice wrote:</p><blockquote style="border-left: 2px solid #ccc; padding-left: 8px; margin-left: 8px;"><p>Hello original email</p></blockquote></div>`;
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["alice@example.com"]}
        initialSubject="Re: Hello"
        initialHtml={quotedHtml}
      />,
    );

    const blockquotes = document.querySelectorAll("blockquote");
    expect(blockquotes.length).toBeGreaterThan(0);
    expect(document.body.textContent).toContain("Hello original email");
  });

  it("toggles AI companion panel and expands dock width from 580px to 880px", () => {
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["partner@example.com"]}
        initialSubject="Partnership Agreement"
      />,
    );

    const dock = screen.getByTestId("webmail-compose-dock");
    expect(dock.className).toContain("w-[580px]");

    const aiBtn = screen.getByTestId("compose-ai-button");
    fireEvent.click(aiBtn);

    expect(dock.className).toContain("w-[880px]");
    expect(screen.getByTestId("compose-ai-companion")).toBeTruthy();

    // Toggle off
    fireEvent.click(aiBtn);
    expect(dock.className).toContain("w-[580px]");
    expect(screen.queryByTestId("compose-ai-companion")).toBeNull();
  });

  it("applies draft from companion and updates subject input and draft", () => {
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["partner@example.com"]}
        initialSubject="Original Subject"
        initialText="Original Text"
      />,
    );

    // Open companion
    fireEvent.click(screen.getByTestId("compose-ai-button"));
    expect(screen.getByTestId("compose-ai-companion")).toBeTruthy();

    // Apply draft from companion
    const applyBtn = screen.getByTestId("btn-apply-draft");
    fireEvent.click(applyBtn);

    const subjectInput = screen.getByTestId(
      "compose-subject-input",
    ) as HTMLInputElement;
    expect(subjectInput.value).toBe("Original Subject");
  });
});
