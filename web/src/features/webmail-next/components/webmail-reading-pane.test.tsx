import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailReadingPane } from "./webmail-reading-pane";
import type { MessageDetail } from "@/features/webmail/mailbox-api";
import { resetSanitizeWorkerForTests } from "@/features/webmail/sanitize-html-mail-async";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  resetSanitizeWorkerForTests(null);
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

  it("renders a localised attachments heading and a spec-sized plain-text body", () => {
    render(
      <WebmailReadingPane
        message={{ ...mockMessage, html: undefined, text: "Plain body text" }}
        folderEnc="INBOX"
      />,
    );

    // Heading is localised, never hardcoded English.
    expect(screen.getByText("webmail_next.attachments_count")).toBeTruthy();
    // Plain-text body meets the reading floor: 14px / 22px, selectable.
    const pre = screen.getByTestId("mail-body-container").querySelector("pre");
    expect(pre?.className).toContain("text-sm");
    expect(pre?.className).toContain("leading-[22px]");
    expect(screen.getByTestId("mail-body-container").className).toContain(
      "select-text",
    );
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

  it("does not put raw html into srcdoc before the worker replies", async () => {
    const worker = {
      onmessage: null as
        ((event: MessageEvent<{ id: number; html: string }>) => void) | null,
      onerror: null as ((event: Event) => void) | null,
      postMessage(data: { id: number }) {
        setTimeout(() => {
          worker.onmessage?.({
            data: { id: data.id, html: "<p>worker-safe</p>" },
          } as MessageEvent<{ id: number; html: string }>);
        }, 0);
      },
    };
    resetSanitizeWorkerForTests(() => worker as never);
    render(
      <WebmailReadingPane
        message={{
          ...mockMessage,
          html: "<p>raw body</p><script>alert(1)</script>",
        }}
        folderEnc="INBOX"
      />,
    );

    expect(screen.getByTestId("mail-html-skeleton")).toBeTruthy();
    expect(screen.queryByTestId("mail-html-frame")).toBeNull();
    expect(document.body.innerHTML).not.toContain("alert(1)");

    const frame = await screen.findByTestId("mail-html-frame");
    expect(frame.getAttribute("srcDoc")).toBe("<p>worker-safe</p>");
  });
});

it("renders the reading view full width without the centered paper card", () => {
  render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);

  const pane = screen.getByTestId("webmail-reading-pane");
  // No centering: the pane must not rely on mx-auto/max-w-* on the detail
  // column (Gmail/Outlook style full-width reading).
  const centered = pane.querySelector(".mx-auto.max-w-4xl, .max-w-4xl.mx-auto");
  expect(centered).toBeNull();

  const body = screen.getByTestId("mail-body-container");
  // The body container is not boxed into a white card with its own padding;
  // it spans the pane width.
  expect(body.className).not.toContain("bg-white");
  expect(body.className).not.toContain("rounded-xl");
  expect(body.className).not.toContain("shadow-sm");
  const frame = screen.getByTestId("mail-html-frame");
  expect(frame.className).toContain("w-full");
});

describe("SanitizedHtmlFrame auto height", () => {
  it("starts at the minimum height and keeps the measurer-safe sandbox", () => {
    render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);
    const frame = screen.getByTestId("mail-html-frame") as HTMLIFrameElement;
    // allow-same-origin alone (never with allow-scripts) so the parent can
    // measure the srcdoc body; the injected CSP keeps it script-inert.
    expect(frame.getAttribute("sandbox")).toBe("allow-same-origin");
    expect(frame.style.height).toBe("420px");
    expect(frame.style.minHeight).toBe("420px");
  });

  it("grows past the minimum when the measured content is taller", () => {
    const { rerender } = render(
      <WebmailReadingPane message={mockMessage} folderEnc="INBOX" />,
    );
    const frame = screen.getByTestId("mail-html-frame") as HTMLIFrameElement;
    Object.defineProperty(frame, "contentDocument", {
      configurable: true,
      value: {
        body: { scrollHeight: 2200, offsetHeight: 2200 },
        documentElement: { scrollHeight: 2200 },
        images: [],
      },
    });
    frame.dispatchEvent(new Event("load"));
    rerender(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);
    expect(frame.style.height).toBe("2200px");
  });

  it("clamps pathological measurements to the 10000px ceiling", () => {
    const { rerender } = render(
      <WebmailReadingPane message={mockMessage} folderEnc="INBOX" />,
    );
    const frame = screen.getByTestId("mail-html-frame") as HTMLIFrameElement;
    Object.defineProperty(frame, "contentDocument", {
      configurable: true,
      value: {
        body: { scrollHeight: 999999, offsetHeight: 999999 },
        documentElement: { scrollHeight: 999999 },
        images: [],
      },
    });
    frame.dispatchEvent(new Event("load"));
    rerender(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);
    expect(frame.style.height).toBe("10000px");
  });

  it("resets to the minimum height when switching to another message", () => {
    const { rerender } = render(
      <WebmailReadingPane message={mockMessage} folderEnc="INBOX" />,
    );
    const frame = screen.getByTestId("mail-html-frame") as HTMLIFrameElement;
    Object.defineProperty(frame, "contentDocument", {
      configurable: true,
      value: {
        body: { scrollHeight: 3000, offsetHeight: 3000 },
        documentElement: { scrollHeight: 3000 },
        images: [],
      },
    });
    frame.dispatchEvent(new Event("load"));
    rerender(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);
    expect(frame.style.height).toBe("3000px");

    rerender(
      <WebmailReadingPane
        message={{
          ...mockMessage,
          uid: 202,
          subject: "Short note",
          html: "<p>tiny</p>",
        }}
        folderEnc="INBOX"
      />,
    );
    // New srcdoc must not inherit the previous mail's height.
    expect(frame.style.height).toBe("420px");
  });

  it("keeps the frame height when the measurement surface is inaccessible", () => {
    render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);
    const frame = screen.getByTestId("mail-html-frame") as HTMLIFrameElement;
    Object.defineProperty(frame, "contentDocument", {
      configurable: true,
      value: null,
    });
    frame.dispatchEvent(new Event("load"));
    // Falls back to the minimum; no crash, no zero-height clip.
    expect(frame.style.height).toBe("420px");
  });

  it("renders ai-cue-strip on long messages (>1500 chars), but not on short messages", () => {
    // Short message: no cue strip
    const { rerender } = render(
      <WebmailReadingPane
        message={{ ...mockMessage, text: "Short message under 1500 chars." }}
        folderEnc="INBOX"
      />,
    );
    expect(screen.queryByTestId("ai-cue-strip")).toBeNull();

    // Long message (>1500 chars): cue strip shown
    const longText = "A".repeat(1600);
    rerender(
      <WebmailReadingPane
        message={{ ...mockMessage, text: longText }}
        folderEnc="INBOX"
      />,
    );
    expect(screen.getByTestId("ai-cue-strip")).toBeTruthy();
    expect(screen.getByTestId("btn-trigger-summary")).toBeTruthy();
  });

  it("triggers AI summary from cue strip and displays structured summary card", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "cfg-digest-123",
      endpoint: "https://api.openai.com/v1",
      model: "gpt-4o",
      provider: "openai",
    });
    vi.spyOn(mailboxApi, "previewMailboxAI").mockResolvedValue({
      content_digest: "content-digest-456",
    });
    vi.spyOn(mailboxApi, "runMailboxAI").mockResolvedValue({
      template_version: 2,
      degraded: false,
      cache_hit: true,
      result: {
        bullets: ["Key contract term 1", "Key contract term 2"],
        todos: [{ task: "Sign contract", due_date: "2026-11-20" }],
      },
    });

    const longText = "A".repeat(1600);
    render(
      <WebmailReadingPane
        message={{ ...mockMessage, text: longText }}
        folderEnc="INBOX"
      />,
    );

    const triggerBtn = screen.getByTestId("btn-trigger-summary");
    fireEvent.click(triggerBtn);

    const summaryCard = await screen.findByTestId("structured-summary-card");
    expect(summaryCard).toBeTruthy();
    expect(screen.getByText("Key contract term 1")).toBeTruthy();
    expect(screen.getByText("Sign contract")).toBeTruthy();
    expect(screen.getByTestId("ai-cache-hit")).toBeTruthy();

    // Reading operations act on the whole message: the server derives the body
    // from the live message and rejects any selection that is not an exact
    // substring of its own plain-text rendering (409 AI_SOURCE_CHANGED).
    const previewInput = vi.mocked(mailboxApi.previewMailboxAI).mock
      .calls[0][1] as Record<string, unknown>;
    expect(previewInput).not.toHaveProperty("selection");
    expect(previewInput.uid).toBe(mockMessage.uid);
    const runInput = vi.mocked(mailboxApi.runMailboxAI).mock
      .calls[0][0] as Record<string, unknown>;
    expect(runInput).not.toHaveProperty("selection");
    expect(runInput.content_digest).toBe("content-digest-456");
  });

  it("omits selection for HTML-only long messages so the summary chain is accepted", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockClear().mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "cfg-digest-123",
      endpoint: "https://api.openai.com/v1",
      model: "gpt-4o",
      provider: "openai",
    });
    vi.spyOn(mailboxApi, "previewMailboxAI")
      .mockClear()
      .mockResolvedValue({ content_digest: "content-digest-789" });
    vi.spyOn(mailboxApi, "runMailboxAI")
      .mockClear()
      .mockResolvedValue({
        result: { bullets: ["HTML-only summary bullet"] },
      });

    // HTML-only message: no plain-text part, and the rendered markup carries a
    // long inline <style> block (the Infura template shape).
    const htmlOnlyMessage: MessageDetail = {
      ...mockMessage,
      uid: 37929,
      text: undefined,
      html: `<style>${".urmail-vp{color:#fff;background:#121212}" + "a".repeat(1600)}</style><div class="urmail-vp">Hello from the newsletter</div>`,
    };

    render(<WebmailReadingPane message={htmlOnlyMessage} folderEnc="INBOX" />);

    fireEvent.click(screen.getByTestId("btn-trigger-summary"));

    await screen.findByTestId("structured-summary-card");

    const previewInput = vi.mocked(mailboxApi.previewMailboxAI).mock
      .calls[0][1] as Record<string, unknown>;
    expect(previewInput).not.toHaveProperty("selection");
    const runInput = vi.mocked(mailboxApi.runMailboxAI).mock
      .calls[0][0] as Record<string, unknown>;
    expect(runInput).not.toHaveProperty("selection");
  });

  it("displays phishing warning banner with indicators when risk_level is high", () => {
    const phishingMessage: MessageDetail = {
      ...mockMessage,
      risk_level: "high",
      indicators: [
        {
          type: "spoofed_domain",
          evidence: "From address does not match DKIM d= domain",
        },
        {
          type: "urgent_pressure",
          evidence: "Wire transfer requested within 24 hours",
        },
      ],
    };

    render(<WebmailReadingPane message={phishingMessage} folderEnc="INBOX" />);

    expect(screen.getByTestId("phishing-warning-banner")).toBeTruthy();
    expect(screen.getByText("webmail_next.ai_phishing_high_risk")).toBeTruthy();
    expect(screen.getByText("spoofed_domain")).toBeTruthy();
    expect(
      screen.getByText(": From address does not match DKIM d= domain"),
    ).toBeTruthy();
  });

  it("renders the red banner from message.phishing with score >= 0.6 (Backend #87 shape)", () => {
    const scored: MessageDetail = {
      ...mockMessage,
      phishing: {
        score: 0.99,
        risk_level: "high",
        reasons: ["Sender domain mismatch"],
        analyzed_at: "2026-11-09T07:30:00Z",
        indicators: [
          {
            type: "suspicious_link",
            evidence: "Link points to a look-alike domain",
          },
        ],
      },
    };

    render(<WebmailReadingPane message={scored} folderEnc="INBOX" />);

    expect(screen.getByTestId("phishing-warning-banner")).toBeTruthy();
    expect(screen.getByTestId("phishing-risk-score").textContent).toBe(
      "webmail_next.ai_phishing_score",
    );
    expect(screen.getByText("suspicious_link")).toBeTruthy();
    expect(
      screen.getByText(": Link points to a look-alike domain"),
    ).toBeTruthy();
  });

  it("falls back to reasons when the verdict carries no indicators", () => {
    const reasonsOnly: MessageDetail = {
      ...mockMessage,
      phishing: {
        score: 0.91,
        risk_level: "high",
        reasons: [
          "Requests credentials within 24 hours",
          "Reply address differs from sender domain",
        ],
        indicators: [],
        analyzed_at: "2026-11-09T07:30:00Z",
      },
    };

    render(<WebmailReadingPane message={reasonsOnly} folderEnc="INBOX" />);

    expect(screen.getByTestId("phishing-warning-banner")).toBeTruthy();
    expect(
      screen.getByText("Requests credentials within 24 hours"),
    ).toBeTruthy();
    expect(
      screen.getByText("Reply address differs from sender domain"),
    ).toBeTruthy();
  });

  it("stays silent when message.phishing score < 0.6 even with indicators", () => {
    const medium: MessageDetail = {
      ...mockMessage,
      phishing: {
        score: 0.4,
        risk_level: "medium",
        indicators: [{ type: "urgent_pressure", evidence: "Act within 24h" }],
        analyzed_at: "2026-11-09T07:30:00Z",
      },
    };

    render(<WebmailReadingPane message={medium} folderEnc="INBOX" />);

    expect(screen.queryByTestId("phishing-warning-banner")).toBeNull();
  });

  it("renders nothing for mail without any risk keys (un-analysed = silent)", () => {
    render(<WebmailReadingPane message={mockMessage} folderEnc="INBOX" />);

    expect(screen.queryByTestId("phishing-warning-banner")).toBeNull();
    // The on-demand check entry stays available for an explicit check.
    expect(screen.getByTestId("btn-security-check")).toBeTruthy();
  });

  it("runs the on-demand phishing check and renders the red banner on high score", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "cfg-digest-sec",
      endpoint: "https://api.openai.com/v1",
      model: "gpt-4o",
      provider: "openai",
    });
    vi.spyOn(mailboxApi, "previewMailboxAI").mockResolvedValue({
      content_digest: "content-digest-sec",
    });
    const runSpy = vi.spyOn(mailboxApi, "runMailboxAI").mockResolvedValue({
      result: {
        score: 0.99,
        risk_level: "high",
        indicators: [
          {
            type: "credential_request",
            evidence: "Asks for the account password",
          },
        ],
      },
    });

    render(
      <WebmailReadingPane
        message={{ ...mockMessage, uid: 37931 }}
        folderEnc="INBOX"
      />,
    );

    fireEvent.click(screen.getByTestId("btn-security-check"));

    const banner = await screen.findByTestId("phishing-warning-banner");
    expect(banner).toBeTruthy();
    expect(screen.getByText("credential_request")).toBeTruthy();
    expect(screen.queryByTestId("security-check-note")).toBeNull();

    // Contract: two-step assist call without selection, same intent key.
    const previewInput = vi.mocked(mailboxApi.previewMailboxAI).mock
      .calls[0][1] as Record<string, unknown>;
    expect(previewInput.operation).toBe("phishing");
    expect(previewInput).not.toHaveProperty("selection");
    expect(previewInput.uid).toBe(37931);
    const runInput = runSpy.mock.calls[0][0] as Record<string, unknown>;
    expect(runInput.confirmed).toBe(true);
    expect(runInput.content_digest).toBe("content-digest-sec");
    expect(runInput.request_key).toBe(previewInput.request_key);
  });

  it("keeps silent with a quiet note when the on-demand check scores low", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "cfg-digest-sec",
    });
    vi.spyOn(mailboxApi, "previewMailboxAI").mockResolvedValue({
      content_digest: "content-digest-sec",
    });
    vi.spyOn(mailboxApi, "runMailboxAI").mockResolvedValue({
      result: { score: 0.21, risk_level: "low" },
    });

    render(
      <WebmailReadingPane
        message={{ ...mockMessage, uid: 37932 }}
        folderEnc="INBOX"
      />,
    );

    fireEvent.click(screen.getByTestId("btn-security-check"));

    const note = await screen.findByTestId("security-check-note");
    expect(note.textContent).toBe("webmail_next.security_check_clear");
    expect(screen.queryByTestId("phishing-warning-banner")).toBeNull();
  });

  it("shows the unconfigured modal when the on-demand check runs without AI setup", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockResolvedValue({
      enabled: false,
      allowed: false,
      configuration_digest: "",
    });

    const onOpenAISettings = vi.fn();
    render(
      <WebmailReadingPane
        message={mockMessage}
        folderEnc="INBOX"
        onOpenAISettings={onOpenAISettings}
      />,
    );

    fireEvent.click(screen.getByTestId("btn-security-check"));

    const modal = await screen.findByTestId("ai-unconfigured-modal");
    expect(modal).toBeTruthy();
  });

  it("silently retries a stale 409 once during the on-demand check", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockResolvedValue({
      enabled: true,
      allowed: true,
      configuration_digest: "cfg-digest-sec",
    });
    vi.spyOn(mailboxApi, "previewMailboxAI").mockResolvedValue({
      content_digest: "content-digest-sec",
    });
    vi.spyOn(mailboxApi, "runMailboxAI")
      .mockRejectedValueOnce({
        status: 409,
        code: "AI_CONTENT_CONSENT_STALE",
      })
      .mockResolvedValueOnce({
        result: { score: 0.99, risk_level: "high" },
      });

    render(
      <WebmailReadingPane
        message={{ ...mockMessage, uid: 37933 }}
        folderEnc="INBOX"
      />,
    );

    fireEvent.click(screen.getByTestId("btn-security-check"));

    await screen.findByTestId("phishing-warning-banner");
    expect(mailboxApi.previewMailboxAI).toHaveBeenCalledTimes(2);
    expect(mailboxApi.runMailboxAI).toHaveBeenCalledTimes(2);
    expect(screen.queryByTestId("security-check-note")).toBeNull();
  });

  it("shows unconfigured modal when AI is not configured", async () => {
    const mailboxApi = await import("@/features/webmail/mailbox-api");
    vi.spyOn(mailboxApi, "fetchMailboxAITarget").mockResolvedValue({
      enabled: false,
      allowed: false,
      configuration_digest: "",
      endpoint: "",
      model: "",
      provider: "",
    });

    const onOpenAISettings = vi.fn();
    const longText = "A".repeat(1600);
    render(
      <WebmailReadingPane
        message={{ ...mockMessage, text: longText }}
        folderEnc="INBOX"
        onOpenAISettings={onOpenAISettings}
      />,
    );

    fireEvent.click(screen.getByTestId("btn-trigger-summary"));

    const modal = await screen.findByTestId("ai-unconfigured-modal");
    expect(modal).toBeTruthy();

    const goToSettingsBtn = screen.getByText("webmail_next.ai_go_to_settings");
    fireEvent.click(goToSettingsBtn);
    expect(onOpenAISettings).toHaveBeenCalledTimes(1);
  });
});
