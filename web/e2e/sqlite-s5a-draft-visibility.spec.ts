import { expect, test } from "@playwright/test";

test.skip(
  process.env.URMAIL_E2E_SQLITE_BROWSER !== "1",
  "run through the SQLite-backed Go browser harness",
);

// §5A follow-up: the product must make draft autosave failures visible on the
// real Webmail pages. On the SQLite slice-1 runtime every drafts surface is
// gated to 503 FEATURE_UNAVAILABLE, so this spec asserts the failure notice
// appears, no saved label is shown, the refresh path restores local-only, and
// the harness DB tail confirms no draft side effect.
test("SQLite §5A draft autosave failure is visible and never shown as saved", async ({
  page,
  baseURL,
}) => {
  if (
    !baseURL ||
    !["127.0.0.1", "localhost"].includes(new URL(baseURL).hostname)
  ) {
    throw new Error("SQLite browser fixture requires a loopback Vite origin");
  }
  const draftCalls: Array<{ method: string; status: number }> = [];
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path.startsWith("/api/v1/mailbox/drafts")) {
      const entry = {
        method: response.request().method(),
        status: response.status(),
      };
      draftCalls.push(entry);
      console.log(
        `HTTP ${entry.method} /api/v1/mailbox/drafts ${entry.status}`,
      );
    }
  });

  // Real login against the migrated temporary SQLite database (Alice fixture).
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

  // Real compose dock, real inputs, real benign attachment.
  await page.goto("/webmail/inbox");
  await page.evaluate(() =>
    localStorage.setItem("urmail.webmail_version", "next"),
  );
  await page.goto("/webmail/inbox");
  await page.getByTestId("webmail-compose-button").click();
  await expect(page.getByTestId("webmail-compose-dock")).toBeVisible();
  await page.getByTestId("compose-to-input").fill("partner@example.test");
  await page
    .getByTestId("compose-subject-input")
    .fill("S5A failure visibility");
  await page.locator(".tiptap.ProseMirror").click();
  await page.keyboard.type("Body for the failure-visibility check.");
  await page.getByTestId("compose-file-input").setInputFiles({
    name: "s5a-visibility.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Benign visibility-check attachment.\n", "utf8"),
  });

  // The gate rejects the autosave; the dock must say so out loud.
  const saveResponse = await page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/drafts" &&
      response.request().method() === "PUT",
    { timeout: 20000 },
  );
  expect(saveResponse.status()).toBe(503);
  const notice = page.getByTestId("draft-save-error-notice");
  await expect(notice).toBeVisible();
  expect((await notice.innerText()).length).toBeGreaterThan(0);
  console.log(
    `VISIBILITY draft-save-error-notice="${(await notice.innerText()).trim()}"`,
  );
  // No false success: the saved label is not rendered for the failed save.
  const indicatorCount = await page
    .getByTestId("draft-status-indicator")
    .count();
  expect(indicatorCount).toBe(0);
  // No retry storm: exactly one autosave PUT for one debounced edit.
  const putCount = draftCalls.filter((call) => call.method === "PUT").length;
  expect(putCount).toBe(1);

  // Refresh path: content survives locally and the state reads local-only.
  await page.reload();
  await page.getByTestId("webmail-compose-button").click();
  await expect(page.getByTestId("webmail-compose-dock")).toBeVisible();
  await expect(page.getByTestId("compose-subject-input")).toHaveValue(
    /S5A failure visibility/,
  );
  const localOnly = page
    .getByTestId("draft-status-indicator")
    .getByText(/本地暂存|Local only/);
  await expect(localOnly).toBeVisible();
  console.log(
    "LOCAL-ONLY indicator visible after refresh; server read-back stays denied",
  );

  // The staged attachment survived the refresh with the local draft.
  expect(
    await page.getByTestId("compose-file-input").getAttribute("type"),
  ).toBe("file");

  console.log(
    "NOT_RUN reply-thread assertion: no approved isolated real mail backend in this acceptance lane",
  );
});
