import { expect, type Locator, type Page, test } from "@playwright/test";

test.skip(
  process.env.URMAIL_E2E_SQLITE_BROWSER !== "1",
  "run through the SQLite-backed Go browser harness",
);

async function measureListBounds(list: Locator, viewportWidth: number) {
  return list.evaluate((element, width) => {
    const rect = element.getBoundingClientRect();
    const ancestors: Array<Record<string, string | number>> = [];
    for (
      let node: HTMLElement | null = element;
      node && ancestors.length < 10;
      node = node.parentElement
    ) {
      const nodeRect = node.getBoundingClientRect();
      const style = getComputedStyle(node);
      ancestors.push({
        tag: node.tagName.toLowerCase(),
        id: node.id,
        className: String(node.className).slice(0, 160),
        left: Math.round(nodeRect.left * 100) / 100,
        right: Math.round(nodeRect.right * 100) / 100,
        width: Math.round(nodeRect.width * 100) / 100,
        clientWidth: node.clientWidth,
        scrollWidth: node.scrollWidth,
        overflowX: style.overflowX,
      });
    }
    const clippingAncestors = ancestors.filter((ancestor) =>
      ["hidden", "clip", "auto", "scroll"].includes(String(ancestor.overflowX)),
    );
    const clipRight = Math.min(
      width,
      ...clippingAncestors.map((ancestor) => Number(ancestor.right)),
    );
    return {
      viewportWidth: width,
      documentWidth: document.documentElement.scrollWidth,
      documentClientWidth: document.documentElement.clientWidth,
      listLeft: Math.round(rect.left * 100) / 100,
      listRight: Math.round(rect.right * 100) / 100,
      contentWidth: element.scrollWidth,
      listWidth: element.clientWidth,
      boundsWidth: Math.round(rect.width * 1000) / 1000,
      clipRight: Math.round(clipRight * 100) / 100,
      clippedRightPixels: Math.max(
        0,
        Math.round((rect.right - clipRight) * 100) / 100,
      ),
    };
  }, viewportWidth);
}

async function loginToInbox(
  page: Page,
  viewport: { width: number; height: number },
) {
  await page.setViewportSize(viewport);
  await page.goto("/webmail/login");
  const email = page.locator("#mailbox-email");
  const password = page.locator("#mailbox-password");
  const submit = page.locator('button[type="submit"]');
  await expect(email).toBeVisible();
  await expect(email).toBeEnabled();
  await expect(password).toBeVisible();
  await expect(password).toBeEnabled();
  await expect(submit).toBeVisible();
  await expect(submit).toBeEnabled();

  const loginBounds = await page.evaluate(
    (width) => ({
      viewportWidth: width,
      documentWidth: document.documentElement.scrollWidth,
      controls: [
        "#mailbox-email",
        "#mailbox-password",
        'button[type="submit"]',
      ].map((selector) => {
        const rect = document.querySelector(selector)!.getBoundingClientRect();
        return {
          selector,
          left: Math.round(rect.left * 100) / 100,
          right: Math.round(rect.right * 100) / 100,
          width: Math.round(rect.width * 100) / 100,
        };
      }),
    }),
    viewport.width,
  );
  expect(loginBounds.documentWidth).toBeLessThanOrEqual(viewport.width);
  for (const control of loginBounds.controls) {
    expect(control.left).toBeGreaterThanOrEqual(0);
    expect(control.right).toBeLessThanOrEqual(viewport.width);
  }

  await email.fill("alice@a.example.test");
  await password.fill("slice1-mailbox-password");
  const loginPromise = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/session" &&
      response.request().method() === "POST",
  );
  const foldersPromise = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/folders" &&
      response.request().method() === "GET",
  );
  const messagesPromise = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      /^\/api\/v1\/mailbox\/folders\/[^/]+\/messages$/.test(url.pathname) &&
      response.request().method() === "GET" &&
      !url.searchParams.has("q")
    );
  });
  await submit.click();
  expect((await loginPromise).status()).toBe(200);
  await expect(page).toHaveURL(/\/webmail\/inbox/);

  const foldersRes = await foldersPromise;
  expect(foldersRes.status()).toBe(200);
  const foldersJson = (await foldersRes.json()) as {
    folders: Array<{ name: string }>;
  };
  expect(foldersJson.folders.length).toBeGreaterThanOrEqual(5);

  const messagesRes = await messagesPromise;
  expect(messagesRes.status()).toBe(200);
  const messagesJson = (await messagesRes.json()) as {
    total: number;
    messages: Array<{ uid: number; subject: string }>;
  };
  return { loginBounds, foldersJson, messagesJson };
}

test("390x844 and 1280x800 SQLite login renders empty inbox without clipping", async ({
  page,
}) => {
  const unexpectedConsoleErrors: string[] = [];
  page.on("pageerror", (error) => unexpectedConsoleErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") {
      unexpectedConsoleErrors.push(message.text());
    }
  });

  const { foldersJson, messagesJson } = await loginToInbox(page, {
    width: 390,
    height: 844,
  });
  expect(messagesJson.total).toBe(0);
  expect(messagesJson.messages).toHaveLength(0);
  console.log(
    `EMPTY_INBOX_API folders=${foldersJson.folders.length} messagesTotal=${messagesJson.total}`,
  );

  const list = page.getByTestId("webmail-message-list");
  await expect(list).toBeVisible();
  await expect(page.getByTestId("empty-folder")).toBeVisible();
  await expect(page.getByTestId("list-load-error")).toHaveCount(0);

  const mobileBounds = await measureListBounds(list, 390);
  const emptyMobileBox = await page.getByTestId("empty-folder").boundingBox();
  console.log(
    `EMPTY_INBOX_MOBILE_390 ${JSON.stringify({ ...mobileBounds, emptyBox: emptyMobileBox })}`,
  );
  expect(mobileBounds.documentWidth).toBeLessThanOrEqual(390);
  expect(mobileBounds.boundsWidth).toBeLessThanOrEqual(390);
  expect(mobileBounds.contentWidth).toBeLessThanOrEqual(390);
  expect(mobileBounds.clippedRightPixels).toBe(0);
  expect(emptyMobileBox).not.toBeNull();
  expect(emptyMobileBox!.x).toBeGreaterThanOrEqual(0);
  expect(emptyMobileBox!.x + emptyMobileBox!.width).toBeLessThanOrEqual(390);

  await page.setViewportSize({ width: 1280, height: 800 });
  await expect(page.getByTestId("webmail-navigation-rail")).toBeVisible();
  await expect(page.getByTestId("empty-folder")).toBeVisible();
  const desktopBounds = await measureListBounds(list, 1280);
  console.log(`EMPTY_INBOX_DESKTOP_1280 ${JSON.stringify(desktopBounds)}`);
  expect(desktopBounds.documentWidth).toBeLessThanOrEqual(1280);
  expect(desktopBounds.listLeft).toBeGreaterThanOrEqual(0);
  expect(desktopBounds.listRight).toBeLessThanOrEqual(1280);
  expect(desktopBounds.clippedRightPixels).toBe(0);
  expect(
    unexpectedConsoleErrors.filter((msg) => !msg.includes("status of 503")),
  ).toEqual([]);
});

test("390x844 and 1280x800 SQLite seeded inbox renders rows and 503 error state without clipping", async ({
  page,
}) => {
  const seedResponse = await fetch(
    `${process.env.URMAIL_E2E_SQLITE_CONTROL_URL}/seed-inbox`,
    { method: "POST" },
  );
  expect(seedResponse.status).toBe(200);

  const consoleErrors: string[] = [];
  const apiResponses: Array<{ method: string; path: string; status: number }> =
    [];
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path.startsWith("/api/v1/mailbox/")) {
      const entry = {
        method: response.request().method(),
        path,
        status: response.status(),
      };
      apiResponses.push(entry);
      console.log(`HTTP ${entry.method} ${entry.path} ${entry.status}`);
    }
  });

  const { foldersJson, messagesJson } = await loginToInbox(page, {
    width: 390,
    height: 844,
  });
  expect(messagesJson.total).toBe(2);
  expect(messagesJson.messages).toHaveLength(2);
  console.log(
    `SEEDED_INBOX_API folders=${foldersJson.folders.length} messagesTotal=${messagesJson.total}`,
  );

  const list = page.getByTestId("webmail-message-list");
  await expect(list).toBeVisible();
  await expect(page.getByTestId("message-rows-container")).toBeVisible();
  await expect(page.getByTestId("mail-row-101")).toBeVisible();
  await expect(page.getByTestId("mail-row-102")).toBeVisible();
  await expect(page.getByTestId("empty-folder")).toHaveCount(0);
  await expect(page.getByTestId("list-load-error")).toHaveCount(0);

  const mobileBounds = await measureListBounds(list, 390);
  const row101Mobile = await page.getByTestId("mail-row-101").boundingBox();
  const row102Mobile = await page.getByTestId("mail-row-102").boundingBox();
  console.log(
    `SEEDED_INBOX_MOBILE_390 ${JSON.stringify({ ...mobileBounds, row101Mobile, row102Mobile })}`,
  );
  expect(mobileBounds.documentWidth).toBeLessThanOrEqual(390);
  expect(mobileBounds.boundsWidth).toBeLessThanOrEqual(390);
  expect(mobileBounds.contentWidth).toBeLessThanOrEqual(390);
  expect(mobileBounds.clippedRightPixels).toBe(0);
  for (const rowBox of [row101Mobile, row102Mobile]) {
    expect(rowBox).not.toBeNull();
    expect(rowBox!.x).toBeGreaterThanOrEqual(0);
    expect(rowBox!.x + rowBox!.width).toBeLessThanOrEqual(390);
  }

  await page.setViewportSize({ width: 1280, height: 800 });
  await expect(page.getByTestId("webmail-navigation-rail")).toBeVisible();
  await expect(page.getByTestId("mail-row-101")).toBeVisible();
  await expect(page.getByTestId("mail-row-102")).toBeVisible();
  const desktopBounds = await measureListBounds(list, 1280);
  console.log(`SEEDED_INBOX_DESKTOP_1280 ${JSON.stringify(desktopBounds)}`);
  expect(desktopBounds.documentWidth).toBeLessThanOrEqual(1280);
  expect(desktopBounds.listLeft).toBeGreaterThanOrEqual(0);
  expect(desktopBounds.listRight).toBeLessThanOrEqual(1280);
  expect(desktopBounds.clippedRightPixels).toBe(0);

  // Unimplemented write route still fails closed with 503 FEATURE_UNAVAILABLE without layout clipping.
  const folderCreatePromise = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/folders" &&
      response.request().method() === "POST",
  );
  await page.getByTestId("folder-create-button").click();
  const createInput = page.getByTestId("folder-create-input");
  await createInput.fill("BlockedWrite");
  await createInput.press("Enter");
  const folderCreateRes = await folderCreatePromise;
  expect(folderCreateRes.status()).toBe(503);
  expect(await folderCreateRes.json()).toMatchObject({
    error: { code: "FEATURE_UNAVAILABLE" },
  });
  const folderActionError = page.getByTestId("folder-action-error");
  await expect(folderActionError).toBeVisible();
  const folderErrorBox = await folderActionError.boundingBox();
  expect(folderErrorBox).not.toBeNull();
  expect(folderErrorBox!.x).toBeGreaterThanOrEqual(0);
  expect(folderErrorBox!.x + folderErrorBox!.width).toBeLessThanOrEqual(1280);

  // Unimplemented search endpoint on 390x844 mobile triggers real 503 list-load-error without clipping.
  await page.setViewportSize({ width: 390, height: 844 });
  const searchPromise = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/mailbox/search" &&
      response.request().method() === "GET",
  );
  const searchInput = page.getByTestId("webmail-search-input");
  await searchInput.fill("Alice");
  await searchInput.press("Enter");
  const searchRes = await searchPromise;
  expect(searchRes.status()).toBe(503);
  expect(await searchRes.json()).toMatchObject({
    error: { code: "FEATURE_UNAVAILABLE" },
  });
  const loadError = page.getByTestId("list-load-error");
  await expect(loadError).toBeVisible({ timeout: 10000 });
  const errorMobileBounds = await measureListBounds(list, 390);
  const loadErrorBox = await loadError.boundingBox();
  console.log(
    `ERROR_STATE_MOBILE_390 ${JSON.stringify({ ...errorMobileBounds, loadErrorBox })}`,
  );
  expect(errorMobileBounds.documentWidth).toBeLessThanOrEqual(390);
  expect(errorMobileBounds.boundsWidth).toBeLessThanOrEqual(390);
  expect(errorMobileBounds.contentWidth).toBeLessThanOrEqual(390);
  expect(errorMobileBounds.clippedRightPixels).toBe(0);
  expect(loadErrorBox).not.toBeNull();
  expect(loadErrorBox!.x).toBeGreaterThanOrEqual(0);
  expect(loadErrorBox!.x + loadErrorBox!.width).toBeLessThanOrEqual(390);

  const non503Errors = consoleErrors.filter(
    (msg) => !msg.includes("status of 503"),
  );
  expect(non503Errors).toEqual([]);
  console.log(`API_RESPONSES ${JSON.stringify(apiResponses)}`);
});

test("real API serves the English locale catalog", async ({ page }) => {
  const localeResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/i18n/en" &&
      response.request().method() === "GET",
  );
  await page.goto("/webmail/login");
  const response = await localeResponse;
  expect(response.status()).toBe(200);
  const catalog = await response.json();
  expect(catalog["page.webmail.login_title"]).toBeTruthy();
  console.log(
    `I18N_HTTP_STATUS ${response.status()} locale=en keys=${Object.keys(catalog).length} source=real product route through SQLite test server`,
  );
});
