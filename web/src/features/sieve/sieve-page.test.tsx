import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SievePage } from "./sieve-page";
const { t } = vi.hoisted(() => ({ t: (key: string) => key }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t }) }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
describe("independent mailbox Sieve editor", () => {
  it("requires confirmation and sends only a scoped CSRF credential", async () => {
    const session = {
      mailbox: "independent@example.test",
      csrf: "scoped-csrf",
      state: "desired",
      version: "a".repeat(64),
      rules: { rules: [], vacation: { enabled: false, days: 0 } },
    };
    const fetcher = vi
      .fn()
      .mockResolvedValue({ ok: true, json: async () => session });
    vi.stubGlobal("fetch", fetcher);
    render(<SievePage />);
    await screen.findByText("independent@example.test");
    expect(
      (
        screen.getByRole("button", {
          name: "page.sieve.save",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    fireEvent.click(
      screen.getByRole("button", { name: "page.sieve.add_rule" }),
    );
    fireEvent.change(screen.getByLabelText("page.sieve.value"), {
      target: { value: "invoice" },
    });
    fireEvent.change(screen.getByLabelText("page.sieve.folder"), {
      target: { value: "Reviewed" },
    });
    fireEvent.click(
      screen.getByRole("checkbox", { name: "page.sieve.confirm" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "page.sieve.save" }));
    await waitFor(() =>
      expect(fetcher).toHaveBeenCalledWith(
        "/api/v1/mailbox/sieve/rules",
        expect.objectContaining({
          method: "PUT",
          credentials: "same-origin",
          headers: {
            "Content-Type": "application/json",
            "X-Urmail-Sieve-CSRF": "scoped-csrf",
          },
        }),
      ),
    );
    const body = JSON.parse(fetcher.mock.calls.at(-1)?.[1].body as string);
    expect(body.confirmed).toBe(true);
    expect(body.expected_version).toBe("a".repeat(64));
    expect(body.mailbox_id).toBeUndefined();
    expect(body.tenant_id).toBeUndefined();
  });
  it("preserves raw legacy rules by offering only the authenticated webmail editor", async () => {
    const fetcher = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        mailbox: "legacy@example.test",
        csrf: "scoped-csrf",
        state: "legacy_managesieve",
        version: "",
        rules: {},
      }),
    });
    vi.stubGlobal("fetch", fetcher);
    render(<SievePage />);
    await screen.findByText("page.sieve.legacy_managesieve");
    expect(
      screen
        .getByRole("link", { name: "page.sieve.open_managesieve" })
        .getAttribute("href"),
    ).toBe("/mail/?_task=settings&_action=plugin.managesieve");
    expect(
      screen.queryByRole("button", { name: "page.sieve.save" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "page.sieve.add_rule" }),
    ).toBeNull();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it("shows mailbox reauthorization when the credential expires", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    render(<SievePage />);
    await screen.findByText("page.sieve.reauthenticate");
    expect(
      screen.queryByRole("button", { name: "page.sieve.save" }),
    ).toBeNull();
  });
});
