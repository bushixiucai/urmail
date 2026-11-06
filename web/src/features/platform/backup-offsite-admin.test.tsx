import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
  BackupOffsiteAdmin,
  buildDestinationInput,
  buildCredentials,
} from "./backup-offsite-admin";
import { ApiRequestError } from "@/lib/api/client";

const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string, params?: Record<string, unknown>) =>
    params ? `${key}${JSON.stringify(params)}` : key,
  i18n: { exists: () => true },
  getStatus: () =>
    ({
      configured: false,
      enabled: false,
      has_credentials: false,
      credential_summary: {},
    }) as Record<string, unknown>,
  testResponse: { ok: true } as unknown,
  testError: null as unknown,
  putResponse: null as unknown,
  puts: [] as unknown[],
  tests: [] as unknown[],
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));

vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, request: state.request };
});

installDefaultRequestImpl();

function baseStatus(extra: Record<string, unknown> = {}) {
  return {
    configured: false,
    enabled: false,
    has_credentials: false,
    credential_summary: {},
    ...extra,
  };
}

async function settle() {
  await act(async () => {
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

function installDefaultRequestImpl() {
  state.request.mockImplementation(
    async (
      path: string,
      options?: {
        method?: string;
        body?: unknown;
      },
    ) => {
      if (path.endsWith("/test-connection")) {
        state.tests.push(options?.body);
        if (state.testError) throw state.testError;
        return state.testResponse;
      }
      if (options?.method === "PUT") {
        state.puts.push(options.body);
        return (
          state.putResponse ??
          (typeof options.body === "object" && options.body !== null
            ? options.body
            : {})
        );
      }
      return state.getStatus();
    },
  );
}

function resetState() {
  state.request.mockReset();
  installDefaultRequestImpl();
  state.getStatus = () =>
    ({
      configured: false,
      enabled: false,
      has_credentials: false,
      credential_summary: {},
    }) as Record<string, unknown>;
  state.testResponse = { ok: true };
  state.testError = null;
  state.putResponse = null;
  state.puts = [];
  state.tests = [];
}

afterEach(() => {
  cleanup();
  resetState();
});

/** Render and wait for the initial GET to settle. */
async function setup() {
  const view = render(<BackupOffsiteAdmin />);
  await settle();
  return view;
}

function selectProtocol(value: string) {
  fireEvent.change(screen.getByTestId("backup-type-select"), {
    target: { value },
  });
}

async function clickButton(testId: string) {
  await act(async () => {
    fireEvent.click(screen.getByTestId(testId));
    for (let i = 0; i < 20; i++) await Promise.resolve();
  });
}

it("shows the unconfigured empty state as a normal state, not an error", async () => {
  const view = await setup();
  expect(view.getByTestId("backup-empty-state")).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.status.not_configured"),
  ).toBeTruthy();
  expect(view.queryByTestId("backup-current-config")).toBeNull();
  // Nothing to save or probe until a protocol is selected.
  expect(
    (screen.getByTestId("backup-save") as HTMLButtonElement).disabled,
  ).toBe(true);
  expect(
    (screen.getByTestId("backup-test-connection") as HTMLButtonElement)
      .disabled,
  ).toBe(true);
});

it("renders dynamic fields for all three protocols", async () => {
  const view = await setup();
  selectProtocol("s3");
  expect(view.getByTestId("backup-bucket")).toBeTruthy();
  expect(view.getByTestId("backup-access-key-id")).toBeTruthy();
  expect(view.getByTestId("backup-secret-key")).toBeTruthy();
  expect(view.getByTestId("backup-region")).toBeTruthy();

  selectProtocol("sftp");
  expect(view.queryByTestId("backup-bucket")).toBeNull();
  expect(view.getByTestId("backup-sftp-user")).toBeTruthy();
  expect(view.getByTestId("backup-sftp-password")).toBeTruthy();
  expect(view.getByTestId("backup-private-key-path")).toBeTruthy();
  expect(view.getByTestId("backup-host-key-fingerprint")).toBeTruthy();

  selectProtocol("webdav");
  expect(view.getByTestId("backup-webdav-username")).toBeTruthy();
  expect(view.getByTestId("backup-webdav-password")).toBeTruthy();
  expect(view.queryByTestId("backup-bucket")).toBeNull();
  expect(view.queryByTestId("backup-sftp-user")).toBeNull();
});

it("validates required fields locally before any PUT", async () => {
  const view = await setup();
  selectProtocol("s3");
  await clickButton("backup-save");
  expect(view.getByTestId("backup-validation-errors")).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.validation.required_bucket"),
  ).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.validation.required_region"),
  ).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.validation.required_access_key"),
  ).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.validation.required_secret_key"),
  ).toBeTruthy();
  // No request left the client.
  expect(state.puts).toHaveLength(0);
});

it("rejects enabling without any credentials locally", async () => {
  const view = await setup();
  selectProtocol("webdav");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "https://dav.example.test" },
  });
  fireEvent.click(screen.getByTestId("backup-enabled"));
  await clickButton("backup-save");
  expect(
    view.getByText(
      "platform_backup_offsite.validation.credentials_required_for_enable",
    ),
  ).toBeTruthy();
  expect(state.puts).toHaveLength(0);
});

it("saves an s3 destination with the contract credential shape", async () => {
  await setup();
  selectProtocol("s3");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "https://s3.example.internal:9000" },
  });
  fireEvent.change(screen.getByTestId("backup-bucket"), {
    target: { value: "urmail-backups" },
  });
  fireEvent.change(screen.getByTestId("backup-age-recipient"), {
    target: { value: "age1example" },
  });
  fireEvent.change(screen.getByTestId("backup-access-key-id"), {
    target: { value: "AKID" },
  });
  fireEvent.change(screen.getByTestId("backup-secret-key"), {
    target: { value: "SECRET" },
  });
  fireEvent.change(screen.getByTestId("backup-region"), {
    target: { value: "us-east-1" },
  });
  state.putResponse = baseStatus({
    configured: true,
    enabled: false,
    type: "s3",
    endpoint: "https://s3.example.internal:9000",
    bucket: "urmail-backups",
    age_recipient: "age1example",
    has_credentials: true,
    credential_summary: { access_key: true, secret_key: true },
  });
  await clickButton("backup-save");
  expect(state.puts).toHaveLength(1);
  expect(state.puts[0]).toEqual({
    type: "s3",
    endpoint: "https://s3.example.internal:9000",
    bucket: "urmail-backups",
    age_recipient: "age1example",
    enabled: false,
    credentials: {
      s3: { access_key_id: "AKID", secret_key: "SECRET", region: "us-east-1" },
    },
  });
  // Credentials are never echoed back into the form after save.
  expect(
    (screen.getByTestId("backup-access-key-id") as HTMLInputElement).value,
  ).toBe("");
});

it("masks stored credentials: summary chips shown, no plaintext backfill", async () => {
  state.getStatus = () =>
    baseStatus({
      configured: true,
      enabled: true,
      type: "s3",
      endpoint: "https://s3.example.internal:9000",
      bucket: "urmail-backups",
      prefix: "gen",
      age_recipient: "age1example",
      has_credentials: true,
      credential_summary: { access_key: true, secret_key: true },
    });
  const view = await setup();
  expect(view.getByTestId("backup-current-config")).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.credentials.summary.access_key"),
  ).toBeTruthy();
  expect(
    view.getByText("platform_backup_offsite.credentials.summary.secret_key"),
  ).toBeTruthy();
  expect(
    (view.getByTestId("backup-access-key-id") as HTMLInputElement).value,
  ).toBe("");
  expect(
    (view.getByTestId("backup-secret-key") as HTMLInputElement).value,
  ).toBe("");
});

it("omits credentials from PUT when editing keeps stored secrets", async () => {
  state.getStatus = () =>
    baseStatus({
      configured: true,
      enabled: true,
      type: "s3",
      endpoint: "https://s3.example.internal:9000",
      bucket: "urmail-backups",
      has_credentials: true,
      credential_summary: { access_key: true, secret_key: true },
    });
  state.putResponse = baseStatus({
    configured: true,
    enabled: true,
    type: "s3",
    endpoint: "https://s3.example.internal:9000",
    bucket: "urmail-backups",
    has_credentials: true,
    credential_summary: { access_key: true, secret_key: true },
  });
  await setup();
  await clickButton("backup-save");
  expect(state.puts).toHaveLength(1);
  expect(state.puts[0]).not.toHaveProperty("credentials");
  expect(state.puts[0]).toMatchObject({ type: "s3", enabled: true });
});

it("shows test-connection success state", async () => {
  const view = await setup();
  selectProtocol("webdav");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "https://dav.example.test" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-username"), {
    target: { value: "ops" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-password"), {
    target: { value: "pw" },
  });
  await clickButton("backup-test-connection");
  expect(state.tests).toHaveLength(1);
  expect(state.tests[0]).toMatchObject({
    type: "webdav",
    credentials: { username: "ops", password: "pw" },
  });
  expect(view.getByTestId("backup-test-result")).toBeTruthy();
  expect(view.getByText("platform_backup_offsite.test.success")).toBeTruthy();
});

it("maps the two known test-connection failure details to localised copy", async () => {
  const view = await setup();
  selectProtocol("webdav");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "https://dav.example.test" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-username"), {
    target: { value: "ops" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-password"), {
    target: { value: "pw" },
  });
  state.testResponse = { ok: false, error: "destination not configured" };
  await clickButton("backup-test-connection");
  expect(
    view.getByText("platform_backup_offsite.test.not_configured"),
  ).toBeTruthy();
  state.testResponse = {
    ok: false,
    error: "credentials required to test an unsaved destination",
  };
  await clickButton("backup-test-connection");
  expect(
    view.getByText("platform_backup_offsite.test.credentials_required"),
  ).toBeTruthy();
  // Unknown detail strings are shown verbatim with the failed prefix.
  state.testResponse = { ok: false, error: "connection refused" };
  await clickButton("backup-test-connection");
  expect(view.getByTestId("backup-test-result").textContent).toContain(
    "connection refused",
  );
});

it("maps 400 INVALID_PARAMS and 503 SECRETS_UNCONFIGURED error codes to copy", async () => {
  const view = await setup();
  selectProtocol("webdav");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "https://dav.example.test" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-username"), {
    target: { value: "ops" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-password"), {
    target: { value: "pw" },
  });

  state.testError = new ApiRequestError(400, {
    error: { code: "INVALID_PARAMS" },
  });
  await clickButton("backup-test-connection");
  expect(
    view.getByText("platform_backup_offsite.error.invalid_params"),
  ).toBeTruthy();

  state.testError = new ApiRequestError(503, {
    error: { code: "SECRETS_UNCONFIGURED" },
  });
  await clickButton("backup-test-connection");
  expect(
    view.getByText("platform_backup_offsite.error.secrets_unconfigured"),
  ).toBeTruthy();
});

it("rejects a webdav endpoint with embedded credentials", async () => {
  const view = await setup();
  selectProtocol("webdav");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "https://user:pass@dav.example.test" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-username"), {
    target: { value: "ops" },
  });
  fireEvent.change(screen.getByTestId("backup-webdav-password"), {
    target: { value: "pw" },
  });
  await clickButton("backup-save");
  expect(
    view.getByText("platform_backup_offsite.validation.endpoint_webdav_format"),
  ).toBeTruthy();
  expect(state.puts).toHaveLength(0);
});

it("rejects a malformed sftp host key key fingerprint", async () => {
  const view = await setup();
  selectProtocol("sftp");
  fireEvent.change(screen.getByTestId("backup-endpoint"), {
    target: { value: "backup.example.test" },
  });
  fireEvent.change(screen.getByTestId("backup-sftp-user"), {
    target: { value: "ops" },
  });
  fireEvent.change(screen.getByTestId("backup-sftp-password"), {
    target: { value: "pw" },
  });
  fireEvent.change(screen.getByTestId("backup-host-key-fingerprint"), {
    target: { value: "md5:bogus" },
  });
  await clickButton("backup-save");
  expect(
    view.getByText("platform_backup_offsite.validation.fingerprint_invalid"),
  ).toBeTruthy();
  expect(state.puts).toHaveLength(0);
});

it("clears the destination with a two-step confirmation", async () => {
  state.getStatus = () =>
    baseStatus({
      configured: true,
      enabled: true,
      type: "sftp",
      endpoint: "backup.example.test",
      has_credentials: true,
      credential_summary: {
        username: true,
        password: true,
        host_key_pinned: true,
      },
    });
  const view = await setup();
  await clickButton("backup-clear");
  // First click arms the confirmation, nothing sent yet.
  expect(view.getByTestId("backup-clear-confirm")).toBeTruthy();
  expect(state.puts).toHaveLength(0);
  await clickButton("backup-clear-confirm");
  expect(state.puts).toHaveLength(1);
  expect(state.puts[0]).toEqual({ type: "", enabled: false });
  expect(view.getByTestId("backup-empty-state")).toBeTruthy();
});

it("shows load failure with retry", async () => {
  state.request.mockImplementationOnce(async () => {
    throw new ApiRequestError(500, { error: { code: "DB_ERROR" } });
  });
  const view = await setup();
  expect(view.getByTestId("backup-load-failed")).toBeTruthy();
  expect(view.getByText("platform_backup_offsite.load_failed")).toBeTruthy();
});

// ---- pure helpers -------------------------------------------------------

it("buildDestinationInput validates per protocol (pure function tests)", () => {
  const fields = (
    over: Partial<Parameters<typeof buildDestinationInput>[1]> = {},
  ) => ({
    endpoint: "",
    bucket: "",
    prefix: "",
    age_recipient: "",
    enabled: false,
    ...over,
  });
  const draft = (
    over: Partial<Parameters<typeof buildDestinationInput>[2]> = {},
  ) => ({
    ...{
      access_key_id: "A",
      secret_key: "s",
      region: "r",
      sftp_user: "u",
      sftp_password: "pw",
      private_key_path: "",
      host_key_fingerprint: "SHA256:" + "A".repeat(43),
      webdav_username: "u",
      webdav_password: "pw",
    },
    ...over,
  });

  // s3 with no endpoint is fine (blank = AWS).
  expect(
    buildDestinationInput(
      "s3",
      fields({ bucket: "b", endpoint: "" }),
      draft(),
      false,
    ).ok,
  ).toBe(true);
  // length limits (endpoint <=300, bucket <=255, prefix <=300).
  expect(
    buildDestinationInput(
      "s3",
      fields({ bucket: "b", prefix: "x".repeat(301) }),
      draft(),
      false,
    ).ok,
  ).toBe(false);
  // sftp rejects schemes and paths.
  expect(
    buildDestinationInput(
      "sftp",
      fields({ endpoint: "sftp://h" }),
      draft(),
      false,
    ).ok,
  ).toBe(false);
  expect(
    buildDestinationInput("sftp", fields({ endpoint: "h/sub" }), draft(), false)
      .ok,
  ).toBe(false);
  // webdav rejects embedded credentials and paths.
  expect(
    buildDestinationInput(
      "webdav",
      fields({ endpoint: "https://u:p@h" }),
      draft({ webdav_username: "u", webdav_password: "p" }),
      false,
    ).ok,
  ).toBe(false);
  // fingerprint format enforced (exact 50 chars: SHA256: + 43 unpadded base64 chars).
  expect(
    buildDestinationInput(
      "sftp",
      fields({ endpoint: "h" }),
      draft({ host_key_fingerprint: "SHA256:short" }),
      false,
    ).ok,
  ).toBe(false);
  expect(
    buildDestinationInput(
      "sftp",
      fields({ endpoint: "h" }),
      draft({ host_key_fingerprint: "SHA256:" + "A".repeat(43) + "=" }),
      false,
    ).ok,
  ).toBe(false);
  expect(
    buildDestinationInput(
      "sftp",
      fields({ endpoint: "h" }),
      draft({ host_key_fingerprint: "SHA256:" + "A".repeat(43) }),
      false,
    ).ok,
  ).toBe(true);
  // stored credentials relax the inline requirement.
  const stored = buildDestinationInput(
    "webdav",
    fields({ endpoint: "https://d" }),
    draft({ webdav_username: "", webdav_password: "" }),
    true,
  );
  expect(stored.ok).toBe(true);
  if (stored.ok) expect(stored.input).not.toHaveProperty("credentials");
  // PUT is a replace: prefix only, no bucket for sftp.
  const sftp = buildDestinationInput(
    "sftp",
    fields({ endpoint: "h:2222", prefix: "x", bucket: "nope" }),
    draft(),
    false,
  );
  expect(sftp.ok).toBe(true);
  if (sftp.ok) expect(sftp.input).not.toHaveProperty("bucket");
  if (sftp.ok) expect(sftp.input).toMatchObject({ prefix: "x" });
});

it("buildCredentials emits the contract shapes", () => {
  const draft = (
    over: Partial<Parameters<typeof buildCredentials>[1]> = {},
  ) => ({
    access_key_id: "",
    secret_key: "",
    region: "",
    sftp_user: "",
    sftp_password: "",
    private_key_path: "",
    host_key_fingerprint: "",
    webdav_username: "",
    webdav_password: "",
    ...over,
  });
  expect(
    buildCredentials(
      "s3",
      draft({ access_key_id: "A", secret_key: "S", region: "r" }),
    ),
  ).toEqual({ s3: { access_key_id: "A", secret_key: "S", region: "r" } });
  expect(
    buildCredentials("sftp", draft({ sftp_user: "u", sftp_password: "p" })),
  ).toEqual({
    sftp_user: "u",
    sftp: { password: "p" },
  });
  expect(
    buildCredentials(
      "webdav",
      draft({ webdav_username: "u", webdav_password: "p" }),
    ),
  ).toEqual({
    username: "u",
    password: "p",
  });
  expect(buildCredentials("s3", draft())).toBeUndefined();
});
