import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  InvitationStatus,
  SendInvitationsButton,
  invitationStatusLabel,
} from "./mailbox-invitation";
import * as api from "./mailbox-api";

vi.mock("react-i18next", () => {
  const t = (key: string, params?: { count?: number }) =>
    params?.count !== undefined ? key + ":" + params.count : key;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});

vi.mock("@/lib/api/client", () => ({
  getApiErrorKey: (err: unknown) => ({
    key: err instanceof Error ? err.message : "error.REQUEST_FAILED",
    params: {},
  }),
}));

const sendInvitations = vi.fn();
vi.mock("@tanstack/react-query", () => ({
  useMutation: (options: {
    mutationFn: () => Promise<unknown>;
    onSuccess: (res: unknown) => void;
    onError: (err: unknown) => void;
  }) => ({
    isPending: false,
    mutate: () => {
      options
        .mutationFn()
        .then((res) => options.onSuccess(res))
        .catch((err) => options.onError(err));
    },
  }),
}));

vi.mock("./mailbox-api", () => ({
  sendEventInvitations: (...args: unknown[]) => sendInvitations(...args),
}));

afterEach(() => {
  cleanup();
  sendInvitations.mockReset();
});

describe("invitation status display", () => {
  it("maps every stored PARTSTAT to an i18n key and defaults unknowns", () => {
    expect(invitationStatusLabel("accepted")).toBe(
      "page.webmail.invitation_status_accepted",
    );
    expect(invitationStatusLabel("declined")).toBe(
      "page.webmail.invitation_status_declined",
    );
    expect(invitationStatusLabel("tentative")).toBe(
      "page.webmail.invitation_status_tentative",
    );
    expect(invitationStatusLabel("needs-action")).toBe(
      "page.webmail.invitation_status_needs-action",
    );
    expect(invitationStatusLabel(undefined)).toBe(
      "page.webmail.invitation_status_needs-action",
    );
    expect(invitationStatusLabel("organizer-approved")).toBe(
      "page.webmail.invitation_status_needs-action",
    );
  });

  it("renders one row per attendee with her own status", () => {
    render(
      <InvitationStatus
        attendees={[
          { email: "ada@example.test", partstat: "accepted" },
          { email: "bob@example.test", partstat: "needs-action" },
          { email: "carol@example.test", partstat: "declined" },
        ]}
      />,
    );
    expect(
      screen.getByText(
        "ada@example.test: page.webmail.invitation_status_accepted",
      ),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "bob@example.test: page.webmail.invitation_status_needs-action",
      ),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "carol@example.test: page.webmail.invitation_status_declined",
      ),
    ).toBeTruthy();
  });

  it("renders nothing without attendees", () => {
    const { container } = render(<InvitationStatus attendees={undefined} />);
    expect(container.childElementCount).toBe(0);
  });
});

describe("send invitations confirmation", () => {
  it("sends with confirm=true and shows the i18n-visible queued result", async () => {
    sendInvitations.mockResolvedValue({
      queued: 2,
      sequence: 1,
      method: "request",
    });
    render(<SendInvitationsButton eventId={42} />);
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "page.webmail.invitation_sent:2",
      ),
    );
    expect(sendInvitations).toHaveBeenCalledWith(42, "request", true);
  });

  it("surfaces a refused burst as an i18n-visible error", async () => {
    sendInvitations.mockRejectedValue(
      new Error("error.INVITATION_ALREADY_SENT"),
    );
    render(<SendInvitationsButton eventId={42} />);
    fireEvent.click(screen.getByRole("button"));
    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "error.INVITATION_ALREADY_SENT",
      ),
    );
  });
});

type ApiModule = typeof api;
expect(typeof (api as ApiModule).sendEventInvitations).toBe("function");
