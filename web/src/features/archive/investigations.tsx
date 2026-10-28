import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiRequestError, getSessionToken, request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";

type CaseRow = {
  id: number;
  title: string;
  purpose: string;
  version: number;
  status: string;
  expires_at: string;
  created_by: number;
};
type Member = { user_id: number; email: string; removed_at: string | null };
type Source = {
  id: number;
  subject: string | null;
  size_bytes: number;
  raw_sha256: string;
  legal_hold: boolean;
  deleted: boolean;
};
type Approval = {
  id: number;
  requester_user_id: number;
  approver_user_id: number | null;
  state: string;
  expires_at: string;
  source_digest: string;
  stale: boolean;
  manifest_sha256: string;
  decision_reason: string;
};
type Detail = {
  case: CaseRow;
  members: Member[];
  sources: Source[];
  approvals: Approval[];
  source_digest: string;
  actor_id: number;
  can_manage: boolean;
  can_export: boolean;
};
const endpoint = "/api/v1/archive/cases";
const selectStyle =
  "h-10 min-w-0 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";

export function ArchiveInvestigations({
  archiveRevision = 0,
}: {
  archiveRevision?: number;
}) {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();
  const [cases, setCases] = useState<CaseRow[]>([]);
  const [eligible, setEligible] = useState<Member[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [loading, setLoading] = useState(true);
  const [authorized, setAuthorized] = useState(false);
  const [busy, setBusy] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const downloadController = useRef<AbortController | null>(null);
  useEffect(() => () => downloadController.current?.abort(), []);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [revision, setRevision] = useState(0);
  const [requestKey, setRequestKey] = useState(() => crypto.randomUUID());
  const [title, setTitle] = useState("");
  const [purpose, setPurpose] = useState("");
  const [expiry, setExpiry] = useState(() =>
    new Date(Date.now() + 30 * 86400000).toISOString().slice(0, 10),
  );
  const [memberID, setMemberID] = useState("");
  const [search, setSearch] = useState("");
  const [matches, setMatches] = useState<Source[]>([]);
  const [entryID, setEntryID] = useState("");
  const [reasons, setReasons] = useState<Record<number, string>>({});
  const [confirmExport, setConfirmExport] = useState<Record<number, boolean>>(
    {},
  );

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    Promise.all([
      request<{ cases: CaseRow[] }>(endpoint, { signal: controller.signal }),
      request<{ members: Member[] }>(`${endpoint}/eligible-members`, {
        signal: controller.signal,
      }),
    ])
      .then(([list, members]) => {
        setCases(list.cases);
        setEligible(members.members);
        setAuthorized(true);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(err));
          if (
            err instanceof ApiRequestError &&
            (err.status === 401 || err.status === 403)
          ) {
            setAuthorized(false);
            setCases([]);
            setEligible([]);
            setDetail(null);
            setSelected(null);
          }
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [revision, archiveRevision, errorMessage]);

  useEffect(() => {
    if (selected === null) {
      setDetail(null);
      return;
    }
    const controller = new AbortController();
    setDetail(null);
    request<Detail>(`${endpoint}/${selected}`, { signal: controller.signal })
      .then(setDetail)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [selected, revision, archiveRevision, errorMessage]);

  const perform = useCallback(
    async (work: () => Promise<void>) => {
      setBusy(true);
      setError("");
      setNotice("");
      try {
        await work();
        setNotice(t("archive.case.saved"));
        setRevision((n) => n + 1);
      } catch (err) {
        if (err instanceof DOMException && err.name === "AbortError")
          setNotice(t("archive.case.cancelled"));
        else setError(errorMessage(err));
      } finally {
        setBusy(false);
      }
    },
    [t, errorMessage],
  );
  const mutate = (
    suffix: string,
    body: unknown,
    method: "POST" | "DELETE" = "POST",
  ) =>
    perform(async () => {
      await request(`${endpoint}/${selected}${suffix}`, { method, body });
    });
  const create = (event: FormEvent) => {
    event.preventDefault();
    void perform(async () => {
      const result = await request<{ id: number }>(endpoint, {
        method: "POST",
        body: {
          title,
          purpose,
          expires_at: new Date(expiry + "T23:59:59Z").toISOString(),
          request_key: requestKey,
        },
      });
      setSelected(result.id);
      setTitle("");
      setPurpose("");
      setRequestKey(crypto.randomUUID());
    });
  };
  const findSources = (event: FormEvent) => {
    event.preventDefault();
    void perform(async () => {
      const data = await request<{ entries: Source[] }>(
        `/api/v1/archive/search?q=${encodeURIComponent(search)}`,
      );
      setMatches(data.entries);
      setEntryID("");
    });
  };
  const download = (approval: Approval) =>
    perform(async () => {
      const controller = new AbortController();
      downloadController.current = controller;
      setDownloading(true);
      try {
        const response = await fetch(
          `${endpoint}/${selected}/approvals/${approval.id}/package`,
          {
            headers: { Authorization: `Bearer ${getSessionToken() ?? ""}` },
            signal: controller.signal,
          },
        );
        if (!response.ok) {
          const body = await response.json();
          throw new ApiRequestError(response.status, body);
        }
        if (
          response.headers.get("X-Manifest-SHA256") !== approval.manifest_sha256
        )
          throw new Error("evidence manifest mismatch");
        const blob = await response.blob();
        const url = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.href = url;
        link.download = `evidence-case-${selected}-approval-${approval.id}.zip`;
        document.body.appendChild(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 0);
      } finally {
        downloadController.current = null;
        setDownloading(false);
      }
    });

  const open =
    detail?.case.status === "open" &&
    new Date(detail.case.expires_at).getTime() > Date.now();
  return (
    <section
      className="space-y-5 border-t pt-6"
      aria-labelledby="archive-cases-title"
    >
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <h2 id="archive-cases-title" className="text-xl font-semibold">
            {t("archive.case.title")}
          </h2>
          <p className="max-w-prose text-sm text-muted-foreground">
            {t("archive.case.description")}
          </p>
        </div>
        <Button
          variant="outline"
          disabled={busy || loading}
          onClick={() => {
            setError("");
            setRevision((n) => n + 1);
          }}
        >
          {t("archive.case.refresh")}
        </Button>
      </header>
      {error && (
        <p role="alert" className="break-words text-sm text-destructive">
          {error}
        </p>
      )}
      {downloading && (
        <Button
          variant="outline"
          onClick={() => downloadController.current?.abort()}
        >
          {t("archive.case.cancel_download")}
        </Button>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      <details>
        <summary className="cursor-pointer text-sm font-medium">
          {t("archive.case.create")}
        </summary>
        <form
          onSubmit={create}
          className="mt-3 grid gap-3 sm:grid-cols-2"
          aria-label={t("archive.case.create")}
        >
          <label className="space-y-1 text-sm">
            {t("archive.case.name")}
            <Input
              name="case_title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              maxLength={200}
              required
              disabled={busy}
            />
          </label>
          <label className="space-y-1 text-sm">
            {t("archive.case.expires")}
            <Input
              name="case_expiry"
              type="date"
              value={expiry}
              onChange={(e) => setExpiry(e.target.value)}
              required
              disabled={busy}
            />
          </label>
          <label className="space-y-1 text-sm sm:col-span-2">
            {t("archive.case.purpose")}
            <textarea
              name="case_purpose"
              className="min-h-20 w-full rounded-md border border-input bg-background p-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={purpose}
              onChange={(e) => setPurpose(e.target.value)}
              maxLength={2000}
              required
              disabled={busy}
            />
          </label>
          <Button
            type="submit"
            disabled={busy || !authorized || !title.trim() || !purpose.trim()}
            className="justify-self-start"
          >
            {t("archive.case.create")}
          </Button>
        </form>
      </details>
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(12rem,1fr)_minmax(0,3fr)]">
        <nav aria-label={t("archive.case.choose")}>
          {loading ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t("archive.case.loading")}
            </p>
          ) : cases.length === 0 ? (
            <p className="max-w-prose text-sm text-muted-foreground">
              {t("archive.case.empty")}
            </p>
          ) : (
            <ul className="divide-y">
              {cases.map((item) => (
                <li key={item.id}>
                  <button
                    type="button"
                    disabled={busy}
                    className="w-full rounded-sm px-2 py-3 text-left text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
                    aria-current={selected === item.id ? "page" : undefined}
                    onClick={() => {
                      setSelected(item.id);
                      setError("");
                      setNotice("");
                    }}
                  >
                    <span className="block break-words font-medium">
                      {item.title}
                    </span>
                    <span className="text-muted-foreground">
                      {t(`archive.case.status.${item.status}`)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </nav>
        {selected !== null && !detail && !error && (
          <p role="status" className="text-sm">
            {t("archive.case.loading")}
          </p>
        )}
        {detail && (
          <div
            className="min-w-0 space-y-6"
            aria-label={t("archive.case.detail")}
          >
            <header className="space-y-2">
              <h3 className="break-words text-lg font-semibold">
                {detail.case.title}
              </h3>
              <p className="max-w-prose whitespace-pre-wrap break-words text-sm">
                {detail.case.purpose}
              </p>
              <p className="text-sm text-muted-foreground">
                {t(`archive.case.status.${detail.case.status}`)} ·{" "}
                {t("archive.case.version", { version: detail.case.version })} ·{" "}
                {new Date(detail.case.expires_at).toLocaleDateString()}
              </p>
            </header>
            <section className="space-y-3" aria-labelledby="case-members-title">
              <h4 id="case-members-title" className="font-medium">
                {t("archive.case.members")}
              </h4>
              <ul className="divide-y">
                {detail.members.map((member) => (
                  <li
                    key={member.user_id}
                    className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm"
                  >
                    <span className="break-all">
                      {member.email}
                      {member.removed_at && ` · ${t("archive.case.removed")}`}
                    </span>
                    {detail.can_manage &&
                      !member.removed_at &&
                      member.user_id !== detail.case.created_by && (
                        <Button
                          variant="outline"
                          disabled={busy}
                          onClick={() =>
                            void mutate(
                              `/members/${member.user_id}`,
                              { version: detail.case.version },
                              "DELETE",
                            )
                          }
                        >
                          {t("archive.case.remove_member")}
                        </Button>
                      )}
                  </li>
                ))}
              </ul>
              {detail.can_manage && (
                <form
                  aria-label={t("archive.case.add_member")}
                  className="flex flex-wrap gap-2"
                  onSubmit={(e) => {
                    e.preventDefault();
                    void mutate("/members", {
                      user_id: Number(memberID),
                      version: detail.case.version,
                    });
                  }}
                >
                  <label className="sr-only" htmlFor="case-member">
                    {t("archive.case.add_member")}
                  </label>
                  <select
                    id="case-member"
                    name="case_member"
                    value={memberID}
                    onChange={(e) => setMemberID(e.target.value)}
                    className={selectStyle + " flex-1"}
                    disabled={busy}
                    required
                  >
                    <option value="">{t("archive.case.choose_member")}</option>
                    {eligible
                      .filter(
                        (m) =>
                          !detail.members.some(
                            (current) =>
                              current.user_id === m.user_id &&
                              !current.removed_at,
                          ),
                      )
                      .map((m) => (
                        <option key={m.user_id} value={m.user_id}>
                          {m.email}
                        </option>
                      ))}
                  </select>
                  <Button type="submit" disabled={busy || !memberID}>
                    {t("archive.case.add_member")}
                  </Button>
                </form>
              )}
              <p className="max-w-prose text-xs text-muted-foreground">
                {t("archive.case.member_help")}
              </p>
            </section>
            <section className="space-y-3" aria-labelledby="case-sources-title">
              <h4 id="case-sources-title" className="font-medium">
                {t("archive.case.sources")}
              </h4>
              {detail.sources.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {t("archive.case.no_sources")}
                </p>
              ) : (
                <ul className="divide-y">
                  {detail.sources.map((source) => (
                    <li
                      key={source.id}
                      className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm"
                    >
                      <span className="min-w-0 break-words">
                        {source.subject ?? t("archive.case.untitled")} · #
                        {source.id} ·{" "}
                        {t("archive.case.bytes", { count: source.size_bytes })}
                        {source.legal_hold && ` · ${t("archive.case.held")}`}
                        {source.deleted && ` · ${t("archive.case.deleted")}`}
                      </span>
                      {open && (
                        <Button
                          variant="outline"
                          disabled={busy}
                          onClick={() =>
                            void mutate(
                              `/sources/${source.id}`,
                              { version: detail.case.version },
                              "DELETE",
                            )
                          }
                        >
                          {t("archive.case.remove_source")}
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {open && (
                <div className="space-y-2">
                  <form
                    aria-label={t("archive.case.search_sources")}
                    className="flex flex-wrap gap-2"
                    onSubmit={findSources}
                  >
                    <label className="sr-only" htmlFor="case-search">
                      {t("archive.case.search_sources")}
                    </label>
                    <Input
                      id="case-search"
                      name="case_search"
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                      maxLength={500}
                      disabled={busy}
                      className="min-w-40 flex-1"
                    />
                    <Button variant="outline" type="submit" disabled={busy}>
                      {t("archive.case.search_sources")}
                    </Button>
                  </form>
                  {matches.length > 0 && (
                    <form
                      aria-label={t("archive.case.add_source")}
                      className="flex flex-wrap gap-2"
                      onSubmit={(e) => {
                        e.preventDefault();
                        void mutate("/sources", {
                          entry_id: Number(entryID),
                          version: detail.case.version,
                        });
                      }}
                    >
                      <label className="sr-only" htmlFor="case-source">
                        {t("archive.case.add_source")}
                      </label>
                      <select
                        id="case-source"
                        name="case_source"
                        className={selectStyle + " flex-1"}
                        value={entryID}
                        onChange={(e) => setEntryID(e.target.value)}
                        disabled={busy}
                        required
                      >
                        <option value="">
                          {t("archive.case.choose_source")}
                        </option>
                        {matches
                          .filter(
                            (m) => !detail.sources.some((s) => s.id === m.id),
                          )
                          .map((m) => (
                            <option key={m.id} value={m.id}>
                              #{m.id} {m.subject ?? t("archive.case.untitled")}
                            </option>
                          ))}
                      </select>
                      <Button type="submit" disabled={busy || !entryID}>
                        {t("archive.case.add_source")}
                      </Button>
                    </form>
                  )}
                </div>
              )}
            </section>
            <section
              className="space-y-3"
              aria-labelledby="case-approvals-title"
            >
              <h4 id="case-approvals-title" className="font-medium">
                {t("archive.case.approvals")}
              </h4>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t("archive.case.approval_help")}
              </p>
              {open && detail.can_export && (
                <Button
                  disabled={busy || detail.sources.length === 0}
                  onClick={() =>
                    void mutate("/approvals", {
                      version: detail.case.version,
                      digest: detail.source_digest,
                    })
                  }
                >
                  {t("archive.case.request_approval")}
                </Button>
              )}
              {!detail.can_export && (
                <p className="text-sm text-muted-foreground">
                  {t("archive.case.export_grant_required")}
                </p>
              )}
              <ul className="space-y-4">
                {detail.approvals.map((approval) => (
                  <li
                    key={approval.id}
                    className="space-y-3 border-t pt-4"
                    data-approval-id={approval.id}
                  >
                    <div className="flex flex-wrap justify-between gap-2 text-sm">
                      <span className="font-medium">
                        #{approval.id} ·{" "}
                        {t(`archive.case.approval_state.${approval.state}`)}
                      </span>
                      <span>
                        {t("archive.case.requester", {
                          id: approval.requester_user_id,
                        })}
                      </span>
                    </div>
                    {approval.stale && (
                      <p role="status" className="text-sm text-destructive">
                        {t("archive.case.stale")}
                      </p>
                    )}
                    {approval.decision_reason && (
                      <p className="max-w-prose break-words text-sm">
                        {approval.decision_reason}
                      </p>
                    )}
                    {open &&
                      !approval.stale &&
                      detail.can_export &&
                      approval.state === "pending" && (
                        <div className="space-y-2">
                          <label className="block space-y-1 text-sm">
                            {t("archive.case.reason")}
                            <Input
                              name={`approval_reason_${approval.id}`}
                              value={reasons[approval.id] ?? ""}
                              onChange={(e) =>
                                setReasons((old) => ({
                                  ...old,
                                  [approval.id]: e.target.value,
                                }))
                              }
                              maxLength={2000}
                              disabled={busy}
                            />
                          </label>
                          <div className="flex flex-wrap gap-2">
                            {approval.requester_user_id === detail.actor_id ? (
                              <Button
                                variant="outline"
                                disabled={busy || !reasons[approval.id]?.trim()}
                                onClick={() =>
                                  void mutate(
                                    `/approvals/${approval.id}/withdraw`,
                                    { reason: reasons[approval.id] },
                                  )
                                }
                              >
                                {t("archive.case.withdraw")}
                              </Button>
                            ) : (
                              <>
                                <Button
                                  disabled={
                                    busy || !reasons[approval.id]?.trim()
                                  }
                                  onClick={() =>
                                    void mutate(
                                      `/approvals/${approval.id}/decision`,
                                      {
                                        decision: "approved",
                                        digest: approval.source_digest,
                                        reason: reasons[approval.id],
                                      },
                                    )
                                  }
                                >
                                  {t("archive.case.approve")}
                                </Button>
                                <Button
                                  variant="outline"
                                  disabled={
                                    busy || !reasons[approval.id]?.trim()
                                  }
                                  onClick={() =>
                                    void mutate(
                                      `/approvals/${approval.id}/decision`,
                                      {
                                        decision: "rejected",
                                        digest: approval.source_digest,
                                        reason: reasons[approval.id],
                                      },
                                    )
                                  }
                                >
                                  {t("archive.case.reject")}
                                </Button>
                              </>
                            )}
                          </div>
                        </div>
                      )}
                    {open &&
                      !approval.stale &&
                      detail.can_export &&
                      approval.state === "approved" && (
                        <div className="space-y-3">
                          {!approval.manifest_sha256 &&
                            approval.requester_user_id === detail.actor_id && (
                              <>
                                <label className="flex items-start gap-2 text-sm">
                                  <input
                                    type="checkbox"
                                    name={`confirm_export_${approval.id}`}
                                    checked={
                                      confirmExport[approval.id] ?? false
                                    }
                                    onChange={(e) =>
                                      setConfirmExport((old) => ({
                                        ...old,
                                        [approval.id]: e.target.checked,
                                      }))
                                    }
                                    disabled={busy}
                                    className="mt-1"
                                  />
                                  {t("archive.case.confirm_export")}
                                </label>
                                <Button
                                  disabled={busy || !confirmExport[approval.id]}
                                  onClick={() =>
                                    void mutate(
                                      `/approvals/${approval.id}/evidence`,
                                      {
                                        digest: approval.source_digest,
                                        confirm: true,
                                      },
                                    )
                                  }
                                >
                                  {t("archive.case.prepare")}
                                </Button>
                              </>
                            )}
                          {approval.manifest_sha256 && (
                            <>
                              <p
                                className="break-all text-xs font-mono"
                                data-manifest-sha256={approval.manifest_sha256}
                              >
                                {t("archive.case.manifest_sha")}{" "}
                                {approval.manifest_sha256}
                              </p>
                              <Button
                                disabled={busy}
                                onClick={() => void download(approval)}
                              >
                                {t("archive.case.download")}
                              </Button>
                              <p className="max-w-prose text-sm text-muted-foreground">
                                {t("archive.case.verify_help")}
                              </p>
                              <code className="block overflow-x-auto rounded-md bg-muted p-3 text-xs">
                                urmailctl evidence verify --manifest-sha256{" "}
                                {approval.manifest_sha256} package.zip
                              </code>
                            </>
                          )}
                        </div>
                      )}
                  </li>
                ))}
              </ul>
            </section>
            {detail.can_manage && (
              <details className="border-t pt-4">
                <summary className="cursor-pointer text-sm font-medium">
                  {t("archive.case.close")}
                </summary>
                <p className="my-3 max-w-prose text-sm text-muted-foreground">
                  {t("archive.case.close_help")}
                </p>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    void mutate("/close", { version: detail.case.version })
                  }
                >
                  {t("archive.case.close_confirm")}
                </Button>
              </details>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
