import {
  ApiRequestError,
  getCSRFToken,
  getSessionToken,
  request,
} from "@/lib/api/client";
export type MarketingFact = {
  record_kind: string;
  record_id: string;
  version: number;
  instance_id: number;
  email: string;
  state: string;
  result_code: string;
  occurred_at: string;
  weight: number;
  suppressed: boolean;
  uncertain: boolean;
};
export type MarketingReport = {
  schema_version: number;
  kind: "campaigns" | "flows";
  resource_id: number;
  name: string;
  from: string;
  until: string;
  observed_at: string;
  digest: string;
  counts: Record<string, number>;
  total_facts: number;
  rows: MarketingFact[];
  next_offset: number;
  max_facts: number;
  max_days: number;
};
export type ReportKind = "campaigns" | "flows";
export const reportBase = (kind: ReportKind, id: number) =>
  `/api/v1/marketing/reports/${kind}/${id}`;
export async function loadMarketingReport(
  kind: ReportKind,
  id: number,
  from: string,
  until: string,
  offset: number,
  digest: string | undefined,
  signal: AbortSignal,
) {
  const q = new URLSearchParams({
    from,
    until,
    offset: String(offset),
    limit: "50",
  });
  if (digest) q.set("expected_digest", digest);
  return request<{ report: MarketingReport }>(reportBase(kind, id) + "?" + q, {
    signal,
  });
}
export async function exportMarketingReport(
  report: MarketingReport,
  signal: AbortSignal,
): Promise<Uint8Array> {
  const token = getSessionToken(),
    csrf = getCSRFToken();
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "text/csv",
  };
  if (token) headers.Authorization = "Bearer " + token;
  if (csrf) headers["X-CSRF-Token"] = csrf;
  const response = await fetch(
    reportBase(report.kind, report.resource_id) + "/export",
    {
      method: "POST",
      credentials: "same-origin",
      redirect: "error",
      cache: "no-store",
      headers,
      signal,
      body: JSON.stringify({
        from: report.from,
        until: report.until,
        expected_digest: report.digest,
        confirmed: true,
      }),
    },
  );
  if (!response.ok) {
    const body = await response
      .json()
      .catch(() => ({ error: { code: "MARKETING_REPORT_UNAVAILABLE" } }));
    throw new ApiRequestError(response.status, body);
  }
  if (
    !response.headers
      .get("content-type")
      ?.toLowerCase()
      .startsWith("text/csv") ||
    response.headers.get("X-UrMail-Report-Digest") !== report.digest
  )
    throw new ApiRequestError(502, {
      error: { code: "MARKETING_REPORT_UNAVAILABLE" },
    });
  const max = 4 << 20,
    reader = response.body?.getReader();
  if (!reader)
    throw new ApiRequestError(502, {
      error: { code: "MARKETING_REPORT_UNAVAILABLE" },
    });
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.length;
    if (total > max) {
      await reader.cancel();
      throw new ApiRequestError(413, {
        error: { code: "MARKETING_REPORT_BUDGET" },
      });
    }
    chunks.push(value);
  }
  const out = new Uint8Array(total);
  let cursor = 0;
  for (const c of chunks) {
    out.set(c, cursor);
    cursor += c.length;
  }
  return out;
}
