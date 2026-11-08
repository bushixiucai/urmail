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
    else if (path.includes("/messages/202"))
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
    else if (path.includes("/messages/201"))
      payload = {
        uid: 201,
        subject: "Project Status Update",
        from: [{ name: "Alice", email: "alice@example.test" }],
        date: "2026-11-06T09:00:00Z",
        flags: ["\\Seen"],
        size: 1024,
        text: "Kicking off the project plan.",
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
  test("renders threads, expands/collapses threads, cycles sort, and toggles selection", async ({
    page,
  }) => {
    await setupListFixture(page);
    await page.goto("/webmail/inbox");

    // Wait for message list to load
    const listPane = page.getByTestId("webmail-message-list-pane");
    await expect(listPane).toBeVisible();

    // 1. Thread toggle & expand/collapse
    const threadToggle = page.getByTestId("thread-toggle-10");
    await expect(threadToggle).toBeVisible();

    // Top message 202 is visible
    const row202 = page.getByTestId("mail-row-202");
    await expect(row202).toBeVisible();

    // Initially child message 201 is collapsed
    const row201 = page.getByTestId("mail-row-201");
    await expect(row201).toHaveCount(0);

    // Expand thread
    await threadToggle.click();
    await expect(page.getByTestId("mail-row-201")).toBeVisible();

    // Collapse thread
    await threadToggle.click();
    await expect(page.getByTestId("mail-row-201")).toHaveCount(0);

    // 2. Sort cycle button
    const sortBtn = page.getByTestId("sort-toggle-button");
    await expect(sortBtn).toBeVisible();
    const initialSortTitle = await sortBtn.getAttribute("title");
    await sortBtn.click();
    const newSortTitle = await sortBtn.getAttribute("title");
    expect(newSortTitle).not.toEqual(initialSortTitle);

    // 3. Select all and range selection
    const selectAllBtn = page.getByTestId("select-all-button");
    await expect(selectAllBtn).toBeVisible();
    await selectAllBtn.click();
    await expect(row202).toHaveAttribute("data-selected", "true");

    await selectAllBtn.click();
    await expect(row202).toHaveAttribute("data-selected", "false");
  });

  test("thread stack: opens with only the newest card expanded and navigates", async ({
    page,
  }) => {
    await setupListFixture(page);
    await page.goto("/webmail/inbox");

    // Wait for the list to settle before opening (auto-select effect runs
    // once messages arrive; clicking a just-mounted virtual row can race).
    await expect(page.getByTestId("mail-row-202")).toBeVisible();
    await page.waitForTimeout(500);

    // Open the thread representative (newest = uid 202). Click the subject
    // area — the row's center sits on the thread badge, which only expands.
    await page.getByTestId("mail-row-202").click({
      position: { x: 200, y: 20 },
    });

    // Reading pane switches to the stacked cards view.
    const stack = page.getByTestId("thread-stack");
    await expect(stack).toBeVisible();
    await expect(page.getByTestId("thread-card-201")).toBeVisible();
    await expect(page.getByTestId("thread-card-202")).toBeVisible();

    // Default: only the newest (202) is expanded.
    await expect(page.getByTestId("thread-card-202")).toHaveAttribute(
      "data-expanded",
      "true",
    );
    await expect(page.getByTestId("thread-card-201")).toHaveAttribute(
      "data-expanded",
      "false",
    );

    // Thread bar shows member count and prev/next controls.
    await expect(page.getByTestId("thread-prev")).toBeVisible();
    await expect(page.getByTestId("thread-next")).toBeVisible();

    // Click the older card header: it expands, the other collapses.
    await page.getByTestId("thread-card-header-201").click();
    await expect(page.getByTestId("thread-card-201")).toHaveAttribute(
      "data-expanded",
      "true",
    );
    await expect(page.getByTestId("thread-card-202")).toHaveAttribute(
      "data-expanded",
      "false",
    );

    // Reply actions scope to the expanded card.
    await expect(page.getByTestId("thread-card-reply")).toBeVisible();

    // Prev at the oldest card is disabled; navigate next back to 202.
    await expect(page.getByTestId("thread-prev")).toBeDisabled();
    await page.getByTestId("thread-next").click();
    await expect(page.getByTestId("thread-card-202")).toHaveAttribute(
      "data-expanded",
      "true",
    );
    await expect(page.getByTestId("thread-card-201")).toHaveAttribute(
      "data-expanded",
      "false",
    );
  });
});
