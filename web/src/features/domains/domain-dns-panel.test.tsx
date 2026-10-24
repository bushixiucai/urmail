import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DomainDNSPanel } from "./domain-dns-panel";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  error: () => "safe-error",
  t: (key: string) => key,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: state.t }) }));
vi.mock("@/lib/api/use-api-error-message", () => ({
  useApiErrorMessage: () => state.error,
}));
vi.mock("@/lib/api/client", () => ({ request: state.request }));
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
const domain = {
  id: 1,
  fqdn: "owned.example.test",
  mode: "delegated",
  provisioning_state: "pending",
};
const draft = {
  id: "plan-one",
  kind: "provider",
  status: "draft",
  source_digest: "immutable-digest",
  before_records: [],
  desired_records: [],
  expires_at: "2099-01-01T00:00:00Z",
  changes: {
    actions: [
      {
        kind: "create",
        after: {
          name: "owned.example.test",
          type: "MX",
          value: "mail.platform.test",
          priority: 10,
        },
      },
    ],
    conflicts: [],
  },
};
function mockReads(plans: unknown[] = []) {
  state.request.mockImplementation(
    (path: string, opts?: { method?: string }) => {
      if (opts?.method) return Promise.resolve({});
      if (path.endsWith("/credentials"))
        return Promise.resolve({ credentials: [] });
      if (path.endsWith("/plans")) return Promise.resolve({ plans });
      return Promise.resolve({ tasks: [] });
    },
  );
}
describe("explicit DNS execution boundaries", () => {
  it("a preview never confirms a plan; confirmation requires a checkbox and a second explicit action", async () => {
    mockReads([draft]);
    render(<DomainDNSPanel domain={domain} />);
    const confirm = await screen.findByRole("button", {
      name: "page.dns.confirm",
    });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    expect(
      state.request.mock.calls.some(([path]) =>
        String(path).includes("/confirm"),
      ),
    ).toBe(false);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(confirm);
    expect(
      state.request.mock.calls.some(([path]) =>
        String(path).includes("/confirm"),
      ),
    ).toBe(false);
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "action.confirm",
      }),
    );
    await waitFor(() =>
      expect(state.request).toHaveBeenCalledWith(
        "/api/v1/domains/1/dns/plans/plan-one/confirm",
        expect.objectContaining({
          method: "POST",
          body: expect.objectContaining({
            source_digest: "immutable-digest",
            confirm: true,
          }),
        }),
      ),
    );
  });
  it("credentials use a password input, probe only on explicit save and clear the secret afterward", async () => {
    mockReads();
    render(<DomainDNSPanel domain={domain} />);
    const token = screen.getByLabelText("page.dns.token") as HTMLInputElement;
    expect(token.type).toBe("password");
    fireEvent.change(token, { target: { value: "ephemeral-secret" } });
    expect(state.request.mock.calls.every(([, opts]) => !opts?.method)).toBe(
      true,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "page.dns.probe_save" }),
    );
    await waitFor(() => expect(token.value).toBe(""));
    expect(state.request).toHaveBeenCalledWith(
      "/api/v1/domains/1/dns/credentials",
      expect.objectContaining({
        method: "PUT",
        body: {
          provider: "cloudflare",
          zone: "owned.example.test",
          credentials: { token: "ephemeral-secret" },
        },
      }),
    );
  });
  it("conflicts block execution even after acknowledgment", async () => {
    mockReads([
      {
        ...draft,
        changes: { actions: [], conflicts: ["MX requires manual resolution"] },
      },
    ]);
    render(<DomainDNSPanel domain={domain} />);
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("checkbox"));
    expect(
      (
        screen.getByRole("button", {
          name: "page.dns.confirm",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
  });
  it("an unmounted tenant scope cannot render a late credential response", async () => {
    let resolve!: (value: unknown) => void;
    state.request.mockImplementation((path: string) =>
      path.endsWith("/credentials")
        ? new Promise((done) => {
            resolve = done;
          })
        : Promise.resolve(
            path.endsWith("/plans") ? { plans: [] } : { tasks: [] },
          ),
    );
    const view = render(<DomainDNSPanel domain={domain} />);
    view.unmount();
    resolve({
      credentials: [
        {
          id: 1,
          provider: "cloudflare",
          zone_name: "foreign.example.test",
          version: 1,
          enabled: true,
        },
      ],
    });
    await Promise.resolve();
    expect(screen.queryByText("foreign.example.test")).toBeNull();
  });
});
