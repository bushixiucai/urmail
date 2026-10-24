import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/app/auth-context";
import {
  ApiRequestError,
  apiErrorSchema,
  getSessionToken,
  request,
} from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/ui/notice-dialog";

interface Entry {
  id: number;
  subject: string | null;
  message_id: string;
  from_addr: string | null;
  received_at: string;
  size_bytes: number;
  legal_hold: boolean;
  deleted_at?: string | null;
}
interface Content {
  id: number;
  body_text: string;
  body_html?: string;
  extraction_error: string;
  raw_sha256: string;
  attachments: {
    index: number;
    name: string;
    media_type: string;
    size: number;
    sha256: string;
  }[];
}
interface Preview {
  id: string;
  candidate_count: number;
  retention_days: number;
  expires_at: string;
}

export function ArchivePage() {
  const { currentTenant } = useAuth();
  return <ArchiveWorkspace key={currentTenant?.id ?? 0} />;
}

function ArchiveWorkspace() {
  const { t } = useTranslation();
  const { currentTenant } = useAuth();
  const errorMessage = useApiErrorMessage();
  const [entries, setEntries] = useState<Entry[]>([]);
  const [content, setContent] = useState<Content | null>(null);
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [fromAddr, setFromAddr] = useState("");
  const [mailboxID, setMailboxID] = useState("");
  const [afterTime, setAfterTime] = useState("");
  const [beforeTime, setBeforeTime] = useState("");
  const [filters, setFilters] = useState("");
  const [trashed, setTrashed] = useState(false);
  const [cursor, setCursor] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [showHTML, setShowHTML] = useState(false);
  const [offset, setOffset] = useState(0);
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [days, setDays] = useState("365");
  const [policy, setPolicy] = useState<{
    active: boolean;
    retention_days?: number;
    configured_active?: boolean;
    can_manage?: boolean;
  } | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [deleting, setDeleting] = useState<Entry | null>(null);
  const lifetime = useRef<AbortController | null>(null);
  const mayManage =
    currentTenant?.role === "owner" || currentTenant?.role === "admin";

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => controller.abort();
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    const parameters = new URLSearchParams(filters);
    parameters.set("q", query);
    if (trashed) parameters.set("deleted", "true");
    if (cursor) parameters.set("cursor", cursor);
    const searching = Boolean(query || filters || trashed);
    const path = searching
      ? `/api/v1/archive/search?${parameters}`
      : `/api/v1/archive?limit=50&offset=${offset}`;
    void request<{ entries: Entry[]; next_cursor?: string }>(path, {
      signal: controller.signal,
    })
      .then((data) => {
        if (!controller.signal.aborted) {
          setEntries(data.entries ?? []);
          setNextCursor(data.next_cursor ?? "");
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setEntries([]);
          setNotice(errorMessage(error));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [query, filters, trashed, cursor, offset, refresh, errorMessage]);

  useEffect(() => {
    if (!mayManage) return;
    const controller = new AbortController();
    void request<{
      active: boolean;
      retention_days?: number;
      configured_active?: boolean;
      can_manage?: boolean;
    }>("/api/v1/archive/retention", { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) setPolicy(data);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) setNotice(errorMessage(error));
      });
    return () => controller.abort();
  }, [mayManage, refresh, errorMessage]);

  const act = async (operation: (signal: AbortSignal) => Promise<void>) => {
    const signal = lifetime.current?.signal;
    if (!signal || signal.aborted) return;
    setBusy(true);
    setNotice("");
    try {
      await operation(signal);
    } catch (error: unknown) {
      if (!signal.aborted) setNotice(errorMessage(error));
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  };
  const download = (path: string, filename: string) =>
    act(async (signal) => {
      const token = getSessionToken();
      const response = await fetch(path, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        credentials: "same-origin",
        signal,
      });
      if (!response.ok) {
        const parsed = apiErrorSchema.safeParse(
          await response.json().catch(() => null),
        );
        throw new ApiRequestError(
          response.status,
          parsed.success ? parsed.data : { error: { code: "REQUEST_FAILED" } },
        );
      }
      const blob = await response.blob();
      if (signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      link.click();
      URL.revokeObjectURL(url);
    });
  const applySearch = (event: FormEvent) => {
    event.preventDefault();
    try {
      const params = new URLSearchParams();
      if (fromAddr.trim()) params.set("from_addr", fromAddr.trim());
      if (mailboxID) params.set("mailbox_id", mailboxID);
      if (afterTime) params.set("after", new Date(afterTime).toISOString());
      if (beforeTime) params.set("before", new Date(beforeTime).toISOString());
      setFilters(params.toString());
      setOffset(0);
      setCursor("");
      setHistory([]);
      setQuery(search.trim());
      setContent(null);
      setShowHTML(false);
    } catch {
      setNotice(t("error.INVALID_PARAMS"));
    }
  };
  const previewRetention = (event: FormEvent) => {
    event.preventDefault();
    setPreview(null);
    setConfirmed(false);
    void act(async (signal) => {
      const data = await request<{ preview: Preview }>(
        "/api/v1/archive/retention/preview",
        { method: "POST", body: { retention_days: Number(days) }, signal },
      );
      if (!signal.aborted) setPreview(data.preview);
    });
  };

  return (
    <div className="space-y-8 max-w-6xl mx-auto">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold">{t("archive.title")}</h1>
        <p className="text-sm text-muted-foreground max-w-prose">
          {t("archive.authorization")}
        </p>
        <Link to="/audit" className="text-sm underline underline-offset-4">
          {t("archive.manage_grants")}
        </Link>
      </header>
      {notice && (
        <p role="alert" className="text-sm text-destructive break-words">
          {notice}
        </p>
      )}
      <section className="space-y-4" aria-label={t("archive.title")}>
        <form onSubmit={applySearch} className="flex flex-wrap gap-2">
          <label htmlFor="archive-search" className="sr-only">
            {t("archive.search")}
          </label>
          <Input
            id="archive-search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            maxLength={500}
            placeholder={t("archive.search")}
            className="flex-1 min-w-40"
          />
          <details className="w-full">
            <summary className="text-sm cursor-pointer">
              {t("archive.filters")}
            </summary>
            <div className="grid sm:grid-cols-2 gap-3 mt-3">
              <label className="text-sm space-y-1">
                {t("archive.from_filter")}
                <Input
                  value={fromAddr}
                  onChange={(event) => setFromAddr(event.target.value)}
                  maxLength={320}
                />
              </label>
              <label className="text-sm space-y-1">
                {t("archive.mailbox_filter")}
                <Input
                  type="number"
                  min="1"
                  step="1"
                  value={mailboxID}
                  onChange={(event) => setMailboxID(event.target.value)}
                />
              </label>
              <label className="text-sm space-y-1">
                {t("archive.after_filter")}
                <Input
                  type="datetime-local"
                  value={afterTime}
                  onChange={(event) => setAfterTime(event.target.value)}
                />
              </label>
              <label className="text-sm space-y-1">
                {t("archive.before_filter")}
                <Input
                  type="datetime-local"
                  value={beforeTime}
                  onChange={(event) => setBeforeTime(event.target.value)}
                />
              </label>
            </div>
          </details>
          <Button disabled={loading || busy}>{t("archive.search")}</Button>
          <Button
            type="button"
            variant="outline"
            disabled={loading || busy}
            onClick={() => {
              setSearch("");
              setFromAddr("");
              setMailboxID("");
              setAfterTime("");
              setBeforeTime("");
              setFilters("");
              setCursor("");
              setHistory([]);
              setQuery("");
              setOffset(0);
              setRefresh((value) => value + 1);
            }}
          >
            {t("archive.refresh")}
          </Button>
        </form>
        {mayManage && (
          <Button
            variant="outline"
            disabled={busy || loading}
            onClick={() => {
              setTrashed((value) => !value);
              setContent(null);
              setCursor("");
              setHistory([]);
              setOffset(0);
            }}
          >
            {t(trashed ? "archive.show_active" : "archive.show_deleted")}
          </Button>
        )}
        {trashed && (
          <p className="text-sm text-muted-foreground">
            {t("archive.restore_rule")}
          </p>
        )}
        {loading ? (
          <p role="status">{t("state.loading")}</p>
        ) : entries.length === 0 ? (
          <p className="text-muted-foreground">{t("archive.empty")}</p>
        ) : (
          <ul className="divide-y">
            {entries.map((entry) => (
              <li
                key={entry.id}
                className="py-4 flex flex-wrap justify-between gap-3"
              >
                <div className="min-w-0 flex-1">
                  <h2 className="font-medium break-words">
                    {entry.subject || entry.message_id}
                  </h2>
                  <p className="text-sm text-muted-foreground break-all">
                    {entry.from_addr} ·{" "}
                    {new Date(entry.received_at).toLocaleString()} ·{" "}
                    {entry.size_bytes.toLocaleString()} {t("archive.bytes")}
                  </p>
                  {entry.legal_hold && (
                    <p className="text-sm font-medium">{t("archive.held")}</p>
                  )}
                </div>
                {mayManage && (
                  <div className="flex flex-wrap items-start gap-2">
                    {trashed ? (
                      <Button
                        size="sm"
                        disabled={busy}
                        onClick={() =>
                          void act(async (signal) => {
                            await request(
                              `/api/v1/archive/${entry.id}/restore`,
                              { method: "POST", signal },
                            );
                            if (!signal.aborted) {
                              setNotice(t("archive.restore_rule"));
                              setRefresh((value) => value + 1);
                            }
                          })
                        }
                      >
                        {t("archive.restore")}
                      </Button>
                    ) : (
                      <>
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy}
                          onClick={() =>
                            void act(async (signal) => {
                              const data = await request<Content>(
                                `/api/v1/archive/${entry.id}/content`,
                                { signal },
                              );
                              if (!signal.aborted) setContent(data);
                            })
                          }
                        >
                          {t("archive.preview_content")}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy}
                          onClick={() =>
                            void download(
                              `/api/v1/archive/${entry.id}/export`,
                              `archive-${entry.id}.eml`,
                            )
                          }
                        >
                          {t("archive.export")}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy}
                          onClick={() =>
                            void act(async (signal) => {
                              await request(
                                `/api/v1/archive/${entry.id}/${entry.legal_hold ? "release" : "hold"}`,
                                { method: "POST", signal },
                              );
                              if (!signal.aborted) {
                                setContent(null);
                                setRefresh((value) => value + 1);
                              }
                            })
                          }
                        >
                          {t(
                            entry.legal_hold
                              ? "archive.release_hold"
                              : "archive.hold",
                          )}
                        </Button>
                        <Button
                          size="sm"
                          variant="destructive"
                          disabled={busy || entry.legal_hold}
                          onClick={() => setDeleting(entry)}
                        >
                          {t("archive.delete")}
                        </Button>{" "}
                      </>
                    )}
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
        <div className="flex gap-2">
          <Button
            variant="outline"
            disabled={
              loading ||
              (query || filters || trashed
                ? history.length === 0
                : offset === 0)
            }
            onClick={() => {
              if (query || filters || trashed) {
                setCursor(history[history.length - 1] ?? "");
                setHistory((value) => value.slice(0, -1));
              } else setOffset((value) => Math.max(0, value - 50));
              setContent(null);
            }}
          >
            {t("archive.previous")}
          </Button>
          <Button
            variant="outline"
            disabled={
              loading ||
              (query || filters || trashed ? !nextCursor : entries.length < 50)
            }
            onClick={() => {
              if (query || filters || trashed) {
                setHistory((value) => [...value, cursor]);
                setCursor(nextCursor);
              } else setOffset((value) => value + 50);
              setContent(null);
            }}
          >
            {t("archive.next")}
          </Button>
        </div>
      </section>
      {content && (
        <section
          aria-label={t("archive.preview_content")}
          className="border-t pt-6 space-y-4"
        >
          <div className="flex justify-between gap-3">
            <h2 className="text-lg font-semibold">
              {t("archive.preview_content")}
            </h2>
            <Button variant="ghost" onClick={() => setContent(null)}>
              {t("archive.close")}
            </Button>
          </div>
          {content.body_html && (
            <Button
              variant="outline"
              onClick={() => setShowHTML((value) => !value)}
            >
              {t(showHTML ? "archive.show_plain_text" : "archive.safe_html")}
            </Button>
          )}
          {showHTML && content.body_html ? (
            <iframe
              title={t("archive.safe_html")}
              sandbox=""
              referrerPolicy="no-referrer"
              className="w-full min-h-80 border rounded-md bg-white"
              srcDoc={`<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'none'; style-src 'unsafe-inline'; connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'"></head><body>${content.body_html}</body></html>`}
            />
          ) : (
            <pre className="whitespace-pre-wrap break-words text-sm font-sans max-w-prose">
              {content.body_text || t("archive.no_plain_text")}
            </pre>
          )}
          {content.extraction_error && (
            <p className="text-sm text-destructive">
              {t("archive.extraction_incomplete")}
            </p>
          )}
          <ul className="space-y-2">
            {content.attachments.map((attachment) => (
              <li
                key={attachment.index}
                className="flex flex-wrap items-center justify-between gap-3"
              >
                <span className="text-sm break-all">
                  {attachment.name} · {attachment.size.toLocaleString()}{" "}
                  {t("archive.bytes")}
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() =>
                    void download(
                      `/api/v1/archive/${content.id}/attachments/${attachment.index}`,
                      `attachment-${attachment.index}.bin`,
                    )
                  }
                >
                  {t("archive.download_attachment")}
                </Button>
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground break-all">
            SHA-256: {content.raw_sha256}
          </p>
        </section>
      )}
      {mayManage && (
        <section className="border-t pt-6 space-y-4">
          <h2 className="text-lg font-semibold">{t("archive.retention")}</h2>
          <p className="text-sm text-muted-foreground max-w-prose">
            {t("archive.retention_rule")} {t("archive.retention_authority")}
          </p>
          {policy && (
            <p className="text-sm">
              {t(
                policy.configured_active && !policy.active
                  ? "archive.retention_authorization_paused"
                  : policy.active
                    ? "archive.retention_active"
                    : "archive.retention_permanent",
                { days: policy.retention_days },
              )}
            </p>
          )}
          {policy?.configured_active && (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() =>
                void act(async (signal) => {
                  await request("/api/v1/archive/retention", {
                    method: "DELETE",
                    signal,
                  });
                  if (!signal.aborted) {
                    setNotice(t("archive.retention_disabled"));
                    setRefresh((value) => value + 1);
                    setPreview(null);
                  }
                })
              }
            >
              {t("archive.disable_retention")}
            </Button>
          )}

          <form
            onSubmit={previewRetention}
            className="flex flex-wrap items-end gap-3"
          >
            <div className="space-y-2">
              <label htmlFor="retention-days" className="text-sm">
                {t("archive.retention_days")}
              </label>
              <Input
                id="retention-days"
                type="number"
                min="1"
                max="36500"
                step="1"
                required
                value={days}
                onChange={(event) => {
                  setDays(event.target.value);
                  setPreview(null);
                  setConfirmed(false);
                }}
              />
            </div>
            <Button disabled={busy || !policy?.can_manage}>
              {t("archive.preview_retention")}
            </Button>
          </form>
          {preview && (
            <div className="space-y-3">
              <p>
                {t("archive.retention_candidates", {
                  count: preview.candidate_count,
                })}
              </p>
              <p className="text-sm">
                {t("archive.preview_expires", {
                  time: new Date(preview.expires_at).toLocaleString(),
                })}
              </p>
              <label className="flex gap-2 items-start text-sm">
                <input
                  type="checkbox"
                  checked={confirmed}
                  onChange={(event) => setConfirmed(event.target.checked)}
                  className="mt-1"
                />
                {t("archive.confirm_retention")}
              </label>
              <Button
                variant="destructive"
                disabled={
                  busy ||
                  !confirmed ||
                  new Date(preview.expires_at).getTime() <= Date.now()
                }
                onClick={() =>
                  void act(async (signal) => {
                    await request("/api/v1/archive/retention/confirm", {
                      method: "POST",
                      body: { preview_id: preview.id, confirm: true },
                      signal,
                    });
                    if (!signal.aborted) {
                      setPreview(null);
                      setConfirmed(false);
                      setNotice(t("archive.retention_activated"));
                      setContent(null);
                      setRefresh((value) => value + 1);
                    }
                  })
                }
              >
                {t("archive.activate_retention")}
              </Button>
            </div>
          )}
        </section>
      )}
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={t("archive.delete")}
        description={t("archive.delete_rule")}
        destructive
        onConfirm={() =>
          act(async (signal) => {
            if (!deleting) return;
            await request(`/api/v1/archive/${deleting.id}`, {
              method: "DELETE",
              signal,
            });
            if (!signal.aborted) {
              setDeleting(null);
              setContent(null);
              setRefresh((value) => value + 1);
            }
          })
        }
      />
    </div>
  );
}
