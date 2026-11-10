import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { WebmailRowContextMenu } from "./webmail-row-context-menu";

const folders = [
  { name: "INBOX", name_enc: "INBOX" },
  { name: "Archive", name_enc: "Archive" },
  { name: "Projects", name_enc: "Projects" },
];

const handlers = {
  onOpen: vi.fn(),
  onReply: vi.fn(),
  onForward: vi.fn(),
  onArchive: vi.fn(),
  onDelete: vi.fn(),
  onToggleRead: vi.fn(),
  onMoveTo: vi.fn(),
};

describe("webmail row context menu", () => {
  it("renders nothing without state", () => {
    const { container } = render(
      <WebmailRowContextMenu
        state={null}
        folders={folders}
        handlers={handlers}
        onClose={vi.fn()}
      />,
    );
    expect(container.textContent).toBe("");
    expect(screen.queryByTestId("webmail-row-context-menu")).toBeNull();
  });

  it("shows the P1-1 action set for a row", () => {
    render(
      <WebmailRowContextMenu
        state={{ uid: 7, x: 10, y: 10, isUnread: true }}
        folders={folders}
        handlers={handlers}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByTestId("ctx-open")).toBeTruthy();
    expect(screen.getByTestId("ctx-reply")).toBeTruthy();
    expect(screen.getByTestId("ctx-forward")).toBeTruthy();
    expect(screen.getByTestId("ctx-archive")).toBeTruthy();
    expect(screen.getByTestId("ctx-delete")).toBeTruthy();
    expect(screen.getByTestId("ctx-toggle-read").textContent).toContain(
      "webmail_next.mark_read",
    );
    expect(screen.getByTestId("ctx-move")).toBeTruthy();
  });

  it("runs the semantic handler and closes on click", () => {
    const onClose = vi.fn();
    render(
      <WebmailRowContextMenu
        state={{ uid: 7, x: 10, y: 10, isUnread: true }}
        folders={folders}
        handlers={handlers}
        onClose={onClose}
      />,
    );
    fireEvent.click(screen.getByTestId("ctx-archive"));
    expect(handlers.onArchive).toHaveBeenCalledWith([7], undefined);
    expect(onClose).toHaveBeenCalled();
  });

  it("opens the move submenu and moves to the chosen folder", () => {
    render(
      <WebmailRowContextMenu
        state={{ uid: 7, x: 10, y: 10, isUnread: false }}
        folders={folders}
        handlers={handlers}
        onClose={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByTestId("ctx-move"));
    fireEvent.click(screen.getByTestId("ctx-move-Projects"));
    expect(handlers.onMoveTo).toHaveBeenCalledWith(
      [7],
      {
        name: "Projects",
        name_enc: "Projects",
      },
      undefined,
    );
  });

  it("closes on Escape and on outside click", () => {
    const onClose = vi.fn();
    render(
      <div data-testid="outside">
        <WebmailRowContextMenu
          state={{ uid: 7, x: 10, y: 10, isUnread: true }}
          folders={folders}
          handlers={handlers}
          onClose={onClose}
        />
      </div>,
    );
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.mouseDown(screen.getByTestId("outside"));
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
