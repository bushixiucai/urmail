import { test, expect } from "@playwright/test";

const email = process.env.URMAIL_E2E_EMAIL || "admin@urmail.test";
const password = process.env.URMAIL_E2E_PASSWORD;

test.skip(!password, "set URMAIL_E2E_PASSWORD to run the browser e2e suite");
test.setTimeout(60_000);

test("login and navigate the management pages", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  page.on("console", (msg) => {
    if (msg.type() === "error") errors.push(msg.text());
  });

  await page.goto("/login");
  const emailInput = page.locator('input[type="email"]').first();
  const passwordInput = page.locator('input[type="password"]').first();
  await emailInput.waitFor();
  await passwordInput.waitFor();
  await emailInput.fill(email);
  await passwordInput.fill(password);
  await page.locator('button[type="submit"]').click();
  await page.waitForURL((url) => !url.pathname.startsWith("/login"), {
    timeout: 20_000,
  });
  await expect(page.locator("h2").first()).toBeVisible();

  for (const path of ["/billing", "/audit", "/domains", "/mailboxes"]) {
    await page.goto(path);
    await expect(page.locator("h2").first()).toBeVisible();
  }

  await page.goto("/platform");
  await expect(page.locator("h2").first()).toBeVisible();

  expect(errors).toEqual([]);
});
