import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MailboxSearchBox, splitMailboxSearch } from "./mailbox-search-box";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

afterEach(cleanup);

it("separates typed dates without changing quoted literal search text", () => {
  expect(
    splitMailboxSearch(
      'subject:"literal since:2026-01-01" since:2026-10-01 before:2026-11-01 has:attachment',
    ),
  ).toEqual({
    draft: 'subject:"literal since:2026-01-01"',
    since: "2026-10-01",
    before: "2026-11-01",
    attachment: true,
  });
  expect(splitMailboxSearch("since:2026-01-01 since:2026-02-01").draft).toBe(
    "since:2026-01-01 since:2026-02-01",
  );
});

it("submits controlled filters to the existing authenticated IMAP search path", () => {
  const searched = vi.fn();
  render(
    <MailboxSearchBox value="from:owner@example.test" onSearch={searched} />,
  );
  fireEvent.click(screen.getByText("page.webmail.search_advanced"));
  fireEvent.change(screen.getByLabelText("page.webmail.search_since"), {
    target: { value: "2026-10-01" },
  });
  fireEvent.click(screen.getByLabelText("page.webmail.search_attachment"));
  fireEvent.click(screen.getByRole("button", { name: "page.webmail.search" }));
  expect(searched).toHaveBeenCalledWith(
    "from:owner@example.test since:2026-10-01 has:attachment",
  );
});

it("renders scope selector dropdown for Current Folder vs All Folders (WP02 / I01)", () => {
  const onScopeChange = vi.fn();
  render(
    <MailboxSearchBox
      value=""
      onSearch={vi.fn()}
      scope="current"
      currentFolderName="INBOX"
      onScopeChange={onScopeChange}
    />,
  );
  const select = screen.getByLabelText(
    "page.webmail.search_scope",
  ) as HTMLSelectElement;
  expect(select).toBeTruthy();
  expect(select.value).toBe("current");

  fireEvent.change(select, { target: { value: "all" } });
  expect(onScopeChange).toHaveBeenCalledWith("all");
});
