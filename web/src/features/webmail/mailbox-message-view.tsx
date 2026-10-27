import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronUp, ImageOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { sanitizeHtmlMail } from "./sanitize-html-mail";

/**
 * Sandboxed, bounded HTML message body (WEBMAIL-007).
 *
 * The untrusted document renders in an iframe sandboxed WITHOUT
 * allow-scripts and WITHOUT allow-same-origin, fed via srcDoc of the
 * server-sanitized HTML. Height is capped with an expand toggle so a
 * hostile message cannot drive unbounded layout. A privacy bar exposes
 * the "load images" action while remote images are blocked.
 */
export function MessageHTMLFrame({
  html,
  notes,
  loadImages,
  onLoadImages,
}: {
  html: string;
  notes: string[];
  loadImages: boolean;
  onLoadImages: () => void;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);

  // Reset the expanded state when switching messages.
  useEffect(() => {
    setExpanded(false);
  }, [html]);

  const hasBlockedImages = notes.includes("remote_images_blocked");
  const truncated = notes.includes("html_truncated");

  return (
    <div className="space-y-2">
      {(hasBlockedImages || truncated) && (
        <div className="flex flex-wrap items-center gap-2 rounded border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
          <ImageOff className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1">
            {truncated
              ? t("page.webmail.html_truncated")
              : t("page.webmail.remote_images_blocked")}
          </span>
          {hasBlockedImages && !loadImages && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="min-h-11 md:min-h-8"
              onClick={onLoadImages}
            >
              {t("page.webmail.load_images")}
            </Button>
          )}
        </div>
      )}
      <iframe
        title={t("page.webmail.message_body")}
        // No allow-scripts, no allow-same-origin: the mail document cannot
        // run script or touch app storage/tokens even if sanitizer layers
        // were ever bypassed.
        sandbox=""
        referrerPolicy="no-referrer"
        className={
          "w-full rounded border bg-background " +
          (expanded ? "h-[80vh]" : "h-[420px]")
        }
        srcDoc={sanitizeHtmlMail(html, { allowRemoteImages: loadImages })}
      />
      <div className="flex justify-end">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="min-h-11 md:min-h-8"
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? (
            <ChevronUp className="h-3.5 w-3.5 mr-1" />
          ) : (
            <ChevronDown className="h-3.5 w-3.5 mr-1" />
          )}
          {expanded
            ? t("page.webmail.collapse_body")
            : t("page.webmail.expand_body")}
        </Button>
      </div>
    </div>
  );
}
