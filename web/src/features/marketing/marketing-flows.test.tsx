import {
  act,
  cleanup,
  fireEvent,
  render,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MarketingFlows, type Definition } from "./marketing-flows";
import { ApiRequestError } from "@/lib/api/client";
const state = vi.hoisted(() => ({
  request: vi.fn(),
  t: (key: string) => key,
  i18n: { language: "en", exists: () => true },
}));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: state.t, i18n: state.i18n }),
}));
vi.mock("@/lib/api/client", async (original) => ({
  ...(await original<typeof import("@/lib/api/client")>()),
  request: state.request,
}));
const draft: Definition = {
  schema_version: 1,
  event_source: "contact_enrolled",
  entry: "send_1",
  nodes: [
    { id: "send_1", kind: "send", template_id: 2, next: "stop_1" },
    { id: "stop_1", kind: "stop" },
  ],
};
const flow = {
  id: 9,
  name: "Workflow fixture",
  draft_revision: 1,
  published_version: 1,
  status: "active",
  draft,
};
const instance = {
  id: 12,
  email: "reader@example.test",
  node: "send_1",
  version: 1,
  state: "uncertain",
  delivery_uncertain: true,
  error_code: "delivery_uncertain",
  attempt_count: 1,
};
const renderFlow = () =>
  render(
    <MarketingFlows
      active
      templates={[{ id: 2, name: "Welcome" }]}
      lists={[{ id: 3, name: "Consented" }]}
      channelAvailable
    />,
  );
const settle = async () =>
  act(async () => {
    for (let i = 0; i < 15; i++) await Promise.resolve();
  });
beforeEach(() =>
  state.request.mockImplementation(
    (path: string, options?: { method?: string; body?: unknown }) => {
      if (path === "/api/v1/marketing/flows")
        return Promise.resolve({ flows: [flow], worker_enabled: true });
      if (path.includes("/instances?"))
        return Promise.resolve({ instances: [instance], next_cursor: 0 });
      if (path.endsWith("/preview"))
        return Promise.resolve({
          path: [
            {
              node: "send_1",
              kind: "send",
              at: "2026-10-27T00:00:00Z",
              subject: "Preview fixture",
              text_body: "<script>untrusted()</script>",
            },
            { node: "stop_1", kind: "stop", at: "2026-10-27T00:00:00Z" },
          ],
        });
      if (path.endsWith("/events"))
        return Promise.resolve({ enrolled_count: 1 });
      if (options?.method === "PUT")
        return Promise.resolve({
          flow: { ...flow, ...(options.body as object), draft_revision: 2 },
        });
      return Promise.resolve({ flow });
    },
  ),
);
afterEach(() => {
  cleanup();
  state.request.mockReset();
});
async function open(view: ReturnType<typeof renderFlow>) {
  await settle();
  fireEvent.click(view.getByRole("button", { name: "marketing.flow.edit" }));
  await settle();
}

it("edits typed steps and references without a JSON input", async () => {
  const view = renderFlow();
  await open(view);
  expect(document.querySelector("textarea")).toBeNull();
  fireEvent.change(view.getAllByLabelText("marketing.flow.node_type")[0], {
    target: { value: "wait" },
  });
  fireEvent.change(view.getByLabelText(/marketing.flow.wait_seconds/), {
    target: { value: "3600" },
  });
  fireEvent.change(view.getByLabelText("marketing.flow.next_node"), {
    target: { value: "stop_1" },
  });
  expect(
    view
      .getByRole("button", { name: "marketing.flow.publish" })
      .matches(":disabled"),
  ).toBe(true);
  fireEvent.click(view.getByRole("button", { name: "marketing.flow.save" }));
  await settle();
  const saved = state.request.mock.calls.find(
    ([, options]) => options?.method === "PUT",
  );
  expect(saved?.[1].body.draft.nodes[0]).toMatchObject({
    kind: "wait",
    wait_seconds: 3600,
    next: "stop_1",
  });
  expect(saved?.[1].body.expected_revision).toBe(1);
});
it("previews safely without enrollment or sending", async () => {
  const view = renderFlow();
  await open(view);
  fireEvent.submit(
    view.getByLabelText("marketing.flow.sample_email").closest("form")!,
  );
  await settle();
  expect(view.getByText("<script>untrusted()</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBeNull();
  expect(
    state.request.mock.calls.some(([path]) => path.endsWith("/events")),
  ).toBe(false);
  expect(
    view
      .getByRole("button", { name: "marketing.flow.start" })
      .matches(":disabled"),
  ).toBe(true);
});
it("connects server validation to the exact typed control", async () => {
  const normal = state.request.getMockImplementation()!;
  state.request.mockImplementation(
    (path: string, options?: { method?: string; body?: unknown }) =>
      path.endsWith("/preview")
        ? Promise.reject(
            new ApiRequestError(422, {
              error: {
                code: "FLOW_VALIDATION_FAILED",
                params: {
                  nodes: [
                    {
                      node: "send_1",
                      field: "template_id",
                      code: "template_required",
                    },
                  ],
                },
              },
            }),
          )
        : normal(path, options),
  );
  const view = renderFlow();
  await open(view);
  fireEvent.submit(
    view.getByLabelText("marketing.flow.sample_email").closest("form")!,
  );
  await settle();
  fireEvent.click(
    view.getByRole("button", {
      name: /marketing.flow.error.template_required/,
    }),
  );
  expect(document.activeElement).toBe(
    view.getByLabelText("marketing.template"),
  );
  expect(
    view.getByLabelText("marketing.template").getAttribute("aria-invalid"),
  ).toBe("true");
});
it("requires explicit enrollment and confirmation before retrying unknown outcomes", async () => {
  const view = renderFlow();
  await open(view);
  fireEvent.change(view.getByLabelText("marketing.list"), {
    target: { value: "3" },
  });
  fireEvent.click(view.getByLabelText("marketing.flow.enrollment_confirm"));
  expect(
    state.request.mock.calls.some(([path]) => path.endsWith("/events")),
  ).toBe(false);
  fireEvent.click(view.getByRole("button", { name: "marketing.flow.start" }));
  await settle();
  expect(
    state.request.mock.calls.find(([path]) => path.endsWith("/events"))?.[1]
      .body,
  ).toMatchObject({ source: "contact_enrolled", list_id: 3 });
  fireEvent.click(view.getByRole("button", { name: "marketing.flow.retry" }));
  fireEvent.change(view.getByLabelText("marketing.flow.retry_reason"), {
    target: { value: "Dedicated gateway confirmed non-acceptance" },
  });
  expect(
    view
      .getByRole("button", { name: "marketing.flow.confirm_retry" })
      .matches(":disabled"),
  ).toBe(true);
  fireEvent.click(view.getByLabelText("marketing.flow.confirm_not_accepted"));
  fireEvent.click(
    view.getByRole("button", { name: "marketing.flow.confirm_retry" }),
  );
  await waitFor(() =>
    expect(state.request).toHaveBeenCalledWith(
      "/api/v1/marketing/flows/9/instances/12/retry",
      {
        method: "POST",
        body: {
          reason: "Dedicated gateway confirmed non-acceptance",
          confirmed_not_accepted: true,
        },
      },
    ),
  );
});

it("renders inspected immutable graph and escaped content independently of the editable draft", async () => {
  const normal = state.request.getMockImplementation()!;
  state.request.mockImplementation(
    (path: string, options?: { method?: string; body?: unknown }) => {
      if (path.endsWith("/versions"))
        return Promise.resolve({
          versions: [
            {
              id: 41,
              version: 7,
              draft_revision: 2,
              published_by: 1,
              created_at: "2026-10-27T00:00:00Z",
            },
          ],
          next_cursor: 0,
        });
      if (path.endsWith("/versions/7"))
        return Promise.resolve({
          version: 7,
          definition: {
            ...draft,
            nodes: [
              { id: "published_send", kind: "send", next: "published_wait" },
              {
                id: "published_wait",
                kind: "wait",
                wait_seconds: 12,
                next: "published_stop",
              },
              { id: "published_stop", kind: "stop" },
            ],
            entry: "published_send",
          },
          templates: [
            {
              step_key: "published_send",
              subject: "Immutable published subject",
              text_body: "<script>publishedPayload()</script>",
            },
          ],
        });
      return normal(path, options);
    },
  );
  const view = renderFlow();
  await open(view);
  fireEvent.click(
    view.getByRole("button", { name: "marketing.flow.show_versions" }),
  );
  await settle();
  fireEvent.click(
    view.getByRole("button", { name: "marketing.flow.inspect_version" }),
  );
  await settle();
  const snapshot = view.getByRole("region", {
    name: "marketing.flow.snapshot_title",
  });
  expect(snapshot.textContent).toContain("Immutable published subject");
  expect(snapshot.textContent).toContain("<script>publishedPayload()</script>");
  expect(snapshot.textContent).toContain("published_wait");
  expect(snapshot.textContent).toContain("marketing.flow.snapshot_wait");
  expect(snapshot.querySelector("script")).toBeNull();
  expect(snapshot.querySelector("input,select,textarea")).toBeNull();
  expect(
    (view.getByLabelText("marketing.flow.entry_node") as HTMLSelectElement)
      .value,
  ).toBe("send_1");
});
