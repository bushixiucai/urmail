import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MailboxImportDialog } from "./mailbox-import-dialog";
import * as api from "./mailbox-import-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, params?: { index?: number }) =>
      params?.index ? key + ":" + params.index : key,
    i18n: { language: "en" },
  }),
}));
vi.mock("./mailbox-import-api", async (original) => ({
  ...(await original<typeof api>()),
  listMailImports: vi.fn(),
  getMailImport: vi.fn(),
  previewMailImport: vi.fn(),
  mutateMailImport: vi.fn(),
}));
const prefix = "page.webmail.import.";
function job(status = "preview", version = 1): api.MailImportJob {
  return {
    id: 7,
    version,
    status,
    format: "mbox",
    filename: "fixture.mbox",
    folder_enc: "b64.SU5CT1g",
    uid_validity: 9,
    source_digest: "a".repeat(64),
    preview_digest: "b".repeat(64),
    expires_at: "2099-01-01T00:00:00Z",
    total: 3,
    bytes: 200,
    duplicate_policy: "skip",
    items: [
      {
        index: 1,
        subject: "Invoice",
        message_id: "<fixture@local>",
        size: 100,
        sha256: "1".repeat(64),
        status: "ready",
      },
      {
        index: 2,
        subject: "Duplicate",
        message_id: "<fixture@local>",
        size: 100,
        sha256: "1".repeat(64),
        status: "duplicate",
        duplicate_of: 1,
      },
      {
        index: 3,
        subject: "Invalid",
        message_id: "",
        size: 0,
        sha256: "2".repeat(64),
        status: "invalid",
        error_code: "MAIL_IMPORT_MESSAGE_INVALID",
      },
    ],
  };
}
const props = {
  open: true,
  onOpenChange: vi.fn(),
  folder: "b64.SU5CT1g",
  validity: 9,
  onImported: vi.fn(),
};
beforeEach(() => {
  vi.mocked(api.listMailImports).mockResolvedValue({ jobs: [] });
  vi.mocked(api.previewMailImport).mockResolvedValue({ job: job() });
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
async function preview() {
  fireEvent.change(screen.getByLabelText(prefix + "file"), {
    target: {
      files: [
        new File(["From sender\nSubject: Fixture\n\nbody"], "fixture.mbox"),
      ],
    },
  });
  fireEvent.click(screen.getByRole("button", { name: prefix + "preview" }));
  await screen.findByRole("checkbox", { name: prefix + "select_message:1" });
}
function selectAndConfirm() {
  fireEvent.click(
    screen.getByRole("checkbox", { name: prefix + "select_message:1" }),
  );
  fireEvent.click(
    screen.getByRole("checkbox", { name: prefix + "confirm_scope" }),
  );
  fireEvent.change(screen.getByLabelText(prefix + "password"), {
    target: { value: "fixture-private-password" },
  });
  fireEvent.click(screen.getByRole("button", { name: prefix + "confirm" }));
}

describe("durable mail import", () => {
  it("previews only, defaults to no selection and disables duplicate/invalid rows", async () => {
    render(<MailboxImportDialog {...props} />);
    await preview();
    const first = screen.getByRole("checkbox", {
      name: prefix + "select_message:1",
    }) as HTMLInputElement;
    expect(first.checked).toBe(false);
    expect(
      screen
        .getByRole("checkbox", { name: prefix + "select_message:2" })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen
        .getByRole("checkbox", { name: prefix + "select_message:3" })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen
        .getByRole("button", { name: prefix + "confirm" })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(api.mutateMailImport).not.toHaveBeenCalled();
    expect(api.previewMailImport).toHaveBeenCalledWith(
      expect.objectContaining({
        folder_enc: "b64.SU5CT1g",
        uid_validity: 9,
        format: "mbox",
        duplicate_policy: "skip",
      }),
      expect.any(String),
      expect.any(AbortSignal),
    );
  });
  it("reuses the preview idempotency key after a lost response", async () => {
    vi.mocked(api.previewMailImport)
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockResolvedValue({ job: job() });
    render(<MailboxImportDialog {...props} />);
    fireEvent.change(screen.getByLabelText(prefix + "file"), {
      target: {
        files: [new File(["Subject: fixture\n\nbody"], "fixture.eml")],
      },
    });
    fireEvent.click(screen.getByRole("button", { name: prefix + "preview" }));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: prefix + "preview" }));
    await screen.findByRole("checkbox", { name: prefix + "select_message:1" });
    expect(vi.mocked(api.previewMailImport).mock.calls[0][1]).toBe(
      vi.mocked(api.previewMailImport).mock.calls[1][1],
    );
    expect(api.mutateMailImport).not.toHaveBeenCalled();
  });
  it("advances confirmed pending batches but stops on unknown without retrying", async () => {
    const pending = job("confirmed", 2);
    pending.items[0].status = "pending";
    const unknown = job("uncertain", 3);
    unknown.items[0].status = "unknown";
    vi.mocked(api.mutateMailImport)
      .mockResolvedValueOnce({ job: pending })
      .mockResolvedValueOnce({ job: unknown });
    render(<MailboxImportDialog {...props} />);
    await preview();
    selectAndConfirm();
    await screen.findByText(prefix + "unknown_help");
    expect(api.mutateMailImport).toHaveBeenCalledTimes(2);
    expect(api.mutateMailImport).toHaveBeenNthCalledWith(
      1,
      7,
      "confirm",
      expect.objectContaining({
        version: 1,
        preview_digest: "b".repeat(64),
        selected_indices: [1],
        confirmed: true,
        current_password: "fixture-private-password",
      }),
      expect.any(AbortSignal),
    );
    expect(api.mutateMailImport).toHaveBeenNthCalledWith(
      2,
      7,
      "resume",
      { version: 2, current_password: "fixture-private-password" },
      expect.any(AbortSignal),
    );
    expect(
      (screen.getByLabelText(prefix + "password") as HTMLInputElement).value,
    ).toBe("");
    expect(screen.queryByRole("button", { name: prefix + "retry" })).toBeNull();
  });
  it("requires reconcile, manual selection, evidence, declaration and fresh password for unknown retry", async () => {
    const unknown = job("uncertain", 2);
    unknown.items[0].status = "unknown";
    vi.mocked(api.listMailImports).mockResolvedValue({ jobs: [unknown] });
    vi.mocked(api.getMailImport).mockResolvedValue({ job: unknown });
    const reviewed = {
      ...unknown,
      version: 3,
      items: unknown.items.map((item) =>
        item.index === 1
          ? {
              ...item,
              reconcile_absent: true,
              reconciled_at: new Date().toISOString(),
            }
          : item,
      ),
    };
    const completed = job("completed", 4);
    completed.items[0].status = "imported";
    vi.mocked(api.mutateMailImport)
      .mockResolvedValueOnce({ job: reviewed })
      .mockResolvedValueOnce({ job: completed });
    render(<MailboxImportDialog {...props} />);
    fireEvent.click(
      await screen.findByRole("button", { name: prefix + "history_item" }),
    );
    await screen.findByText(prefix + "unknown_help");
    expect(
      screen
        .getByRole("checkbox", { name: prefix + "select_message:1" })
        .hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.change(screen.getByLabelText(prefix + "password"), {
      target: { value: "fresh-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: prefix + "reconcile" }));
    await screen.findByText(prefix + "retry_help");
    expect(
      screen
        .getByRole("button", { name: prefix + "retry" })
        .hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.click(
      screen.getByRole("checkbox", { name: prefix + "select_message:1" }),
    );
    fireEvent.change(screen.getByLabelText(prefix + "reason"), {
      target: { value: "Reviewed target receipt and source checksum" },
    });
    fireEvent.click(
      screen.getByRole("checkbox", { name: prefix + "not_appended" }),
    );
    expect(
      screen
        .getByRole("button", { name: prefix + "retry" })
        .hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.change(screen.getByLabelText(prefix + "password"), {
      target: { value: "new-fresh-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: prefix + "retry" }));
    await waitFor(() =>
      expect(api.mutateMailImport).toHaveBeenLastCalledWith(
        7,
        "retry",
        expect.objectContaining({
          version: 3,
          indices: [1],
          confirmed_not_appended: true,
          current_password: "new-fresh-secret",
        }),
        expect.any(AbortSignal),
      ),
    );
  });
  it("reloads durable facts after a lost confirm response without another APPEND request", async () => {
    const unknown = job("uncertain", 2);
    unknown.items[0].status = "unknown";
    vi.mocked(api.mutateMailImport).mockRejectedValue(
      new Error("response disconnected"),
    );
    vi.mocked(api.getMailImport).mockResolvedValue({ job: unknown });
    render(<MailboxImportDialog {...props} />);
    await preview();
    selectAndConfirm();
    await screen.findByText(prefix + "unknown_help");
    expect(api.mutateMailImport).toHaveBeenCalledTimes(1);
    expect(api.getMailImport).toHaveBeenCalledWith(7, expect.any(AbortSignal));
    expect(
      (screen.getByLabelText(prefix + "password") as HTMLInputElement).value,
    ).toBe("");
  });
});
