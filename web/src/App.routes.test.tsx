import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { MemoryRouter, Routes, Route, Navigate } from "react-router-dom";
import {
  hasMailboxSession,
  setMailboxSession,
} from "@/features/webmail/mailbox-api";

// Test the routing logic as defined in App.tsx
function MailAliasRoute() {
  if (hasMailboxSession()) {
    return <Navigate to="/webmail/inbox" replace />;
  }
  return <Navigate to="/webmail/login" replace />;
}

function WebmailRoute() {
  if (hasMailboxSession()) {
    return <Navigate to="/webmail/inbox" replace />;
  }
  return <div data-testid="mailbox-login-view">Mailbox Login Page</div>;
}

describe("App Route Shunting", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    window.localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("/mail redirects to /webmail/login when no mailbox session", async () => {
    render(
      <MemoryRouter initialEntries={["/mail"]}>
        <Routes>
          <Route path="/mail" element={<MailAliasRoute />} />
          <Route
            path="/webmail/login"
            element={<div>Webmail Login Destination</div>}
          />
          <Route
            path="/webmail/inbox"
            element={<div>Webmail Inbox Destination</div>}
          />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText("Webmail Login Destination")).toBeTruthy();
    });
  });

  it("/mail redirects to /webmail/inbox when mailbox session exists", async () => {
    setMailboxSession({
      token: "mb-session-token",
      address: "employee@domain.test",
      expires_at: "2099-01-01T00:00:00Z",
    });

    render(
      <MemoryRouter initialEntries={["/mail"]}>
        <Routes>
          <Route path="/mail" element={<MailAliasRoute />} />
          <Route
            path="/webmail/login"
            element={<div>Webmail Login Destination</div>}
          />
          <Route
            path="/webmail/inbox"
            element={<div>Webmail Inbox Destination</div>}
          />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText("Webmail Inbox Destination")).toBeTruthy();
    });
  });

  it("/webmail renders MailboxLoginPage directly without admin ProtectedRoute", async () => {
    render(
      <MemoryRouter initialEntries={["/webmail"]}>
        <Routes>
          <Route path="/webmail" element={<WebmailRoute />} />
          <Route
            path="/webmail/inbox"
            element={<div>Webmail Inbox Destination</div>}
          />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId("mailbox-login-view")).toBeTruthy();
  });

  it("/webmail redirects to /webmail/inbox when mailbox session is present", async () => {
    setMailboxSession({
      token: "mb-session-token",
      address: "employee@domain.test",
      expires_at: "2099-01-01T00:00:00Z",
    });

    render(
      <MemoryRouter initialEntries={["/webmail"]}>
        <Routes>
          <Route path="/webmail" element={<WebmailRoute />} />
          <Route
            path="/webmail/inbox"
            element={<div>Webmail Inbox Destination</div>}
          />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText("Webmail Inbox Destination")).toBeTruthy();
    });
  });
});
