import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { WebmailContactsModal } from "./webmail-contacts-modal";
import * as mailboxApi from "@/features/webmail/mailbox-api";

const mockT = (k: string, defaultOrParams?: unknown) =>
  typeof defaultOrParams === "string" ? defaultOrParams : k;

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: mockT,
  }),
}));

vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => {
  const actual = await importOriginal<typeof mailboxApi>();
  return {
    ...actual,
    listContacts: vi.fn(),
    createContact: vi.fn(),
  };
});

describe("WebmailContactsModal", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  const mockContacts = [
    {
      id: 1,
      version: 1,
      display_name: "Alice Smith",
      emails: [{ address: "alice@example.com" }],
    },
    {
      id: 2,
      version: 1,
      display_name: "Bob Jones",
      emails: [{ address: "bob@example.com" }],
    },
  ];

  beforeEach(() => {
    vi.mocked(mailboxApi.listContacts).mockResolvedValue({
      contacts: mockContacts,
    });
  });

  it("renders contact list grouped by alphabet", async () => {
    render(<WebmailContactsModal isOpen={true} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("Alice Smith")).toBeTruthy();
      expect(screen.getByText("Bob Jones")).toBeTruthy();
    });

    expect(screen.getAllByText("A").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("B").length).toBeGreaterThanOrEqual(1);
  });

  it("filters contacts by search query", async () => {
    render(<WebmailContactsModal isOpen={true} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByText("Alice Smith")).toBeTruthy();
    });

    const searchInput = screen.getByTestId("contacts-search-input");
    fireEvent.change(searchInput, { target: { value: "bob" } });

    await waitFor(() => {
      expect(screen.queryByText("Alice Smith")).toBeNull();
      expect(screen.getByText("Bob Jones")).toBeTruthy();
    });
  });

  it("creates a new contact and adds to list", async () => {
    vi.mocked(mailboxApi.createContact).mockResolvedValue({
      id: 3,
      version: 1,
      display_name: "Charlie Brown",
      emails: [{ address: "charlie@example.com" }],
    });

    render(<WebmailContactsModal isOpen={true} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByTestId("btn-toggle-add-contact")).toBeTruthy();
    });

    // Toggle add form
    fireEvent.click(screen.getByTestId("btn-toggle-add-contact"));

    const nameInput = screen.getByTestId("input-contact-name");
    const emailInput = screen.getByTestId("input-contact-email");
    const form = screen.getByTestId("add-contact-form");

    fireEvent.change(nameInput, { target: { value: "Charlie Brown" } });
    fireEvent.change(emailInput, { target: { value: "charlie@example.com" } });
    fireEvent.submit(form);

    await waitFor(() => {
      expect(mailboxApi.createContact).toHaveBeenCalledWith(
        "Charlie Brown",
        "charlie@example.com",
      );
      expect(screen.getByText("Charlie Brown")).toBeTruthy();
    });
  });

  it("triggers onComposeTo when compose button clicked", async () => {
    const onCompose = vi.fn();
    const onClose = vi.fn();

    render(
      <WebmailContactsModal
        isOpen={true}
        onClose={onClose}
        onComposeTo={onCompose}
      />,
    );

    await waitFor(() => {
      expect(screen.getByTestId("btn-compose-to-1")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("btn-compose-to-1"));
    expect(onCompose).toHaveBeenCalledWith("alice@example.com");
    expect(onClose).toHaveBeenCalled();
  });

  it("handles malformed/corrupted contacts safely without crashing", async () => {
    vi.mocked(mailboxApi.listContacts).mockResolvedValueOnce({
      contacts: [
        null as unknown as mailboxApi.MailboxContact,
        {
          id: 99,
          version: 1,
          // display_name missing, emails undefined
        } as unknown as mailboxApi.MailboxContact,
        {
          id: 100,
          version: 1,
          display_name: "",
          emails: [{ address: "orphan@example.com" }],
        },
      ],
    });

    render(<WebmailContactsModal isOpen={true} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByTestId("webmail-contacts-modal")).toBeTruthy();
      expect(
        screen.getAllByText("orphan@example.com").length,
      ).toBeGreaterThanOrEqual(1);
    });
  });
});
