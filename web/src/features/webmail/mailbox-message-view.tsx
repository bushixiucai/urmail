import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronUp, ImageOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { sanitizeHtmlMail } from "./sanitize-html-mail";

/**
 * Sandboxed HTML message body (WEBMAIL-007).
 *
 * The untrusted document renders in an iframe sandboxed WITHOUT
 * allow-scripts and WITHOUT allow-same-origin, fed via srcDoc of the
 * server-sanitized HTML.
 *
 * Height adaptively conforms to content height (minimum 400px, no rigid upper bound)
 * when expanded to eliminate nested iframe scrollbars and allow smooth outer scrolling.
 * An expand toggle allows collapsing down to a bounded preview (420px).
 * A privacy bar exposes the "load images" action while remote images are blocked.
 */
export function MessageHTMLFrame({
  html,
  notes,
  loadImages,
  onLoadImages,
  inlineImages,
}: {
  html: string;
  notes: string[];
  loadImages: boolean;
  onLoadImages: () => void;
  inlineImages?: Record<string, string>;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [contentHeight, setContentHeight] = useState<number | null>(null);
  const iframeRef = useRef<HTMLIFrameElement>(null);

  const updateHeight = useCallback(() => {
    const iframe = iframeRef.current;
    if (!iframe) return;
    try {
      const doc = iframe.contentDocument || iframe.contentWindow?.document;
      if (doc) {
        const body = doc.body;
        const docEl = doc.documentElement;
        const scrollH = Math.max(
          body?.scrollHeight ?? 0,
          body?.offsetHeight ?? 0,
          docEl?.scrollHeight ?? 0,
          docEl?.offsetHeight ?? 0,
        );
        if (scrollH > 0) {
          // Provide buffer (+16px) to eliminate subpixel rounding scrollbars
          setContentHeight(Math.max(400, Math.ceil(scrollH) + 16));
        }
      }
    } catch {
      // In sandboxed frames without allow-same-origin, contentDocument access
      // may throw or be null depending on browser security policies.
    }
  }, []);

  // Reset the expanded and measured state when switching messages.
  useEffect(() => {
    setExpanded(false);
    setContentHeight(null);
  }, [html]);

  // Update height and attach ResizeObserver when contentDocument is accessible
  useEffect(() => {
    const iframe = iframeRef.current;
    if (!iframe) return;

    let ro: ResizeObserver | null = null;
    try {
      const doc = iframe.contentDocument || iframe.contentWindow?.document;
      const target = doc?.body || doc?.documentElement;
      if (target && typeof ResizeObserver !== "undefined") {
        ro = new ResizeObserver(() => {
          updateHeight();
        });
        ro.observe(target);
      }
    } catch {
      // Sandboxed access restricted
    }

    return () => {
      if (ro) ro.disconnect();
    };
  }, [html, loadImages, inlineImages, updateHeight]);

  // Re-measure height upon expand toggle
  useEffect(() => {
    if (expanded) {
      updateHeight();
    }
  }, [expanded, updateHeight]);

  const hasBlockedImages = notes.includes("remote_images_blocked");
  const truncated = notes.includes("html_truncated");

  return (
    <div className="space-y-2.5">
      {(hasBlockedImages || truncated) && (
        <div className="flex flex-wrap sm:flex-nowrap items-center justify-between gap-3 rounded-lg border border-border/70 bg-muted/30 px-3.5 py-2 text-xs text-muted-foreground shadow-2xs transition-colors backdrop-blur-[2px]">
          <div className="flex items-center gap-2.5 min-w-0 flex-1">
            <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border/60 bg-background text-muted-foreground shadow-2xs">
              <ImageOff
                className="h-3.5 w-3.5 shrink-0 text-muted-foreground/80"
                aria-hidden
              />
            </div>
            <span className="min-w-0 flex-1 font-medium text-foreground/80 leading-snug">
              {truncated
                ? t("page.webmail.html_truncated")
                : t("page.webmail.remote_images_blocked")}
            </span>
          </div>
          {hasBlockedImages && !loadImages && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="h-7 px-2.5 text-xs font-medium shrink-0 min-h-11 md:min-h-7 border-border/70 bg-background/80 hover:bg-accent hover:text-accent-foreground shadow-2xs transition-all active:scale-[0.98]"
              onClick={onLoadImages}
            >
              {t("page.webmail.load_images")}
            </Button>
          )}
        </div>
      )}
      <iframe
        ref={iframeRef}
        title={t("page.webmail.message_body")}
        // No allow-scripts, no allow-same-origin: the mail document cannot
        // run script or touch app storage/tokens even if sanitizer layers
        // were ever bypassed.
        sandbox=""
        referrerPolicy="no-referrer"
        onLoad={updateHeight}
        className={
          "w-full rounded border bg-background transition-[height] duration-200 " +
          (expanded ? "h-[80vh]" : "h-[420px]")
        }
        style={
          expanded && contentHeight
            ? { height: `${contentHeight}px` }
            : undefined
        }
        srcDoc={sanitizeHtmlMail(html, {
          allowRemoteImages: loadImages,
          inlineImages,
        })}
      />
      <div className="flex justify-end">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs text-muted-foreground hover:text-foreground min-h-11 md:min-h-7 transition-colors"
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
