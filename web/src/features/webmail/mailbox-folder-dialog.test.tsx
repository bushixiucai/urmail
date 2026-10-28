import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MailboxFolderDialog } from "./mailbox-folder-dialog";
import * as api from "./mailbox-api";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("./mailbox-api", async (original) => ({
  ...(await original<typeof import("./mailbox-api")>()),
  createMailboxFolder: vi.fn(),
  renameMailboxFolder: vi.fn(),
  deleteMailboxFolder: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
const folder = {
  name: "Research",
  name_enc: "b64.UmVzZWFyY2g",
  delimiter: "/",
  flags: [],
};
describe("mailbox folder management", () => {
  it("binds rename to the selected folder generation", async () => {
    vi.mocked(api.renameMailboxFolder).mockResolvedValue({
      name_enc: "b64.UmVuYW1lZA",
    });
    const changed = vi.fn(async () => {});
    render(
      <MailboxFolderDialog
        open
        onOpenChange={vi.fn()}
        folder={folder}
        uidValidity={123}
        onChanged={changed}
      />,
    );
    fireEvent.change(screen.getByLabelText("page.webmail.folder_rename_name"), {
      target: { value: "Renamed" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.folder_rename" }),
    );
    await waitFor(() =>
      expect(api.renameMailboxFolder).toHaveBeenCalledWith(
        folder.name_enc,
        "Renamed",
        123,
      ),
    );
    expect(changed).toHaveBeenCalledWith("b64.UmVuYW1lZA");
  });
  it("requires deliberate empty-folder deletion and blocks protected folders", async () => {
    vi.mocked(api.deleteMailboxFolder).mockResolvedValue({ deleted: true });
    const props = {
      open: true,
      onOpenChange: vi.fn(),
      folder,
      uidValidity: 123,
      onChanged: vi.fn(async () => {}),
    };
    const view = render(<MailboxFolderDialog {...props} />);
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.folder_delete",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(
      screen.getByRole("button", { name: "page.webmail.folder_delete" }),
    );
    await waitFor(() =>
      expect(api.deleteMailboxFolder).toHaveBeenCalledWith(
        folder.name_enc,
        123,
      ),
    );
    view.rerender(
      <MailboxFolderDialog
        {...props}
        folder={{ ...folder, name: "Sent", flags: ["\\Sent"] }}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "page.webmail.folder_delete" }),
    ).toBeNull();
    expect(screen.getByText("page.webmail.folder_protected")).toBeTruthy();
  });
  it("cannot mutate a folder before its SELECT identity is known", () => {
    render(
      <MailboxFolderDialog
        open
        onOpenChange={vi.fn()}
        folder={folder}
        onChanged={vi.fn(async () => {})}
      />,
    );
    fireEvent.change(screen.getByLabelText("page.webmail.folder_rename_name"), {
      target: { value: "Other" },
    });
    expect(
      (
        screen.getByRole("button", {
          name: "page.webmail.folder_rename",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(api.renameMailboxFolder).not.toHaveBeenCalled();
  });
});
