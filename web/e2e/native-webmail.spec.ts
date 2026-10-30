import { expect, test, type BrowserContext, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

// Run against the local Vite server. This fixture does not authenticate or
// submit mail and refuses any non-loopback target.
test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "set URMAIL_E2E_NATIVE_FIXTURE=1 with a local Vite base URL",
);

test("HTML mail blocks CSS, SVG and image tracking in a sandboxed frame", async ({
  page,
  baseURL,
}) => {
  if (
    !baseURL ||
    !["127.0.0.1", "localhost", "[::1]"].includes(new URL(baseURL).hostname)
  ) {
    throw new Error("Native webmail fixtures require a loopback Vite server");
  }
  const requests: string[] = [];
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))
      throw new Error("Fixture attempted non-loopback access");
    if (url.pathname.startsWith("/__tracker/")) {
      requests.push(url.pathname);
      await route.fulfill({
        contentType: "image/gif",
        body: Buffer.from(
          "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7",
          "base64",
        ),
      });
    } else if (url.pathname === "/__webmail_csp_fixture") {
      await route.fulfill({
        contentType: "text/html",
        body: "<!doctype html><html><head></head><body></body></html>",
      });
    } else await route.continue();
  });
  await page.goto("/__webmail_csp_fixture");
  await page.evaluate(async (origin) => {
    const modulePath = "/src/features/webmail/sanitize-html-mail.ts";
    const { sanitizeHtmlMail } = await import(/* @vite-ignore */ modulePath);
    const html = `<html><head><style>@import url('${origin}/__tracker/import.css'); @font-face {font-family: tracker;src:url('${origin}/__tracker/font.woff2')} .tracked {background-image:url('${origin}/__tracker/style.gif');font-family:tracker}</style></head><body><p class="tracked">Preserved message</p><p style="background-image:url('${origin}/__tracker/inline.gif')">Inline style</p><svg width="10" height="10"><image href="${origin}/__tracker/svg.gif" width="10" height="10"/></svg><img src="${origin}/__tracker/img.gif"><video poster="${origin}/__tracker/poster.gif" src="${origin}/__tracker/movie.mp4"></video></body></html>`;
    const frame = document.createElement("iframe");
    frame.sandbox.value = "";
    frame.srcdoc = sanitizeHtmlMail(html);
    document.body.appendChild(frame);
  }, new URL(baseURL).origin);
  await expect(
    page.frameLocator("iframe").getByText("Preserved message"),
  ).toBeVisible();
  // Give queued CSS/image/font loads an opportunity to reach the route handler.
  await page.waitForTimeout(300);
  expect(requests).toEqual([]);
  // Exercise CSP independently of sanitization by inserting an image afterwards.
  await page.evaluate((origin) => {
    const frame = document.querySelector("iframe")!;
    frame.srcdoc = frame.srcdoc.replace(
      "</body>",
      `<img id="policy-probe" src="${origin}/__tracker/after-sanitization.gif"><script>parent.__mailScriptRan=true</script></body>`,
    );
  }, new URL(baseURL).origin);
  await expect(
    page.frameLocator("iframe").locator("#policy-probe"),
  ).toHaveCount(1);
  await expect(page.locator("iframe")).toHaveAttribute("sandbox", "");
  await page.waitForTimeout(300);
  expect(requests).toEqual([]);
  expect(
    await page.evaluate(() => ({
      scriptRan: Boolean(
        (window as Window & { __mailScriptRan?: boolean }).__mailScriptRan,
      ),
      opaque: document.querySelector("iframe")!.contentDocument === null,
    })),
  ).toEqual({ scriptRan: false, opaque: true });
});

interface BrowserDraft {
  uid?: number;
  uid_validity?: number;
  from?: string;
  to?: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  text: string;
  html?: string;
  attachments?: Array<{
    filename: string;
    content_type: string;
    content_base64: string;
  }>;
}

// These routes exercise browser state contracts only. Real mail protocol,
// PostgreSQL/RLS and conflict authority are verified by separate integration tests.
async function composeBrowserFixture(context: BrowserContext) {
  const t = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/en.json"),
      "utf8",
    ),
  ) as Record<string, string>;
  const inbox = "b64.SU5CT1g";
  const draftsFolder = "b64.RHJhZnRz";
  const drafts = new Map<number, BrowserDraft>();
  const saves: BrowserDraft[] = [];
  const rejectedSaves: BrowserDraft[] = [];
  const deleted: number[] = [];
  let nextUID = 100;
  const source = {
    uid: 1,
    uid_validity: 9,
    folder_enc: inbox,
    subject: "Reply browser source",
    message_id: "<browser-source@example.test>",
    from: [{ email: "author@example.test" }],
    reply_to: [
      { email: "reply-one@example.test" },
      { email: "reply-two@example.test" },
    ],
    to: [{ email: "team@example.test" }, { email: "other@example.test" }],
    cc: [{ email: "reader@example.test" }, { email: "copy@example.test" }],
    text: "Original browser source",
    date: "2026-10-30T08:00:00Z",
    flags: ["\\Seen"],
    size: 100,
    attachments: [],
  };
  function detail(uid: number, draft: BrowserDraft) {
    return {
      uid,
      uid_validity: 9,
      folder_enc: draftsFolder,
      subject: draft.subject,
      text: draft.text,
      html: draft.html,
      from: [{ email: draft.from ?? "reader@example.test" }],
      to: (draft.to ?? []).map((email) => ({ email })),
      cc: (draft.cc ?? []).map((email) => ({ email })),
      bcc: (draft.bcc ?? []).map((email) => ({ email })),
      date: "2026-10-30T08:00:00Z",
      flags: ["\\Draft"],
      size: 100,
      attachments: (draft.attachments ?? []).map((part, index) => ({
        part_id: String(index + 1),
        filename: part.filename,
        content_type: part.content_type,
        size: Buffer.from(part.content_base64, "base64").length,
      })),
    };
  }
  await context.addInitScript(() => {
    sessionStorage.setItem("urmail.mailbox-token", "browser-state-fixture");
    sessionStorage.setItem("urmail.mailbox-address", "reader@example.test");
    localStorage.setItem("urmail.locale", "en");
  });
  await context.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))
      throw new Error("Compose browser fixture attempted non-loopback access");
    const path = url.pathname;
    if (!path.startsWith("/api/")) return route.continue();
    let payload: unknown;
    let status = 200;
    const method = route.request().method();
    const message = path.match(/\/folders\/([^/]+)\/messages\/(\d+)$/);
    const part = path.match(/\/messages\/(\d+)\/attachments\/(\d+)$/);
    if (path.startsWith("/api/v1/i18n/")) payload = t;
    else if (path === "/api/v1/branding") payload = { name: "urMail" };
    else if (path.endsWith("/mailbox/status")) payload = { native: true };
    else if (path.endsWith("/mailbox/settings"))
      payload = {
        address: "reader@example.test",
        display_name: "",
        signature_text: "",
        signature_html: "",
        preferences: { reply_identity: "recipient" },
        version: 1,
      };
    else if (path.endsWith("/mailbox/identities"))
      payload = {
        identities: [
          { address: "reader@example.test", primary: true },
          { address: "team@example.test", primary: false },
        ],
      };
    else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [
          { name: "INBOX", name_enc: inbox, flags: [], delimiter: "/" },
          {
            name: "Drafts",
            name_enc: draftsFolder,
            flags: ["\\Drafts"],
            delimiter: "/",
          },
        ],
      };
    else if (path.endsWith("/mailbox/send-receipts")) payload = { items: [] };
    else if (path.endsWith("/mailbox/drafts") && method === "PUT") {
      const body = route.request().postDataJSON() as BrowserDraft;
      if (body.uid && (!drafts.has(body.uid) || body.uid_validity !== 9)) {
        rejectedSaves.push(body);
        status = 409;
        payload = {
          error: { code: "DRAFT_CHANGED", message: "error.DRAFT_CHANGED" },
        };
      } else {
        if (body.uid) drafts.delete(body.uid);
        const uid = ++nextUID;
        drafts.set(uid, body);
        saves.push(body);
        payload = {
          saved: true,
          uid,
          uid_validity: 9,
          folder: "Drafts",
          name_enc: draftsFolder,
        };
      }
    } else if (/\/mailbox\/drafts\/\d+$/.test(path) && method === "DELETE") {
      const uid = Number(path.split("/").at(-1));
      drafts.delete(uid);
      deleted.push(uid);
      payload = { deleted: true, uid };
    } else if (path.endsWith("/mailbox/drafts"))
      payload = {
        folder: "Drafts",
        name_enc: draftsFolder,
        messages: [...drafts].map(([uid, draft]) => detail(uid, draft)),
        total: drafts.size,
        limit: 50,
        offset: 0,
      };
    else if (part) {
      const attachment = drafts.get(Number(part[1]))?.attachments?.[
        Number(part[2]) - 1
      ];
      if (!attachment) throw new Error("Missing fixture attachment");
      return route.fulfill({
        contentType: attachment.content_type,
        body: Buffer.from(attachment.content_base64, "base64"),
      });
    } else if (message) {
      const folder = decodeURIComponent(message[1]);
      const uid = Number(message[2]);
      if (folder === inbox && uid === 1) payload = source;
      else {
        const draft = drafts.get(uid);
        if (!draft) {
          status = 404;
          payload = { error: { code: "MESSAGE_NOT_FOUND" } };
        } else payload = detail(uid, draft);
      }
    } else if (path.endsWith("/messages")) {
      const inDrafts = path.includes(draftsFolder);
      const messages = inDrafts
        ? [...drafts].map(([uid, draft]) => detail(uid, draft))
        : [source];
      payload = {
        messages,
        uid_validity: 9,
        total: messages.length,
        limit: 50,
        offset: 0,
      };
    } else if (path.endsWith("/flags")) payload = { updated: true };
    else if (path.endsWith("/calendars/reminders")) payload = { reminders: [] };
    else if (path.endsWith("/ai/target"))
      payload = { enabled: false, allowed: false };
    else if (path.endsWith("/mailbox/quota"))
      payload = { used_bytes: 100, quota_bytes: 1000000 };
    else {
      status = 404;
      payload = { error: { code: "NOT_FOUND" } };
    }
    return route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
  return { t, drafts, saves, rejectedSaves, deleted, draftsFolder };
}

test("browser reply-all uses granted identity and every Reply-To without self aliases", async ({
  page,
  context,
}) => {
  const fixture = await composeBrowserFixture(context);
  await page.goto("/webmail/inbox");
  await page
    .getByRole("button")
    .filter({ hasText: "Reply browser source" })
    .click();
  await page
    .getByRole("button", { name: "Reply all", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("#compose-from")).toHaveValue(
    "team@example.test",
  );
  await expect(dialog.locator("#compose-to")).toHaveValue(
    "reply-one@example.test, reply-two@example.test",
  );
  await expect(dialog.locator("#compose-cc")).toHaveValue(
    "other@example.test, copy@example.test",
  );
  await dialog.getByRole("button", { name: "Save draft", exact: true }).click();
  await expect.poll(() => fixture.saves.at(-1)?.from).toBe("team@example.test");
  expect(fixture.saves.at(-1)?.to).toEqual([
    "reply-one@example.test",
    "reply-two@example.test",
  ]);
  expect(fixture.saves.at(-1)?.cc).toEqual([
    "other@example.test",
    "copy@example.test",
  ]);
});

test("browser inline expansion and pending attachment preserve one draft through restore and discard", async ({
  page,
  context,
}) => {
  const fixture = await composeBrowserFixture(context);
  await page.addInitScript(() => {
    const Original = window.FileReader;
    const pending: Array<() => void> = [];
    class DelayedReader extends Original {
      override readAsDataURL(blob: Blob) {
        pending.push(() => super.readAsDataURL(blob));
      }
    }
    window.FileReader = DelayedReader;
    Object.assign(window, {
      releaseFixtureAttachments: () =>
        pending.splice(0).forEach((finish) => finish()),
    });
  });
  await page.goto("/webmail/inbox");
  await page
    .getByRole("button")
    .filter({ hasText: "Reply browser source" })
    .click();
  await page.getByRole("button", { name: "Reply", exact: true }).last().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("#compose-from")).toBeEnabled();
  await dialog.locator("#compose-from").selectOption("team@example.test");
  await dialog.locator("#compose-to").fill("edited@example.test");
  await dialog
    .getByRole("button", { name: "HTML source", exact: true })
    .click();
  await dialog
    .getByRole("textbox", { name: "HTML source", exact: true })
    .fill("<p><strong>Visible inline browser edit</strong></p>");
  await dialog.getByRole("button", { name: "Save draft", exact: true }).click();
  await expect
    .poll(() => fixture.saves.at(-1)?.html)
    .toContain("Visible inline browser edit");
  const beforeRead = fixture.saves.length;
  await dialog.locator("#compose-attach").setInputFiles({
    name: "retained.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("retained browser attachment"),
  });
  await expect(
    dialog.getByRole("status").filter({
      hasText: fixture.t["page.webmail.compose_reading_attachments"],
    }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(dialog).toBeVisible();
  expect(fixture.saves.length).toBe(beforeRead);
  await page.evaluate(() =>
    (
      window as Window & { releaseFixtureAttachments?: () => void }
    ).releaseFixtureAttachments?.(),
  );
  await expect(dialog.getByText("retained.txt", { exact: true })).toBeVisible();
  await dialog.getByTitle("Maximize").click();
  await expect(dialog.locator("#compose-from")).toHaveValue(
    "team@example.test",
  );
  await expect(dialog.locator("#compose-to")).toHaveValue(
    "edited@example.test",
  );
  await expect(
    dialog.getByRole("textbox", { name: "HTML source", exact: true }),
  ).toHaveValue("<p><strong>Visible inline browser edit</strong></p>");
  await expect(dialog.getByText("retained.txt", { exact: true })).toBeVisible();
  await dialog.getByTitle("Close").click();
  await expect(dialog).toBeHidden();
  await expect
    .poll(() => fixture.saves.at(-1)?.attachments?.[0]?.filename)
    .toBe("retained.txt");
  await page.getByRole("button", { name: "Drafts", exact: true }).click();
  await page
    .getByRole("button")
    .filter({ hasText: "Re: Reply browser source" })
    .click();
  await page
    .getByRole("button", { name: "Continue draft", exact: true })
    .click();
  await expect(dialog.locator("#compose-from")).toBeEnabled();
  await expect(dialog.locator("#compose-from")).toHaveValue(
    "team@example.test",
  );
  await expect(dialog.getByText("retained.txt", { exact: true })).toBeVisible();
  await expect(
    dialog.getByRole("textbox", { name: "Rich text", exact: true }),
  ).toContainText("Visible inline browser edit");
  await dialog
    .getByRole("button", { name: "Discard draft", exact: true })
    .click();
  await expect(dialog).toBeHidden();
  await expect.poll(() => fixture.drafts.size).toBe(0);
  expect(fixture.deleted.length).toBe(1);
});

test("browser stale tab cannot replace a draft saved by another tab", async ({
  page,
  context,
}) => {
  const fixture = await composeBrowserFixture(context);
  fixture.drafts.set(80, {
    from: "team@example.test",
    subject: "Shared draft conflict",
    text: "Original shared draft",
    to: ["recipient@example.test"],
  });
  async function resume(tab: Page) {
    await tab.goto("/webmail/inbox");
    await tab.getByRole("button", { name: "Drafts", exact: true }).click();
    await tab
      .getByRole("button")
      .filter({ hasText: "Shared draft conflict" })
      .click();
    await tab
      .getByRole("button", { name: "Continue draft", exact: true })
      .click();
    await expect(
      tab.getByRole("dialog").locator("#compose-from"),
    ).toBeEnabled();
  }
  await resume(page);
  const second = await context.newPage();
  await resume(second);
  const firstDialog = page.getByRole("dialog");
  await firstDialog
    .getByRole("textbox", { name: "Rich text", exact: true })
    .fill("First tab saved content");
  await firstDialog
    .getByRole("button", { name: "Save draft", exact: true })
    .click();
  await expect
    .poll(() => fixture.saves.at(-1)?.text)
    .toContain("First tab saved content");
  const stale = second.getByRole("dialog");
  await stale
    .getByRole("textbox", { name: "Rich text", exact: true })
    .fill("Second tab unsaved content");
  await stale.getByRole("button", { name: "Save draft", exact: true }).click();
  await expect(stale.getByRole("alert")).toHaveText(
    fixture.t["error.DRAFT_CHANGED"],
  );
  await stale.getByTitle("Close").click();
  await expect(stale).toBeVisible();
  await expect(
    stale.getByRole("textbox", { name: "Rich text", exact: true }),
  ).toContainText("Second tab unsaved content");
  expect([...fixture.drafts.values()][0]?.text).toContain(
    "First tab saved content",
  );
  expect(fixture.rejectedSaves.length).toBeGreaterThan(0);
  await second.close();
});
