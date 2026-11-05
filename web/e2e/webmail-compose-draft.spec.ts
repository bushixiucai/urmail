import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupComposeFixture(page: Page) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/zh-CN.json"),
      "utf8",
    ),
  ) as Record<string, string>;

  await page.addInitScript(() => {
    sessionStorage.setItem("urmail.mailbox-token", "fixture-token");
    sessionStorage.setItem("urmail.mailbox-address", "reader@example.test");
    localStorage.setItem("urmail.webmail_version", "next");
  });

  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (!path.startsWith("/api/")) return route.continue();

    let payload: unknown = {};
    if (path.startsWith("/api/v1/i18n/")) payload = catalogue;
    else if (path.endsWith("/branding")) payload = { brand: { name: "urMail Next", theme: {} } };
    else if (path.endsWith("/mailbox/status")) payload = { native: true, mail_host: "box.example.test" };
    else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [{ name: "INBOX", name_enc: "INBOX", delimiter: "/", flags: [] }],
      };
    else if (path.endsWith("/messages"))
      payload = { messages: [], total: 0 };
    else if (path.endsWith("/mailbox/drafts"))
      payload = { saved: true, uid: 888, uid_validity: 1 };
    else if (path.endsWith("/mailbox/send"))
      payload = { sent: true };

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test.describe("Webmail Compose & Debounced Drafts", () => {
  test("opens floating compose dock and enters recipient chip", async ({ page }) => {
    await setupComposeFixture(page);
    await page.goto("/webmail/inbox");

    // Click compose button in rail
    const composeBtn = page.getByTestId("webmail-compose-button");
    await composeBtn.click();

    // Verify dock appears
    await expect(page.getByTestId("webmail-compose-dock")).toBeVisible();

    // Type recipient and press Enter
    const toInput = page.getByTestId("compose-to-input");
    await toInput.fill("partner@example.test");
    await toInput.press("Enter");

    await expect(page.getByTestId("recipient-pill-partner@example.test")).toBeVisible();

    // Fill subject
    const subjectInput = page.getByTestId("compose-subject-input");
    await subjectInput.fill("Project Kickoff Proposal");

    // Verify send button is enabled
    const sendBtn = page.getByTestId("compose-send-button");
    await expect(sendBtn).toBeEnabled();

    // Trigger send and verify 10-second undo toast
    await sendBtn.click();
    await expect(page.getByTestId("undo-send-toast")).toBeVisible();
    await expect(page.getByTestId("undo-send-button")).toBeVisible();
  });
});
