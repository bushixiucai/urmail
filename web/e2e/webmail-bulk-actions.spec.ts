import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupBulkFixture(page: Page) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/zh-CN.json"),
      "utf8",
    ),
  ) as Record<string, string>;
  const moves: Array<{ to?: string; uids?: number[] }> = [];

  await page.addInitScript(() => {
    sessionStorage.setItem("urmail.mailbox-token", "fixture-token");
    sessionStorage.setItem("urmail.mailbox-address", "reader@example.test");
    localStorage.setItem("urmail.webmail_version", "next");
    localStorage.setItem("urmail.locale", "zh-CN");
  });

  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (!path.startsWith("/api/")) return route.continue();
    let payload: unknown = {};
    if (path.startsWith("/api/v1/i18n/")) payload = catalogue;
    else if (path.endsWith("/branding"))
      payload = { brand: { name: "urMail Next", theme: {} } };
    else if (path.endsWith("/mailbox/status")) payload = { native: true };
    else if (path.endsWith("/mailbox/settings"))
      payload = {
        display_name: "Reader",
        preferences: {},
        version: 1,
        updated_at: "2026-11-06T12:00:00Z",
        address: "reader@example.test",
      };
    else if (path.endsWith("/mailbox/rules"))
      payload = { version: "v1", state: "active", rules: { rules: [] } };
    else if (path.endsWith("/mailbox/quota")) {
      await route.fulfill({ status: 404, body: "{}" });
      return;
    } else if (
      path.endsWith("/mailbox/folders") &&
      route.request().method() === "POST"
    ) {
      const body = JSON.parse(route.request().postData() || "{}") as {
        name?: string;
      };
      payload = {
        folder: {
          name: body.name,
          name_enc: body.name,
          delimiter: "/",
          flags: body.name === "Archive" ? ["\\Archive"] : [],
        },
      };
    } else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [
          {
            name: "INBOX",
            name_enc: "INBOX",
            delimiter: "/",
            flags: ["\\Inbox"],
          },
        ],
      };
    else if (path.endsWith("/messages/move")) {
      const body = JSON.parse(route.request().postData() || "{}") as {
        to?: string;
        uids?: number[];
      };
      moves.push(body);
      payload = { moved: body.uids?.length ?? 0, to: body.to };
    } else if (path.endsWith("/messages"))
      payload = {
        uid_validity: 9,
        total: 2,
        limit: 50,
        offset: 0,
        messages: [21, 22].map((uid) => ({
          uid,
          subject: `Loaded ${uid}`,
          from: [{ name: "Ann", email: "ann@example.test" }],
          date: "2026-11-06T10:00:00Z",
          flags: ["\\Seen"],
          size: 80,
        })),
      };
    else if (path.includes("/messages/"))
      payload = {
        uid: 21,
        subject: "Loaded 21",
        from: [{ name: "Ann", email: "ann@example.test" }],
        to: [{ email: "reader@example.test" }],
        date: "2026-11-06T10:00:00Z",
        flags: ["\\Seen"],
        size: 80,
        html: "<p>Stay</p>",
        attachments: [],
      };

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
  return { moves };
}

test("checkbox selection archives loaded rows without changing the open message", async ({
  page,
}) => {
  const fixture = await setupBulkFixture(page);
  await page.goto("/webmail/inbox");
  await expect(page.getByText("Loaded 21")).toBeVisible();
  await expect(page.getByTestId("mail-row-21")).toHaveAttribute(
    "data-active",
    "true",
  );

  await page.getByTestId("mail-check-21").click();
  await page.getByTestId("mail-check-22").click({ modifiers: ["Shift"] });
  await expect(page.getByTestId("mail-row-22")).toHaveAttribute(
    "data-active",
    "false",
  );
  await expect(page.getByTestId("bulk-selected-label")).toContainText(
    "已选已加载的 2 封",
  );
  await page.getByTestId("bulk-archive").click();
  await expect.poll(() => fixture.moves[0]?.uids).toEqual([21, 22]);
  await expect(page.getByTestId("mail-row-21")).toHaveAttribute(
    "data-active",
    "true",
  );
  await expect(
    page.getByTestId("webmail-move-notice").locator("button"),
  ).toHaveCount(0);
});
