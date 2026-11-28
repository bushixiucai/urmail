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
  // REVIEW-006: the receive chain currently emits no proof for any message,
  // so the no-signal badge is hidden instead of being shown as pure noise.
  it("hides the badge entirely when no receiving evidence exists", () => {
    render(<MailboxDetailHeader message={message} dateText="2026-10-30" />);
    expect(screen.queryByText("page.webmail.auth_dkim_unknown")).toBeNull();
    expect(screen.queryByText("page.webmail.auth_spf_unknown")).toBeNull();
    expect(screen.queryByText("page.webmail.auth_receiver_source")).toBeNull();
    expect(screen.queryByText("page.webmail.auth_dkim_pass")).toBeNull();
  });
  it("hides the badge for an unsupported source without an alert signal", () => {
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
    expect(screen.queryByText("page.webmail.auth_dkim_unknown")).toBeNull();
  });
  it("keeps rendering an explicit receive-proof alert from an untrusted source", () => {
    render(
      <MailboxDetailHeader
        message={{
          ...message,
          authentication: {
            status: "fail",
            source: "sender-header",
            reason: "receive_proof_invalid",
          },
        }}
        dateText="date"
      />,
    );
    expect(
      screen.getAllByText("page.webmail.auth_dkim_unknown").length,
    ).toBeGreaterThan(0);
    expect(
      screen.getByText("page.webmail.auth_reason_receive_proof_invalid"),
    ).toBeTruthy();
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
