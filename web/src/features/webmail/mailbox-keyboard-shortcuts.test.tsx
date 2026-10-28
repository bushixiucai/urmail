import { fireEvent, render, screen, cleanup } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { MailboxKeyboardShortcuts } from "./mailbox-keyboard-shortcuts";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(() => {
  cleanup();
  localStorage.clear();
});
it("ignores text entry, IME, modifiers and dialogs; Delete requests the normal trash confirmation", () => {
  const action = vi.fn();
  render(
    <>
      <MailboxKeyboardShortcuts disabled={false} onAction={action} />
      <input aria-label="editor" />
    </>,
  );
  fireEvent.keyDown(screen.getByLabelText("editor"), { key: "c" });
  fireEvent.keyDown(window, { key: "r", ctrlKey: true });
  fireEvent.keyDown(window, { key: "f", isComposing: true });
  expect(action).not.toHaveBeenCalled();
  fireEvent.keyDown(window, { key: "Delete" });
  expect(action).toHaveBeenLastCalledWith("trash");
  fireEvent.keyDown(window, { key: "?" });
  expect(screen.getByRole("dialog")).toBeTruthy();
  fireEvent.keyDown(window, { key: "c" });
  expect(action).toHaveBeenCalledTimes(1);
});
it("supports disabling character shortcuts with a persistent preference", () => {
  localStorage.setItem("urmail.mailbox-shortcuts", "off");
  const action = vi.fn();
  render(<MailboxKeyboardShortcuts disabled={false} onAction={action} />);
  fireEvent.keyDown(window, { key: "c" });
  expect(action).not.toHaveBeenCalled();
  fireEvent.keyDown(window, { key: "?" });
  expect(screen.getByRole("dialog")).toBeTruthy();
});
