import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DomainTransportPanel } from "./domain-transport-panel";
const api = vi.hoisted(() => vi.fn());
const errorText = vi.hoisted(() => (error: Error) => error.message);
vi.mock("@/lib/api/client", () => ({ request: api }));
vi.mock("@/lib/api/use-api-error-message", () => ({
  useApiErrorMessage: () => errorText,
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
const domain = { id: 9, fqdn: "customer.test", status: "active" };
it("requires explicit confirmation and keeps public observation separate from publication", async () => {
  let policy = {
    enabled: false,
    version: 0,
    mode: "testing",
    dane_enabled: false,
  };
  api.mockImplementation(
    async (path: string, options?: { method?: string; body?: unknown }) => {
      if (path.endsWith("/reports")) return { reports: [] };
      if (options?.method === "PUT") {
        policy = { ...policy, enabled: true, version: 1 };
        return policy;
      }
      if (path.endsWith("/observe")) return { status: "failed" };
      return policy;
    },
  );
  render(<DomainTransportPanel domain={domain} />);
  const publish = await screen.findByRole("button", {
    name: "transport.publish",
  });
  expect((publish as HTMLButtonElement).disabled).toBe(true);
  expect(api.mock.calls.every(([, options]) => !options?.method)).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(publish);
  await waitFor(() =>
    expect(api).toHaveBeenCalledWith("/api/v1/domains/9/transport-security", {
      method: "PUT",
      body: { mode: "testing", max_age: 86400, version: 0, confirm: true },
    }),
  );
  await waitFor(() =>
    expect(
      screen
        .getByRole("button", { name: "transport.observe" })
        .getAttribute("disabled"),
    ).toBeNull(),
  );
  expect(api.mock.calls.some(([path]) => path.endsWith("/observe"))).toBe(
    false,
  );
  fireEvent.click(screen.getByRole("button", { name: "transport.observe" }));
  await waitFor(() =>
    expect(api).toHaveBeenCalledWith(
      "/api/v1/domains/9/transport-security/observe",
      { method: "POST", body: {} },
    ),
  );
  expect(screen.getByText("transport.reports_unverified")).toBeTruthy();
});
it("cannot publish the previous domain snapshot while the next one is still loading", async () => {
  let resolveNext: (value: unknown) => void = () => {};
  api.mockImplementation((path: string) =>
    path.endsWith("/reports")
      ? Promise.resolve({ reports: [] })
      : path.includes("/10/")
        ? new Promise((resolve) => {
            resolveNext = resolve;
          })
        : Promise.resolve({
            enabled: true,
            version: 1,
            mode: "testing",
            dane_enabled: false,
          }),
  );
  const view = render(<DomainTransportPanel domain={domain} />);
  await screen.findByRole("button", { name: "transport.publish" });
  view.rerender(
    <DomainTransportPanel domain={{ ...domain, id: 10, fqdn: "next.test" }} />,
  );
  expect(
    screen.queryByRole("button", { name: "transport.publish" }),
  ).toBeNull();
  await act(async () => {
    resolveNext({
      enabled: false,
      version: 0,
      mode: "testing",
      dane_enabled: false,
    });
  });
  await screen.findByRole("button", { name: "transport.publish" });
  expect(api.mock.calls.every(([, options]) => options?.method !== "PUT")).toBe(
    true,
  );
});
it("shows verification requirements without requesting inactive-domain state", () => {
  render(<DomainTransportPanel domain={{ ...domain, status: "disabled" }} />);
  expect(screen.getByText("transport.domain_required")).toBeTruthy();
  expect(api).not.toHaveBeenCalled();
});
