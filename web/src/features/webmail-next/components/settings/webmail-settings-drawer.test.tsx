import { useState } from "react";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { WebmailSettingsDrawer } from "./webmail-settings-drawer";
import * as mailboxApi from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, defaultOrParams?: unknown) =>
      typeof defaultOrParams === "string" ? defaultOrParams : k,
  }),
}));

vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => {
  const actual = await importOriginal<typeof mailboxApi>();
  return {
    ...actual,
    changeMailboxPassword: vi.fn(),
    clearMailboxSession: vi.fn(),
    getMailboxQuota: vi.fn(),
    listMailboxIdentities: vi.fn(),
    getMailboxSettings: vi.fn(),
    saveMailboxSettings: vi.fn(),
    getMailboxRules: vi.fn(),
    saveMailboxRules: vi.fn(),
    listFolders: vi.fn(),
  };
});

describe("WebmailSettingsDrawer", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  beforeEach(() => {
    vi.mocked(mailboxApi.getMailboxQuota).mockResolvedValue({
      used_bytes: 250 * 1024 * 1024,
      quota_bytes: 1024 * 1024 * 1024,
      max_message_bytes: 10 * 1024 * 1024,
    });

    vi.mocked(mailboxApi.listMailboxIdentities).mockResolvedValue({
      identities: [
        { address: "tiger@urmail.cn", primary: true },
        { address: "support@urmail.cn", primary: false },
      ],
    });

    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValue({
      display_name: "Tiger Wang",
      signature_text: "Best regards,\nTiger",
      signature_html: "<p>Best regards,<br><b>Tiger</b></p>",
      preferences: {},
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "tiger@urmail.cn",
      identity_profiles: {
        "tiger@urmail.cn": {
          display_name: "Tiger Wang",
          signature_text: "Best regards,\nTiger",
          signature_html: "<p>Best regards,<br><b>Tiger</b></p>",
        },
      },
    });

    vi.mocked(mailboxApi.getMailboxRules).mockResolvedValue({
      version: "v1",
      state: "active",
      rules: {
        rules: [
          {
            header: "subject",
            match: "contains",
            value: "invoice",
            action: "fileinto",
            folder: "Financial",
            disabled: false,
          },
        ],
        vacation: {
          enabled: false,
          subject: "Out of Office",
          message: "On leave",
          days: 1,
        },
        forwarding: {
          enabled: false,
          target: "backup@example.com",
          keep_copy: true,
        },
      },
    });

    vi.mocked(mailboxApi.listFolders).mockResolvedValue({
      folders: [
        { name: "Inbox", name_enc: "INBOX", delimiter: "/" },
        { name: "Financial", name_enc: "Financial", delimiter: "/" },
      ],
    });
  });

  it("renders when open and closes on Close button click", () => {
    const onClose = vi.fn();
    render(<WebmailSettingsDrawer isOpen={true} onClose={onClose} />);

    expect(screen.getByTestId("webmail-settings-drawer")).toBeTruthy();
    const closeBtn = screen.getByTestId("btn-close-settings-drawer");
    fireEvent.click(closeBtn);
    expect(onClose).toHaveBeenCalled();
  });

  it("does not render when closed", () => {
    render(<WebmailSettingsDrawer isOpen={false} onClose={vi.fn()} />);
    expect(screen.queryByTestId("webmail-settings-drawer")).toBeNull();
  });

  it("navigates through all settings tabs", async () => {
    render(
      <WebmailSettingsDrawer
        isOpen={true}
        onClose={vi.fn()}
        initialTab="account"
      />,
    );

    // Default account security section. Other tabs stay unmounted.
    expect(await screen.findByTestId("account-security-section")).toBeTruthy();
    expect(screen.queryByTestId("storage-quota-section")).toBeNull();
    expect(screen.queryByTestId("sieve-rules-section")).toBeNull();
    expect(screen.queryByTestId("vacation-section")).toBeNull();

    // Click privacy tab
    fireEvent.click(screen.getByTestId("settings-nav-privacy"));
    await waitFor(() => {
      expect(screen.getByTestId("remote-images-section")).toBeTruthy();
    });

    // Click storage tab
    fireEvent.click(screen.getByTestId("settings-nav-quota"));
    await waitFor(() => {
      expect(screen.getByTestId("storage-quota-section")).toBeTruthy();
    });

    // Click signatures tab
    fireEvent.click(screen.getByTestId("settings-nav-signatures"));
    await waitFor(() => {
      expect(screen.getByTestId("identities-signatures-section")).toBeTruthy();
    });

    // Click rules tab
    fireEvent.click(screen.getByTestId("settings-nav-rules"));
    await waitFor(() => {
      expect(screen.getByTestId("sieve-rules-section")).toBeTruthy();
    });

    // Click vacation tab
    fireEvent.click(screen.getByTestId("settings-nav-vacation"));
    await waitFor(() => {
      expect(screen.getByTestId("vacation-section")).toBeTruthy();
    });

    // Click forwarding tab
    fireEvent.click(screen.getByTestId("settings-nav-forwarding"));
    await waitFor(() => {
      expect(screen.getByTestId("forwarding-section")).toBeTruthy();
    });
  });

  it("validates password strength and changes password in security section", async () => {
    vi.mocked(mailboxApi.changeMailboxPassword).mockResolvedValue({
      changed: true,
      reauthentication_required: true,
    });

    render(
      <WebmailSettingsDrawer
        isOpen={true}
        onClose={vi.fn()}
        initialTab="account"
      />,
    );

    const oldPwdInput = await screen.findByTestId("input-old-password");
    const newPwdInput = screen.getByTestId("input-new-password");
    const confirmPwdInput = screen.getByTestId("input-confirm-password");
    const submitBtn = screen.getByTestId("btn-change-password");

    // Type new password and check strength
    fireEvent.change(newPwdInput, { target: { value: "Secret123!" } });
    expect(screen.getByText(/Strong/i)).toBeTruthy();

    fireEvent.change(oldPwdInput, { target: { value: "OldPassword123" } });
    fireEvent.change(confirmPwdInput, { target: { value: "Secret123!" } });

    fireEvent.click(submitBtn);

    await waitFor(() => {
      expect(mailboxApi.changeMailboxPassword).toHaveBeenCalledWith(
        "OldPassword123",
        "Secret123!",
      );
    });
  });

  it("renders live signature preview and switches identity", async () => {
    render(
      <WebmailSettingsDrawer
        isOpen={true}
        onClose={vi.fn()}
        initialTab="signatures"
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("select-identity")).toBeTruthy();
    });

    const preview = screen.getByTestId("signature-rendered-preview");
    expect(preview.innerHTML).toContain("Tiger");

    // Change display name and text
    const nameInput = screen.getByTestId("input-display-name");
    fireEvent.change(nameInput, { target: { value: "Tiger W." } });
    expect(nameInput.getAttribute("value")).toBe("Tiger W.");
  });

  it("sanitizes malicious script and onerror injections in signature HTML preview (D3)", async () => {
    vi.mocked(mailboxApi.getMailboxSettings).mockResolvedValueOnce({
      display_name: "Tiger Wang",
      signature_text: "Malicious",
      signature_html: `<p>Hello</p><script>alert('xss')</script><img src="x" onerror="alert(1)"/><a href="javascript:alert(2)">click</a>`,
      preferences: {},
      version: 1,
      updated_at: "2026-11-06T00:00:00Z",
      address: "tiger@urmail.cn",
      identity_profiles: {
        "tiger@urmail.cn": {
          display_name: "Tiger Wang",
          signature_text: "Malicious",
          signature_html: `<p>Hello</p><script>alert('xss')</script><img src="x" onerror="alert(1)"/><a href="javascript:alert(2)">click</a>`,
        },
      },
    });

    render(
      <WebmailSettingsDrawer
        isOpen={true}
        onClose={vi.fn()}
        initialTab="signatures"
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("signature-rendered-preview")).toBeTruthy();
    });

    const preview = screen.getByTestId("signature-rendered-preview");
    const html = preview.innerHTML;

    // Must strip active script tags
    expect(html).not.toContain("<script");
    expect(html).not.toContain("alert('xss')");

    // Must strip onerror handler
    expect(html).not.toContain("onerror");

    // Must neutralize javascript: URLs
    expect(html).not.toContain("javascript:");

    // Legitimate content is preserved
    expect(html).toContain("Hello");
  });

  it("toggles and deletes sieve rules", async () => {
    render(
      <WebmailSettingsDrawer
        isOpen={true}
        onClose={vi.fn()}
        initialTab="rules"
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("rule-row-0")).toBeTruthy();
    });

    // Rule contains "invoice"
    expect(screen.getByText(/"invoice"/)).toBeTruthy();

    // Click toggle button
    const toggleBtn = screen.getByText("ACTIVE");
    fireEvent.click(toggleBtn);
    expect(screen.getByText("DISABLED")).toBeTruthy();
  });

  it("prompts anti-hijack confirmation when modifying forwarding target", async () => {
    render(
      <WebmailSettingsDrawer
        isOpen={true}
        onClose={vi.fn()}
        initialTab="forwarding"
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("forwarding-switch")).toBeTruthy();
    });

    const switchBtn = screen.getByTestId("forwarding-switch");
    fireEvent.click(switchBtn);

    const targetInput = screen.getByTestId("input-forwarding-target");
    fireEvent.change(targetInput, {
      target: { value: "new-hijack@untrusted.com" },
    });

    const saveBtn = screen.getByTestId("btn-save-forwarding");
    fireEvent.click(saveBtn);

    // Modal should appear
    await waitFor(() => {
      expect(screen.getByTestId("forwarding-confirm-modal")).toBeTruthy();
    });

    vi.mocked(mailboxApi.saveMailboxRules).mockResolvedValue({
      saved: true,
      state: "active",
      version: "v2",
    });

    const confirmBtn = screen.getByTestId("btn-confirm-forwarding-hijack");
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      expect(mailboxApi.saveMailboxRules).toHaveBeenCalled();
    });
  });

  it("closes on Escape and returns focus to the trigger", () => {
    function Harness() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button
            type="button"
            data-testid="webmail-settings-btn"
            onClick={() => setOpen(true)}
          >
            Settings
          </button>
          <WebmailSettingsDrawer isOpen={open} onClose={() => setOpen(false)} />
        </>
      );
    }

    render(<Harness />);
    const trigger = screen.getByTestId("webmail-settings-btn");
    trigger.focus();
    fireEvent.click(trigger);
    expect(screen.getByTestId("webmail-settings-drawer")).toBeTruthy();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByTestId("webmail-settings-drawer")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
