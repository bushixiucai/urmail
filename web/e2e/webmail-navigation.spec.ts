import { expect, test, type Locator, type Page } from "@playwright/test";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);
const evidenceDirectory = resolve(
  process.cwd(),
  "../output/playwright/webmail-remediation",
);

async function bounded(locator: Locator, width: number) {
  await expect(locator).toBeVisible();
  const box = await locator.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(-1);
  expect(box!.x + box!.width).toBeLessThanOrEqual(width + 1);
  expect(box!.width).toBeGreaterThan(0);
  return box;
}

async function localFixture(page: Page, locale: string) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), `../internal/i18n/locales/${locale}.json`),
      "utf8",
    ),
  ) as Record<string, string>;
  const longHTML =
    "<html><body>" +
    Array.from(
      { length: 100 },
      (_, i) => `<p>Message paragraph ${i + 1}</p>`,
    ).join("") +
    "<p>FINAL MESSAGE LINE</p></body></html>";
  await page.addInitScript(
    ({ locale }) => {
      sessionStorage.setItem(
        "urmail.mailbox-token",
        "isolated-browser-fixture",
      );
      sessionStorage.setItem("urmail.mailbox-address", "reader@example.test");
      localStorage.setItem("urmail.locale", locale);
      // Exercise a persisted desktop size on narrow screens.
      localStorage.setItem("urmail.webmail.listWidth", "650");
    },
    { locale },
  );
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))
      throw new Error("Non-loopback request in isolated fixture");
    const path = url.pathname;
    if (!path.startsWith("/api/")) return route.continue();
    let payload: unknown;
    let status = 200;
    if (path.startsWith("/api/v1/i18n/")) payload = catalogue;
    else if (path.endsWith("/mailbox/status"))
      payload = { native: true, mail_host: "box.example.test" };
    else if (path.endsWith("/mailbox/settings"))
      payload = {
        address: "reader@example.test",
        display_name: "Reader",
        signature_text: "",
        signature_html: "",
        preferences: {},
        version: 1,
      };
    else if (path.endsWith("/mailbox/identities"))
      payload = {
        identities: [{ address: "reader@example.test", primary: true }],
      };
    else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [
          { name: "INBOX", name_enc: "b64.SU5CT1g", delimiter: "/", flags: [] },
        ],
      };
    else if (/\/messages\/1$/.test(path))
      payload = {
        uid: 1,
        uid_validity: 9,
        subject: "Viewport fixture",
        from: [{ email: "sender@example.test" }],
        to: [{ email: "reader@example.test" }],
        date: "2026-10-30T08:00:00Z",
        flags: ["\\Seen"],
        size: 90000,
        has_attachments: false,
        html: longHTML,
        attachments: [],
        notes: [],
        authentication: { status: "unknown", reason: "receive_proof_missing" },
      };
    else if (path.endsWith("/messages"))
      payload = {
        messages: [
          {
            uid: 1,
            uid_validity: 9,
            folder_enc: "b64.SU5CT1g",
            subject: "Viewport fixture",
            from: [{ email: "sender@example.test" }],
            date: "2026-10-30T08:00:00Z",
            flags: ["\\Seen"],
            size: 90000,
            has_attachments: false,
          },
        ],
        uid_validity: 9,
        total: 1,
        limit: 50,
        offset: 0,
      };
    else if (path.endsWith("/calendars/invitations")) {
      status = 404;
      payload = { error: { code: "CALENDAR_INVITATION_NOT_FOUND" } };
    } else if (path.endsWith("/calendars/reminders"))
      payload = { reminders: [] };
    else if (path.endsWith("/calendars/events"))
      payload = { events: [], next_cursor: 0 };
    else if (path.endsWith("/contacts/groups")) payload = { groups: [] };
    else if (path.endsWith("/contacts"))
      payload = { contacts: [], next_cursor: 0 };
    else if (path.endsWith("/ai/target"))
      payload = {
        enabled: false,
        managed: false,
        provider: "",
        model: "",
        config_digest: "none",
      };
    else {
      status = 404;
      payload = { error: { code: "NOT_FOUND" } };
    }
    await route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
  for (const key of [
    "page.webmail.app_mail",
    "page.webmail.message_actions",
    "page.webmail.ai_copilot",
  ])
    expect(catalogue[key], `Missing ${locale} translation ${key}`).toBeTruthy();
  return catalogue;
}

for (const locale of ["en", "zh-CN"]) {
  test(`mailbox navigation and long HTML remain usable across viewports (${locale})`, async ({
    page,
    baseURL,
    browserName,
  }) => {
    test.setTimeout(120000);
    if (
      !baseURL ||
      !["127.0.0.1", "localhost", "[::1]"].includes(new URL(baseURL).hostname)
    )
      throw new Error("Loopback fixture required");
    const t = await localFixture(page, locale);
    const geometries: unknown[] = [];
    await mkdir(evidenceDirectory, { recursive: true });
    const views = [320, 375, 390, 430, 768, 1440].map((width) => ({
      width,
      height: 900,
      label: String(width),
    }));
    views.push(
      { width: 720, height: 450, label: "zoom200-equivalent" },
      { width: 390, height: 420, label: "short-viewport" },
    );
    for (const { width, height, label } of views) {
      await page.setViewportSize({ width, height });
      await page.goto("/webmail/inbox");
      await expect(
        page.getByText("Viewport fixture", { exact: true }),
      ).toBeVisible();
      const more = page.getByRole("button", {
        name: t["page.webmail.more_menu"],
        exact: true,
      });
      const compose = page.getByRole("button", {
        name: t["page.webmail.compose_new"],
        exact: true,
      });
      const list = page.getByRole("list", {
        name: t["page.webmail.message_list"],
        exact: true,
      });
      const moreBox = await bounded(more, width);
      const composeBox = await bounded(compose, width);
      const listBox = await bounded(list, width);
      if (width < 768) {
        expect(Math.abs(listBox!.width - width)).toBeLessThanOrEqual(2);
        const navigation = page.getByRole("navigation", {
          name: t["page.webmail.mobile_navigation"],
          exact: true,
        });
        for (const key of [
          "page.webmail.app_mail",
          "page.webmail.contacts_title",
          "page.webmail.calendar_title",
          "page.webmail.ai_copilot",
        ])
          await bounded(
            navigation.getByRole("button", { name: t[key], exact: true }),
            width,
          );
        await navigation
          .getByRole("button", {
            name: t["page.webmail.contacts_title"],
            exact: true,
          })
          .click();
        await expect(
          page
            .getByRole("heading", {
              name: t["page.webmail.contacts_title"],
              exact: true,
            })
            .first(),
        ).toBeVisible();
        await navigation
          .getByRole("button", {
            name: t["page.webmail.calendar_title"],
            exact: true,
          })
          .click();
        await expect(
          page
            .getByRole("heading", {
              name: t["page.webmail.calendar_title"],
              exact: true,
            })
            .first(),
        ).toBeVisible();
        await navigation
          .getByRole("button", {
            name: t["page.webmail.ai_copilot"],
            exact: true,
          })
          .click();
        await bounded(
          page.getByRole("complementary", {
            name: t["page.webmail.ai_copilot"],
            exact: true,
          }),
          width,
        );
        await navigation
          .getByRole("button", {
            name: t["page.webmail.app_mail"],
            exact: true,
          })
          .click();
        await expect(
          page.getByRole("complementary", {
            name: t["page.webmail.ai_copilot"],
            exact: true,
          }),
        ).toHaveCount(0);
      }
      await more.click();
      await bounded(
        page.getByRole("menuitem", {
          name: t["page.webmail.settings_title"],
          exact: true,
        }),
        width,
      );
      const logout = page.getByRole("menuitem", {
        name: t["page.webmail.logout"],
        exact: true,
      });
      await logout.scrollIntoViewIfNeeded();
      await bounded(logout, width);
      await page.keyboard.press("Escape");
      await list.getByText("Viewport fixture", { exact: true }).click();
      await expect(
        page.getByRole("heading", { name: "Viewport fixture", exact: true }),
      ).toBeVisible();
      const readingArea = page
        .getByRole("heading", { name: "Viewport fixture", exact: true })
        .locator("xpath=ancestor::div[contains(@class,'overflow-y-auto')][1]");
      const readingBox = await readingArea.boundingBox();
      expect(readingBox!.height).toBeGreaterThanOrEqual(
        Math.min(180, height * 0.3),
      );
      const summary = page
        .locator("summary")
        .filter({ hasText: t["page.webmail.message_actions"] });
      await summary.click();
      for (const key of [
        "page.webmail.view_headers",
        "page.webmail.download_eml",
        "page.webmail.print_message",
      ]) {
        const action = page.getByRole("button", { name: t[key], exact: true });
        await action.scrollIntoViewIfNeeded();
        await bounded(action, width);
      }
      const close = summary
        .locator("..")
        .getByRole("button", { name: t["action.close"], exact: true });
      await close.scrollIntoViewIfNeeded();
      await close.click();
      const frame = page.frameLocator("iframe");
      await expect(
        frame.getByText("FINAL MESSAGE LINE", { exact: true }),
      ).toBeAttached();
      const finalLine = frame.getByText("FINAL MESSAGE LINE", { exact: true });
      // Scroll the two independent viewports in sequence. A single recursive
      // scrollIntoView can oscillate between an opaque frame and its parent
      // on a short viewport; this also verifies both actual scroll containers.
      await finalLine.evaluate((element) => {
        const scroller = element.ownerDocument.scrollingElement;
        scroller?.scrollTo({ top: scroller.scrollHeight, behavior: "instant" });
      });
      await page
        .locator("iframe")
        .evaluate((element) =>
          element.scrollIntoView({ block: "end", behavior: "instant" }),
        );
      // Opaque iframe borders can clip half a CSS pixel of the line box.
      // Require the complete readable line, allowing only that rounding margin.
      await expect(finalLine).toBeInViewport({ ratio: 0.95 });
      expect(await page.locator("iframe").getAttribute("sandbox")).toBe("");
      await page.screenshot({
        path: resolve(
          evidenceDirectory,
          `${browserName}-${locale}-${label}.png`,
        ),
        fullPage: true,
      });
      geometries.push({
        browserName,
        locale,
        height,
        label,
        reading: readingBox,
        width,
        more: moreBox,
        compose: composeBox,
        list: listBox,
      });
    }
    await writeFile(
      resolve(evidenceDirectory, `${browserName}-${locale}-geometry.json`),
      JSON.stringify(geometries, null, 2),
    );
  });
}
