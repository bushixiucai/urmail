import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupReadFixture(page: Page) {
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
      payload = {
        messages: [
          {
            uid: 301,
            subject: "Security Digest and Remote Image",
            from: [{ name: "Security Team", email: "sec@example.test" }],
            date: "2026-11-06T12:00:00Z",
            flags: ["\\Seen"],
            size: 4096,
          },
        ],
        total: 1,
      };
    else if (path.includes("/messages/301"))
      payload = {
        uid: 301,
        subject: "Security Digest and Remote Image",
        from: [{ name: "Security Team", email: "sec@example.test" }],
        date: "2026-11-06T12:00:00Z",
        flags: ["\\Seen"],
        size: 4096,
        html: "<p>Tracking image <img data-blocked-src='https://tracker.test/pixel.png'/></p>",
        notes: ["blocked_remote_image"],
        authentication: {
          status: "pass",
          domain: "example.test",
          reason: "SPF: PASS, DKIM: PASS",
        },
        attachments: [
          {
            part_id: "att-1",
            filename: "audit.pdf",
            content_type: "application/pdf",
            size: 204800,
          },
        ],
      };

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test.describe("Webmail Safe Reading & Image Neutralization", () => {
  test("neutralizes remote images by default and shows unblock banner", async ({ page }) => {
    await setupReadFixture(page);
    await page.goto("/webmail/inbox");

    // Click message to read
    const row = page.getByTestId("mail-row-301");
    await row.click();

    // Verify remote images banner
    await expect(page.getByTestId("remote-images-banner")).toBeVisible();
    await expect(page.getByTestId("show-images-button")).toBeVisible();

    // Verify security badge
    await expect(page.getByTestId("security-badge-button")).toBeVisible();

    // Verify attachment card
    await expect(page.getByTestId("attachment-card-att-1")).toBeVisible();
  });
});
