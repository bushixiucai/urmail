import { sanitizeHtmlMail } from "./sanitize-html-mail";

type SanitizeRequest = {
  id: number;
  html: string;
  allowRemoteImages?: boolean;
  inlineImages?: Record<string, string>;
};

type WorkerScope = {
  onmessage: ((event: MessageEvent<SanitizeRequest>) => void) | null;
  postMessage(message: unknown): void;
};

const scope = globalThis as unknown as WorkerScope;

scope.onmessage = (event) => {
  const { id, html, allowRemoteImages, inlineImages } = event.data;
  try {
    const sanitized = sanitizeHtmlMail(html, {
      allowRemoteImages,
      inlineImages,
    });
    scope.postMessage({ id, html: sanitized });
  } catch (error) {
    scope.postMessage({
      id,
      error: error instanceof Error ? error.message : "sanitize failed",
    });
  }
};
