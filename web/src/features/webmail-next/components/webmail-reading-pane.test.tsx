import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailReadingPane } from "./webmail-reading-pane";
import type { MessageDetail } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  cleanup();
});

const mockMessage: MessageDetail = {
  uid: 101,
  subject: "Quarterly Financial Overview",
  from: [{ name: "Finance Director", email: "finance@company.com" }],
  to: [{ name: "Team", email: "team@company.com" }],
  date: "2026-11-06T14:30:00Z",
  flags: ["\\Seen"],
  size: 4096,
  html: "<p>Here is the quarterly summary <img data-blocked-src='https://tracker.com/pixel.png'/></p>",
  notes: ["remote_images_blocked"],
  authentication: {
    status: "pass",
    domain: "company.com",
    reason: "SPF: PASS, DKIM: PASS, TLS 1.3",
  },
  attachments: [
    {
      part_id: "2",
      filename: "Report_Q3.pdf",
      content_type: "application/pdf",
      size: 1024 * 500, // 500 KB
    },
  ],
};

describe("WebmailReadingPane", () => {
  it("displays remote image blocked banner and reloads on click", () => {
    const onLoadRemoteImages = vi.fn();
    render(
      <WebmailReadingPane
        message={mockMessage}
        folderEnc="INBOX"
        onLoadRemoteImages={onLoadRemoteImages}
      />,
    );

    expect(screen.getByTestId("remote-images-banner")).toBeTruthy();
    const showBtn = screen.getByTestId("show-images-button");
    fireEvent.click(showBtn);
    expect(onLoadRemoteImages).toHaveBeenCalledWith(101);
  });

  it("renders SPF/DKIM authentication badge and details popover", () => {
    render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);

    const badgeBtn = screen.getByTestId("security-badge-button");
    expect(badgeBtn).toBeTruthy();

    fireEvent.click(badgeBtn);
    expect(screen.getByTestId("security-details-popover")).toBeTruthy();
    expect(screen.getByText("company.com")).toBeTruthy();
  });

  it("renders attachment cards and handles download trigger", () => {
    render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);

    expect(screen.getByTestId("attachment-card-2")).toBeTruthy();
    expect(screen.getByText("Report_Q3.pdf")).toBeTruthy();
    const downloadLink = screen.getByTestId("download-attachment-2");
    expect(downloadLink.getAttribute("href")).toContain("/attachments/2");
  });

  it("handles instant add sender to contacts", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "createContact").mockResolvedValue({
      id: 99,
      version: 1,
      display_name: "Finance Director",
      emails: [{ address: "finance@company.com" }],
    });

    render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);

    const addContactBtn = screen.getByTestId("btn-add-contact");
    expect(addContactBtn).toBeTruthy();

    fireEvent.click(addContactBtn);

    expect(mailboxApi.createContact).toHaveBeenCalledWith(
      "Finance Director",
      "finance@company.com",
    );
  });

  it("handles malformed/corrupted message objects safely without crashing", () => {
    // Malformed message with missing flags, notes, from, date, subject
    const malformedMessage = {
      uid: 999,
      size: 0,
    } as unknown as MessageDetail;

    render(<WebmailReadingPane message={malformedMessage} folderEnc="INBOX" />);

    expect(screen.getByTestId("webmail-reading-pane")).toBeTruthy();
    expect(screen.getByText("(No subject)")).toBeTruthy();
  });

  it("shows the manual load bar for server note remote_images_blocked when the image was removed", () => {
    const serverMessage: MessageDetail = {
      ...mockMessage,
      html: "<p>Here is the quarterly summary</p>",
      notes: ["remote_images_blocked"],
    };

    render(<WebmailReadingPane message={serverMessage} folderEnc="INBOX" />);

    expect(screen.getByTestId("remote-images-banner")).toBeTruthy();
    expect(screen.getByTestId("show-images-button")).toBeTruthy();
    const frame = screen.getByTestId("mail-html-frame");
    const srcDoc = frame.getAttribute("srcDoc") ?? "";
    expect(srcDoc).not.toContain("tracker.com");
    expect(srcDoc).not.toContain("data-blocked-src");
    expect(srcDoc).toContain("quarterly summary");
  });

  it("does not show remote images banner when isGlobalRemoteImagesAllowed is true", () => {
    render(
      <WebmailReadingPane
        message={mockMessage}
        folderEnc="INBOX"
        isGlobalRemoteImagesAllowed={true}
      />,
    );

    expect(screen.queryByTestId("remote-images-banner")).toBeNull();
    const frame = screen.getByTestId("mail-html-frame");
    // iframe srcDoc should allow remote image
    expect(frame.getAttribute("srcDoc")).toContain(
      "https://tracker.com/pixel.png",
    );
  });

  it("preserves manual consent even when isGlobalRemoteImagesAllowed is false (turning off global does not revoke manual)", () => {
    const { rerender } = render(
      <WebmailReadingPane
        message={mockMessage}
        folderEnc="INBOX"
        isGlobalRemoteImagesAllowed={true}
        isManualRemoteImagesAllowed={true}
      />,
    );

    expect(screen.queryByTestId("remote-images-banner")).toBeNull();

    // Rerender with global turned off, but manual remains true
    rerender(
      <WebmailReadingPane
        message={mockMessage}
        folderEnc="INBOX"
        isGlobalRemoteImagesAllowed={false}
        isManualRemoteImagesAllowed={true}
      />,
    );

    // Banner remains hidden, image remains allowed
    expect(screen.queryByTestId("remote-images-banner")).toBeNull();
    const frame = screen.getByTestId("mail-html-frame");
    expect(frame.getAttribute("srcDoc")).toContain(
      "https://tracker.com/pixel.png",
    );
  });

  it("resets local manual grant when message changes", () => {
    const { rerender } = render(
      <WebmailReadingPane
        message={mockMessage}
        folderEnc="INBOX"
        isGlobalRemoteImagesAllowed={false}
        isManualRemoteImagesAllowed={false}
      />,
    );

    expect(screen.getByTestId("remote-images-banner")).toBeTruthy();

    // Click show images to grant local manual permission
    fireEvent.click(screen.getByTestId("show-images-button"));
    expect(screen.queryByTestId("remote-images-banner")).toBeNull();

    // Switch to another message
    const anotherMessage: MessageDetail = {
      ...mockMessage,
      uid: 102,
      subject: "Second message",
    };
    rerender(
      <WebmailReadingPane
        message={anotherMessage}
        folderEnc="INBOX"
        isGlobalRemoteImagesAllowed={false}
        isManualRemoteImagesAllowed={false}
      />,
    );

    // Banner must reappear for the new message
    expect(screen.getByTestId("remote-images-banner")).toBeTruthy();
  });
});
