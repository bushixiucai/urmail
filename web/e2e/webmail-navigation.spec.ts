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
      await page.goto("/webmail/inbox?v=legacy");
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

// These loopback route fixtures exercise browser state and request contracts.
// Real SMTP, IMAP, PostgreSQL and RLS are verified by separate acceptance tests.
const crossFolderRows = [
  {
    uid: 1,
    uid_validity: 9,
    folder_enc: "b64.SU5CT1g",
    folder: "INBOX",
    subject: "Browser cross inbox",
    from: [{ email: "sender@example.test" }],
    date: "2026-10-30T08:00:00Z",
    flags: ["\\Seen"],
    size: 100,
    has_attachments: false,
  },
  {
    uid: 1,
    uid_validity: 11,
    folder_enc: "b64.QXJjaGl2ZQ",
    folder: "Archive",
    subject: "Browser cross archive",
    from: [{ email: "sender@example.test" }],
    date: "2026-10-30T08:00:00Z",
    flags: ["\\Seen"],
    size: 100,
    has_attachments: false,
  },
];

async function crossFolderFixture(page: Page) {
  const t = await localFixture(page, "en");
  const operations: Array<{
    path: string;
    body: { uids: number[]; uid_validity: number };
  }> = [];
  let archiveAttempts = 0;
  await page.route("**/api/v1/mailbox/**", async (route) => {
    const url = new URL(route.request().url());
    let payload: unknown;
    if (url.pathname.endsWith("/folders")) {
      payload = {
        folders: crossFolderRows.map((row) => ({
          name: row.folder,
          name_enc: row.folder_enc,
          delimiter: "/",
          flags: [],
        })),
      };
    } else if (url.pathname.endsWith("/search")) {
      payload = { messages: crossFolderRows, total: 2, limit: 50, offset: 0 };
    } else if (url.pathname.endsWith("/messages/trash")) {
      const body = route.request().postDataJSON() as {
        uids: number[];
        uid_validity: number;
      };
      operations.push({ path: url.pathname, body });
      const archive = url.pathname.includes("b64.QXJjaGl2ZQ");
      if (archive) archiveAttempts++;
      payload = {
        results: [
          {
            uid: 1,
            status: archive && archiveAttempts === 1 ? "failed" : "success",
          },
        ],
      };
    } else if (/\/messages\/1$/.test(url.pathname)) {
      const row = crossFolderRows.find((item) =>
        url.pathname.includes(`/folders/${item.folder_enc}/`),
      );
      if (!row) throw new Error("Unexpected cross-folder detail source");
      payload = { ...row, text: `${row.subject} body`, attachments: [] };
    } else {
      return route.fallback();
    }
    await route.fulfill({ json: payload });
  });
  return { t, operations };
}

async function searchEveryFolder(
  page: Page,
  t: Record<string, string>,
  query: string,
) {
  const scope = page.getByRole("checkbox", {
    name: t["page.webmail.search_all_folders"],
    exact: true,
  });
  await scope.check();
  await expect(scope).toBeChecked();
  const input = page.getByRole("textbox", {
    name: t["page.webmail.search_aria"],
    exact: true,
  });
  await expect(input).toBeEnabled();
  await input.fill(query);
  await input.press("Enter");
}

test("cross-folder equal UIDs keep separate actions and retry only failed selections", async ({
  page,
}) => {
  const { t, operations } = await crossFolderFixture(page);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/webmail/inbox?v=legacy");
  await expect(
    page.getByText("Viewport fixture", { exact: true }),
  ).toBeVisible();
  await searchEveryFolder(page, t, "cross");
  for (const row of crossFolderRows) {
    await page.getByText(row.subject, { exact: true }).click();
    await expect(
      page.getByText(`${row.subject} body`, { exact: true }),
    ).toBeVisible();
  }
  await expect(
    page.getByText("Browser cross inbox body", { exact: true }),
  ).toHaveCount(0);
  const listItem = (subject: string) =>
    page
      .getByRole("listitem")
      .filter({ has: page.getByText(subject, { exact: true }) });
  for (const row of crossFolderRows)
    await listItem(row.subject).getByRole("checkbox").check();
  const removeSelection = async () => {
    await page
      .getByRole("toolbar", { name: t["page.webmail.bulk_actions"] })
      .getByRole("button", { name: t["page.webmail.delete"], exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog
      .getByRole("button", { name: t["action.confirm"], exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
  };
  await removeSelection();
  await expect(listItem("Browser cross inbox")).toHaveCount(0);
  await expect(
    listItem("Browser cross archive").getByRole("checkbox"),
  ).toBeChecked();
  expect(operations).toEqual([
    {
      path: "/api/v1/mailbox/folders/b64.SU5CT1g/messages/trash",
      body: { uids: [1], uid_validity: 9 },
    },
    {
      path: "/api/v1/mailbox/folders/b64.QXJjaGl2ZQ/messages/trash",
      body: { uids: [1], uid_validity: 11 },
    },
  ]);
  await removeSelection();
  await expect(listItem("Browser cross archive")).toHaveCount(0);
  expect(operations).toHaveLength(3);
  expect(operations[2]).toEqual({
    path: "/api/v1/mailbox/folders/b64.QXJjaGl2ZQ/messages/trash",
    body: { uids: [1], uid_validity: 11 },
  });
  await mkdir(evidenceDirectory, { recursive: true });
  await writeFile(
    resolve(evidenceDirectory, "cross-folder-actions.json"),
    JSON.stringify(operations, null, 2),
  );
});

test("a delayed cross-folder search cannot replace a newer folder search", async ({
  page,
}) => {
  const { t } = await crossFolderFixture(page);
  let releaseOld!: () => void;
  const oldGate = new Promise<void>((resolve) => {
    releaseOld = resolve;
  });
  let oldRequested = false;
  let oldReleased = false;
  await page.route("**/api/v1/mailbox/**", async (route) => {
    const url = new URL(route.request().url());
    if (
      url.pathname.endsWith("/search") &&
      url.searchParams.get("q") === "delayed"
    ) {
      oldRequested = true;
      await oldGate;
      await route.fulfill({
        json: {
          messages: [
            { ...crossFolderRows[0], subject: "Obsolete browser search" },
          ],
          total: 1,
        },
      });
      oldReleased = true;
    } else if (
      url.pathname.endsWith("/messages") &&
      url.pathname.includes("b64.QXJjaGl2ZQ")
    ) {
      await route.fulfill({
        json: {
          messages: [
            {
              ...crossFolderRows[1],
              subject: url.searchParams.get("q")
                ? "Newest browser search"
                : "Current archive message",
            },
          ],
          uid_validity: 11,
          total: 1,
          limit: 50,
          offset: 0,
        },
      });
    } else return route.fallback();
  });
  await page.goto("/webmail/inbox?v=legacy");
  await expect(
    page.getByText("Viewport fixture", { exact: true }),
  ).toBeVisible();
  await searchEveryFolder(page, t, "delayed");
  await expect.poll(() => oldRequested).toBe(true);
  await page.getByRole("button", { name: "Archive", exact: true }).click();
  await expect(
    page.getByText("Current archive message", { exact: true }),
  ).toBeVisible();
  const scope = page.getByRole("checkbox", {
    name: t["page.webmail.search_all_folders"],
    exact: true,
  });
  await scope.uncheck();
  await expect(scope).not.toBeChecked();
  const input = page.getByRole("textbox", {
    name: t["page.webmail.search_aria"],
    exact: true,
  });
  await expect(input).toBeEnabled();
  await input.fill("latest");
  await input.press("Enter");
  await expect(
    page.getByText("Newest browser search", { exact: true }),
  ).toBeVisible();
  releaseOld();
  await expect.poll(() => oldReleased).toBe(true);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  await expect(input).toBeEnabled();
  await expect(input).toHaveValue("latest");
  await expect(
    page.getByText("Newest browser search", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Obsolete browser search", { exact: true }),
  ).toHaveCount(0);
  await mkdir(evidenceDirectory, { recursive: true });
  await writeFile(
    resolve(evidenceDirectory, "search-lifecycle.json"),
    JSON.stringify(
      {
        oldRequested,
        oldReleased,
        latestQuery: await input.inputValue(),
        currentResult: await page
          .getByText("Newest browser search", { exact: true })
          .innerText(),
        obsoleteResultCount: await page
          .getByText("Obsolete browser search", { exact: true })
          .count(),
      },
      null,
      2,
    ),
  );
});
