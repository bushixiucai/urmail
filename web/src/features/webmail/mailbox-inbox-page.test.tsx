import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { MailboxInboxPage } from "./mailbox-inbox-page";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string) => k,
    i18n: { language: "en" },
  }),
}));

vi.mock("./use-mailbox-native", () => ({
  useMailboxNative: () => ({ native: true, loaded: true }),
}));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe("mailbox inbox smoke", () => {
  it("lists folders and messages with mailbox bearer", async () => {
    window.sessionStorage.setItem("urmail.mailbox-token", "mbox-token");
    window.sessionStorage.setItem(
      "urmail.mailbox-address",
      "user@example.test",
    );

    const fetcher = vi.fn(async (url: string) => {
      if (url === "/api/v1/mailbox/folders") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            folders: [
              {
                name: "INBOX",
                name_enc: "b64.SU5CT1g",
                delimiter: "/",
                flags: [],
              },
            ],
          }),
        };
      }
      if (String(url).includes("/messages") && !String(url).includes("/1")) {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            messages: [
              {
                uid: 1,
                subject: "Hello inbox",
                from: [{ email: "a@example.test", name: "Alice" }],
                date: "2026-01-02T03:04:05Z",
                flags: [],
                size: 12,
              },
            ],
            total: 1,
            limit: 50,
            offset: 0,
          }),
        };
      }
      return { ok: false, status: 404, json: async () => ({}) };
    });
    vi.stubGlobal("fetch", fetcher);

    render(
      <MemoryRouter>
        <MailboxInboxPage />
      </MemoryRouter>,
    );

    await screen.findByText("user@example.test");
    await screen.findByText("INBOX");
    await screen.findByText("Hello inbox");

    await waitFor(() => {
      const foldersCall = fetcher.mock.calls.find(
        (c) => c[0] === "/api/v1/mailbox/folders",
      ) as [string, RequestInit] | undefined;
      const headers = foldersCall?.[1]?.headers as
        | Record<string, string>
        | undefined;
      expect(headers?.Authorization).toBe("Bearer mbox-token");
    });
  });
});
