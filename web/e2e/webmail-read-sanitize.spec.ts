import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test.skip(
  process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
  "Requires the isolated local Vite fixture",
);

async function setupReadFixture(
  page: Page,
  initialSettings?: Record<string, unknown>,
) {
  const catalogue = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/zh-CN.json"),
      "utf8",
    ),
  ) as Record<string, string>;

  let savedSettings = {
    display_name: "Reader",
    signature_text: "",
    signature_html: "",
    preferences: { allow_all_remote_images: false },
    version: 1,
    updated_at: "2026-11-06T12:00:00Z",
    address: "reader@example.test",
    ...initialSettings,
  };

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
    else if (path.endsWith("/mailbox/settings")) {
      if (route.request().method() === "PUT") {
        const body = JSON.parse(route.request().postData() || "{}");
        savedSettings = { ...savedSettings, ...body };
        payload = {
          saved: true,
          version: ((savedSettings.version as number) || 1) + 1,
        };
      } else {
        payload = savedSettings;
      }
    } else if (path.endsWith("/mailbox/rules"))
      payload = { version: "v1", state: "active", rules: { rules: [] } };
    else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [
          { name: "INBOX", name_enc: "INBOX", delimiter: "/", flags: [] },
        ],
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
        html: "<p>Tracking image <img id='allowed-img' src='https://tracker.test/allowed.png'/><img id='proto-img' src='//tracker.test/proto.png'/><video id='poster-vid' poster='https://tracker.test/poster.jpg'></video><div id='bg-div' background='https://tracker.test/bg.jpg'></div><object id='data-obj' data='https://tracker.test/data.swf'></object><img src='https://tracker.test/pixel.gif' onerror='window.__xssRan=true'/><link rel='stylesheet' href='https://tracker.test/x.css'/><script>window.__scriptRan=true</script></p>",
        notes: ["remote_images_blocked"],
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
  test("neutralizes remote images by default, sandboxes iframe, blocks trackers and scripts", async ({
    page,
  }) => {
    const trackerRequests: string[] = [];
    await page.route(/tracker\.test/, async (route) => {
      trackerRequests.push(route.request().url());
      await route.abort();
    });

    await setupReadFixture(page);
    await page.goto("/webmail/inbox");

    // Click message to read
    const row = page.getByTestId("mail-row-301");
    await row.click();

    // Verify sandboxed iframe attributes (P0 #2 regression assertion)
    const frame = page.getByTestId("mail-html-frame");
    await expect(frame).toBeVisible();
    await expect(frame).toHaveAttribute("sandbox", "");
    await expect(frame).toHaveAttribute("referrerPolicy", "no-referrer");

    // Allow any async style/image fetches to attempt network contact
    await page.waitForTimeout(300);

    // Assert zero tracking requests leaked
    expect(trackerRequests).toHaveLength(0);

    // Assert zero script / inline event execution
    const scriptsExecuted = await page.evaluate(() => {
      const w = window as unknown as {
        __scriptRan?: boolean;
        __xssRan?: boolean;
      };
      return Boolean(w.__scriptRan || w.__xssRan);
    });
    expect(scriptsExecuted).toBe(false);

    // Verify remote images banner
    await expect(page.getByTestId("remote-images-banner")).toBeVisible();
    await expect(page.getByTestId("show-images-button")).toBeVisible();

    // Verify security badge
    await expect(page.getByTestId("security-badge-button")).toBeVisible();

    // Verify attachment card
    await expect(page.getByTestId("attachment-card-att-1")).toBeVisible();
  });

  test("allows only absolute https:// <img> src on manual consent, stripping protocol-relative, remote poster, background, and data", async ({
    page,
  }) => {
    await setupReadFixture(page);
    await page.goto("/webmail/inbox");

    const row = page.getByTestId("mail-row-301");
    await row.click();

    await expect(page.getByTestId("remote-images-banner")).toBeVisible();

    // Grant manual consent by clicking Show Images
    const showBtn = page.getByTestId("show-images-button");
    await showBtn.click();

    // Banner disappears
    await expect(page.getByTestId("remote-images-banner")).toBeHidden();

    // Inspect iframe srcDoc content
    const frame = page.getByTestId("mail-html-frame");
    const srcDoc = (await frame.getAttribute("srcDoc")) || "";

    // 1. Absolute https <img> src IS allowed
    expect(srcDoc).toContain('src="https://tracker.test/allowed.png"');

    // 2. Protocol-relative //... src is stripped
    expect(srcDoc).not.toContain("//tracker.test/proto.png");

    // 3. Remote poster on video is stripped
    expect(srcDoc).not.toContain("https://tracker.test/poster.jpg");

    // 4. Remote background on div is stripped
    expect(srcDoc).not.toContain("https://tracker.test/bg.jpg");

    // 5. Remote data on object is stripped
    expect(srcDoc).not.toContain("https://tracker.test/data.swf");
  });

  test("persists manual consent when global remote images setting is disabled in privacy settings", async ({
    page,
  }) => {
    // Start with global setting enabled
    await setupReadFixture(page, {
      preferences: { allow_all_remote_images: true },
    });
    await page.goto("/webmail/inbox");

    const row = page.getByTestId("mail-row-301");
    await row.click();

    // With global setting enabled, banner is initially hidden and only
    // absolute https img src survives in the sandboxed document.
    await expect(page.getByTestId("remote-images-banner")).toBeHidden();
    const frame = page.getByTestId("mail-html-frame");
    await expect(frame).toBeVisible();
    const allowedSrc = (await frame.getAttribute("srcDoc")) ?? "";
    expect(allowedSrc).toContain("https://tracker.test/allowed.png");
    expect(allowedSrc).not.toContain("//tracker.test/proto.png");

    // Explicitly click show images to establish manual consent
    // Now open settings drawer and switch to privacy tab
    await page.getByTestId("webmail-settings-btn").click();
    await expect(page.getByTestId("webmail-settings-drawer")).toBeVisible();

    await page.getByTestId("settings-nav-privacy").click();
    await expect(page.getByTestId("remote-images-section")).toBeVisible();

    // Toggle global setting to OFF
    const toggle = page.getByTestId("switch-allow-remote-images");
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-checked", "false");

    // Close settings drawer
    await page.getByTestId("btn-close-settings-drawer").click();

    // Global is now disabled: the absolute remote URL leaves srcDoc and the
    // manual load bar is shown. Protocol-relative URLs stay stripped.
    await expect(page.getByTestId("remote-images-banner")).toBeVisible();
    await expect(page.getByTestId("show-images-button")).toBeVisible();
    await expect(frame).toBeVisible();
    await expect
      .poll(async () => (await frame.getAttribute("srcDoc")) ?? "")
      .not.toContain("https://tracker.test/allowed.png");
    const blockedSrc = (await frame.getAttribute("srcDoc")) ?? "";
    expect(blockedSrc).not.toContain("//tracker.test/proto.png");

    // Now grant manual per-message consent
    await page.getByTestId("show-images-button").click();
    await expect(page.getByTestId("remote-images-banner")).toBeHidden();

    // Reopen settings and confirm turning settings on/off does not revoke manual consent
    await page.getByTestId("webmail-settings-btn").click();
    await page.getByTestId("settings-nav-privacy").click();
    await page.getByTestId("btn-close-settings-drawer").click();

    // Manual consent remains intact
    await expect(page.getByTestId("remote-images-banner")).toBeHidden();
  });
});

test("reading view spans the pane width without a centered paper card", async ({
  page,
}) => {
  await setupReadFixture(page);
  await page.goto("/webmail/inbox");
  await page.getByTestId("mail-row-301").click();
  await expect(page.getByTestId("mail-html-frame")).toBeVisible();

  // The detail column must not be centered with a max width: its left edge
  // hugs the pane and the body container is not a boxed card.
  const widths = await page.evaluate(() => {
    const pane = document.querySelector<HTMLElement>(
      "[data-testid='webmail-reading-pane']",
    );
    const container = document.querySelector<HTMLElement>(
      "[data-testid='mail-body-container']",
    );
    if (!pane || !container) return null;
    const paneRect = pane.getBoundingClientRect();
    const bodyRect = container.getBoundingClientRect();
    const centered = Boolean(
      container.closest(".mx-auto") ??
      container.closest("[class*='max-w-4xl']"),
    );
    return {
      gapLeft: bodyRect.left - paneRect.left,
      gapRight: paneRect.right - bodyRect.right,
      centered,
    };
  });
  expect(widths).not.toBeNull();
  // Body hugs the pane edges (padding only, no centering strip).
  expect(widths!.centered).toBe(false);
  expect(widths!.gapLeft).toBeLessThan(60);
  expect(widths!.gapRight).toBeLessThan(60);
});

test("dark-themed HTML mail keeps background and text colors after sanitization", async ({
  page,
}) => {
  await setupReadFixture(page);
  // Prod Infura dark mail shape: white text over a dark canvas. The sanitizer
  // must keep the color pair (plus table bgcolor) or it collapses into
  // white-on-white.
  await page.goto("/webmail/inbox");
  const result = await page.evaluate(async () => {
    const modulePath = "/src/features/webmail/sanitize-html-mail.ts";
    const { sanitizeHtmlMail } = await import(/* @vite-ignore */ modulePath);
    const html =
      '<body style="background-color:#0b0b0f;color:#ffffff"><div style="background-color:#1a1a22;color:#f5f5f5">Weekly usage</div><table><td bgcolor="#101018" style="color:#e5e5e5">Cell copy</td></table></body>';
    return sanitizeHtmlMail(html);
  });
  expect(result).toMatch(/background-color:\s*#0b0b0f/i);
  expect(result).toMatch(/color:\s*#ffffff/i);
  expect(result).toContain('bgcolor="#101018"');
  expect(result).toContain("Weekly usage");
});

test("dark-themed HTML mail keeps background and text colors after sanitization", async ({
  page,
}) => {
  await setupReadFixture(page);
  // Prod Infura dark mail shape: white text over a dark canvas. The sanitizer
  // must keep the color pair (plus table bgcolor) or it collapses into
  // white-on-white.
  await page.goto("/webmail/inbox");
  const result = await page.evaluate(async () => {
    const modulePath = "/src/features/webmail/sanitize-html-mail.ts";
    const { sanitizeHtmlMail } = await import(/* @vite-ignore */ modulePath);
    const html =
      '<body style="background-color:#0b0b0f;color:#ffffff"><div style="background-color:#1a1a22;color:#f5f5f5">Weekly usage</div><table><td bgcolor="#101018" style="color:#e5e5e5">Cell copy</td></table></body>';
    return sanitizeHtmlMail(html);
  });
  expect(result).toMatch(/background-color:\s*#0b0b0f/i);
  expect(result).toMatch(/color:\s*#ffffff/i);
  expect(result).toContain('bgcolor="#101018"');
  expect(result).toContain("Weekly usage");
});
