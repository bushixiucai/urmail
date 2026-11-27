import { expect, test } from "@playwright/test";

test.skip(
  process.env.URMAIL_E2E_SQLITE_BROWSER !== "1",
  "run through the SQLite-backed Go browser harness",
);

test("SQLite login/settings persistence and expired write recovery", async ({
  page,
  baseURL,
}) => {
  if (
    !baseURL ||
    !["127.0.0.1", "localhost"].includes(new URL(baseURL).hostname)
  ) {
    throw new Error("SQLite browser fixture requires a loopback Vite origin");
  }
  const responses: Array<{ method: string; path: string; status: number }> = [];
  let settingsWrites = 0;
  page.on("request", (request) => {
    if (
      new URL(request.url()).pathname === "/api/v1/mailbox/settings" &&
      request.method() === "PUT"
    ) {
      settingsWrites += 1;
    }
  });
  page.on("response", (response) => {
    const request = response.request();
    const path = new URL(response.url()).pathname;
    if (path.startsWith("/api/v1/mailbox/")) {
      responses.push({
        method: request.method(),
        path,
        status: response.status(),
      });
      console.log(`HTTP ${request.method()} ${path} ${response.status()}`);
    }
  });

  await page.goto("/webmail/login?v=legacy");
  await page.locator("#mailbox-email").fill("alice@a.example.test");
  await page.locator("#mailbox-password").fill("slice1-mailbox-password");
  const loginResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/session" &&
      response.request().method() === "POST",
  );
  await page.locator('button[type="submit"]').click();
  expect((await loginResponse).status()).toBe(200);
  await expect(page).toHaveURL(/\/webmail\/inbox/);
  expect(
    await page.evaluate(() => sessionStorage.getItem("urmail.mailbox-token")),
  ).toBeTruthy();

  await page.goto("/webmail/help");
  const settings = await page.evaluate(async () => {
    const api = await import("/src/features/webmail/mailbox-api.ts");
    const initial = await api.getMailboxSettings();
    const saved = await api.saveMailboxSettings({
      display_name: "SQLite Browser Alice",
      signature_text: "SQLite browser signature",
      signature_html: "",
      expected_version: initial.version,
    });
    return { initialVersion: initial.version, savedVersion: saved.version };
  });
  expect(settings.initialVersion).toBe(0);
  expect(settings.savedVersion).toBe(1);

  const token = await page.evaluate(() =>
    sessionStorage.getItem("urmail.mailbox-token"),
  );
  expect(token).toBeTruthy();
  const expireResponse = await fetch(
    `${process.env.URMAIL_E2E_SQLITE_CONTROL_URL}/expire-session`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token }),
    },
  );
  expect(expireResponse.status).toBe(200);

  let loginDocumentNavigationRequests = 0;
  let loginFrameEvents = 0;
  let topLevelLoginTransitions = 0;
  let lastTopLevelPath = new URL(page.url()).pathname;
  page.on("request", (request) => {
    const requestFrame = request.frame();
    if (
      request.isNavigationRequest() &&
      requestFrame === page.mainFrame() &&
      new URL(request.url()).pathname === "/webmail/login"
    ) {
      loginDocumentNavigationRequests += 1;
      console.log(
        `TOP_LEVEL_LOGIN_DOCUMENT_REQUEST ${loginDocumentNavigationRequests} ${request.url()}`,
      );
    }
  });
  page.on("framenavigated", (frame) => {
    if (frame !== page.mainFrame()) return;
    const path = new URL(frame.url()).pathname;
    if (path === "/webmail/login") {
      loginFrameEvents += 1;
      if (lastTopLevelPath !== "/webmail/login") {
        topLevelLoginTransitions += 1;
      }
      console.log(
        `TOP_LEVEL_LOGIN_FRAME_EVENT raw=${loginFrameEvents} transitions=${topLevelLoginTransitions} ${frame.url()}`,
      );
    }
    lastTopLevelPath = path;
  });
  const expiredWrite = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/settings" &&
      response.request().method() === "PUT",
  );
  await page.evaluate(() => {
    void import("/src/features/webmail/mailbox-api.ts").then((api) =>
      api
        .saveMailboxSettings({
          display_name: "Must Not Persist",
          signature_text: "Must Not Persist",
          signature_html: "",
          expected_version: 1,
        })
        .catch(() => undefined),
    );
  });
  expect((await expiredWrite).status()).toBe(401);
  // Recovery contract (REVIEW-UX-001 / REVIEW-FE-008): the 401 funnel
  // clears the dead session, records the route for the post-login return
  // and keeps the loaded page alive — no top-level navigation to
  // /webmail/login happens any more. The mounted banner owns re-login.
  await expect(page).toHaveURL(/\/webmail\/help/);
  await page.waitForLoadState("networkidle");
  console.log(
    `ASSERT top-level login document requests=${loginDocumentNavigationRequests}; distinct route transitions=${topLevelLoginTransitions}; raw main-frame events=${loginFrameEvents}`,
  );
  expect(loginDocumentNavigationRequests).toBe(0);
  expect(topLevelLoginTransitions).toBe(0);
  expect(
    await page.evaluate(() =>
      sessionStorage.getItem("urmail.webmail.session-return-to"),
    ),
  ).toBe("/webmail/help");
  expect(
    await page.evaluate(() => sessionStorage.getItem("urmail.mailbox-token")),
  ).toBeNull();
  expect(settingsWrites).toBe(2);
  expect(
    responses
      .filter(
        (response) =>
          response.path === "/api/v1/mailbox/settings" &&
          response.method === "PUT",
      )
      .map((response) => response.status),
  ).toEqual([200, 401]);
  console.log(
    `ASSERT settings PUT count=${settingsWrites}; statuses=200,401; final route=${new URL(page.url()).pathname}`,
  );
});
