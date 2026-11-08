import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { WebmailStructuredSummary } from "./webmail-structured-summary";
import type { MailboxAIAssistResponse } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (
      k: string,
      opts?: {
        score?: number;
        count?: number;
        date?: string;
        assignee?: string;
      },
    ) => {
      if (opts?.score != null) return `${k} ${opts.score}%`;
      if (opts?.date && opts?.assignee)
        return `${k} ${opts.assignee} ${opts.date}`;
      if (opts?.date) return `${k} ${opts.date}`;
      if (opts?.count != null) return `${k} (${opts.count})`;
      return k;
    },
  }),
}));

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const mockSuccessResponse: MailboxAIAssistResponse = {
  template_version: 2,
  degraded: false,
  cache_hit: false,
  result: {
    language: "en",
    confidence: 0.95,
    bullets: [
      "Contract expires on 2026-12-31, renewal required before 11-20.",
      "Invoice billing adjustment approved for September.",
      "Security Schedule C needs compliance sign-off.",
    ],
    todos: [
      {
        task: "Complete Schedule C DocuSign signing",
        assignee: "Legal",
        due_date: "2026-11-13",
      },
      {
        task: "Confirm cloud contract renewal order",
        due_date: "2026-11-20",
      },
    ],
    key_dates: [
      { date: "2026-11-13", label: "Legal deadline" },
      { date: "2026-11-20", label: "Renewal deadline" },
    ],
  },
};

describe("WebmailStructuredSummary", () => {
  it("renders loading state with spinner and cancel button", () => {
    const onCancel = vi.fn();
    render(
      <WebmailStructuredSummary
        response={null}
        isLoading={true}
        messageUid={101}
        onDismiss={vi.fn()}
        onCancel={onCancel}
      />,
    );

    expect(screen.getByTestId("ai-summary-loading")).toBeTruthy();
    expect(screen.getByText("page.webmail.ai_generating")).toBeTruthy();

    const cancelBtn = screen.getByText("page.webmail.ai_cancel");
    fireEvent.click(cancelBtn);
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("renders error state with retry button", () => {
    const onRetry = vi.fn();
    render(
      <WebmailStructuredSummary
        response={null}
        isLoading={false}
        error="Network timeout error"
        messageUid={101}
        onDismiss={vi.fn()}
        onRetry={onRetry}
      />,
    );

    expect(screen.getByTestId("ai-summary-error")).toBeTruthy();
    expect(screen.getByText("Network timeout error")).toBeTruthy();

    const retryBtn = screen.getByText("webmail_next.ai_retry");
    fireEvent.click(retryBtn);
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("renders full structured success state with bullets, todos, key dates, and language", () => {
    render(
      <WebmailStructuredSummary
        response={mockSuccessResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={vi.fn()}
      />,
    );

    expect(screen.getByTestId("structured-summary-card")).toBeTruthy();
    expect(screen.getByTestId("ai-summary-language")).toBeTruthy();
    expect(screen.getByText("en")).toBeTruthy();

    // Key dates badges
    const dateBadges = screen.getAllByTestId("ai-key-date-badge");
    expect(dateBadges.length).toBe(2);
    expect(screen.getByText(/Renewal deadline: 2026-11-20/)).toBeTruthy();

    // Bullets list
    expect(screen.getByTestId("ai-key-points-list")).toBeTruthy();
    expect(screen.getByText(/Contract expires on 2026-12-31/)).toBeTruthy();

    // Confidence
    expect(screen.getByText("webmail_next.ai_confidence 95%")).toBeTruthy();

    // Todos list
    expect(screen.getByTestId("ai-todo-list")).toBeTruthy();
    expect(
      screen.getByText("Complete Schedule C DocuSign signing"),
    ).toBeTruthy();
    expect(
      screen.getByText("Confirm cloud contract renewal order"),
    ).toBeTruthy();
  });

  it("renders cache_hit badge when response.cache_hit is true", () => {
    const cacheHitResponse: MailboxAIAssistResponse = {
      ...mockSuccessResponse,
      cache_hit: true,
    };

    render(
      <WebmailStructuredSummary
        response={cacheHitResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={vi.fn()}
      />,
    );

    expect(screen.getByTestId("ai-cache-hit")).toBeTruthy();
    expect(screen.getByText("webmail_next.ai_cache_hit")).toBeTruthy();
  });

  it("renders degraded warning banner and fallback text when degraded is true", () => {
    const degradedResponse: MailboxAIAssistResponse = {
      template_version: 1,
      degraded: true,
      degrade_reason: "schema_retry_exhausted",
      cache_hit: false,
      result: {
        text: "Degraded plain text summary fallback from raw model output.",
      },
    };

    render(
      <WebmailStructuredSummary
        response={degradedResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={vi.fn()}
      />,
    );

    expect(screen.getByTestId("ai-degraded-notice")).toBeTruthy();
    expect(screen.getByText("webmail_next.ai_degraded_notice")).toBeTruthy();

    expect(screen.getByTestId("ai-fallback-text")).toBeTruthy();
    expect(
      screen.getByText(
        "Degraded plain text summary fallback from raw model output.",
      ),
    ).toBeTruthy();
    expect(screen.queryByTestId("ai-todo-list")).toBeNull();
  });

  it("toggles and persists todo checkbox state in localStorage", () => {
    render(
      <WebmailStructuredSummary
        response={mockSuccessResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={vi.fn()}
      />,
    );

    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes.length).toBe(2);
    expect((checkboxes[0] as HTMLInputElement).checked).toBe(false);

    fireEvent.click(checkboxes[0]);
    expect((checkboxes[0] as HTMLInputElement).checked).toBe(true);
    expect(localStorage.getItem("urmail_ai_todo_101_0")).toBe("true");

    // Click again to uncheck
    fireEvent.click(checkboxes[0]);
    expect((checkboxes[0] as HTMLInputElement).checked).toBe(false);
    expect(localStorage.getItem("urmail_ai_todo_101_0")).toBe("false");
  });

  it("triggers refine action callbacks when chips are clicked", () => {
    const onRefineStandard = vi.fn();
    const onRefineShorter = vi.fn();
    const onRefineTodos = vi.fn();
    const onRefineTranslate = vi.fn();

    render(
      <WebmailStructuredSummary
        response={mockSuccessResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={vi.fn()}
        onRefineStandard={onRefineStandard}
        onRefineShorter={onRefineShorter}
        onRefineTodos={onRefineTodos}
        onRefineTranslate={onRefineTranslate}
      />,
    );

    fireEvent.click(screen.getByTestId("chip-refine-standard"));
    expect(onRefineStandard).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId("chip-refine-shorter"));
    expect(onRefineShorter).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId("chip-refine-todos"));
    expect(onRefineTodos).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId("chip-refine-translate"));
    expect(onRefineTranslate).toHaveBeenCalledTimes(1);
  });

  it("dismisses summary on close button click and Escape key", () => {
    const onDismiss = vi.fn();
    render(
      <WebmailStructuredSummary
        response={mockSuccessResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={onDismiss}
      />,
    );

    fireEvent.click(screen.getByTestId("btn-dismiss-summary"));
    expect(onDismiss).toHaveBeenCalledTimes(1);

    fireEvent.keyDown(window, { key: "Escape" });
    expect(onDismiss).toHaveBeenCalledTimes(2);
  });

  it("renders translated text block when translated_text is present", () => {
    const translationResponse: MailboxAIAssistResponse = {
      ...mockSuccessResponse,
      result: {
        ...mockSuccessResponse.result,
        translated_text: "这是一段完整的中文对照翻译内容。",
      },
    };

    render(
      <WebmailStructuredSummary
        response={translationResponse}
        isLoading={false}
        messageUid={101}
        onDismiss={vi.fn()}
      />,
    );

    expect(screen.getByTestId("ai-translation-block")).toBeTruthy();
    expect(screen.getByText("这是一段完整的中文对照翻译内容。")).toBeTruthy();
  });
});
