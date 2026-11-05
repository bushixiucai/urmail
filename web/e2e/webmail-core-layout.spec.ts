import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupMockFixture(page: Page) {
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
          { name: "Sent", name_enc: "Sent", delimiter: "/", flags: [] },
        ],
      };
    else if (path.endsWith("/messages"))
      payload = {
        messages: [
          {
            uid: 101,
            subject: "Welcome to urMail Next",
            from: [{ name: "Admin", email: "admin@example.test" }],
            date: "2026-11-06T10:00:00Z",
            flags: ["\\Seen"],
            size: 2048,
          },
        ],
        total: 1,
      };
    else if (path.includes("/messages/101"))
      payload = {
        uid: 101,
        subject: "Welcome to urMail Next",
        from: [{ name: "Admin", email: "admin@example.test" }],
        date: "2026-11-06T10:00:00Z",
        flags: ["\\Seen"],
        size: 2048,
        text: "Enjoy the new Webmail!",
        attachments: [],
      };

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test.describe("Webmail Core Layout & Responsiveness", () => {
  test("renders 3-pane workbench at 1440px desktop breakpoint", async ({
    page,
  }) => {
    await setupMockFixture(page);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/webmail/inbox");

    await expect(page.getByTestId("webmail-header")).toBeVisible();
    await expect(page.getByTestId("webmail-navigation-rail")).toBeVisible();
    await expect(page.getByTestId("webmail-message-list-pane")).toBeVisible();
    await expect(
      page.getByTestId("webmail-reading-pane-wrapper"),
    ).toBeVisible();

    // Verify Mini Rail toggle
    const toggleBtn = page.getByTestId("rail-toggle-button");
    await toggleBtn.click();
    await expect(page.getByTestId("webmail-navigation-rail")).toHaveAttribute(
      "data-collapsed",
      "true",
    );

    // Verify zero horizontal scrollbar
    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(1440);
  });

  test("adapts to 1024px tablet breakpoint without horizontal scroll", async ({
    page,
  }) => {
    await setupMockFixture(page);
    await page.setViewportSize({ width: 1024, height: 768 });
    await page.goto("/webmail/inbox");

    await expect(page.getByTestId("webmail-header")).toBeVisible();
    await expect(page.getByTestId("webmail-message-list-pane")).toBeVisible();
    await expect(
      page.getByTestId("webmail-reading-pane-wrapper"),
    ).toBeVisible();

    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(1024);
  });

  test("adapts to 375px mobile single pane stack without horizontal scroll", async ({
    page,
  }) => {
    await setupMockFixture(page);
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto("/webmail/inbox");

    await expect(page.getByTestId("webmail-header")).toBeVisible();
    // On 375px when reading, reading pane covers list or list covers reading
    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollWidth).toBeLessThanOrEqual(375);
  });
});
