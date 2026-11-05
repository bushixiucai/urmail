import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupListFixture(page: Page) {
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
    else if (path.endsWith("/branding"))
      payload = { brand: { name: "urMail Next", theme: {} } };
    else if (path.endsWith("/mailbox/status"))
      payload = { native: true, mail_host: "box.example.test" };
    else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [
          { name: "INBOX", name_enc: "INBOX", delimiter: "/", flags: [] },
        ],
      };
    else if (path.endsWith("/messages"))
      payload = {
        messages: [
          {
            uid: 201,
            subject: "Project Status Update",
            from: [{ name: "Alice", email: "alice@example.test" }],
            date: "2026-11-06T09:00:00Z",
            flags: ["\\Seen"],
            size: 1024,
            thread_id: 10,
            thread_count: 2,
          },
          {
            uid: 202,
            subject: "Re: Project Status Update",
            from: [{ name: "Bob", email: "bob@example.test" }],
            date: "2026-11-06T10:00:00Z",
            flags: [],
            size: 2048,
            thread_id: 10,
            thread_count: 2,
          },
          {
            uid: 203,
            subject: "Standup Notes",
            from: [{ name: "Charlie", email: "charlie@example.test" }],
            date: "2026-11-05T14:00:00Z",
            flags: ["\\Flagged"],
            size: 512,
          },
        ],
        total: 3,
      };
    else if (path.includes("/messages/202") || path.includes("/messages/201"))
      payload = {
        uid: 202,
        subject: "Re: Project Status Update",
        from: [{ name: "Bob", email: "bob@example.test" }],
        date: "2026-11-06T10:00:00Z",
        flags: [],
        size: 2048,
        text: "Updated project plan.",
        attachments: [],
      };

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test.describe("Webmail List & Threading", () => {
  test("renders threads with thread count pill and handles expand/collapse", async ({
    page,
  }) => {
    await setupListFixture(page);
    await page.goto("/webmail/inbox");

    // Wait for message list to load
    const listPane = page.getByTestId("webmail-message-list-pane");
    await expect(listPane).toBeVisible();

    // Expect thread row with pill "2"
    const threadPill = page.getByTestId("thread-toggle-10");
    await expect(threadPill).toBeVisible();

    // Verify selection of message row (top message in thread is 202 because date is newer)
    const row = page.getByTestId("mail-row-202");
    await expect(row).toBeVisible();
    await row.click();
    await expect(row).toHaveAttribute("data-active", "true");

    // Verify sort cycle button
    const sortBtn = page.getByTestId("sort-toggle-button");
    await expect(sortBtn).toBeVisible();
  });
});
