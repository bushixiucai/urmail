import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailComposeDock } from "./webmail-compose-dock";

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
});
