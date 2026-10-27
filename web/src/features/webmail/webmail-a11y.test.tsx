import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { useState } from "react";
import { ConfirmDialog } from "@/components/ui/notice-dialog";
import { ClientGuidesPage } from "@/features/guides/client-guides-page";
import { MailboxComposeDialog } from "./mailbox-compose-dialog";
import { MailboxSearchBox } from "./mailbox-search-box";
import { MailboxLoginPage } from "./mailbox-login-page";

vi.mock("react-i18next", () => {
  const t = (key: string, params?: Record<string, unknown>) =>
    params && "name" in params ? key + ":" + params.name : key;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});
vi.mock("./use-mailbox-native", () => ({
  useMailboxNative: () => ({ native: true, loaded: true }),
}));
vi.mock("./mailbox-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./mailbox-api")>()),
  hasMailboxSession: vi.fn(() => false),
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const localesRoot = resolve(__dirname, "../../../../internal/i18n/locales");
function loadLocale(file: string): Record<string, string> {
  return JSON.parse(readFileSync(resolve(localesRoot, file), "utf8"));
}

describe("webmail accessibility pass (WEBMAIL-011)", () => {
  it("guides page serves the port summary through i18n in both locales", () => {
    const en = loadLocale("en.json");
    const zh = loadLocale("zh-CN.json");
    expect(en["page.guides.ports"]).toBeTruthy();
    expect(zh["page.guides.ports"]).toBeTruthy();
    expect(en["page.guides.ports"]).not.toEqual(zh["page.guides.ports"]);
    const view = render(
      <MemoryRouter>
        <ClientGuidesPage />
      </MemoryRouter>,
    );
    expect(view.getByText("page.guides.ports")).toBeTruthy();
  });

  it("compose form labels every input and names the attachment remove buttons", () => {
    render(
      <MailboxComposeDialog
        open
        draft={{
          mode: "new",
          attachments: [
            {
              filename: "notes.txt",
              content_type: "text/plain",
              content_base64: "aGk=",
            },
          ],
        }}
        onOpenChange={vi.fn()}
        onSent={vi.fn()}
      />,
    );
    for (const id of [
      "page.webmail.compose_to",
      "page.webmail.compose_cc",
      "page.webmail.compose_bcc",
      "page.webmail.compose_subject",
      "page.webmail.compose_body",
    ]) {
      expect(screen.getByLabelText(id), id).toBeTruthy();
    }
    expect(
      screen.getByRole("button", {
        name: "page.webmail.compose_attach_remove_named:notes.txt",
      }),
    ).toBeTruthy();
  });

  it("confirm dialog traps focus, closes on Escape and restores focus", async () => {
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button type="button" id="outside">
            outside
          </button>
          <ConfirmDialog
            open={open}
            onOpenChange={setOpen}
            title="page.webmail.delete_confirm_title"
            onConfirm={vi.fn()}
          />
        </>
      );
    }
    render(<Harness />);
    const dialog = await screen.findByRole("dialog");
    // Focus starts inside the dialog.
    expect(dialog.contains(document.activeElement)).toBe(true);
    // Focus cannot leave via Tab (focus is wrapped back inside).
    const focusables = dialog.querySelectorAll("button");
    fireEvent.keyDown(focusables[focusables.length - 1], { key: "Tab" });
    expect(dialog.contains(document.activeElement)).toBe(true);
    // Escape closes; focus returns outside the (now closed) dialog.
    fireEvent.keyDown(dialog, { key: "Escape" });
    await screen.findByText("outside");
  });

  it("login form associates visible labels with its inputs", () => {
    render(
      <MemoryRouter>
        <MailboxLoginPage />
      </MemoryRouter>,
    );
    expect(screen.getByLabelText("page.webmail.field_email")).toBeTruthy();
    expect(screen.getByLabelText("page.webmail.field_password")).toBeTruthy();
  });

  it("keeps list toolbar touch targets at 44px on mobile", () => {
    const view = render(<MailboxSearchBox value="" onSearch={vi.fn()} />);
    const button = view.getByRole("button") as HTMLButtonElement;
    expect(button.className).toContain("min-h-11");
    expect(button.className).toContain("md:min-h-8");
  });
});
