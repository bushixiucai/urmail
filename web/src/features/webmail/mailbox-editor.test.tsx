import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MailboxEditor } from "./mailbox-editor";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(cleanup);
describe("real mailbox editor", () => {
  it("updates the focused visible rich editor after adopting external text", async () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <MailboxEditor
        initialContent="first"
        isHtml={false}
        onChange={onChange}
      />,
    );
    const body = screen.getByRole("textbox", {
      name: "page.webmail.compose_rich_text",
    });
    fireEvent.focus(body);
    rerender(
      <MailboxEditor
        initialContent="Adopted & retained <text>"
        isHtml={false}
        onChange={onChange}
      />,
    );
    await waitFor(() =>
      expect(body.textContent).toBe("Adopted & retained <text>"),
    );
    expect(body.querySelector("text")).toBeNull();
  });
  it("does not drop the first visible editor input after mount", async () => {
    const onChange = vi.fn();
    render(<MailboxEditor initialContent="" onChange={onChange} />);
    const body = screen.getByRole("textbox", {
      name: "page.webmail.compose_rich_text",
    });
    body.innerHTML = "<p>First keystroke</p>";
    fireEvent.input(body);
    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith({
        html: "<p>First keystroke</p>",
        text: "First keystroke",
      }),
    );
  });
  it("gives the actual source textarea a name and preserves source edits", () => {
    const onChange = vi.fn();
    render(
      <MailboxEditor
        initialContent="<p>Original</p>"
        isSourceMode
        onChange={onChange}
      />,
    );
    const source = screen.getByRole("textbox", {
      name: "page.webmail.compose_source_code",
    });
    fireEvent.change(source, {
      target: { value: "<p><strong>Revised</strong></p>" },
    });
    expect(onChange).toHaveBeenCalledWith({
      html: "<p><strong>Revised</strong></p>",
      text: "Revised",
    });
  });
});
