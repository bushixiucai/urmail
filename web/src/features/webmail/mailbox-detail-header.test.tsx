import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MailboxDetailHeader } from "./mailbox-detail-header";
import type { MessageDetail } from "./mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key, i18n: { language: "en" } }),
}));
afterEach(cleanup);
const message: MessageDetail = {
  uid: 1,
  uid_validity: 9,
  subject: "Mail",
  from: [{ email: "alice@example.test" }],
  date: "2026-10-30",
  flags: [],
  size: 1,
  attachments: [],
};

describe("receive authentication display", () => {
  it("shows unknown for historical mail without receiving evidence", () => {
    render(<MailboxDetailHeader message={message} dateText="2026-10-30" />);
    expect(
      screen.getAllByText("page.webmail.auth_dkim_unknown").length,
    ).toBeGreaterThan(0);
    expect(screen.queryByText("page.webmail.auth_dkim_pass")).toBeNull();
    expect(screen.getByText("page.webmail.auth_spf_unknown")).toBeTruthy();
  });
  it("does not accept an unsupported authentication source", () => {
    render(
      <MailboxDetailHeader
        message={{
          ...message,
          authentication: {
            status: "pass",
            source: "sender-header",
            reason: "dkim_verified",
          },
        }}
        dateText="date"
      />,
    );
    expect(screen.queryByText("page.webmail.auth_dkim_pass")).toBeNull();
    expect(
      screen.queryByText("page.webmail.auth_reason_dkim_verified"),
    ).toBeNull();
  });
  it.each(["pass", "fail"] as const)(
    "shows the trusted DKIM %s result and its concrete evidence",
    (status) => {
      render(
        <MailboxDetailHeader
          message={{
            ...message,
            authentication: {
              status,
              source: "urmail-receive-dkim",
              reason:
                status === "pass"
                  ? "dkim_verified"
                  : "dkim_verification_failed",
              domain: "example.test",
              checked_at: "2026-10-30T08:00:00Z",
            },
          }}
          dateText="date"
        />,
      );
      expect(screen.getByText(`page.webmail.auth_dkim_${status}`)).toBeTruthy();
      expect(screen.getByText("example.test")).toBeTruthy();
      expect(
        screen.getByText("page.webmail.auth_receiver_source"),
      ).toBeTruthy();
      expect(screen.getByText("page.webmail.auth_spf_unknown")).toBeTruthy();
    },
  );
});
