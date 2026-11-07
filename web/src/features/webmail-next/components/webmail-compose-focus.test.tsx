import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { WebmailComposeDock } from "./webmail-compose-dock";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("./webmail-editor-tiptap", () => ({
  WebmailEditorTiptap: () => <div data-testid="compose-editor" />,
}));

describe("WebmailComposeDock focus", () => {
  it("closes on Escape and returns focus to the trigger", () => {
    function Harness() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button
            type="button"
            data-testid="webmail-compose-button"
            onClick={() => setOpen(true)}
          >
            Compose
          </button>
          <WebmailComposeDock isOpen={open} onClose={() => setOpen(false)} />
        </>
      );
    }

    render(<Harness />);
    const trigger = screen.getByTestId("webmail-compose-button");
    trigger.focus();
    fireEvent.click(trigger);
    expect(screen.getByTestId("webmail-compose-dock")).toBeTruthy();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByTestId("webmail-compose-dock")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
