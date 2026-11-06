import { sanitizeHtmlMail } from "./sanitize-html-mail";

type SanitizeOptions = {
  allowRemoteImages?: boolean;
  inlineImages?: Record<string, string>;
};

type SanitizeRequest = {
  id: number;
  html: string;
  allowRemoteImages?: boolean;
  inlineImages?: Record<string, string>;
};

type SanitizeResponse = {
  id: number;
  html?: string;
  error?: string;
};

type SanitizeWorker = {
  postMessage(message: SanitizeRequest): void;
  onmessage: ((event: MessageEvent<SanitizeResponse>) => void) | null;
  onerror: ((event: Event) => void) | null;
  terminate?: () => void;
};

type PendingJob = {
  html: string;
  opts?: SanitizeOptions;
  resolve: (html: string) => void;
};

let worker: SanitizeWorker | null = null;
let workerBroken = false;
let nextId = 1;
let factoryOverride: (() => SanitizeWorker | null) | null = null;
const pending = new Map<number, PendingJob>();

function fallback(html: string, opts?: SanitizeOptions): string {
  return sanitizeHtmlMail(html, opts);
}

function failWorker(): void {
  workerBroken = true;
  worker?.terminate?.();
  worker = null;
  for (const [id, job] of pending) {
    pending.delete(id);
    job.resolve(fallback(job.html, job.opts));
  }
}

function bindWorker(created: SanitizeWorker): void {
  created.onmessage = (event) => {
    const data = event.data;
    const job = pending.get(data?.id);
    if (!job) return;
    pending.delete(data.id);
    if (typeof data.html === "string") job.resolve(data.html);
    else job.resolve(fallback(job.html, job.opts));
  };
  created.onerror = () => {
    failWorker();
  };
  worker = created;
}

function getWorker(): SanitizeWorker | null {
  if (workerBroken) return null;
  if (worker) return worker;
  try {
    const created = factoryOverride
      ? factoryOverride()
      : typeof Worker === "undefined"
        ? null
        : (new Worker(
            new URL("./sanitize-html-mail.worker.ts", import.meta.url),
            { type: "module" },
          ) as unknown as SanitizeWorker);
    if (!created) {
      workerBroken = true;
      return null;
    }
    bindWorker(created);
    return created;
  } catch {
    workerBroken = true;
    return null;
  }
}

/** True when a worker can be started. A missing Worker uses the sync sanitizer. */
export function sanitizeWorkerAvailable(): boolean {
  if (workerBroken) return false;
  if (factoryOverride) return true;
  return typeof Worker !== "undefined";
}

export function sanitizeHtmlMailAsync(
  html: string,
  opts?: SanitizeOptions,
): Promise<string> {
  if (!html) return Promise.resolve("");
  const current = getWorker();
  if (!current) return Promise.resolve(fallback(html, opts));
  return new Promise((resolve) => {
    const id = nextId++;
    const job: PendingJob = { html, opts, resolve };
    pending.set(id, job);
    try {
      current.postMessage({
        id,
        html,
        allowRemoteImages: opts?.allowRemoteImages === true,
        inlineImages: opts?.inlineImages,
      });
    } catch {
      pending.delete(id);
      resolve(fallback(html, opts));
    }
  });
}

/** Test hook. Pass null to restore the default Worker constructor. */
export function resetSanitizeWorkerForTests(
  factory: (() => SanitizeWorker | null) | null = null,
): void {
  worker?.terminate?.();
  worker = null;
  workerBroken = false;
  factoryOverride = factory;
  for (const [id, job] of pending) {
    pending.delete(id);
    job.resolve(fallback(job.html, job.opts));
  }
}
