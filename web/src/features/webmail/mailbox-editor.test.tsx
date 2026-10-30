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

  it("renders table, font size, and color toolbar controls", () => {
    const onChange = vi.fn();
    render(<MailboxEditor initialContent="<p>Table test</p>" onChange={onChange} />);

    expect(screen.getByLabelText("page.webmail.compose_font_size")).toBeDefined();
    expect(screen.getByLabelText("page.webmail.compose_font_color")).toBeDefined();
    expect(screen.getByLabelText("page.webmail.compose_insert_table")).toBeDefined();
    expect(screen.getByLabelText("page.webmail.compose_insert_image")).toBeDefined();
  });

  it("inserts table structure when table button is clicked", async () => {
    const onChange = vi.fn();
    render(<MailboxEditor initialContent="<p>Content</p>" onChange={onChange} />);

    const tableBtn = screen.getByLabelText("page.webmail.compose_insert_table");
    fireEvent.click(tableBtn);

    await waitFor(() => {
      const calls = onChange.mock.calls;
      const lastCall = calls[calls.length - 1]?.[0];
      expect(lastCall?.html).toContain("<table");
    });
  });

  it("handles clipboard image paste and invokes onPasteAttachment", async () => {
    const onChange = vi.fn();
    const onPasteAttachment = vi.fn();
    render(
      <MailboxEditor
        initialContent="<p>Paste test</p>"
        onChange={onChange}
        onPasteAttachment={onPasteAttachment}
      />,
    );

    const body = screen.getByRole("textbox", {
      name: "page.webmail.compose_rich_text",
    });

    const file = new File(["dummy image content"], "screenshot.png", {
      type: "image/png",
    });
    const clipboardData = {
      items: [
        {
          type: "image/png",
          getAsFile: () => file,
        },
      ],
      types: ["Files"],
      files: [file],
      getData: () => "",
    };

    const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(pasteEvent, "clipboardData", {
      value: clipboardData,
    });
    body.dispatchEvent(pasteEvent);

    await waitFor(() => {
      expect(onPasteAttachment).toHaveBeenCalled();
      const att = onPasteAttachment.mock.calls[0][0];
      expect(att.filename).toBe("screenshot.png");
      expect(att.content_type).toBe("image/png");
      expect(att.inline).toBe(true);
      expect(att.content_id).toMatch(/^inline-/);
    });
  });

  it("changes font size and font color from toolbar", async () => {
    const onChange = vi.fn();
    render(<MailboxEditor initialContent="<p>Formatted text</p>" onChange={onChange} />);

    const sizeSelect = screen.getByLabelText("page.webmail.compose_font_size") as HTMLSelectElement;
    fireEvent.change(sizeSelect, { target: { value: "18px" } });
    expect(sizeSelect.value).toBe("18px");

    const colorBtn = screen.getByLabelText("page.webmail.compose_font_color");
    fireEvent.click(colorBtn);
    const redColorBtn = screen.getByTitle("Red");
    expect(redColorBtn).toBeDefined();
    fireEvent.click(redColorBtn);
  });
});
