import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { DomainAuthorityPanel } from "./domain-authority-panel";
const api = vi.hoisted(() => vi.fn());
const errorMessage = () => "authority-error";
vi.mock("@/lib/api/client", () => ({ request: api }));
vi.mock("@/lib/api/use-api-error-message", () => ({
  useApiErrorMessage: () => errorMessage,
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(() => {
  cleanup();
  api.mockReset();
});
it("requires a named operation and explicit confirmation without a private key input", async () => {
  api.mockImplementation((_path: string, options?: { method?: string }) =>
    Promise.resolve(
      options?.method === "POST"
        ? { changed: true }
        : { phase: "unprepared", ds: [], ready: false },
    ),
  );
  render(
    <DomainAuthorityPanel
      domain={{
        id: 7,
        fqdn: "example.test",
        mode: "managed",
        status: "active",
      }}
    />,
  );
  await screen.findByText(/phase_unprepared/);
  expect(
    (
      screen.getByRole("button", {
        name: "domain.authority.execute",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(
    screen.getByRole("button", { name: "domain.authority.execute" }),
  );
  await waitFor(() =>
    expect(api).toHaveBeenCalledWith("/api/v1/domains/7/authority", {
      method: "POST",
      body: { action: "prepare", confirm: true },
    }),
  );
  expect(screen.queryByLabelText(/secret|private/i)).toBeNull();
});
it("does not offer authoritative changes for delegated DNS", () => {
  const view = render(
    <DomainAuthorityPanel
      domain={{
        id: 7,
        fqdn: "example.test",
        mode: "delegated",
        status: "active",
      }}
    />,
  );
  expect(view.container.textContent).toBe("");
  expect(api).not.toHaveBeenCalled();
});
