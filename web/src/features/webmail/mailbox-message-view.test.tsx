import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MessageHTMLFrame } from "./mailbox-message-view";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});

afterEach(cleanup);

function renderFrame(overrides?: { loadImages?: boolean; notes?: string[] }) {
  const onLoadImages = vi.fn();
  render(
    <MessageHTMLFrame
      html="<p>hello</p>"
      notes={overrides?.notes ?? ["remote_images_blocked"]}
      loadImages={overrides?.loadImages ?? false}
      onLoadImages={onLoadImages}
    />,
  );
  return { onLoadImages };
}

describe("MessageHTMLFrame", () => {
  it("sandboxes the untrusted document without scripts or same-origin", () => {
    renderFrame();
    const frame = screen.getByTitle("page.webmail.message_body");
    expect(frame.getAttribute("sandbox")).toBe("");
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(frame.getAttribute("srcdoc")).toContain("hello");
  });

  it("caps height and expands on demand", () => {
    renderFrame();
    const frame = screen.getByTitle("page.webmail.message_body");
    expect(frame.className).toContain("h-[420px]");
    fireEvent.click(screen.getByText("page.webmail.expand_body"));
    expect(frame.className).toContain("h-[80vh]");
    expect(screen.getByText("page.webmail.collapse_body")).toBeTruthy();
  });

  it("shows the blocked-image bar and re-requests on load images", () => {
    const { onLoadImages } = renderFrame();
    expect(screen.getByText("page.webmail.remote_images_blocked")).toBeTruthy();
    fireEvent.click(screen.getByText("page.webmail.load_images"));
    expect(onLoadImages).toHaveBeenCalledTimes(1);
  });

  it("hides the load action once images are allowed", () => {
    renderFrame({ loadImages: true });
    expect(screen.queryByText("page.webmail.load_images")).toBeNull();
  });

  it("shows the truncation notice from server notes", () => {
    renderFrame({ notes: ["html_truncated"] });
    expect(screen.getByText("page.webmail.html_truncated")).toBeTruthy();
  });
});
