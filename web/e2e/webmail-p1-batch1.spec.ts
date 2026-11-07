import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

// P1-1 row context menu / P1-2 drag to folder / P1-7 folder management.
test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

interface Fixture {
  moves: Array<{ to?: string; uids?: number[] }>;
  created: Array<string | undefined>;
  renames: Array<{ from: string; to?: string; uid_validity?: number }>;
  deletes: Array<{ name: string; uid_validity?: number }>;
}

async function setupFixture(page: Page) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/zh-CN.json"),
      "utf8",
    ),
  ) as Record<string, string>;
  const fixture: Fixture = { moves: [], created: [], renames: [], deletes: [] };

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
    const status = 200;
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
      fixture.created.push(body.name);
      payload = {
        name: body.name,
        name_enc: body.name,
        uid_validity: 7,
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
          {
            name: "Archive",
            name_enc: "Archive",
            delimiter: "/",
            flags: ["\\Archive"],
          },
          {
            name: "Trash",
            name_enc: "Trash",
            delimiter: "/",
            flags: ["\\Trash"],
          },
          { name: "Projects", name_enc: "Projects", delimiter: "/" },
        ],
      };
    else if (
      path.endsWith("/mailbox/folders/Projects") &&
      route.request().method() === "PATCH"
    ) {
      const body = JSON.parse(route.request().postData() || "{}") as {
        name?: string;
        uid_validity?: number;
      };
      fixture.renames.push({
        from: "Projects",
        to: body.name,
        uid_validity: body.uid_validity,
      });
      payload = {
        name: body.name,
        name_enc: body.name,
        destination: body.name,
      };
    } else if (
      path.endsWith("/mailbox/folders/Projects") &&
      route.request().method() === "DELETE"
    ) {
      fixture.deletes.push({
        name: "Projects",
        uid_validity: Number(url.searchParams.get("uid_validity") || 0),
      });
      payload = {
        deleted: true,
        destination: "Trash/Deleted",
        recoverable: true,
      };
    } else if (path.endsWith("/messages/move")) {
      const body = JSON.parse(route.request().postData() || "{}") as {
        to?: string;
        uids?: number[];
      };
      fixture.moves.push(body);
      // COPYUID-style mapping so the undo chain can engage.
      payload = {
        moved: body.uids?.length ?? 0,
        to: body.to,
        destination_uids: body.uids ?? [],
      };
    } else if (path.endsWith("/messages")) {
      payload = {
        uid_validity: 9,
        total: url.searchParams.get("q") === "is:unseen" ? 3 : 3,
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
                {
                  uid: 13,
                  subject: "Third note",
                  from: [{ name: "Cy", email: "cy@example.test" }],
                  date: "2026-11-06T12:00:00Z",
                  flags: [],
                  size: 130,
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
      status,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });

  return fixture;
}

test("P1-1 row context menu opens by mouse and Shift+F10 and moves via submenu", async ({
  page,
}) => {
  const fixture = await setupFixture(page);
  await page.goto("/webmail/inbox");
  await expect(page.getByTestId("mail-row-11")).toBeVisible();

  // Mouse: context menu opens on the row.
  await page.getByTestId("mail-row-11").click({ button: "right" });
  await expect(page.getByTestId("webmail-row-context-menu")).toBeVisible();
  // Width per audit: ~220px.
  const box = await page.getByTestId("webmail-row-context-menu").boundingBox();
  expect(Math.round(box?.width ?? 0)).toBe(220);

  // Esc closes and focus returns to the row.
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("webmail-row-context-menu")).toBeHidden();
  await expect(page.getByTestId("mail-row-11")).toBeFocused();

  // Keyboard parity: Shift+F10 opens the menu on the focused row.
  await page.getByTestId("mail-row-12").focus();
  await page.keyboard.press("Shift+F10");
  await expect(page.getByTestId("webmail-row-context-menu")).toBeVisible();

  // Move-to submenu reuses the P0 move pipeline.
  await page.getByTestId("ctx-move").click();
  await page.getByTestId("ctx-move-Projects").click();
  await expect
    .poll(() => fixture.moves, { timeout: 5000 })
    .toContainEqual({ to: "Projects", uids: [12], uid_validity: 9 });
});

test("P1-2 drag three selected rows onto Archive moves all three", async ({
  page,
}) => {
  const fixture = await setupFixture(page);
  await page.goto("/webmail/inbox");
  await expect(page.getByTestId("mail-row-11")).toBeVisible();

  // Select all three loaded rows (multi-select uses the platform modifier).
  const modifier = process.platform === "darwin" ? "Meta" : "Control";
  for (const uid of [11, 12, 13]) {
    await page.getByTestId(`mail-check-${uid}`).click({
      modifiers: [modifier],
    });
  }
  // The bulk bar confirms the group is selected before the drag starts.
  await expect(page.getByTestId("bulk-selected-label")).toContainText("3");

  // HTML5 drag-and-drop across components via DataTransfer.
  const dataTransfer = await page.evaluateHandle(() => new DataTransfer());
  await page.getByTestId("mail-row-11").dispatchEvent("dragstart", {
    dataTransfer,
  });
  await page.getByTestId("nav-folder-archive").dispatchEvent("dragover", {
    dataTransfer,
  });
  // Highlight token during hover.
  const highlighted = await page.evaluate(() => {
    const row = document.querySelector<HTMLElement>(
      "[data-testid='nav-folder-archive']",
    );
    return row?.style.backgroundColor ?? "";
  });
  expect(highlighted).toContain("var(--vellum-row-active)");
  await page.getByTestId("nav-folder-archive").dispatchEvent("drop", {
    dataTransfer,
  });

  await expect
    .poll(() => fixture.moves, { timeout: 5000 })
    .toContainEqual({
      to: "Archive",
      uids: [11, 12, 13],
      uid_validity: 9,
    });
});

test("P1-7 creates a folder, renames and deletes a custom folder", async ({
  page,
}) => {
  const fixture = await setupFixture(page);
  await page.goto("/webmail/inbox");
  await expect(page.getByTestId("mail-row-11")).toBeVisible();

  // Create: immediately usable as a move target.
  await page.getByTestId("folder-create-button").click();
  await page.getByTestId("folder-create-input").fill("Work");
  await page.getByTestId("folder-create-input").press("Enter");
  await expect.poll(() => fixture.created, { timeout: 5000 }).toContain("Work");

  // Rename a custom folder via its context menu.
  await page.getByTestId("nav-folder-Projects").click({ button: "right" });
  await page.getByTestId("folder-menu-rename").click();
  await page.getByTestId("folder-rename-input").fill("Projects 2");
  await page.getByTestId("folder-rename-input").press("Enter");
  await expect
    .poll(() => fixture.renames, { timeout: 5000 })
    .toContainEqual({ from: "Projects", to: "Projects 2", uid_validity: 9 });

  // Delete: confirmation states the server contract; system folders have no
  // delete path (their rows carry no context menu binding).
  await page.getByTestId("nav-folder-Projects").click({ button: "right" });
  await page.getByTestId("folder-menu-delete").click();
  await expect(page.getByTestId("folder-delete-dialog")).toBeVisible();
  await page.getByTestId("folder-delete-cancel").click();
  await expect(page.getByTestId("folder-delete-dialog")).toBeHidden();

  await page.getByTestId("nav-folder-Projects").click({ button: "right" });
  await page.getByTestId("folder-menu-delete").click();
  await page.getByTestId("folder-delete-confirm").click();
  await expect
    .poll(() => fixture.deletes, { timeout: 5000 })
    .toContainEqual({ name: "Projects", uid_validity: 9 });
});
