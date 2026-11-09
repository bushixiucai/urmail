import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WebmailThreadSummaryBar } from "./webmail-thread-summary-bar";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, p?: Record<string, number>) => {
      if (!p) return k;
      return `${k}:${Object.entries(p)
        .map(([key, v]) => `${key}=${v}`)
        .join(",")}`;
    },
  }),
}));

describe("WebmailThreadSummaryBar (C1 §3)", () => {
  it("renders the earlier count and a load button when members remain", () => {
    const onLoad = vi.fn();
    render(
      <WebmailThreadSummaryBar
        earlierCount={741}
        state="idle"
        step={20}
        onLoadEarlier={onLoad}
        onRetry={vi.fn()}
      />,
    );
    expect(screen.getByTestId("thread-summary-bar").textContent).toContain(
      "webmail_next.thread_earlier_count:count=741",
    );
    fireEvent.click(screen.getByTestId("thread-load-earlier"));
    expect(onLoad).toHaveBeenCalledTimes(1);
  });

  it("hides entirely when everything is loaded", () => {
    render(
      <WebmailThreadSummaryBar
        earlierCount={0}
        state="done"
        step={20}
        onLoadEarlier={vi.fn()}
        onRetry={vi.fn()}
      />,
    );
    expect(screen.queryByTestId("thread-summary-bar")).toBeNull();
  });

  it("shows loading, done, and error states with a retry action", () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <WebmailThreadSummaryBar
        earlierCount={5}
        state="loading"
        step={20}
        onLoadEarlier={vi.fn()}
        onRetry={onRetry}
      />,
    );
    expect(screen.getByTestId("thread-summary-loading")).toBeTruthy();

    rerender(
      <WebmailThreadSummaryBar
        earlierCount={5}
        state="done"
        step={20}
        onLoadEarlier={vi.fn()}
        onRetry={onRetry}
      />,
    );
    expect(screen.getByTestId("thread-summary-done")).toBeTruthy();

    rerender(
      <WebmailThreadSummaryBar
        earlierCount={5}
        state="error"
        step={20}
        onLoadEarlier={vi.fn()}
        onRetry={onRetry}
      />,
    );
    fireEvent.click(screen.getByTestId("thread-summary-retry"));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });
});
