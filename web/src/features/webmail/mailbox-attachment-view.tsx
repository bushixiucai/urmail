import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import type { AttachmentMeta, MessageDetail } from "./mailbox-api";
import {
  attachmentURL,
  boundedAttachment,
  decodePreview,
  previewType,
  PREVIEW_LIMIT,
  PREVIEW_PIXELS,
  type AttachmentSource,
  type SafePreview,
} from "./mailbox-attachment-preview";
import { MessageHTMLFrame } from "./mailbox-message-view";

export function AttachmentPreview({
  source,
  attachment,
}: {
  source: AttachmentSource;
  attachment: AttachmentMeta;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [preview, setPreview] = useState<SafePreview | null>(null);
  const [error, setError] = useState(false);
  const { folderEnc, uid, uidValidity } = source;
  useEffect(() => {
    setPreview(null);
    setError(false);
    if (!open) return;
    const controller = new AbortController();
    void boundedAttachment(
      { folderEnc, uid, uidValidity },
      attachment.part_id,
      controller.signal,
    )
      .then((bytes) => decodePreview(bytes, attachment))
      .then((result) => {
        if (!controller.signal.aborted) setPreview(result);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [open, folderEnc, uid, uidValidity, attachment]);
  if (
    !previewType(attachment.content_type) ||
    attachment.size > PREVIEW_LIMIT ||
    !uidValidity
  )
    return null;
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" className="min-h-11 md:min-h-8">
          {t("page.webmail.preview")}
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="break-all">
            {attachment.filename || t("page.webmail.preview")}
          </DialogTitle>
          <DialogDescription>
            {t("page.webmail.preview_scope")}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <p role="alert">{t("page.webmail.preview_failed")}</p>
        ) : !preview ? (
          <p role="status">{t("state.loading")}</p>
        ) : preview.kind === "image" ? (
          <img
            src={preview.dataURL}
            alt={attachment.filename}
            className="mx-auto max-h-[65vh] max-w-full object-contain"
            onError={() => setError(true)}
          />
        ) : preview.kind === "pdf" ? (
          <iframe
            src={preview.dataURL}
            title={attachment.filename}
            className="w-full h-[65vh] rounded border"
          />
        ) : preview.kind === "office" ? (
          <div className="p-4 text-center space-y-3">
            <p className="text-sm text-muted-foreground">
              Office 文档需下载后使用本地软件打开，系统不上传至第三方预览服务。
            </p>
            <a
              href={`/api/v1/mailbox/folders/${encodeURIComponent(source.folderEnc)}/messages/${source.uid}/attachments/${attachment.part_id}?uid_validity=${source.uidValidity}`}
              download={attachment.filename}
              className="inline-flex items-center justify-center rounded-md text-sm font-medium h-9 px-4 py-2 bg-primary text-primary-foreground shadow hover:bg-primary/90"
            >
              {t("action.download")}
            </a>
          </div>
        ) : (
          <pre className="max-h-[65vh] overflow-auto whitespace-pre-wrap break-words text-sm">
            {preview.text}
          </pre>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** Fetch only surfaced MIME parts using the mailbox bearer, never sender URLs. */
export function MessageHTMLWithAttachments({
  message,
  source,
  loadImages,
  onLoadImages,
}: {
  message: MessageDetail;
  source: AttachmentSource;
  loadImages: boolean;
  onLoadImages: () => void;
}) {
  const [images, setImages] = useState<Record<string, string>>({});
  const { folderEnc, uid, uidValidity } = source;
  useEffect(() => {
    setImages({});
    if (!uidValidity) return;
    const controller = new AbortController();
    void (async () => {
      const result: Record<string, string> = {};
      let total = 0,
        pixels = 0;
      const current = { folderEnc, uid, uidValidity };
      const candidates = message.attachments
        .filter(
          (part) =>
            part.content_id && previewType(part.content_type) === "image",
        )
        .slice(0, 8);
      for (const part of candidates) {
        if (controller.signal.aborted) return;
        if (part.size > PREVIEW_LIMIT || part.size + total > 4 * PREVIEW_LIMIT)
          continue;
        try {
          const bytes = await boundedAttachment(
            current,
            part.part_id,
            controller.signal,
          );
          total += bytes.length;
          if (total > 4 * PREVIEW_LIMIT) break;
          const preview = decodePreview(bytes, part);
          if (
            preview.kind !== "image" ||
            pixels + preview.pixels > PREVIEW_PIXELS
          )
            continue;
          pixels += preview.pixels;
          const cid = part.content_id!.replace(/^[<\s]+|[>\s]+$/g, "");
          result[`cid:${cid}`] = preview.dataURL;
          result[attachmentURL(current, part.part_id)] = preview.dataURL;
        } catch {
          if (controller.signal.aborted) return;
        }
      }
      if (!controller.signal.aborted) setImages(result);
    })();
    return () => controller.abort();
  }, [message, folderEnc, uid, uidValidity]);
  return (
    <MessageHTMLFrame
      html={message.html ?? ""}
      notes={message.notes ?? []}
      loadImages={loadImages}
      onLoadImages={onLoadImages}
      inlineImages={images}
    />
  );
}
