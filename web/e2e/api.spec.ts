import { test, expect, request, APIRequestContext } from "@playwright/test";

const email = process.env.URMAIL_E2E_EMAIL || "admin@urmail.test";
const password = process.env.URMAIL_E2E_PASSWORD;

let api: APIRequestContext;
let token = "";

test.beforeAll(async ({ baseURL }) => {
  if (!password) return;
  api = await request.newContext({
    baseURL,
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { "Content-Type": "application/json" },
  });
  const res = await api.post("/api/v1/auth/login", {
    data: { email, password },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  token = body.token;
});

test.afterAll(async () => {
  if (api) await api.dispose();
});

test.skip(!password, "set URMAIL_E2E_PASSWORD to run the API e2e suite");

test("healthz is ok", async ({ request }) => {
  const res = await request.get("/api/v1/healthz");
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.status).toBe("ok");
});

test("unknown admin path is 404", async ({ request }) => {
  const res = await request.get("/admin/does-not-exist");
  expect(res.status()).toBe(404);
});

test("tenant and platform APIs are reachable", async () => {
  const headers = { Authorization: "Bearer " + token };
  for (const path of [
    "/api/v1/auth/me",
    "/api/v1/auth/sessions",
    "/api/v1/auth/mfa/status",
    "/api/v1/billing/plans",
    "/api/v1/billing/subscription",
    "/api/v1/audit",
    "/api/v1/platform/overview",
    "/api/v1/platform/tenants",
  ]) {
    const res = await api.get(path, { headers });
    expect(res.status(), path).toBe(200);
  }
});

test("legacy basic-auth status works", async ({ request }) => {
  const res = await request.get("/admin/system/status", {
    headers: {
      Authorization:
        "Basic " + Buffer.from(email + ":" + password).toString("base64"),
    },
  });
  expect(res.status()).toBe(200);
});
