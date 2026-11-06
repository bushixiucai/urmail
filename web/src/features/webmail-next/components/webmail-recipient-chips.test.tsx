import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailRecipientChips } from "./webmail-recipient-chips";
import * as mailboxApi from "@/features/webmail/mailbox-api";

vi.mock("@/features/webmail/mailbox-api", async (importOriginal) => {
  const actual = await importOriginal<typeof mailboxApi>();
  return {
    ...actual,
    searchCorporateContacts: vi.fn(),
  };
});

describe("WebmailRecipientChips", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("shows corporate directory autocomplete suggestions and adds recipient on click", async () => {
    vi.mocked(mailboxApi.searchCorporateContacts).mockResolvedValue({
      contacts: [
        {
          id: 10,
          display_name: "Tiger Wang",
          department: "Engineering",
          job_title: "Frontend Lead",
          emails: [{ address: "tiger@urmail.cn", primary: true }],
          phones: [],
        },
      ],
    });

    const onChange = vi.fn();
    render(
      <WebmailRecipientChips label="To" recipients={[]} onChange={onChange} />,
    );

    const input = screen.getByTestId("recipient-input");
    fireEvent.change(input, { target: { value: "tig" } });

    await waitFor(() => {
      expect(
        screen.getByTestId("corporate-autocomplete-dropdown"),
      ).toBeTruthy();
      expect(screen.getByText("Tiger Wang")).toBeTruthy();
      expect(screen.getByText("tiger@urmail.cn")).toBeTruthy();
    });

    const suggestionItem = screen.getByTestId("corporate-suggestion-0");
    fireEvent.click(suggestionItem);

    expect(onChange).toHaveBeenCalledWith(["tiger@urmail.cn"]);
  });

  it("selects corporate contact with keyboard navigation ArrowDown and Enter", async () => {
    vi.mocked(mailboxApi.searchCorporateContacts).mockResolvedValue({
      contacts: [
        {
          id: 20,
          display_name: "Alice Developer",
          department: "QA",
          emails: [{ address: "alice@urmail.cn", primary: true }],
          phones: [],
        },
      ],
    });

    const onChange = vi.fn();
    render(
      <WebmailRecipientChips label="To" recipients={[]} onChange={onChange} />,
    );

    const input = screen.getByTestId("recipient-input");
    fireEvent.change(input, { target: { value: "ali" } });

    await waitFor(() => {
      expect(
        screen.getByTestId("corporate-autocomplete-dropdown"),
      ).toBeTruthy();
    });

    // Press ArrowDown to highlight and Enter to select
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onChange).toHaveBeenCalledWith(["alice@urmail.cn"]);
  });
});
