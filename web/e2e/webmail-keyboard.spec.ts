import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupKeyboardFixture(page: Page) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/zh-CN.json"),
      "utf8",
    ),
  ) as Record<string, string>;
  const moves: Array<{ to?: string; uids?: number[] }> = [];
  const created: string[] = [];
  const listed: string[] = [];

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
        signature_text: "",
        signature_html: "",
        preferences: { allow_all_remote_images: false },
        version: 1,
        updated_at: "2026-11-06T12:00:00Z",
        address: "reader@example.test",
      };
    else if (path.endsWith("/mailbox/rules"))
      payload = { version: "v1", state: "active", rules: { rules: [] } };
    else if (path.endsWith("/mailbox/quota")) {
      await route.fulfill({ status: 404, body: "{}" });
      return;
    } else if (path.endsWith("/mailbox/scheduled-sends"))
      payload = { items: [] };
    else if (
      path.endsWith("/mailbox/folders") &&
      route.request().method() === "POST"
    ) {
      const body = JSON.parse(route.request().postData() || "{}") as {
        name?: string;
      };
      created.push(body.name || "");
      payload = {
        folder: { name: body.name, name_enc: body.name, delimiter: "/" },
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
    } else if (path.endsWith("/messages/delete")) {
      await route.fulfill({
        status: 500,
        body: JSON.stringify({ error: "delete-not-allowed" }),
      });
      return;
    } else if (path.endsWith("/messages")) {
      listed.push(url.search);
      payload = {
        uid_validity: 9,
        total: url.searchParams.get("q") === "is:unseen" ? 4 : 2,
        limit: 50,
        offset: 0,
        messages:
          url.searchParams.get("q") === "is:unseen"
            ? []
            : [
                {
                  uid: 11,
                  subject: "First note",
                  from: [{ name: "Ann", email: "ann@example.test" }],
                  date: "2026-11-06T10:00:00Z",
                  flags: ["\\Seen"],
                  size: 100,
                },
                {
                  uid: 12,
                  subject: "Second note",
                  from: [{ name: "Bo", email: "bo@example.test" }],
                  date: "2026-11-06T11:00:00Z",
                  flags: [],
                  size: 120,
                },
              ],
      };
    } else if (path.includes("/messages/"))
      payload = {
        uid: Number(path.split("/").pop()),
        subject: "Opened",
        from: [{ name: "Ann", email: "ann@example.test" }],
        to: [{ email: "reader@example.test" }],
        date: "2026-11-06T10:00:00Z",
        flags: ["\\Seen"],
        size: 100,
        html: "<p>Hello</p>",
        attachments: [],
      };

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });

  return { moves, created, listed };
}

test("keyboard moves the cursor, archives, and trashes without deleting", async ({
  page,
}) => {
  const fixture = await setupKeyboardFixture(page);
  await page.goto("/webmail/inbox");
  await expect(page.getByText("First note")).toBeVisible();
  await expect(page.getByTestId("webmail-quota-bar")).toHaveCount(0);
  await expect(page.getByText("1.2 GB / 5.0 GB")).toHaveCount(0);
  await expect(page.getByTestId("inbox-unread-badge")).toHaveText("4");

  await page.keyboard.press("j");
  await expect(page.getByTestId("mail-row-12")).toHaveAttribute(
    "data-cursor",
    "true",
  );
  await expect(page.getByTestId("mail-row-12")).toHaveAttribute(
    "data-active",
    "false",
  );
  await page.keyboard.press("o");
  await expect(page.getByTestId("mail-row-12")).toHaveAttribute(
    "data-active",
    "true",
  );

  await page.keyboard.press("e");
  await expect.poll(() => fixture.created).toContain("Archive");
  await expect
    .poll(() => fixture.moves.some((move) => move.to === "Archive"))
    .toBe(true);
  await expect(page.getByTestId("webmail-move-notice")).toBeVisible();
  await expect(
    page.getByTestId("webmail-move-notice").locator("button"),
  ).toHaveCount(0);

  await page.keyboard.press("#");
  await expect.poll(() => fixture.created).toContain("Trash");
  await expect
    .poll(() => fixture.moves.some((move) => move.to === "Trash"))
    .toBe(true);

  await page.getByTestId("nav-folder-scheduled").click();
  await expect
    .poll(() => fixture.listed.some((q) => q.includes("Scheduled")))
    .toBe(false);
  await expect(page.getByTestId("webmail-scheduled-list")).toBeVisible();

  await page.keyboard.press("?");
  await expect(page.getByTestId("webmail-shortcut-help")).toBeVisible();
  await expect(page.getByTestId("webmail-shortcut-help")).toContainText("移动");
  await expect(page.getByTestId("webmail-shortcut-help")).toContainText("写信");
});
