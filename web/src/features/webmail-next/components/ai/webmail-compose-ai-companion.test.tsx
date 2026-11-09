import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailComposeAICompanion } from "./webmail-compose-ai-companion";
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

describe("WebmailComposeAICompanion", () => {
  it("renders initial version card with initial subject and body", () => {
    render(
      <WebmailComposeAICompanion
        currentSubject="Contract Renewal"
        currentBody="Initial draft body text"
        onRollback={vi.fn()}
        onApply={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByTestId("compose-ai-companion")).toBeTruthy();
    expect(screen.getByText("Initial draft body text")).toBeTruthy();
    expect(
      screen.getByText("webmail_next.ai_current_version_badge"),
    ).toBeTruthy();
  });

  it("generates a new version when preset chip is clicked and displays diff & assumptions", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI).mockResolvedValueOnce({
      result: {
        subject: "Contract Renewal (Formal)",
        body: "Formal revised draft body text",
        assumptions: ["Assume signing by Nov 20", "Assume standard terms"],
        tone_notes: "Formal & Professional",
      },
      degraded: false,
      cache_hit: true,
    });

    render(
      <WebmailComposeAICompanion
        currentSubject="Contract Renewal"
        currentBody="Initial draft body text"
        onRollback={vi.fn()}
        onApply={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    const formalChip = screen.getByTestId("chip-formal");
    fireEvent.click(formalChip);

    await waitFor(() => {
      expect(mailboxApi.runMailboxAI).toHaveBeenCalledTimes(1);
    });

    await waitFor(() => {
      expect(screen.getByTestId("compose-diff-view")).toBeTruthy();
    });

    expect(screen.getByText("Assume signing by Nov 20")).toBeTruthy();
    expect(screen.getByText("Formal & Professional")).toBeTruthy();
    expect(screen.getByText("webmail_next.ai_cache_hit")).toBeTruthy();
  });

  it("handles rollback button click and notifies parent", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI).mockResolvedValueOnce({
      result: {
        subject: "Version 2 Subject",
        body: "Version 2 Body",
        assumptions: [],
      },
    });

    const onRollback = vi.fn();

    render(
      <WebmailComposeAICompanion
        currentSubject="V1 Subject"
        currentBody="V1 Body"
        onRollback={onRollback}
        onApply={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByTestId("chip-concise"));

    await waitFor(() => {
      expect(screen.getByTestId("compose-diff-view")).toBeTruthy();
    });
    expect(screen.getByText("Version 2")).toBeTruthy();

    // Rollback to v1
    const rollbackButtons = screen.getAllByText("webmail_next.ai_rollback_btn");
    expect(rollbackButtons.length).toBeGreaterThan(0);
    fireEvent.click(rollbackButtons[0]);

    expect(onRollback).toHaveBeenCalledWith(
      expect.objectContaining({
        version: 1,
        body: "V1 Body",
      }),
    );
  });

  it("handles apply draft button click", async () => {
    const onApply = vi.fn();

    render(
      <WebmailComposeAICompanion
        currentSubject="Test Subject"
        currentBody="Test Body"
        onRollback={vi.fn()}
        onApply={onApply}
        onClose={vi.fn()}
      />,
    );

    const applyBtn = screen.getByTestId("btn-apply-draft");
    fireEvent.click(applyBtn);

    expect(onApply).toHaveBeenCalledWith(
      expect.objectContaining({
        version: 1,
        subject: "Test Subject",
        body: "Test Body",
      }),
    );
  });

  it("renders degraded warning notice when degraded is true", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: true,
      allowed: true,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    vi.mocked(mailboxApi.runMailboxAI).mockResolvedValueOnce({
      result: {
        subject: "Degraded Subject",
        body: "Degraded Body",
      },
      degraded: true,
    });

    render(
      <WebmailComposeAICompanion
        currentSubject="Test Subject"
        currentBody="Test Body"
        onRollback={vi.fn()}
        onApply={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByTestId("chip-friendly"));

    await waitFor(() => {
      expect(screen.getByText("webmail_next.ai_degraded_notice")).toBeTruthy();
    });
  });

  it("renders unconfigured state when allowed is false and allows navigating to settings", async () => {
    vi.mocked(mailboxApi.fetchMailboxAITarget).mockResolvedValueOnce({
      enabled: false,
      allowed: false,
      configuration_digest: "test-digest",
    } as mailboxApi.MailboxAITarget);

    const onOpenAISettings = vi.fn();

    render(
      <WebmailComposeAICompanion
        currentSubject="Test Subject"
        currentBody="Test Body"
        onRollback={vi.fn()}
        onApply={vi.fn()}
        onClose={vi.fn()}
        onOpenAISettings={onOpenAISettings}
      />,
    );

    fireEvent.click(screen.getByTestId("chip-formal"));

    await waitFor(() => {
      expect(screen.getByTestId("companion-unconfigured-card")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("btn-go-to-settings"));
    expect(onOpenAISettings).toHaveBeenCalledTimes(1);
  });

  it("renders actionable consent stale card on 409 and supports re-authorizing and retrying", async () => {
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
          subject: "Test Subject",
          body: "Recovered draft after consent refresh",
          assumptions: [],
        },
        degraded: false,
        cache_hit: false,
      });

    render(
      <WebmailComposeAICompanion
        currentSubject="Test Subject"
        currentBody="Initial Body"
        onRollback={vi.fn()}
        onApply={vi.fn()}
        onClose={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByTestId("chip-formal"));

    await waitFor(() => {
      expect(screen.getByTestId("companion-consent-stale-card")).toBeTruthy();
      expect(
        screen.getByText("webmail_next.ai_consent_stale_title"),
      ).toBeTruthy();
    });

    // Click retry
    fireEvent.click(screen.getByTestId("btn-ai-consent-retry"));

    await waitFor(() => {
      expect(screen.queryByTestId("companion-consent-stale-card")).toBeNull();
      expect(screen.getByTestId("compose-diff-view")).toBeTruthy();
      expect(screen.getByTestId("compose-diff-view").textContent).toContain(
        "draft after consent refresh",
      );
    });
  });
});
