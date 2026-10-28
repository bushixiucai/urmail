import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { RecallWorkspace } from "./recall-workspace";
const api = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/client", () => ({ request: api }));
vi.mock("@/lib/api/use-api-error-message", () => ({
  useApiErrorMessage: () => () => "request failed",
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
const entries = [
  {
    id: 1,
    subject: "A source",
    message_id: "<one@example.test>",
    legal_hold: false,
  },
];
const row = {
  id: 9,
  archive_entry_id: 1,
  status: "preview",
  reason: null,
  preview_token: "preview-proof",
  preview_digest: "digest-proof",
  preview_expires_at: "2099-01-01",
  selected_copy_keys: [],
  local_result: { items: [] },
  attempt_epoch: 1,
};
const copies = [
  { key: "original-one", folder: "INBOX", uid: 1, uid_validity: 77 },
  { key: "later-copy", folder: "Sent", uid: 2, uid_validity: 77 },
];
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
it("requires preview and explicit per-copy selection and sends only the reviewed proof", async () => {
  api.mockImplementation((path: string, options?: { method?: string }) =>
    Promise.resolve(
      path.endsWith("/preview")
        ? {
            targets: [
              {
                entry_id: 1,
                address: "one@example.test",
                reason: "",
                copies,
                recall: row,
              },
            ],
          }
        : options?.method === "POST"
          ? {
              recalls: [
                {
                  ...row,
                  status: "local_recalled",
                  local_result: {
                    items: [
                      {
                        key: "original-one",
                        status: "confirmed_absent",
                        reason: "local_copy_absent",
                      },
                    ],
                  },
                },
              ],
            }
          : { recalls: [] },
    ),
  );
  render(<RecallWorkspace entries={entries} />);
  fireEvent.change(screen.getByLabelText("recall.source"), {
    target: { value: "1" },
  });
  fireEvent.click(screen.getByRole("button", { name: "recall.preview" }));
  await screen.findByText(/INBOX/);
  expect(
    (
      screen.getByRole("button", {
        name: "recall.review_selection",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.click(screen.getByLabelText(/INBOX/));
  fireEvent.click(
    screen.getByRole("button", { name: "recall.review_selection" }),
  );
  expect(screen.getByRole("dialog")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: /^recall\.confirm$/ }));
  await waitFor(() =>
    expect(
      api.mock.calls.some(
        ([path, options]) =>
          path === "/api/v1/compliance/recalls" && options?.method === "POST",
      ),
    ).toBe(true),
  );
  const call = api.mock.calls.find(
    ([path, options]) =>
      path === "/api/v1/compliance/recalls" && options?.method === "POST",
  );
  expect(call?.[1].body).toEqual({
    confirmed: true,
    targets: [
      {
        id: 9,
        preview_token: "preview-proof",
        digest: "digest-proof",
        copy_keys: ["original-one"],
      },
    ],
  });
  expect(JSON.stringify(call?.[1].body)).not.toContain("later-copy");
});
it("shows held and remote targets without offering a destructive selection", async () => {
  api.mockImplementation((path: string) =>
    Promise.resolve(
      path.endsWith("/preview")
        ? {
            targets: [
              {
                entry_id: 1,
                address: null,
                reason: "remote_delivery_not_recallable",
                copies: [],
                recall: null,
              },
              {
                entry_id: 2,
                address: "held@example.test",
                reason: "legal_hold",
                copies: [],
                recall: null,
              },
            ],
          }
        : { recalls: [] },
    ),
  );
  render(<RecallWorkspace entries={entries} />);
  fireEvent.change(screen.getByLabelText("recall.source"), {
    target: { value: "1" },
  });
  fireEvent.click(screen.getByRole("button", { name: "recall.preview" }));
  await screen.findByText("recall.reason.legal_hold");
  expect(
    screen.getByText("recall.reason.remote_delivery_not_recallable"),
  ).toBeTruthy();
  expect(
    (
      screen.getByRole("button", {
        name: "recall.review_selection",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
it("retries a pending intent with the original persisted selection rather than current copies", async () => {
  api.mockImplementation((path: string, options?: { method?: string }) =>
    Promise.resolve(
      path.endsWith("/preview")
        ? {
            targets: [
              {
                entry_id: 1,
                address: "one@example.test",
                reason: "review_in_progress",
                copies,
                recall: {
                  ...row,
                  status: "pending",
                  selected_copy_keys: ["original-one"],
                },
              },
            ],
          }
        : options?.method === "POST"
          ? {
              recalls: [
                {
                  ...row,
                  status: "pending",
                  selected_copy_keys: ["original-one"],
                },
              ],
            }
          : { recalls: [] },
    ),
  );
  render(<RecallWorkspace entries={entries} />);
  fireEvent.change(screen.getByLabelText("recall.source"), {
    target: { value: "1" },
  });
  fireEvent.click(screen.getByRole("button", { name: "recall.preview" }));
  fireEvent.click(
    await screen.findByRole("button", { name: "recall.resume_exact" }),
  );
  await waitFor(() =>
    expect(api.mock.calls.some(([, options]) => options?.body?.confirmed)).toBe(
      true,
    ),
  );
  const call = api.mock.calls.find(([, options]) => options?.body?.confirmed);
  expect(call?.[1].body.targets[0].copy_keys).toEqual(["original-one"]);
});
