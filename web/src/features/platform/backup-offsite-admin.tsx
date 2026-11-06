import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiRequestError, request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
// Classic Vellum & Ink design tokens (03-DESIGN-SYSTEM-AND-STYLES.md §2).
import "@/features/webmail-next/theme-vellum.css";

/**
 * FE-03: platform admin form for offsite backup destinations.
 *
 * API contract (frozen): /Users/tiger/OpenBot/Shared/be04-api-contract-draft.md
 * - GET/PUT /api/v1/platform/backup/offsite, POST .../test-connection.
 * - Credentials are write-only: responses only carry has_credentials /
 *   credential_summary presence booleans, so the form never back-fills or
 *   echoes secrets. Disabling keeps the stored ciphertext (re-enabling does
 *   not require re-entering credentials). PUT replaces the whole destination;
 *   `{"type": ""}` clears it explicitly. An unconfigured destination is a
 *   normal, explicit disabled state — not an error.
 */

export type BackupProtocol = "s3" | "sftp" | "webdav";

export interface BackupOffsiteStatus {
  configured: boolean;
  enabled: boolean;
  type?: BackupProtocol | "";
  endpoint?: string;
  bucket?: string;
  prefix?: string;
  age_recipient?: string;
  has_credentials: boolean;
  credential_summary: Record<string, boolean>;
}

export interface DestinationInput {
  type: BackupProtocol | "";
  endpoint: string;
  bucket?: string;
  prefix?: string;
  enabled: boolean;
  age_recipient?: string;
  credentials?: Record<string, unknown>;
}

type CredentialDraft = {
  access_key_id: string;
  secret_key: string;
  region: string;
  sftp_user: string;
  sftp_password: string;
  private_key_path: string;
  host_key_fingerprint: string;
  webdav_username: string;
  webdav_password: string;
};

const EMPTY_DRAFT: CredentialDraft = {
  access_key_id: "",
  secret_key: "",
  region: "",
  sftp_user: "",
  sftp_password: "",
  private_key_path: "",
  host_key_fingerprint: "",
  webdav_username: "",
  webdav_password: "",
};

const UNCONFIGURED_STATUS: BackupOffsiteStatus = {
  configured: false,
  enabled: false,
  has_credentials: false,
  credential_summary: {},
};

const selectClass = "h-10 rounded-md border bg-background px-3 text-sm";
const hintClass = "text-xs leading-4 text-muted-foreground";
// Host key fingerprints are pinned as SHA256:<44 base64 chars> per contract.
const SHA256_FINGERPRINT = /^SHA256:[A-Za-z0-9+/]{43}=?$/;
const ERROR_PREFIX = "platform_backup_offsite";

type FormFields = {
  endpoint: string;
  bucket: string;
  prefix: string;
  age_recipient: string;
  enabled: boolean;
};

type BuildOk = { ok: true; input: DestinationInput };
type BuildFail = { ok: false; errors: string[] };

function protocolFromStatus(status: BackupOffsiteStatus): BackupProtocol | "" {
  return status.configured &&
    (status.type === "s3" || status.type === "sftp" || status.type === "webdav")
    ? status.type
    : "";
}

/** Credentials of the selected protocol count as "provided" when non-empty. */
export function credentialsProvided(
  protocol: BackupProtocol | "",
  draft: CredentialDraft,
): boolean {
  switch (protocol) {
    case "s3":
      return (
        draft.access_key_id.trim() !== "" ||
        draft.secret_key.trim() !== "" ||
        draft.region.trim() !== ""
      );
    case "sftp":
      return (
        draft.sftp_user.trim() !== "" ||
        draft.sftp_password.trim() !== "" ||
        draft.private_key_path.trim() !== "" ||
        draft.host_key_fingerprint.trim() !== ""
      );
    case "webdav":
      return (
        draft.webdav_username.trim() !== "" ||
        draft.webdav_password.trim() !== ""
      );
    default:
      return false;
  }
}

/** Assemble the write-only credentials object for the selected protocol. */
export function buildCredentials(
  protocol: BackupProtocol,
  draft: CredentialDraft,
): Record<string, unknown> | undefined {
  if (protocol === "s3") {
    const s3: Record<string, string> = {};
    if (draft.access_key_id.trim() !== "")
      s3.access_key_id = draft.access_key_id.trim();
    if (draft.secret_key.trim() !== "") s3.secret_key = draft.secret_key.trim();
    if (draft.region.trim() !== "") s3.region = draft.region.trim();
    return Object.keys(s3).length > 0 ? { s3 } : undefined;
  }
  if (protocol === "sftp") {
    const sftp: Record<string, string> = {};
    if (draft.sftp_password.trim() !== "")
      sftp.password = draft.sftp_password.trim();
    if (draft.private_key_path.trim() !== "")
      sftp.private_key_path = draft.private_key_path.trim();
    if (draft.host_key_fingerprint.trim() !== "")
      sftp.host_key_fingerprint = draft.host_key_fingerprint.trim();
    const user = draft.sftp_user.trim();
    if (Object.keys(sftp).length === 0 && user === "") return undefined;
    return { ...(user !== "" ? { sftp_user: user } : {}), sftp: sftp };
  }
  const username = draft.webdav_username.trim();
  const password = draft.webdav_password.trim();
  if (username === "" && password === "") return undefined;
  return { username, password };
}

function parseWebdavEndpoint(endpoint: string): URL | null {
  let url: URL;
  try {
    url = new URL(endpoint);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") return null;
  // http(s)://host[:port] only — no embedded credentials, no path.
  if (url.username !== "" || url.password !== "") return null;
  if (url.pathname !== "/" && url.pathname !== "") return null;
  return url;
}

/**
 * Local (pre-save) validation + input assembly per the frozen contract.
 * When the destination already stores credentials, empty credential fields
 * mean "retain the stored ciphertext"; otherwise every required credential
 * must be provided inline.
 */
export function buildDestinationInput(
  protocol: BackupProtocol | "",
  fields: FormFields,
  draft: CredentialDraft,
  hasStoredCredentials: boolean,
): BuildOk | BuildFail {
  if (protocol === "") {
    return {
      ok: true,
      input: { type: "", endpoint: "", enabled: false },
    };
  }
  const errors: string[] = [];
  const endpoint = fields.endpoint.trim();
  const bucket = fields.bucket.trim();
  const prefix = fields.prefix.trim();
  const age = fields.age_recipient.trim();

  if (endpoint.length > 300 || bucket.length > 255 || prefix.length > 300) {
    errors.push(`${ERROR_PREFIX}.validation.too_long`);
  }
  if (age.length > 200) {
    errors.push(`${ERROR_PREFIX}.validation.age_too_long`);
  }

  if (protocol === "s3") {
    if (bucket === "")
      errors.push(`${ERROR_PREFIX}.validation.required_bucket`);
    if (endpoint !== "") {
      let url: URL | null = null;
      try {
        url = new URL(endpoint);
      } catch {
        url = null;
      }
      if (!url || (url.protocol !== "https:" && url.protocol !== "http:")) {
        errors.push(`${ERROR_PREFIX}.validation.endpoint_invalid`);
      }
    }
  } else if (protocol === "sftp") {
    if (endpoint === "")
      errors.push(`${ERROR_PREFIX}.validation.required_endpoint`);
    else if (
      /^[a-z][a-z0-9+.-]*:\/\//i.test(endpoint) ||
      endpoint.includes("/")
    )
      errors.push(`${ERROR_PREFIX}.validation.endpoint_sftp_format`);
  } else if (endpoint === "") {
    errors.push(`${ERROR_PREFIX}.validation.required_endpoint`);
  } else if (!parseWebdavEndpoint(endpoint)) {
    errors.push(`${ERROR_PREFIX}.validation.endpoint_webdav_format`);
  }

  if (
    protocol === "s3" &&
    (credentialsProvided("s3", draft) || !hasStoredCredentials) &&
    draft.region.trim() === ""
  ) {
    errors.push(`${ERROR_PREFIX}.validation.required_region`);
  }
  if (protocol === "sftp" && draft.host_key_fingerprint.trim() !== "") {
    const fp = draft.host_key_fingerprint.trim();
    if (!SHA256_FINGERPRINT.test(fp)) {
      errors.push(`${ERROR_PREFIX}.validation.fingerprint_invalid`);
    }
  }
  if (!hasStoredCredentials) {
    if (protocol === "s3") {
      if (draft.access_key_id.trim() === "")
        errors.push(`${ERROR_PREFIX}.validation.required_access_key`);
      if (draft.secret_key.trim() === "")
        errors.push(`${ERROR_PREFIX}.validation.required_secret_key`);
    }
    if (protocol === "sftp") {
      if (draft.sftp_user.trim() === "")
        errors.push(`${ERROR_PREFIX}.validation.required_sftp_user`);
      if (draft.host_key_fingerprint.trim() === "")
        errors.push(`${ERROR_PREFIX}.validation.required_fingerprint`);
      if (
        draft.sftp_password.trim() === "" &&
        draft.private_key_path.trim() === ""
      )
        errors.push(`${ERROR_PREFIX}.validation.required_sftp_secret`);
    }
    if (protocol === "webdav") {
      if (draft.webdav_username.trim() === "")
        errors.push(`${ERROR_PREFIX}.validation.required_username`);
      if (draft.webdav_password.trim() === "")
        errors.push(`${ERROR_PREFIX}.validation.required_password`);
    }
    if (fields.enabled) {
      errors.push(`${ERROR_PREFIX}.validation.credentials_required_for_enable`);
    }
  }

  if (errors.length > 0) return { ok: false, errors };

  const input: DestinationInput = {
    type: protocol,
    endpoint,
    enabled: fields.enabled,
  };
  if (protocol === "s3") input.bucket = bucket;
  if (prefix !== "") input.prefix = prefix;
  if (age !== "") input.age_recipient = age;
  const credentials = buildCredentials(protocol, draft);
  if (credentials) input.credentials = credentials;
  return { ok: true, input };
}

const TEST_DETAIL_COPY: Record<string, string> = {
  "credentials required to test an unsaved destination": `${ERROR_PREFIX}.test.credentials_required`,
  "destination not configured": `${ERROR_PREFIX}.test.not_configured`,
};

export function BackupOffsiteAdmin() {
  const { t } = useTranslation();
  const errorMessage = useApiErrorMessage();

  const [status, setStatus] = useState<BackupOffsiteStatus | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveNotice, setSaveNotice] = useState<{
    ok: boolean;
    key: string;
  } | null>(null);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);
  const [testState, setTestState] = useState<
    "idle" | "network" | "loading" | "success" | "failed"
  >("idle");
  const [testNotice, setTestNotice] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);
  const [protocol, setProtocol] = useState<BackupProtocol | "">("");
  const [fields, setFields] = useState<FormFields>({
    endpoint: "",
    bucket: "",
    prefix: "",
    age_recipient: "",
    enabled: false,
  });
  const [draft, setDraft] = useState<CredentialDraft>(EMPTY_DRAFT);
  const [confirmClear, setConfirmClear] = useState(false);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoadFailed(false);
    request<BackupOffsiteStatus>("/api/v1/platform/backup/offsite", {
      signal: controller.signal,
    })
      .then((data) => {
        if (controller.signal.aborted) return;
        const next = { ...UNCONFIGURED_STATUS, ...data };
        setStatus(next);
        const nextProtocol = protocolFromStatus(next);
        setProtocol(nextProtocol);
        setFields({
          endpoint: next.endpoint ?? "",
          bucket: next.bucket ?? "",
          prefix: next.prefix ?? "",
          age_recipient: next.age_recipient ?? "",
          enabled: next.enabled,
        });
        setDraft(EMPTY_DRAFT);
      })
      .catch(() => {
        if (!controller.signal.aborted) setLoadFailed(true);
      });
    return () => controller.abort();
  }, [revision]);

  const summaryEntries = useMemo(() => {
    if (!status?.configured || !status.has_credentials) return [];
    return Object.entries(status.credential_summary ?? {}).filter(
      ([key, present]) => key !== "unreadable" && present === true,
    );
  }, [status]);
  const unreadable = status?.credential_summary?.unreadable === true;

  const describeError = useCallback(
    (err: unknown): string => {
      if (err instanceof ApiRequestError) {
        switch (err.code) {
          case "INVALID_PARAMS":
            return t(`${ERROR_PREFIX}.error.invalid_params`);
          case "SECRETS_UNCONFIGURED":
            return t(`${ERROR_PREFIX}.error.secrets_unconfigured`);
          case "DB_ERROR":
            return t(`${ERROR_PREFIX}.error.db_error`);
          case "INTERNAL_ERROR":
            return t(`${ERROR_PREFIX}.error.internal_error`);
        }
      }
      return errorMessage(err);
    },
    [t, errorMessage],
  );

  const fieldsDirty = useMemo(() => {
    if (!status?.configured) return true;
    return (
      protocol !== protocolFromStatus(status) ||
      fields.endpoint.trim() !== (status.endpoint ?? "") ||
      fields.bucket.trim() !== (status.bucket ?? "") ||
      fields.prefix.trim() !== (status.prefix ?? "") ||
      fields.age_recipient.trim() !== (status.age_recipient ?? "") ||
      fields.enabled !== status.enabled
    );
  }, [status, protocol, fields]);

  function resetTest() {
    setTestState("idle");
    setTestNotice(null);
  }

  /** Validate locally, then assemble the PUT body or a test-connection body. */
  function assemble(): BuildOk | BuildFail {
    return buildDestinationInput(
      protocol,
      fields,
      draft,
      status?.has_credentials ?? false,
    );
  }

  async function handleSave() {
    if (busy) return;
    setSaveNotice(null);
    setValidationErrors([]);
    resetTest();
    const built = assemble();
    if (!built.ok) {
      setValidationErrors(built.errors);
      return;
    }
    setBusy(true);
    try {
      const next = await request<BackupOffsiteStatus>(
        "/api/v1/platform/backup/offsite",
        { method: "PUT", body: built.input },
      );
      const saved = { ...UNCONFIGURED_STATUS, ...next };
      setStatus(saved);
      setDraft(EMPTY_DRAFT);
      setProtocol(protocolFromStatus(saved));
      setFields({
        endpoint: saved.endpoint ?? "",
        bucket: saved.bucket ?? "",
        prefix: saved.prefix ?? "",
        age_recipient: saved.age_recipient ?? "",
        enabled: saved.enabled,
      });
      setSaveNotice({ ok: true, key: `${ERROR_PREFIX}.save.success` });
    } catch (e: unknown) {
      setSaveNotice({ ok: false, key: describeError(e) });
    } finally {
      setBusy(false);
    }
  }

  async function handleTestConnection() {
    if (busy) return;
    resetTest();
    setValidationErrors([]);
    const built = assemble();
    if (!built.ok) {
      setValidationErrors(built.errors);
      return;
    }
    // An unsaved destination cannot be probed without inline credentials.
    if (!status?.configured && !credentialsProvided(protocol, draft)) {
      setTestState("failed");
      setTestNotice({
        ok: false,
        text: t(`${ERROR_PREFIX}.test.credentials_required`),
      });
      return;
    }
    // Contract: empty body tests the already-saved destination.
    const body =
      status?.configured &&
      !credentialsProvided(protocol, draft) &&
      !fieldsDirty
        ? {}
        : built.input;
    setTestState("loading");
    try {
      const result = await request<{ ok: boolean; error?: string }>(
        "/api/v1/platform/backup/offsite/test-connection",
        { method: "POST", body },
      );
      if (result.ok) {
        setTestState("success");
        setTestNotice({ ok: true, text: t(`${ERROR_PREFIX}.test.success`) });
      } else {
        const detail = result.error ?? "";
        const copyKey = TEST_DETAIL_COPY[detail];
        setTestState("failed");
        setTestNotice({
          ok: false,
          text: copyKey
            ? t(copyKey)
            : t(`${ERROR_PREFIX}.test.failed_detail`, { detail }),
        });
      }
    } catch (e: unknown) {
      setTestState("failed");
      setTestNotice({ ok: false, text: describeError(e) });
    }
  }

  async function handleClear() {
    if (busy) return;
    setBusy(true);
    setSaveNotice(null);
    try {
      const next = await request<BackupOffsiteStatus>(
        "/api/v1/platform/backup/offsite",
        { method: "PUT", body: { type: "", enabled: false } },
      );
      const cleared = { ...UNCONFIGURED_STATUS, ...next };
      setStatus(cleared);
      setProtocol("");
      setFields({
        endpoint: "",
        bucket: "",
        prefix: "",
        age_recipient: "",
        enabled: false,
      });
      setDraft(EMPTY_DRAFT);
      setConfirmClear(false);
      setSaveNotice({ ok: true, key: `${ERROR_PREFIX}.clear.success` });
    } catch (e: unknown) {
      setSaveNotice({ ok: false, key: describeError(e) });
    } finally {
      setBusy(false);
    }
  }

  function setField<K extends keyof FormFields>(key: K, value: FormFields[K]) {
    setFields((prev) => ({ ...prev, [key]: value }));
    resetTest();
  }
  function setDraftField<K extends keyof CredentialDraft>(
    key: K,
    value: string,
  ) {
    setDraft((prev) => ({ ...prev, [key]: value }));
    resetTest();
  }

  const unconfigured = status !== null && !status.configured;

  return (
    <Card data-testid="platform-backup-offsite-admin">
      <CardHeader>
        <CardTitle>{t(`${ERROR_PREFIX}.title`)}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm text-muted-foreground">
          {t(`${ERROR_PREFIX}.help`)}
        </p>

        {status === null && !loadFailed && (
          <p role="status">{t("common.loading")}</p>
        )}
        {loadFailed && (
          <div
            role="alert"
            data-testid="backup-load-failed"
            className="space-y-2 text-sm"
          >
            <p>{t(`${ERROR_PREFIX}.load_failed`)}</p>
            <Button variant="outline" onClick={() => setRevision((v) => v + 1)}>
              {t(`${ERROR_PREFIX}.retry`)}
            </Button>
          </div>
        )}

        {status !== null && (
          <div
            data-testid="backup-status"
            className="rounded-md border p-3 text-sm"
          >
            {unconfigured ? (
              <div data-testid="backup-empty-state" className="space-y-1">
                <p className="font-medium">
                  {t(`${ERROR_PREFIX}.status.not_configured`)}
                </p>
                <p className="text-xs leading-4 text-muted-foreground">
                  {t(`${ERROR_PREFIX}.status.not_configured_hint`)}
                </p>
              </div>
            ) : (
              <div data-testid="backup-current-config" className="space-y-1">
                <p>
                  <span className="text-muted-foreground">
                    {t(`${ERROR_PREFIX}.status.state`)}
                  </span>{" "}
                  <span data-testid="backup-state-value">
                    {status.enabled
                      ? t(`${ERROR_PREFIX}.status.enabled`)
                      : t(`${ERROR_PREFIX}.status.disabled`)}
                  </span>
                </p>
                <p>
                  <span className="text-muted-foreground">
                    {t(`${ERROR_PREFIX}.type.label`)}
                  </span>{" "}
                  {t(`${ERROR_PREFIX}.type.${status.type}`)}
                  {status.endpoint ? ` · ${status.endpoint}` : ""}
                  {status.type === "s3" && status.bucket
                    ? ` · ${status.bucket}`
                    : ""}
                </p>
                {status.prefix ? (
                  <p className="break-all">
                    <span className="text-muted-foreground">
                      {t(`${ERROR_PREFIX}.field.prefix`)}
                    </span>{" "}
                    {status.prefix}
                  </p>
                ) : null}
                {status.age_recipient ? (
                  <p className="break-all">
                    <span className="text-muted-foreground">
                      {t(`${ERROR_PREFIX}.field.age_recipient`)}
                    </span>{" "}
                    {status.age_recipient}
                  </p>
                ) : null}
                {status.has_credentials && (
                  <p className="text-xs text-muted-foreground">
                    {t(`${ERROR_PREFIX}.credentials.retained`)}
                  </p>
                )}
              </div>
            )}
          </div>
        )}

        {saveNotice && (
          <p
            role={saveNotice.ok ? "status" : "alert"}
            data-testid="backup-save-result"
          >
            {t(saveNotice.key)}
          </p>
        )}

        {status !== null && (
          <>
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="grid gap-1 text-sm">
                {t(`${ERROR_PREFIX}.type.label`)}
                <select
                  data-testid="backup-type-select"
                  className={selectClass}
                  value={protocol}
                  disabled={busy}
                  onChange={(e) => {
                    setProtocol(e.target.value as BackupProtocol | "");
                    resetTest();
                  }}
                >
                  <option value="">
                    {t(`${ERROR_PREFIX}.type.placeholder`)}
                  </option>
                  <option value="s3">{t(`${ERROR_PREFIX}.type.s3`)}</option>
                  <option value="sftp">{t(`${ERROR_PREFIX}.type.sftp`)}</option>
                  <option value="webdav">
                    {t(`${ERROR_PREFIX}.type.webdav`)}
                  </option>
                </select>
              </label>
              <label className="grid gap-1 text-sm">
                {t(`${ERROR_PREFIX}.field.prefix`)}
                <Input
                  data-testid="backup-prefix"
                  name="prefix"
                  maxLength={300}
                  value={fields.prefix}
                  disabled={busy}
                  onChange={(e) => setField("prefix", e.target.value)}
                />
                <span className={hintClass}>
                  {t(`${ERROR_PREFIX}.field.prefix_hint`)}
                </span>
              </label>
            </div>

            {protocol !== "" && (
              <>
                <label className="grid gap-1 text-sm">
                  {t(`${ERROR_PREFIX}.field.endpoint`)}
                  <Input
                    data-testid="backup-endpoint"
                    name="endpoint"
                    maxLength={300}
                    value={fields.endpoint}
                    disabled={busy}
                    onChange={(e) => setField("endpoint", e.target.value)}
                  />
                  <span className={hintClass}>
                    {t(`${ERROR_PREFIX}.field.endpoint_${protocol}_hint`)}
                  </span>
                </label>

                {protocol === "s3" && (
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.field.bucket`)}
                    <Input
                      data-testid="backup-bucket"
                      name="bucket"
                      maxLength={255}
                      value={fields.bucket}
                      disabled={busy}
                      onChange={(e) => setField("bucket", e.target.value)}
                    />
                  </label>
                )}
              </>
            )}

            <label className="grid gap-1 text-sm">
              {t(`${ERROR_PREFIX}.field.age_recipient`)}
              <Input
                data-testid="backup-age-recipient"
                name="age_recipient"
                maxLength={200}
                value={fields.age_recipient}
                disabled={busy}
                onChange={(e) => setField("age_recipient", e.target.value)}
              />
              <span className={hintClass}>
                {t(`${ERROR_PREFIX}.field.age_recipient_hint`)}
              </span>
            </label>

            <label className="flex items-start gap-2 text-sm">
              <input
                data-testid="backup-enabled"
                type="checkbox"
                checked={fields.enabled}
                disabled={busy}
                onChange={(e) => setField("enabled", e.target.checked)}
              />
              <span>
                {t(`${ERROR_PREFIX}.field.enabled`)}
                <span className="block text-xs leading-4 text-muted-foreground">
                  {status.has_credentials
                    ? t(`${ERROR_PREFIX}.field.enabled_retain_hint`)
                    : t(`${ERROR_PREFIX}.field.enabled_hint`)}
                </span>
              </span>
            </label>

            <fieldset
              data-testid="backup-credentials"
              className="space-y-3 rounded-md border p-3"
            >
              <legend className="px-1 text-sm font-medium">
                {t(`${ERROR_PREFIX}.credentials.section_title`)}
              </legend>
              {status.configured && status.has_credentials && (
                <div className="space-y-1">
                  <div
                    data-testid="backup-credentials-summary"
                    className="flex flex-wrap gap-2"
                  >
                    {summaryEntries.map(([key]) => (
                      <span
                        key={key}
                        className="rounded border bg-muted/40 px-2 py-0.5 text-xs"
                      >
                        {t(`${ERROR_PREFIX}.credentials.summary.${key}`)}
                      </span>
                    ))}
                  </div>
                  {unreadable && (
                    <p role="alert" className="text-xs text-destructive">
                      {t(`${ERROR_PREFIX}.credentials.summary.unreadable`)}
                    </p>
                  )}
                  <p className={hintClass}>
                    {t(`${ERROR_PREFIX}.credentials.retained_hint`)}
                  </p>
                </div>
              )}
              {protocol === "s3" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.access_key_id`)}
                    <Input
                      data-testid="backup-access-key-id"
                      name="access_key_id"
                      autoComplete="off"
                      value={draft.access_key_id}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("access_key_id", e.target.value)
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.secret_key`)}
                    <Input
                      data-testid="backup-secret-key"
                      name="secret_key"
                      type="password"
                      autoComplete="new-password"
                      value={draft.secret_key}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("secret_key", e.target.value)
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.region`)}
                    <Input
                      data-testid="backup-region"
                      name="region"
                      autoComplete="off"
                      value={draft.region}
                      disabled={busy}
                      onChange={(e) => setDraftField("region", e.target.value)}
                    />
                    <span className={hintClass}>
                      {t(`${ERROR_PREFIX}.credentials.field.region_hint`)}
                    </span>
                  </label>
                </div>
              )}
              {protocol === "sftp" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.sftp_user`)}
                    <Input
                      data-testid="backup-sftp-user"
                      name="sftp_user"
                      autoComplete="off"
                      value={draft.sftp_user}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("sftp_user", e.target.value)
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.sftp_password`)}
                    <Input
                      data-testid="backup-sftp-password"
                      name="sftp_password"
                      type="password"
                      autoComplete="new-password"
                      value={draft.sftp_password}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("sftp_password", e.target.value)
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.private_key_path`)}
                    <Input
                      data-testid="backup-private-key-path"
                      name="private_key_path"
                      autoComplete="off"
                      value={draft.private_key_path}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("private_key_path", e.target.value)
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(
                      `${ERROR_PREFIX}.credentials.field.host_key_fingerprint`,
                    )}
                    <Input
                      data-testid="backup-host-key-fingerprint"
                      name="host_key_fingerprint"
                      autoComplete="off"
                      value={draft.host_key_fingerprint}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("host_key_fingerprint", e.target.value)
                      }
                    />
                    <span className={hintClass}>
                      {t(
                        `${ERROR_PREFIX}.credentials.field.host_key_fingerprint_hint`,
                      )}
                    </span>
                  </label>
                </div>
              )}
              {protocol === "webdav" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.webdav_username`)}
                    <Input
                      data-testid="backup-webdav-username"
                      name="webdav_username"
                      autoComplete="off"
                      value={draft.webdav_username}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("webdav_username", e.target.value)
                      }
                    />
                  </label>
                  <label className="grid gap-1 text-sm">
                    {t(`${ERROR_PREFIX}.credentials.field.webdav_password`)}
                    <Input
                      data-testid="backup-webdav-password"
                      name="webdav_password"
                      type="password"
                      autoComplete="new-password"
                      value={draft.webdav_password}
                      disabled={busy}
                      onChange={(e) =>
                        setDraftField("webdav_password", e.target.value)
                      }
                    />
                  </label>
                </div>
              )}
            </fieldset>

            {validationErrors.length > 0 && (
              <div
                role="alert"
                data-testid="backup-validation-errors"
                className="space-y-1 text-sm text-[color:var(--vellum-color-danger,#dc2626)]"
              >
                {validationErrors.map((key) => (
                  <p key={key}>{t(key)}</p>
                ))}
              </div>
            )}

            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                data-testid="backup-test-connection"
                disabled={busy || protocol === ""}
                onClick={() => void handleTestConnection()}
              >
                {testState === "loading"
                  ? t(`${ERROR_PREFIX}.test.loading`)
                  : t(`${ERROR_PREFIX}.test.button`)}
              </Button>
              <Button
                data-testid="backup-save"
                disabled={busy || protocol === ""}
                onClick={() => void handleSave()}
              >
                {t(`${ERROR_PREFIX}.save.button`)}
              </Button>
              {status.configured && (
                <Button
                  variant="outline"
                  className="text-destructive"
                  data-testid={
                    confirmClear ? "backup-clear-confirm" : "backup-clear"
                  }
                  disabled={busy}
                  onClick={() => {
                    if (!confirmClear) {
                      setConfirmClear(true);
                      window.setTimeout(() => setConfirmClear(false), 6000);
                      return;
                    }
                    void handleClear();
                  }}
                >
                  {confirmClear
                    ? t(`${ERROR_PREFIX}.clear.confirm`)
                    : t(`${ERROR_PREFIX}.clear.button`)}
                </Button>
              )}
            </div>

            {testNotice && (
              <p
                role={testNotice.ok ? "status" : "alert"}
                data-testid="backup-test-result"
                className={
                  testNotice.ok
                    ? "text-sm"
                    : "text-sm text-[color:var(--vellum-color-danger,#dc2626)]"
                }
              >
                {testNotice.text}
              </p>
            )}

            <p className="text-xs leading-4 text-muted-foreground">
              {t(`${ERROR_PREFIX}.failure_semantics`)}
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
