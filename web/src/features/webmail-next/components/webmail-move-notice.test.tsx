import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailMoveNotice } from "./webmail-move-notice";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

afterEach(() => {
  cleanup();
});

describe("WebmailMoveNotice", () => {
  it("renders move message and undo button", () => {
    const onUndo = vi.fn();
    render(
      <WebmailMoveNotice
        message="Moved to Archive"
        token={1}
        onDone={vi.fn()}
        onUndo={onUndo}
      />,
    );

    expect(screen.getByTestId("webmail-move-notice")).toBeTruthy();
    expect(screen.getByText("Moved to Archive")).toBeTruthy();

    const undoBtn = screen.getByTestId("webmail-move-undo");
    fireEvent.click(undoBtn);
    expect(onUndo).toHaveBeenCalledTimes(1);
  });

  it("renders Sieve rule recommendation when suggestRule is provided", () => {
    const onSuggestRule = vi.fn();
    render(
      <WebmailMoveNotice
        message="Moved to Newsletters"
        token={2}
        onDone={vi.fn()}
        suggestRule={{ sender: "news@example.com", folder: "Newsletters" }}
        onSuggestRule={onSuggestRule}
      />,
    );

    expect(screen.getByText("webmail_next.suggest_sieve_rule")).toBeTruthy();
    const createRuleBtn = screen.getByTestId("btn-create-sieve-rule");
    expect(createRuleBtn).toBeTruthy();

    fireEvent.click(createRuleBtn);
    expect(onSuggestRule).toHaveBeenCalledTimes(1);
  });

  it("does not render when message is null", () => {
    const { container } = render(
      <WebmailMoveNotice message={null} token={0} onDone={vi.fn()} />,
    );
    expect(container.firstChild).toBeNull();
  });
});
