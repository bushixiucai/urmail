import { test, expect, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const email = process.env.URMAIL_E2E_EMAIL || "admin@urmail.test";
const password = process.env.URMAIL_E2E_PASSWORD;

test.setTimeout(60_000);

test("login and navigate the management pages", async ({ page }) => {
  test.skip(!password, "set URMAIL_E2E_PASSWORD to run the browser e2e suite");
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

  for (const path of ["/audit", "/domains", "/mailboxes"]) {
    await page.goto(path);
    await expect(page.locator("h2").first()).toBeVisible();
  }

  await page.goto("/platform");
  await expect(page.locator("h2").first()).toBeVisible();

  expect(errors).toEqual([]);
});

// These browser fixtures validate visible UI state and request contracts only.
// Provider authorization, encryption and accounting have separate true-PG tests.
async function aiBrowserFixture(page: Page) {
  const t = JSON.parse(
    await readFile(
      resolve(process.cwd(), "../internal/i18n/locales/en.json"),
      "utf8",
    ),
  ) as Record<string, string>;
  const folder = "b64.SU5CT1g";
  const messages = [1, 2].map((uid) => ({
    uid,
    uid_validity: 9,
    folder_enc: folder,
    subject: `AI source ${uid}`,
    from: [{ email: `sender${uid}@example.test` }],
    to: [{ email: "reader@example.test" }],
    date: "2026-10-30T08:00:00Z",
    flags: ["\\Seen"],
    size: 100,
    has_attachments: false,
    text: `Actual private content for source ${uid}`,
    attachments: [],
  }));
  const state = {
    settings: {
      address: "reader@example.test",
      display_name: "Reader",
      signature_text: "",
      signature_html: "",
      preferences: {} as Record<string, unknown>,
      version: 1,
    },
    model: "original-model",
    previews: [] as Record<string, unknown>[],
    generations: [] as Record<string, unknown>[],
    saved: [] as Record<string, unknown>[],
    delayFirstGeneration: false,
    firstGenerationReplied: false,
    releaseFirstGeneration: () => {},
  };
  let release!: () => void;
  const generationBarrier = new Promise<void>((resolve) => {
    release = resolve;
  });
  state.releaseFirstGeneration = release;
  await page.addInitScript(() => {
    sessionStorage.setItem("urmail.mailbox-token", "isolated-ai-ui-fixture");
    sessionStorage.setItem("urmail.mailbox-address", "reader@example.test");
    localStorage.setItem("urmail.locale", "en");
  });
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost", "[::1]"].includes(url.hostname))
      throw new Error("AI UI fixture refused non-loopback access");
    const path = url.pathname;
    if (!path.startsWith("/api/")) return route.continue();
    let payload: unknown;
    let status = 200;
    if (path.startsWith("/api/v1/i18n/")) payload = t;
    else if (path.endsWith("/mailbox/status")) payload = { native: true };
    else if (path.endsWith("/mailbox/settings")) {
      if (route.request().method() === "PUT") {
        const body = route.request().postDataJSON() as Record<string, unknown>;
        state.saved.push(body);
        expect(body.expected_version).toBe(state.settings.version);
        state.settings.version++;
        state.settings.preferences = body.preferences as Record<
          string,
          unknown
        >;
        const ai = state.settings.preferences.ai as Record<string, unknown>;
        state.model = String(ai.model);
        // The read contract intentionally redacts the write-only credential.
        delete ai.api_key;
        ai.key_configured = true;
        payload = { saved: true, version: state.settings.version };
      } else payload = state.settings;
    } else if (path.endsWith("/mailbox/identities"))
      payload = {
        identities: [{ address: "reader@example.test", primary: true }],
      };
    else if (path.endsWith("/mailbox/folders"))
      payload = {
        folders: [
          { name: "INBOX", name_enc: folder, delimiter: "/", flags: [] },
        ],
      };
    else if (/\/messages\/[12]$/.test(path))
      payload = messages.find((message) => path.endsWith(`/${message.uid}`));
    else if (path.endsWith("/messages"))
      payload = { messages, uid_validity: 9, total: 2, limit: 50, offset: 0 };
    else if (path.endsWith("/flags")) payload = { updated: true };
    else if (path.endsWith("/ai/target"))
      payload = {
        enabled: true,
        allowed: true,
        provider: "openai",
        model: state.model,
        endpoint: "https://provider.example.test/v1",
        configuration_digest: `target-${state.settings.version}`,
        is_custom: Boolean(state.settings.preferences.ai),
      };
    else if (path.endsWith("/ai/context")) {
      const message = messages.find(
        (item) => item.uid === Number(url.searchParams.get("uid")),
      )!;
      payload = {
        subject: message.subject,
        body: message.text,
        truncated: false,
      };
    } else if (path.endsWith("/ai/assist")) {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      expect(body.folder_enc).toBe(folder);
      expect(body.uid_validity).toBe(9);
      const message = messages.find((item) => item.uid === body.uid)!;
      expect(message).toBeTruthy();
      const digest = `content-${message.uid}-${state.settings.version}`;
      expect(body.body).toBe(message.text);
      expect(body.selection).toBe(message.text);
      if (body.preview_only) {
        state.previews.push(body);
        expect(body.confirmed).toBe(false);
        payload = {
          content_digest: digest,
          provider: "openai",
          model: state.model,
          endpoint: "https://provider.example.test/v1",
          messages: [{ role: "user", content: message.text }],
        };
      } else {
        state.generations.push(body);
        expect(body.request_key).toBe(state.previews.at(-1)?.request_key);
        expect(body.confirmed).toBe(true);
        expect(body.content_digest).toBe(digest);
        expect(body.configuration_digest).toBe(
          `target-${state.settings.version}`,
        );
        if (state.delayFirstGeneration && state.generations.length === 1)
          await generationBarrier;
        payload = {
          result: {
            subject: `Reply to source ${message.uid}`,
            body: `Proposal for source ${message.uid}`,
          },
        };
        state.firstGenerationReplied = true;
      }
    } else if (path.endsWith("/calendars/reminders"))
      payload = { reminders: [] };
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
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/webmail/inbox?v=legacy");
  const list = page.getByRole("list", {
    name: t["page.webmail.message_list"],
    exact: true,
  });
  await expect(list.getByText("AI source 1", { exact: true })).toBeVisible();
  return { t, state, list };
}

test("native AI browser binds every preview and discards late proposals after source changes", async ({
  page,
  baseURL,
}) => {
  test.skip(
    process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
    "Requires isolated loopback fixture",
  );
  expect(["127.0.0.1", "localhost", "[::1]"]).toContain(
    new URL(baseURL!).hostname,
  );
  const { t, state, list } = await aiBrowserFixture(page);
  state.delayFirstGeneration = true;
  await list.getByRole("button").filter({ hasText: "AI source 1" }).click();
  await page
    .getByRole("button", { name: t["page.webmail.ai_summary"], exact: true })
    .click();
  const drawer = page.getByRole("complementary", {
    name: t["page.webmail.ai_copilot"],
    exact: true,
  });
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_quick_reply"],
      exact: true,
    })
    .click();
  const confirmation = page.getByRole("dialog", {
    name: t["page.webmail.ai_confirm_title"],
    exact: true,
  });
  await expect(confirmation).toContainText(
    "Actual private content for source 1",
  );
  await expect(confirmation).toContainText("original-model");
  await confirmation.screenshot({
    path: resolve(
      process.cwd(),
      "../output/playwright/webmail-remediation/ai-explicit-consent.png",
    ),
  });
  expect(state.generations).toHaveLength(0);
  await confirmation
    .getByRole("button", {
      name: t["page.webmail.ai_confirm_execute"],
      exact: true,
    })
    .click();
  await expect.poll(() => state.generations.length).toBe(1);
  await list.getByRole("button").filter({ hasText: "AI source 2" }).click();
  await expect(drawer.getByText("AI source 2", { exact: true })).toBeVisible();
  state.releaseFirstGeneration();
  await expect.poll(() => state.firstGenerationReplied).toBe(true);
  await expect(
    drawer.getByText("Proposal for source 1", { exact: true }),
  ).toHaveCount(0);
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_quick_reply"],
      exact: true,
    })
    .click();
  await expect(confirmation).toContainText(
    "Actual private content for source 2",
  );
  expect(state.generations).toHaveLength(1);
  await confirmation
    .getByRole("button", { name: t["action.cancel"], exact: true })
    .click();
  expect(state.generations).toHaveLength(1);
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_quick_reply"],
      exact: true,
    })
    .click();
  await expect(confirmation).toContainText(
    "Actual private content for source 2",
  );
  expect(state.previews).toHaveLength(3);
  await confirmation
    .getByRole("button", {
      name: t["page.webmail.ai_confirm_execute"],
      exact: true,
    })
    .click();
  await expect(
    drawer.getByText("Proposal for source 2", { exact: true }),
  ).toBeVisible();
  expect(state.generations).toHaveLength(2);
});

test("native AI browser saves and reloads settings while invalidating the old result", async ({
  page,
  baseURL,
}) => {
  test.skip(
    process.env.URMAIL_E2E_NATIVE_FIXTURE !== "1",
    "Requires isolated loopback fixture",
  );
  expect(["127.0.0.1", "localhost", "[::1]"]).toContain(
    new URL(baseURL!).hostname,
  );
  const { t, state, list } = await aiBrowserFixture(page);
  await list.getByRole("button").filter({ hasText: "AI source 1" }).click();
  await page
    .getByRole("button", { name: t["page.webmail.ai_summary"], exact: true })
    .click();
  const drawer = page.getByRole("complementary", {
    name: t["page.webmail.ai_copilot"],
    exact: true,
  });
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_quick_reply"],
      exact: true,
    })
    .click();
  const confirmation = page.getByRole("dialog", {
    name: t["page.webmail.ai_confirm_title"],
    exact: true,
  });
  await confirmation
    .getByRole("button", {
      name: t["page.webmail.ai_confirm_execute"],
      exact: true,
    })
    .click();
  await expect(
    drawer.getByText("Proposal for source 1", { exact: true }),
  ).toBeVisible();
  await drawer
    .getByRole("button", { name: t["page.webmail.ai_tab_config"], exact: true })
    .click();
  await drawer
    .getByLabel(t["page.webmail.ai_use_custom"], { exact: true })
    .check();
  await drawer
    .getByLabel(t["page.webmail.ai_endpoint"], { exact: true })
    .fill("https://provider.example.test/v1");
  await drawer
    .getByLabel(t["page.webmail.ai_model_label"], { exact: true })
    .fill("replacement-model");
  await drawer
    .getByLabel(t["page.webmail.ai_api_key"], { exact: true })
    .fill("synthetic-ui-fixture-key");
  await drawer
    .getByRole("button", { name: t["action.save"], exact: true })
    .click();
  await expect.poll(() => state.saved.length).toBe(1);
  await expect(
    drawer.getByText(t["page.webmail.ai_custom_saved"], { exact: true }),
  ).toBeVisible();
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_tab_current"],
      exact: true,
    })
    .click();
  await expect(
    drawer.getByText("Proposal for source 1", { exact: true }),
  ).toHaveCount(0);
  await page.reload();
  await page
    .getByRole("list", { name: t["page.webmail.message_list"], exact: true })
    .getByRole("button")
    .filter({ hasText: "AI source 1" })
    .click();
  await page
    .getByRole("button", { name: t["page.webmail.ai_summary"], exact: true })
    .click();
  await drawer
    .getByRole("button", { name: t["page.webmail.ai_tab_config"], exact: true })
    .click();
  await expect(
    drawer.getByLabel(t["page.webmail.ai_model_label"], { exact: true }),
  ).toHaveValue("replacement-model");
  await expect(
    drawer.getByLabel(t["page.webmail.ai_endpoint"], { exact: true }),
  ).toHaveValue("https://provider.example.test/v1");
  await expect(
    drawer.getByLabel(t["page.webmail.ai_api_key"], { exact: true }),
  ).toHaveValue("");
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_tab_current"],
      exact: true,
    })
    .click();
  await drawer
    .getByRole("button", {
      name: t["page.webmail.ai_quick_reply"],
      exact: true,
    })
    .click();
  await expect(confirmation).toContainText("replacement-model");
  expect(state.generations).toHaveLength(1);
  await confirmation
    .getByRole("button", { name: t["action.cancel"], exact: true })
    .click();
});
