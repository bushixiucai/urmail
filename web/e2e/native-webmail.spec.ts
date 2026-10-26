import { expect, test } from "@playwright/test";

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
  const blocked: string[] = [];
  page.on("console", (message) => {
    if (/Content Security Policy|violates.*directive/i.test(message.text()))
      blocked.push(message.text());
  });
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
  await expect.poll(() => blocked.length).toBeGreaterThan(0);
  // Give queued CSS/image/font loads an opportunity to reach the route handler.
  await page.waitForTimeout(300);
  expect(requests).toEqual([]);
});
