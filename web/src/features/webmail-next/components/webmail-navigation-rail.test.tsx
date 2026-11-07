import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { WebmailNavigationRail } from "./webmail-navigation-rail";
import { MAILBOX_DRAG_TYPE } from "./webmail-message-list";
import type { MailboxFolder } from "@/features/webmail/mailbox-api";

const folders: MailboxFolder[] = [
  { name: "INBOX", name_enc: "INBOX", delimiter: "/" },
  {
    name: "Archive",
    name_enc: "Archive",
    delimiter: "/",
    flags: ["\\Archive"],
  },
  { name: "Trash", name_enc: "Trash", delimiter: "/", flags: ["\\Trash"] },
  { name: "Projects", name_enc: "Projects", delimiter: "/" },
];

const baseProps = {
  isCollapsed: false,
  activeFolder: "INBOX",
  onSelectFolder: vi.fn(),
  folders,
  onCompose: vi.fn(),
};

function makeDragEvent(uids: number[]): unknown {
  return {
    dataTransfer: {
      types: [MAILBOX_DRAG_TYPE],
      getData: (type: string) =>
        type === MAILBOX_DRAG_TYPE ? JSON.stringify(uids) : "",
      dropEffect: "none",
    },
    preventDefault: vi.fn(),
  };
}

describe("webmail navigation rail (P1-2 / P1-7)", () => {
  it("renders the create-folder button at the bottom of the folder list", () => {
    render(<WebmailNavigationRail {...baseProps} onCreateFolder={vi.fn()} />);
    expect(screen.getByTestId("folder-create-button")).toBeTruthy();
  });

  it("creates a folder and switches to it", async () => {
    const onCreateFolder = vi
      .fn()
      .mockResolvedValue({ name: "Work", name_enc: "Work", delimiter: "/" });
    const onSelectFolder = vi.fn();
    render(
      <WebmailNavigationRail
        {...baseProps}
        onSelectFolder={onSelectFolder}
        onCreateFolder={onCreateFolder}
      />,
    );
    fireEvent.click(screen.getByTestId("folder-create-button"));
    const input = screen.getByTestId("folder-create-input");
    fireEvent.change(input, { target: { value: "Work" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onCreateFolder).toHaveBeenCalledWith("Work"));
    await waitFor(() => expect(onSelectFolder).toHaveBeenCalledWith("Work"));
  });

  it("offers rename/delete only for custom folders via contextmenu", () => {
    render(
      <WebmailNavigationRail
        {...baseProps}
        onRenameFolder={vi.fn()}
        onDeleteFolder={vi.fn()}
      />,
    );
    // Custom folder: opens the folder menu.
    fireEvent.contextMenu(screen.getByTestId("nav-folder-Projects"));
    expect(screen.getByTestId("webmail-folder-context-menu")).toBeTruthy();
    // Rename and delete apply to the custom folder the menu was opened on.
    expect(screen.queryByTestId("folder-menu-rename")).toBeTruthy();
    expect(screen.queryByTestId("folder-menu-delete")).toBeTruthy();
  });

  it("shows the delete confirmation with the server contract and confirms", async () => {
    const onDeleteFolder = vi.fn().mockResolvedValue(true);
    render(
      <WebmailNavigationRail
        {...baseProps}
        onDeleteFolder={onDeleteFolder}
        deleteConfirmSlot={({ open, folderName, onCancel, onConfirm }) =>
          open ? (
            <div role="dialog" data-testid="folder-delete-dialog">
              <p>Delete {folderName}?</p>
              <button data-testid="folder-delete-cancel" onClick={onCancel} />
              <button data-testid="folder-delete-confirm" onClick={onConfirm} />
            </div>
          ) : null
        }
      />,
    );
    fireEvent.contextMenu(screen.getByTestId("nav-folder-Projects"));
    fireEvent.click(screen.getByTestId("folder-menu-delete"));
    expect(screen.getByTestId("folder-delete-dialog")).toBeTruthy();
    expect(screen.getByTestId("folder-delete-dialog").textContent).toContain(
      "Projects",
    );
    fireEvent.click(screen.getByTestId("folder-delete-confirm"));
    await waitFor(() =>
      expect(onDeleteFolder).toHaveBeenCalledWith("Projects"),
    );
  });

  it("highlights the hovered folder and moves dropped UIDs", async () => {
    const onDropUids = vi.fn().mockResolvedValue(true);
    render(<WebmailNavigationRail {...baseProps} onDropUids={onDropUids} />);
    const target = screen.getByTestId("nav-folder-archive");
    fireEvent.dragOver(target, makeDragEvent([3, 4, 5]) as never);
    expect(target.style.getPropertyValue("background-color")).toContain(
      "var(--vellum-row-active)",
    );
    fireEvent.drop(target, makeDragEvent([3, 4, 5]) as never);
    await waitFor(() =>
      expect(onDropUids).toHaveBeenCalledWith("Archive", [3, 4, 5]),
    );
    expect(target.style.getPropertyValue("background-color")).toBe("");
  });

  it("clears the highlight and keeps rows when the move fails", async () => {
    const onDropUids = vi.fn().mockResolvedValue(false);
    render(<WebmailNavigationRail {...baseProps} onDropUids={onDropUids} />);
    const target = screen.getByTestId("nav-folder-archive");
    fireEvent.dragOver(target, makeDragEvent([3]) as never);
    fireEvent.drop(target, makeDragEvent([3]) as never);
    await waitFor(() => expect(onDropUids).toHaveBeenCalled());
    await waitFor(() =>
      expect(target.style.getPropertyValue("background-color")).toBe(""),
    );
  });
});
