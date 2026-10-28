import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { ApiRequestError, request } from "@/lib/api/client";
import { useApiErrorMessage } from "@/lib/api/use-api-error-message";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type Named = { id: number; name: string };
type Kind = "send" | "wait" | "condition" | "stop";
type Field = "email_domain" | "list_id" | "source" | "consented";
export type FlowNode = {
  id: string;
  kind: Kind;
  next?: string;
  otherwise?: string;
  template_id?: number;
  wait_seconds?: number;
  field?: Field;
  operator?: "equals" | "not_equals";
  value?: string;
};
export type Definition = {
  schema_version: 1;
  event_source: "contact_enrolled";
  entry: string;
  nodes: FlowNode[];
};
type Flow = {
  id: number;
  name: string;
  draft_revision: number;
  published_version: number;
  status: string;
  draft?: Definition;
};
type Problem = { node: string; field: string; code: string };
type PreviewStep = {
  node: string;
  kind: Kind;
  at: string;
  subject?: string;
  text_body?: string;
  matched?: boolean;
  wait_seconds?: number;
  due_at?: string;
};
type Version = {
  id: number;
  version: number;
  draft_revision: number;
  published_by: number;
  created_at: string;
};
type Instance = {
  id: number;
  email: string;
  node: string;
  version: number;
  state: string;
  error_code: string;
  attempt_count: number;
  wait_until: string | null;
  next_attempt_at: string | null;
  delivery_uncertain: boolean;
};
type Execution = {
  node_key: string;
  status: string;
  error_code: string;
  created_at: string;
};
const selectClass =
  "h-10 min-w-0 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";
const freshDefinition = (templateID?: number): Definition => ({
  schema_version: 1,
  event_source: "contact_enrolled",
  entry: "send_1",
  nodes: [
    { id: "send_1", kind: "send", template_id: templateID, next: "stop_1" },
    { id: "stop_1", kind: "stop" },
  ],
});

export function MarketingFlows({
  active,
  templates,
  lists,
  channelAvailable,
}: {
  active: boolean;
  templates: Named[];
  lists: Named[];
  channelAvailable: boolean;
}) {
  const { t, i18n } = useTranslation();
  const apiError = useApiErrorMessage();
  const [flows, setFlows] = useState<Flow[]>([]);
  const [workerEnabled, setWorkerEnabled] = useState(false);
  const [selected, setSelected] = useState<Flow | null>(null);
  const [graph, setGraph] = useState<Definition>(freshDefinition());
  const [name, setName] = useState("");
  const [newName, setNewName] = useState("");
  const [dirty, setDirty] = useState(false);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [success, setSuccess] = useState("");
  const [problems, setProblems] = useState<Problem[]>([]);
  const [preview, setPreview] = useState<PreviewStep[] | null>(null);
  const [sampleEmail, setSampleEmail] = useState("preview@example.test");
  const [listID, setListID] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [snapshot, setSnapshot] = useState<{
    version: number;
    definition: Definition;
    templates: { step_key: string; subject: string; text_body: string }[];
  } | null>(null);
  const [versions, setVersions] = useState<Version[] | null>(null);
  const [versionCursor, setVersionCursor] = useState(0);
  const [instances, setInstances] = useState<Instance[] | null>(null);
  const [instanceCursor, setInstanceCursor] = useState(0);
  const [instanceEmail, setInstanceEmail] = useState("");
  const [executions, setExecutions] = useState<{
    instance: Instance;
    rows: Execution[];
  } | null>(null);
  const [retry, setRetry] = useState<Instance | null>(null);
  const [reason, setReason] = useState("");
  const [notAccepted, setNotAccepted] = useState(false);
  const loaded = useRef(false);
  const eventKey = useRef<string | null>(null);
  const base = selected ? `/api/v1/marketing/flows/${selected.id}` : "";
  const nodeLabel = (node: string) => {
    const index = graph.nodes.findIndex((item) => item.id === node);
    return index < 0
      ? node
      : t("marketing.flow.node_label", {
          number: index + 1,
          kind: t(`marketing.flow.kind.${graph.nodes[index].kind}`),
        });
  };
  const date = (value?: string | null) =>
    value
      ? new Intl.DateTimeFormat(i18n.language, {
          dateStyle: "short",
          timeStyle: "short",
        }).format(new Date(value))
      : "";
  function error(err: unknown) {
    if (err instanceof ApiRequestError && Array.isArray(err.params.nodes)) {
      setProblems(
        err.params.nodes.filter(
          (value): value is Problem =>
            typeof value === "object" &&
            value !== null &&
            typeof value.node === "string" &&
            typeof value.field === "string" &&
            typeof value.code === "string",
        ),
      );
    }
    setNotice(apiError(err));
  }
  async function run(action: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setNotice("");
    setSuccess("");
    setProblems([]);
    try {
      await action();
    } catch (err) {
      error(err);
    } finally {
      setBusy(false);
    }
  }
  async function refresh() {
    const result = await request<{ flows: Flow[]; worker_enabled: boolean }>(
      "/api/v1/marketing/flows",
    );
    setFlows(result.flows ?? []);
    setWorkerEnabled(result.worker_enabled);
  }
  useEffect(() => {
    if (!active || loaded.current) return;
    const controller = new AbortController();
    setLoading(true);
    void request<{ flows: Flow[]; worker_enabled: boolean }>(
      "/api/v1/marketing/flows",
      {
        signal: controller.signal,
      },
    )
      .then((result) => {
        if (!controller.signal.aborted) {
          setFlows(result.flows ?? []);
          setWorkerEnabled(result.worker_enabled);
          loaded.current = true;
        }
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setNotice(apiError(err));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [active, apiError]);
  function selectFlow(flow: Flow) {
    if (!flow.draft) return;
    setSelected(flow);
    setGraph(flow.draft);
    setName(flow.name);
    setDirty(false);
    setPreview(null);
    setVersions(null);
    setSnapshot(null);
    setInstances(null);
    setExecutions(null);
    setInstanceCursor(0);
    setVersionCursor(0);
    setInstanceEmail("");
    setRetry(null);
    setReason("");
    setNotAccepted(false);
    setProblems([]);
    setConfirmed(false);
    eventKey.current = null;
  }
  function edit(nodes: FlowNode[], entry = graph.entry) {
    setGraph({ ...graph, nodes, entry });
    setDirty(true);
    setPreview(null);
    setProblems([]);
  }
  function patch(id: string, values: Partial<FlowNode>) {
    edit(
      graph.nodes.map((node) =>
        node.id === id ? { ...node, ...values } : node,
      ),
    );
  }
  function addNode(kind: Kind) {
    if (graph.nodes.length >= 20) return;
    const node: FlowNode = {
      id: `n_${crypto.randomUUID().slice(0, 8)}`,
      kind,
      ...(kind === "send"
        ? { template_id: templates[0]?.id }
        : kind === "wait"
          ? { wait_seconds: 3600 }
          : kind === "condition"
            ? {
                field: "email_domain",
                operator: "equals",
                value: "example.test",
              }
            : {}),
    };
    edit([...graph.nodes, node]);
  }
  function removeNode(id: string) {
    const nodes = graph.nodes
      .filter((node) => node.id !== id)
      .map((node) => ({
        ...node,
        next: node.next === id ? "" : node.next,
        otherwise: node.otherwise === id ? "" : node.otherwise,
      }));
    edit(nodes, graph.entry === id ? nodes[0].id : graph.entry);
  }
  function moveNode(index: number, offset: number) {
    const nodes = [...graph.nodes];
    [nodes[index], nodes[index + offset]] = [
      nodes[index + offset],
      nodes[index],
    ];
    edit(nodes);
  }
  const fieldError = (id: string, field: string) =>
    problems.find((p) => p.node === id && p.field === field);
  function target(node: FlowNode, field: "next" | "otherwise", label: string) {
    const id = `flow-${node.id}-${field}`;
    return (
      <label className="grid gap-1 text-sm" htmlFor={id}>
        {t(label)}
        <select
          id={id}
          className={selectClass}
          value={node[field] ?? ""}
          aria-invalid={!!fieldError(node.id, field)}
          onChange={(event) => patch(node.id, { [field]: event.target.value })}
        >
          <option value="">{t("marketing.flow.choose_node")}</option>
          {graph.nodes
            .filter((item) => item.id !== node.id)
            .map((item) => (
              <option key={item.id} value={item.id}>
                {nodeLabel(item.id)}
              </option>
            ))}
        </select>
      </label>
    );
  }
  async function loadInstances(cursor = 0) {
    const result = await request<{
      instances: Instance[];
      next_cursor: number;
    }>(
      `${base}/instances?before=${cursor}&email=${encodeURIComponent(instanceEmail.trim())}`,
    );
    setInstances((previous) =>
      cursor ? [...(previous ?? []), ...result.instances] : result.instances,
    );
    setInstanceCursor(result.next_cursor);
  }
  if (!active) return null;
  return (
    <section aria-label={t("marketing.flow.title")} className="space-y-6">
      <header className="space-y-2">
        <h2 className="text-lg font-semibold">{t("marketing.flow.title")}</h2>
        <p className="max-w-prose text-sm text-muted-foreground">
          {t("marketing.flow.intro")}
        </p>
      </header>
      <Button
        variant="outline"
        disabled={busy || loading}
        onClick={() => void run(refresh)}
      >
        {t("marketing.flow.refresh")}
      </Button>
      {!workerEnabled && !loading && (
        <p role="status" className="max-w-prose text-sm text-muted-foreground">
          {t("marketing.flow.worker_disabled")}
        </p>
      )}
      {notice && (
        <p role="alert" className="text-sm text-destructive">
          {notice}
        </p>
      )}
      {success && (
        <p role="status" className="text-sm">
          {success}
        </p>
      )}
      <fieldset disabled={busy} className="min-w-0 space-y-6">
        {loading ? (
          <p role="status">{t("state.loading")}</p>
        ) : (
          <div className="space-y-4 rounded-lg border p-4">
            <form
              className="flex flex-wrap items-end gap-3"
              onSubmit={(event) => {
                event.preventDefault();
                void run(async () => {
                  const result = await request<{ flow: Flow }>(
                    "/api/v1/marketing/flows",
                    {
                      method: "POST",
                      body: {
                        name: newName,
                        draft: freshDefinition(templates[0]?.id),
                      },
                    },
                  );
                  await refresh();
                  selectFlow(result.flow);
                  setNewName("");
                });
              }}
            >
              <label className="grid min-w-0 flex-1 gap-1 text-sm">
                {t("marketing.flow.new_name")}
                <Input
                  value={newName}
                  onChange={(event) => setNewName(event.target.value)}
                  maxLength={200}
                  required
                />
              </label>
              <Button disabled={busy || dirty}>
                {t("marketing.flow.create")}
              </Button>
            </form>
            {flows.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                {t("marketing.flow.empty")}
              </p>
            ) : (
              <ul className="divide-y border-t">
                {flows.map((flow) => (
                  <li
                    key={flow.id}
                    className="flex flex-wrap items-center justify-between gap-3 py-3"
                  >
                    <div className="min-w-0">
                      <p className="break-words font-medium">{flow.name}</p>
                      <p className="text-sm text-muted-foreground">
                        {t(`marketing.flow.state.${flow.status}`)} ·{" "}
                        {t("marketing.flow.version_label", {
                          version: flow.published_version,
                        })}
                      </p>
                    </div>
                    <Button
                      variant={
                        selected?.id === flow.id ? "secondary" : "outline"
                      }
                      disabled={busy || dirty}
                      onClick={() =>
                        void run(async () => {
                          const result = await request<{ flow: Flow }>(
                            `/api/v1/marketing/flows/${flow.id}`,
                          );
                          selectFlow(result.flow);
                        })
                      }
                    >
                      {t("marketing.flow.edit")}
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {selected && (
          <>
            <section
              className="space-y-4 border-t pt-5"
              aria-label={t("marketing.flow.editor")}
            >
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <h3 className="font-semibold">
                    {t("marketing.flow.editor")}
                  </h3>
                  <p className="mt-1 text-sm text-muted-foreground">
                    {t("marketing.flow.revision", {
                      revision: selected.draft_revision,
                      version: selected.published_version,
                    })}
                    {dirty ? ` · ${t("marketing.flow.unsaved")}` : ""}
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    disabled={busy || !name.trim()}
                    onClick={() =>
                      void run(async () => {
                        const result = await request<{ flow: Flow }>(base, {
                          method: "PUT",
                          body: {
                            name,
                            draft: graph,
                            expected_revision: selected.draft_revision,
                          },
                        });
                        setSelected(result.flow);
                        setDirty(false);
                        await refresh();
                        setSuccess(t("marketing.flow.saved"));
                      })
                    }
                  >
                    {t("marketing.flow.save")}
                  </Button>
                  <Button
                    variant="outline"
                    disabled={busy || !dirty}
                    onClick={() =>
                      void run(async () => {
                        const result = await request<{ flow: Flow }>(base);
                        selectFlow(result.flow);
                      })
                    }
                  >
                    {t("marketing.flow.discard")}
                  </Button>
                  <Button
                    variant="outline"
                    disabled={busy || dirty || !channelAvailable}
                    onClick={() =>
                      void run(async () => {
                        await request(`${base}/publish`, {
                          method: "POST",
                          body: { expected_revision: selected.draft_revision },
                        });
                        const result = await request<{ flow: Flow }>(base);
                        setSelected(result.flow);
                        await refresh();
                        setSuccess(t("marketing.flow.published"));
                      })
                    }
                  >
                    {t("marketing.flow.publish")}
                  </Button>
                  {selected.status === "active" && (
                    <Button
                      variant="outline"
                      disabled={busy}
                      onClick={() =>
                        void run(async () => {
                          await request(`${base}/deactivate`, {
                            method: "POST",
                            body: {},
                          });
                          const result = await request<{ flow: Flow }>(base);
                          setSelected(result.flow);
                          await refresh();
                          setSuccess(t("marketing.flow.deactivated"));
                        })
                      }
                    >
                      {t("marketing.flow.deactivate")}
                    </Button>
                  )}
                </div>
              </div>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t("marketing.flow.version_policy")}
              </p>
              {!channelAvailable && (
                <p className="text-sm text-muted-foreground">
                  {t("marketing.flow.channel_required")}
                </p>
              )}
              <div className="grid gap-4 sm:grid-cols-2">
                <label className="grid gap-1 text-sm">
                  {t("marketing.name")}
                  <Input
                    value={name}
                    onChange={(event) => {
                      setName(event.target.value);
                      setDirty(true);
                    }}
                    maxLength={200}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("marketing.flow.entry_node")}
                  <select
                    id="flow-entry"
                    className={selectClass}
                    value={graph.entry}
                    onChange={(event) => edit(graph.nodes, event.target.value)}
                  >
                    {graph.nodes.map((node) => (
                      <option key={node.id} value={node.id}>
                        {nodeLabel(node.id)}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              {problems.length > 0 && (
                <div
                  role="alert"
                  className="space-y-2 rounded-md border border-destructive p-3 text-sm"
                >
                  <p className="font-medium">
                    {t("marketing.flow.validation")}
                  </p>
                  <ul className="space-y-1">
                    {problems.map((problem, index) => (
                      <li key={`${problem.node}-${problem.field}-${index}`}>
                        <button
                          type="button"
                          className="text-left underline underline-offset-2"
                          onClick={() =>
                            document
                              .getElementById(
                                problem.node
                                  ? `flow-${problem.node}-${problem.field}`
                                  : "flow-entry",
                              )
                              ?.focus()
                          }
                        >
                          {problem.node ? `${nodeLabel(problem.node)}: ` : ""}
                          {t(`marketing.flow.error.${problem.code}`)}
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              <ol className="space-y-4">
                {graph.nodes.map((node, index) => (
                  <li key={node.id} className="space-y-4 rounded-lg border p-4">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <h4 className="font-medium">{nodeLabel(node.id)}</h4>
                      <div className="flex gap-1">
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-11 w-11 sm:h-9 sm:w-9"
                          aria-label={t("marketing.flow.move_up", {
                            number: index + 1,
                          })}
                          disabled={busy || index === 0}
                          onClick={() => moveNode(index, -1)}
                        >
                          <ArrowUp className="size-4" />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-11 w-11 sm:h-9 sm:w-9"
                          aria-label={t("marketing.flow.move_down", {
                            number: index + 1,
                          })}
                          disabled={busy || index === graph.nodes.length - 1}
                          onClick={() => moveNode(index, 1)}
                        >
                          <ArrowDown className="size-4" />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="h-11 w-11 sm:h-9 sm:w-9"
                          aria-label={t("marketing.flow.remove_node", {
                            number: index + 1,
                          })}
                          disabled={busy || graph.nodes.length === 1}
                          onClick={() => removeNode(node.id)}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </div>
                    <div className="grid gap-4 sm:grid-cols-2">
                      <label className="grid gap-1 text-sm">
                        {t("marketing.flow.node_type")}
                        <select
                          id={`flow-${node.id}-kind`}
                          className={selectClass}
                          value={node.kind}
                          disabled={busy}
                          onChange={(event) => {
                            const kind = event.target.value as Kind;
                            edit(
                              graph.nodes.map((item) =>
                                item.id === node.id
                                  ? {
                                      id: node.id,
                                      kind,
                                      ...(kind === "send"
                                        ? { template_id: templates[0]?.id }
                                        : kind === "wait"
                                          ? { wait_seconds: 3600 }
                                          : kind === "condition"
                                            ? {
                                                field: "email_domain",
                                                operator: "equals",
                                                value: "example.test",
                                              }
                                            : {}),
                                    }
                                  : item,
                              ),
                            );
                          }}
                        >
                          {(["send", "wait", "condition", "stop"] as const).map(
                            (kind) => (
                              <option key={kind} value={kind}>
                                {t(`marketing.flow.kind.${kind}`)}
                              </option>
                            ),
                          )}
                        </select>
                      </label>
                      {node.kind === "send" && (
                        <label className="grid gap-1 text-sm">
                          {t("marketing.template")}
                          <select
                            id={`flow-${node.id}-template_id`}
                            className={selectClass}
                            value={node.template_id ?? ""}
                            disabled={busy}
                            aria-invalid={!!fieldError(node.id, "template_id")}
                            onChange={(event) =>
                              patch(node.id, {
                                template_id:
                                  Number(event.target.value) || undefined,
                              })
                            }
                          >
                            <option value="">{t("marketing.choose")}</option>
                            {templates.map((template) => (
                              <option key={template.id} value={template.id}>
                                {template.name}
                              </option>
                            ))}
                          </select>
                        </label>
                      )}
                      {node.kind === "wait" && (
                        <label className="grid gap-1 text-sm">
                          {t("marketing.flow.wait_seconds")}
                          <Input
                            id={`flow-${node.id}-wait_seconds`}
                            type="number"
                            min={1}
                            max={2592000}
                            value={node.wait_seconds ?? 3600}
                            disabled={busy}
                            aria-invalid={!!fieldError(node.id, "wait_seconds")}
                            onChange={(event) =>
                              patch(node.id, {
                                wait_seconds: Number(event.target.value),
                              })
                            }
                          />
                          <span className="text-xs text-muted-foreground">
                            {t("marketing.flow.wait_hint")}
                          </span>
                        </label>
                      )}
                      {node.kind === "condition" && (
                        <>
                          <label className="grid gap-1 text-sm">
                            {t("marketing.flow.condition_field")}
                            <select
                              id={`flow-${node.id}-field`}
                              className={selectClass}
                              value={node.field}
                              disabled={busy}
                              onChange={(event) => {
                                const field = event.target.value as Field;
                                patch(node.id, {
                                  field,
                                  value:
                                    field === "consented"
                                      ? "true"
                                      : field === "source"
                                        ? "contact_enrolled"
                                        : field === "list_id"
                                          ? String(lists[0]?.id ?? "")
                                          : "example.test",
                                });
                              }}
                            >
                              {(
                                [
                                  "email_domain",
                                  "list_id",
                                  "source",
                                  "consented",
                                ] as const
                              ).map((field) => (
                                <option key={field} value={field}>
                                  {t(`marketing.flow.field.${field}`)}
                                </option>
                              ))}
                            </select>
                          </label>
                          <label className="grid gap-1 text-sm">
                            {t("marketing.flow.operator")}
                            <select
                              id={`flow-${node.id}-operator`}
                              className={selectClass}
                              value={node.operator}
                              onChange={(event) =>
                                patch(node.id, {
                                  operator: event.target.value as
                                    "equals" | "not_equals",
                                })
                              }
                            >
                              <option value="equals">
                                {t("marketing.flow.equals")}
                              </option>
                              <option value="not_equals">
                                {t("marketing.flow.not_equals")}
                              </option>
                            </select>
                          </label>
                          <label className="grid gap-1 text-sm">
                            {t("marketing.flow.condition_value")}
                            {node.field === "list_id" ? (
                              <select
                                id={`flow-${node.id}-value`}
                                className={selectClass}
                                value={node.value}
                                onChange={(event) =>
                                  patch(node.id, { value: event.target.value })
                                }
                              >
                                {lists.map((list) => (
                                  <option key={list.id} value={list.id}>
                                    {list.name}
                                  </option>
                                ))}
                              </select>
                            ) : node.field === "source" ||
                              node.field === "consented" ? (
                              <select
                                id={`flow-${node.id}-value`}
                                className={selectClass}
                                value={node.value}
                                onChange={(event) =>
                                  patch(node.id, { value: event.target.value })
                                }
                              >
                                {(node.field === "source"
                                  ? ["contact_enrolled"]
                                  : ["true", "false"]
                                ).map((value) => (
                                  <option key={value} value={value}>
                                    {t(`marketing.flow.value.${value}`)}
                                  </option>
                                ))}
                              </select>
                            ) : (
                              <Input
                                id={`flow-${node.id}-value`}
                                value={node.value ?? ""}
                                maxLength={253}
                                onChange={(event) =>
                                  patch(node.id, {
                                    value: event.target.value.toLowerCase(),
                                  })
                                }
                              />
                            )}
                          </label>
                        </>
                      )}
                      {node.kind !== "stop" &&
                        target(
                          node,
                          "next",
                          node.kind === "condition"
                            ? "marketing.flow.when_true"
                            : "marketing.flow.next_node",
                        )}
                      {node.kind === "condition" &&
                        target(node, "otherwise", "marketing.flow.when_false")}
                    </div>
                  </li>
                ))}
              </ol>
              <div className="flex flex-wrap gap-2">
                {(["send", "wait", "condition", "stop"] as const).map(
                  (kind) => (
                    <Button
                      type="button"
                      key={kind}
                      variant="outline"
                      disabled={busy || graph.nodes.length >= 20}
                      onClick={() => addNode(kind)}
                    >
                      <Plus className="size-4" />
                      {t("marketing.flow.add_kind", {
                        kind: t(`marketing.flow.kind.${kind}`),
                      })}
                    </Button>
                  ),
                )}
              </div>
            </section>
            <section
              className="space-y-4 border-t pt-5"
              aria-label={t("marketing.flow.preview")}
            >
              <h3 className="font-semibold">{t("marketing.flow.preview")}</h3>
              <p className="text-sm text-muted-foreground">
                {t("marketing.flow.preview_hint")}
              </p>
              <form
                className="grid gap-3 sm:grid-cols-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  void run(async () => {
                    const result = await request<{ path: PreviewStep[] }>(
                      `${base}/preview`,
                      {
                        method: "POST",
                        body: {
                          draft: graph,
                          sample_email: sampleEmail,
                          list_id: Number(listID),
                        },
                      },
                    );
                    setPreview(result.path);
                  });
                }}
              >
                <label className="grid gap-1 text-sm">
                  {t("marketing.flow.sample_email")}
                  <Input
                    type="email"
                    value={sampleEmail}
                    required
                    onChange={(event) => setSampleEmail(event.target.value)}
                  />
                </label>
                <label className="grid gap-1 text-sm">
                  {t("marketing.flow.sample_list")}
                  <select
                    className={selectClass}
                    value={listID}
                    onChange={(event) => {
                      setListID(event.target.value);
                      eventKey.current = null;
                    }}
                  >
                    <option value="">
                      {t("marketing.flow.no_sample_list")}
                    </option>
                    {lists.map((list) => (
                      <option key={list.id} value={list.id}>
                        {list.name}
                      </option>
                    ))}
                  </select>
                </label>
                <Button variant="outline" disabled={busy}>
                  {t("marketing.flow.run_preview")}
                </Button>
              </form>
              {preview && (
                <ol className="space-y-3">
                  {preview.map((step) => (
                    <li key={step.node} className="space-y-2 border-b pb-3">
                      <p className="font-medium">{nodeLabel(step.node)}</p>
                      <p className="text-sm text-muted-foreground">
                        {date(step.at)}
                        {step.kind === "condition"
                          ? ` · ${t(step.matched ? "marketing.flow.branch_true" : "marketing.flow.branch_false")}`
                          : ""}
                        {step.due_at
                          ? ` · ${t("marketing.flow.due_at", { at: date(step.due_at) })}`
                          : ""}
                      </p>
                      {step.subject && (
                        <p className="break-words text-sm font-medium">
                          {step.subject}
                        </p>
                      )}
                      {step.text_body && (
                        <p className="max-h-64 overflow-auto whitespace-pre-wrap break-words text-sm">
                          {step.text_body}
                        </p>
                      )}
                    </li>
                  ))}
                </ol>
              )}
            </section>
            <section
              className="space-y-4 border-t pt-5"
              aria-label={t("marketing.flow.enrollment")}
            >
              <h3 className="font-semibold">
                {t("marketing.flow.enrollment")}
              </h3>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t("marketing.flow.enrollment_policy")}
              </p>
              <form
                className="grid gap-3"
                onSubmit={(event) => {
                  event.preventDefault();
                  void run(async () => {
                    eventKey.current ??= crypto.randomUUID();
                    const result = await request<{ enrolled_count: number }>(
                      `${base}/events`,
                      {
                        method: "POST",
                        body: {
                          event_key: eventKey.current,
                          source: "contact_enrolled",
                          list_id: Number(listID),
                        },
                      },
                    );
                    eventKey.current = null;
                    setConfirmed(false);
                    setSuccess(
                      t("marketing.flow.enrolled", {
                        count: result.enrolled_count,
                      }),
                    );
                    await loadInstances();
                  });
                }}
              >
                <label className="grid max-w-md gap-1 text-sm">
                  {t("marketing.list")}
                  <select
                    className={selectClass}
                    value={listID}
                    required
                    disabled={busy}
                    onChange={(event) => {
                      setListID(event.target.value);
                      eventKey.current = null;
                    }}
                  >
                    <option value="">{t("marketing.choose")}</option>
                    {lists.map((list) => (
                      <option key={list.id} value={list.id}>
                        {list.name}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="flex items-start gap-2 text-sm">
                  <input
                    className="mt-1"
                    type="checkbox"
                    checked={confirmed}
                    required
                    onChange={(event) => setConfirmed(event.target.checked)}
                  />
                  {t("marketing.flow.enrollment_confirm")}
                </label>
                <Button
                  className="justify-self-start"
                  disabled={
                    busy ||
                    selected.status !== "active" ||
                    !workerEnabled ||
                    !listID ||
                    !confirmed ||
                    dirty
                  }
                >
                  {t("marketing.flow.start")}
                </Button>
              </form>
            </section>
            <section
              className="space-y-4 border-t pt-5"
              aria-label={t("marketing.flow.instances")}
            >
              <div className="flex flex-wrap items-center justify-between gap-3">
                <h3 className="font-semibold">
                  {t("marketing.flow.instances")}
                </h3>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => void run(() => loadInstances())}
                >
                  {t("marketing.flow.refresh_instances")}
                </Button>
              </div>
              <p className="max-w-prose text-sm text-muted-foreground">
                {t("marketing.flow.delivery_policy")}
              </p>
              <form
                className="flex flex-wrap gap-3"
                onSubmit={(event) => {
                  event.preventDefault();
                  void run(() => loadInstances());
                }}
              >
                <label className="grid min-w-0 flex-1 gap-1 text-sm">
                  {t("marketing.flow.filter_email")}
                  <Input
                    value={instanceEmail}
                    type="email"
                    onChange={(event) => setInstanceEmail(event.target.value)}
                  />
                </label>
                <Button className="self-end" variant="outline" disabled={busy}>
                  {t("marketing.flow.search_instances")}
                </Button>
              </form>
              {instances &&
                (instances.length ? (
                  <ul className="divide-y rounded-lg border">
                    {instances.map((instance) => (
                      <li key={instance.id} className="space-y-3 p-4">
                        <div className="flex flex-wrap items-start justify-between gap-3">
                          <div className="min-w-0 space-y-1">
                            <p className="break-all font-medium">
                              {instance.email}
                            </p>
                            <p className="text-sm">
                              {t(`marketing.flow.instance.${instance.state}`)} ·{" "}
                              {t("marketing.flow.version_label", {
                                version: instance.version,
                              })}{" "}
                              · {instance.node}
                            </p>
                            <p className="text-sm text-muted-foreground">
                              {t("marketing.flow.execution_count", {
                                count: instance.attempt_count,
                              })}
                              {instance.wait_until
                                ? ` · ${t("marketing.flow.due_at", { at: date(instance.wait_until) })}`
                                : ""}
                            </p>
                            {instance.delivery_uncertain &&
                              ["stopped", "unsubscribed"].includes(
                                instance.state,
                              ) && (
                                <p className="text-sm text-muted-foreground">
                                  {t("marketing.flow.unknown_after_stop")}
                                </p>
                              )}
                            {instance.error_code && (
                              <p className="text-sm text-destructive">
                                {t(
                                  `marketing.flow.reason.${instance.error_code}`,
                                )}
                              </p>
                            )}
                          </div>
                          <div className="flex flex-wrap gap-2">
                            <Button
                              variant="outline"
                              disabled={busy}
                              onClick={() =>
                                void run(async () => {
                                  const result = await request<{
                                    executions: Execution[];
                                  }>(
                                    `${base}/instances/${instance.id}/executions`,
                                  );
                                  setExecutions({
                                    instance,
                                    rows: result.executions,
                                  });
                                })
                              }
                            >
                              {t("marketing.flow.execution_history")}
                            </Button>
                            {["failed", "uncertain"].includes(
                              instance.state,
                            ) && (
                              <Button
                                variant="outline"
                                disabled={busy}
                                onClick={() => {
                                  setRetry(instance);
                                  setReason("");
                                  setNotAccepted(false);
                                }}
                              >
                                {t("marketing.flow.retry")}
                              </Button>
                            )}
                            {!["completed", "stopped", "unsubscribed"].includes(
                              instance.state,
                            ) && (
                              <Button
                                variant="outline"
                                disabled={busy}
                                onClick={() =>
                                  void run(async () => {
                                    await request(
                                      `${base}/instances/${instance.id}/stop`,
                                      { method: "POST", body: {} },
                                    );
                                    await loadInstances();
                                  })
                                }
                              >
                                {t("marketing.flow.stop_instance")}
                              </Button>
                            )}
                          </div>
                        </div>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    {t("marketing.flow.no_instances")}
                  </p>
                ))}
              {instanceCursor > 0 && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => void run(() => loadInstances(instanceCursor))}
                >
                  {t("marketing.flow.load_more")}
                </Button>
              )}
              {executions && (
                <div className="space-y-3 rounded-lg border p-4">
                  <h4 className="break-all font-medium">
                    {executions.instance.email}
                  </h4>
                  {executions.rows.length ? (
                    <ol className="space-y-2 text-sm">
                      {executions.rows.map((row, index) => (
                        <li key={`${row.created_at}-${index}`}>
                          {date(row.created_at)} · {row.node_key} ·{" "}
                          {t(`marketing.flow.execution.${row.status}`)}
                          {row.error_code
                            ? ` · ${t(`marketing.flow.reason.${row.error_code}`)}`
                            : ""}
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <p className="text-sm text-muted-foreground">
                      {t("marketing.flow.no_executions")}
                    </p>
                  )}
                </div>
              )}
            </section>
            <section className="space-y-4 border-t pt-5">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <h3 className="font-semibold">
                  {t("marketing.flow.versions")}
                </h3>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      const result = await request<{
                        versions: Version[];
                        next_cursor: number;
                      }>(`${base}/versions`);
                      setVersions(result.versions);
                      setVersionCursor(result.next_cursor);
                    })
                  }
                >
                  {t("marketing.flow.show_versions")}
                </Button>
              </div>
              {versions && (
                <ul className="space-y-2 text-sm">
                  {versions.map((version) => (
                    <li key={version.id}>
                      {t("marketing.flow.version_audit", {
                        version: version.version,
                        revision: version.draft_revision,
                        actor: version.published_by,
                        at: date(version.created_at),
                      })}
                      <Button
                        variant="link"
                        disabled={busy}
                        onClick={() =>
                          void run(async () => {
                            const result = await request<
                              NonNullable<typeof snapshot>
                            >(`${base}/versions/${version.version}`);
                            setSnapshot(result);
                          })
                        }
                      >
                        {t("marketing.flow.inspect_version")}
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
              {snapshot && (
                <section
                  aria-label={t("marketing.flow.snapshot_title", {
                    version: snapshot.version,
                  })}
                  className="min-w-0 space-y-4 rounded-lg border p-4"
                >
                  <h4 className="font-medium">
                    {t("marketing.flow.snapshot_title", {
                      version: snapshot.version,
                    })}
                  </h4>
                  <p className="break-words text-sm">
                    {t("marketing.flow.entry_node")}:{" "}
                    {snapshot.definition.entry}
                  </p>
                  <ol className="space-y-4 text-sm">
                    {snapshot.definition.nodes.map((node) => {
                      const content = snapshot.templates.find(
                        (template) => template.step_key === node.id,
                      );
                      return (
                        <li
                          key={node.id}
                          className="min-w-0 space-y-2 border-t pt-3"
                        >
                          <p className="break-words font-medium">
                            {node.id} · {t(`marketing.flow.kind.${node.kind}`)}
                          </p>
                          {node.kind === "wait" && (
                            <p>
                              {t("marketing.flow.snapshot_wait", {
                                seconds: node.wait_seconds,
                              })}
                            </p>
                          )}
                          {node.kind === "condition" && (
                            <p className="break-words">
                              {t(`marketing.flow.field.${node.field}`)} ·{" "}
                              {t(`marketing.flow.${node.operator}`)} ·{" "}
                              {node.value}
                            </p>
                          )}
                          {content && (
                            <>
                              <p className="break-words font-medium">
                                {content.subject}
                              </p>
                              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words font-sans">
                                {content.text_body}
                              </pre>
                            </>
                          )}
                          {node.next && (
                            <p className="break-words">
                              {t(
                                node.kind === "condition"
                                  ? "marketing.flow.when_true"
                                  : "marketing.flow.next_node",
                              )}
                              : {node.next}
                            </p>
                          )}
                          {node.otherwise && (
                            <p className="break-words">
                              {t("marketing.flow.when_false")}: {node.otherwise}
                            </p>
                          )}
                        </li>
                      );
                    })}
                  </ol>
                </section>
              )}
              {versionCursor > 0 && (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      const result = await request<{
                        versions: Version[];
                        next_cursor: number;
                      }>(`${base}/versions?before=${versionCursor}`);
                      setVersions((previous) => [
                        ...(previous ?? []),
                        ...result.versions,
                      ]);
                      setVersionCursor(result.next_cursor);
                    })
                  }
                >
                  {t("marketing.flow.load_more")}
                </Button>
              )}
            </section>
          </>
        )}
      </fieldset>
      <Dialog
        open={retry !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setRetry(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("marketing.flow.retry_title")}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!retry) return;
              const current = retry;
              void run(async () => {
                await request(`${base}/instances/${current.id}/retry`, {
                  method: "POST",
                  body: { reason, confirmed_not_accepted: notAccepted },
                });
                setRetry(null);
                await loadInstances();
              });
            }}
          >
            <p className="break-all text-sm">{retry?.email}</p>
            <p className="text-sm text-muted-foreground">
              {t("marketing.flow.retry_policy")}
            </p>
            <label className="grid gap-1 text-sm">
              {t("marketing.flow.retry_reason")}
              <Input
                required
                minLength={10}
                maxLength={2000}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
              />
            </label>
            {retry?.delivery_uncertain && (
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  required
                  checked={notAccepted}
                  onChange={(event) => setNotAccepted(event.target.checked)}
                  className="mt-1"
                />
                {t("marketing.flow.confirm_not_accepted")}
              </label>
            )}
            <Button
              disabled={
                busy ||
                reason.trim().length < 10 ||
                (!!retry?.delivery_uncertain && !notAccepted)
              }
            >
              {t("marketing.flow.confirm_retry")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </section>
  );
}
