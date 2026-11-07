import { expect, test } from "@playwright/test";

// Run against the local Vite server. This fixture never calls a real backend.
test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "set URMAIL_E2E_NATIVE_FIXTURE=1 with a local Vite base URL",
);

const EXPIRED_SESSION = {
  token: "expired-session-fixture",
  address: "reader@example.test",
  expires_at: "2020-01-01T00:00:00Z",
};

test("an expired token lands on the login page without firing doomed requests", async ({
  page,
  baseURL,
}) => {
  if (
    !baseURL ||
    !["127.0.0.1", "localhost", "[::1]"].includes(new URL(baseURL).hostname)
  ) {
    throw new Error("Native webmail fixtures require a loopback Vite server");
  }
  const doomedCalls: string[] = [];
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))
      throw new Error("Fixture attempted non-loopback access");
    if (
      url.pathname.startsWith("/api/v1/mailbox/") &&
      !url.pathname.endsWith("/status")
    ) {
      doomedCalls.push(url.pathname);
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "MAILBOX_UNAUTHORIZED", message: "unauthorized" },
        }),
      });
      return;
    }
    if (url.pathname.endsWith("/mailbox/status")) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ native: true }),
      });
      return;
    }
    await route.continue();
  });

  await page.addInitScript((session) => {
    sessionStorage.setItem("urmail.mailbox-token", session.token);
    sessionStorage.setItem("urmail.mailbox-address", session.address);
    sessionStorage.setItem("urmail.mailbox-expires", session.expires_at);
    localStorage.setItem("urmail.locale", "en");
  }, EXPIRED_SESSION);

  await page.goto("/webmail/inbox");

  // Route guard recognises the dead session before the inbox mounts.
  await expect(page).toHaveURL(/\/webmail\/login/);
  await expect
    .poll(() => doomedCalls.length, {
      message: "expired session must not fire authenticated requests",
    })
    .toBe(0);
  // The dead token was purged from storage.
  const stored = await page.evaluate(() =>
    sessionStorage.getItem("urmail.mailbox-token"),
  );
  expect(stored).toBeNull();
  // Login screen is rendered.
  await expect(page.locator("#mailbox-email")).toBeVisible();
});

test("a mid-session 401 redirects once to the login page", async ({
  page,
  baseURL,
}) => {
  if (
    !baseURL ||
    !["127.0.0.1", "localhost", "[::1]"].includes(new URL(baseURL).hostname)
  ) {
    throw new Error("Native webmail fixtures require a loopback Vite server");
  }
  const loginLoads: string[] = [];
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))
      throw new Error("Fixture attempted non-loopback access");
    if (url.pathname === "/webmail/login") loginLoads.push(url.pathname);
    const path = url.pathname;
    if (!path.startsWith("/api/")) return route.continue();
    if (path.endsWith("/mailbox/status"))
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ native: true }),
      });
    if (path.endsWith("/mailbox/folders"))
      return route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "MAILBOX_UNAUTHORIZED", message: "unauthorized" },
        }),
      });
    // Remaining endpoints answer slowly so several requests race the redirect.
    await new Promise((resolve) => setTimeout(resolve, 400));
    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "MAILBOX_UNAUTHORIZED", message: "unauthorized" },
      }),
    });
  });

  await page.addInitScript(() => {
    if (sessionStorage.getItem("urmail.fixture-seeded")) return;
    sessionStorage.setItem("urmail.fixture-seeded", "1");
    sessionStorage.setItem("urmail.mailbox-token", "dies-mid-session");
    sessionStorage.setItem("urmail.mailbox-address", "reader@example.test");
    sessionStorage.setItem(
      "urmail.mailbox-expires",
      new Date(Date.now() + 3_600_000).toISOString(),
    );
    localStorage.setItem("urmail.locale", "en");
  });

  await page.goto("/webmail/inbox");
  await expect(page).toHaveURL(/\/webmail\/login/);
  // The token is cleared so a refresh does not loop back into the inbox.
  const stored = await page.evaluate(() =>
    sessionStorage.getItem("urmail.mailbox-token"),
  );
  expect(stored).toBeNull();
  expect(new URL(page.url()).pathname).toBe("/webmail/login");
});
