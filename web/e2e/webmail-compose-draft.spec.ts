import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

interface FixtureHandlers {
  onDraftSave?: (data: unknown) => void;
  onSend?: (headers: Record<string, string>, body: unknown) => void;
}

async function setupComposeFixture(page: Page, handlers: FixtureHandlers = {}) {
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
    else if (path.endsWith("/messages")) payload = { messages: [], total: 0 };
    else if (path.endsWith("/mailbox/drafts")) {
      const body = route.request().postDataJSON();
      handlers.onDraftSave?.(body);
      payload = { saved: true, uid: 888, uid_validity: 1 };
    } else if (path.endsWith("/mailbox/send")) {
      const headers = route.request().headers();
      const body = route.request().postDataJSON();
      handlers.onSend?.(headers, body);
      payload = { sent: true };
    }

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload),
    });
  });
}

test.describe("Webmail Compose & Debounced Drafts", () => {
  test("validates recipient chip, subject, and attachment size limits", async ({
    page,
  }) => {
    await setupComposeFixture(page);
    await page.goto("/webmail/inbox");

    // Click compose button in rail
    const composeBtn = page.getByTestId("webmail-compose-button");
    await composeBtn.click();

    // Verify dock appears
    await expect(page.getByTestId("webmail-compose-dock")).toBeVisible();

    // Type recipient and press Enter
    const toInput = page.getByTestId("compose-to-input");
    await toInput.fill("partner@example.test");
    await toInput.press("Enter");

    await expect(
      page.getByTestId("recipient-pill-partner@example.test"),
    ).toBeVisible();

    // Fill subject
    const subjectInput = page.getByTestId("compose-subject-input");
    await subjectInput.fill("Project Kickoff Proposal");

    // Verify send button is enabled
    const sendBtn = page.getByTestId("compose-send-button");
    await expect(sendBtn).toBeEnabled();

    // Test file attachment over 5MB limit rejection
    const fileInput = page.getByTestId("compose-file-input");
    const oversizedBuffer = Buffer.alloc(6 * 1024 * 1024, "a");
    await fileInput.setInputFiles({
      name: "large-video.mp4",
      mimeType: "video/mp4",
      buffer: oversizedBuffer,
    });

    await expect(page.getByTestId("attachment-error-notice")).toBeVisible();
    await expect(sendBtn).toBeDisabled();

    // Attach valid file under limit
    const validBuffer = Buffer.from("Valid attachment content", "utf-8");
    await fileInput.setInputFiles({
      name: "specs.txt",
      mimeType: "text/plain",
      buffer: validBuffer,
    });

    await expect(page.getByTestId("attachment-error-notice")).toHaveCount(0);
    await expect(sendBtn).toBeEnabled();
  });

  test("debounces draft auto-save and restores draft with enabled send button after page reload (P0 regression)", async ({
    page,
  }) => {
    let savedDraftData: unknown = null;
    await setupComposeFixture(page, {
      onDraftSave: (data) => {
        savedDraftData = data;
      },
    });

    await page.goto("/webmail/inbox");
    await page.getByTestId("webmail-compose-button").click();
    await expect(page.getByTestId("webmail-compose-dock")).toBeVisible();

    // Enter recipient
    const toInput = page.getByTestId("compose-to-input");
    await toInput.fill("partner@example.test");
    await toInput.press("Enter");

    // Enter subject
    const subjectInput = page.getByTestId("compose-subject-input");
    await subjectInput.fill("Draft Recovery Subject 2026");

    // Enter body in TipTap editor
    const editor = page.locator(".tiptap.ProseMirror");
    await editor.click();
    await page.keyboard.type("This draft must survive page reload.");

    // Wait for debounced auto-save (> 2000ms debounce timer)
    await page.waitForTimeout(2300);
    expect(savedDraftData).not.toBeNull();

    // Verify sessionStorage has draft payload
    const stored = await page.evaluate(() =>
      sessionStorage.getItem("urmail.webmail.active_draft"),
    );
    expect(stored).not.toBeNull();
    const parsed = JSON.parse(stored!);
    expect(parsed.to).toContain("partner@example.test");
    expect(parsed.subject).toBe("Draft Recovery Subject 2026");

    // Real browser reload (P0 #1 reproduction trigger)
    await page.reload();

    // Reopen compose dock after reload
    await page.getByTestId("webmail-compose-button").click();
    await expect(page.getByTestId("webmail-compose-dock")).toBeVisible();

    // Assert recipient pill is recovered
    await expect(
      page.getByTestId("recipient-pill-partner@example.test"),
    ).toBeVisible();

    // Assert subject is recovered
    await expect(page.getByTestId("compose-subject-input")).toHaveValue(
      "Draft Recovery Subject 2026",
    );

    // Assert send button is ENABLED (P0 #1 bug was disabled permanent lock)
    const sendBtn = page.getByTestId("compose-send-button");
    await expect(sendBtn).toBeEnabled();
  });

  test("sends message with Idempotency-Key header after undo window", async ({
    page,
  }) => {
    let capturedHeaders: Record<string, string> = {};
    let sendCompleted = false;

    await setupComposeFixture(page, {
      onSend: (headers) => {
        capturedHeaders = headers;
        sendCompleted = true;
      },
    });

    await page.goto("/webmail/inbox");
    await page.getByTestId("webmail-compose-button").click();

    const toInput = page.getByTestId("compose-to-input");
    await toInput.fill("partner@example.test");
    await toInput.press("Enter");

    const subjectInput = page.getByTestId("compose-subject-input");
    await subjectInput.fill("Important Notification");

    const sendBtn = page.getByTestId("compose-send-button");
    await expect(sendBtn).toBeEnabled();
    await sendBtn.click();

    // Verify 10-second undo toast appears
    await expect(page.getByTestId("undo-send-toast")).toBeVisible();
    await expect(page.getByTestId("undo-send-button")).toBeVisible();

    // Wait 10.5 seconds for undo countdown to elapse and trigger real send
    await page.waitForTimeout(10500);

    // Verify send API was called
    expect(sendCompleted).toBe(true);

    // Assert Idempotency-Key header presence (UUID format)
    const idempotencyKey =
      capturedHeaders["idempotency-key"] || capturedHeaders["Idempotency-Key"];
    expect(idempotencyKey).toBeDefined();
    expect(idempotencyKey).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i,
    );
  });
});
