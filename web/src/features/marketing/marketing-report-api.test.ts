import { afterEach, expect, it, vi } from "vitest";
import {
  exportMarketingReport,
  loadMarketingReport,
  type MarketingReport,
} from "./marketing-report-api";
const mocks = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: mocks.request,
  getSessionToken: () => "isolated-token",
  getCSRFToken: () => "isolated-csrf",
}));
const report = {
  kind: "flows",
  resource_id: 9,
  from: "2026-10-01T00:00:00Z",
  until: "2026-10-28T00:00:00Z",
  digest: "a".repeat(64),
} as MarketingReport;
afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});
it("binds paginated requests to window and digest", async () => {
  await loadMarketingReport(
    "flows",
    9,
    report.from,
    report.until,
    50,
    report.digest,
    new AbortController().signal,
  );
  const url = mocks.request.mock.calls[0][0] as string;
  const query = new URL(url, "https://fixture.test").searchParams;
  expect(query.get("limit")).toBe("50");
  expect(query.get("expected_digest")).toBe(report.digest);
  expect(query.get("from")).toBe(report.from);
});
it("exports the whole confirmed snapshot with auth, CSRF and digest checks", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response("header\nmeta\n", {
      headers: {
        "Content-Type": "text/csv; charset=utf-8",
        "X-UrMail-Report-Digest": report.digest,
      },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  expect(
    new TextDecoder().decode(
      await exportMarketingReport(report, new AbortController().signal),
    ),
  ).toBe("header\nmeta\n");
  const options = fetch.mock.calls[0][1];
  expect(options.headers.Authorization).toBe("Bearer isolated-token");
  expect(options.headers["X-CSRF-Token"]).toBe("isolated-csrf");
  expect(options.redirect).toBe("error");
  expect(JSON.parse(options.body)).toEqual({
    from: report.from,
    until: report.until,
    expected_digest: report.digest,
    confirmed: true,
  });
});
it("rejects HTML, wrong proof and oversized streams without returning a truncated CSV", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  for (const headers of [
    { "Content-Type": "text/html", "X-UrMail-Report-Digest": report.digest },
    { "Content-Type": "text/csv", "X-UrMail-Report-Digest": "b".repeat(64) },
  ]) {
    fetch.mockResolvedValueOnce(new Response("unsafe", { headers }));
    await expect(
      exportMarketingReport(report, new AbortController().signal),
    ).rejects.toThrow();
  }
  fetch.mockResolvedValueOnce(
    new Response(new Uint8Array((4 << 20) + 1), {
      headers: {
        "Content-Type": "text/csv",
        "X-UrMail-Report-Digest": report.digest,
      },
    }),
  );
  await expect(
    exportMarketingReport(report, new AbortController().signal),
  ).rejects.toMatchObject({ status: 413 });
});
