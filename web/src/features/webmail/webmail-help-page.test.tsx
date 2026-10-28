import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { WebmailHelpPage } from "./webmail-help-page";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("./use-mailbox-native", () => ({
  useMailboxNative: () => ({
    native: false,
    loaded: true,
    mailHost: "mail.selfhost.example",
  }),
}));

afterEach(() => {
  cleanup();
});

it("shows client setup help with host and ports", () => {
  const view = render(
    <MemoryRouter>
      <WebmailHelpPage />
    </MemoryRouter>,
  );
  expect(view.getByText("page.webmail.help_title")).toBeTruthy();
  expect(view.getByText("page.webmail.help_intro")).toBeTruthy();
  expect(view.getByText("mail.selfhost.example")).toBeTruthy();
  expect(view.getByText("993")).toBeTruthy();
  expect(view.getByText("995")).toBeTruthy();
  expect(view.getByText("465 / 587")).toBeTruthy();
  expect(view.getByText("page.webmail.help_coming_soon")).toBeTruthy();
});
