import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailAICueStrip } from "./webmail-ai-cue-strip";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  cleanup();
});

describe("WebmailAICueStrip", () => {
  it("renders cue strip with long mail text and trigger button", () => {
    const onTrigger = vi.fn();
    render(<WebmailAICueStrip onTriggerSummary={onTrigger} />);

    expect(screen.getByTestId("ai-cue-strip")).toBeTruthy();
    const btn = screen.getByTestId("btn-trigger-summary");
    expect(btn).toBeTruthy();

    fireEvent.click(btn);
    expect(onTrigger).toHaveBeenCalledTimes(1);
  });

  it("disables trigger button when isLoading is true", () => {
    const onTrigger = vi.fn();
    render(<WebmailAICueStrip onTriggerSummary={onTrigger} isLoading={true} />);

    const btn = screen.getByTestId("btn-trigger-summary");
    expect(btn.hasAttribute("disabled")).toBe(true);
  });
});
