import { useState, useEffect, useRef, useCallback } from "react";
import { useTranslation } from "react-i18next";
import {
  Reply,
  ReplyAll,
  Forward,
  Trash2,
  Star,
  ShieldAlert,
  ShieldCheck,
  Loader2,
  ArrowLeft,
  UserPlus,
  Check,
  Mail,
} from "lucide-react";
import { useDelayedFlag } from "../hooks/use-delayed-flag";
import { avatarInitial } from "../lib/avatar-initial";
import { ReadingSkeleton, SkeletonBlock } from "./webmail-skeleton";
import { WebmailSecurityBadge } from "./webmail-security-badge";
import { WebmailAttachmentCard } from "./webmail-attachment-card";
import { WebmailAICueStrip } from "./ai/webmail-ai-cue-strip";
import { WebmailStructuredSummary } from "./ai/webmail-structured-summary";
import { sanitizeHtmlMail } from "@/features/webmail/sanitize-html-mail";
import {
  sanitizeHtmlMailAsync,
  sanitizeWorkerAvailable,
} from "@/features/webmail/sanitize-html-mail-async";
import {
  createContact,
  fetchMailboxAITarget,
  previewMailboxAI,
  runMailboxAI,
  type MailboxAIAssistResponse,
  type MailboxAIIndicator,
  type MessageDetail,
  type MessageSummary,
} from "@/features/webmail/mailbox-api";
import { WebmailThreadStack } from "./webmail-thread-stack";
import {
  WebmailThreadSummaryBar,
  type ThreadSummaryState,
} from "./webmail-thread-summary-bar";
import { WebmailThreadNewerBar } from "./webmail-thread-newer-bar";
import { THREAD_LOAD_STEP } from "../lib/thread-members-api";

interface WebmailReadingPaneProps {
  message: MessageDetail | null;
  folderEnc: string;
  isLoading?: boolean;
  isGlobalRemoteImagesAllowed?: boolean;
  isManualRemoteImagesAllowed?: boolean;
  /** Loaded members of the selected thread (oldest→newest optional). */
  threadMembers?: MessageSummary[];
  /** Contract total for the stack count label (cross-box, all pages). */
  threadTotal?: number;
  /** Origin-folder unread count for the subject-bar badge (§11-1). */
  threadUnreadCount?: number;
  /** Composite identity (folder_enc:uid) of the expanded member (§11-5
   * same-uid cross-box safety); null falls back to uid matching. */
  threadExpandedKey?: string | null;
  /** §11-5 expansion relay: focus this card's header (composite key). */
  threadFocusKey?: string | null;
  /** Sent folder name for the §11-4 cross-box badge. */
  threadSentFolderName?: string;
  /** total - loaded - newer; drives the giant-thread summary bar. */
  threadEarlierCount?: number;
  /** Unloaded newer members (deep anchor); renders the bottom entry. */
  threadNewerCount?: number;
  /** Undefined hides the summary bar entirely (no members API data). */
  threadSummaryState?: ThreadSummaryState;
  onLoadThreadEarlier?: () => void;
  onLoadThreadNewer?: () => void;
  onRetryThreadSummary?: () => void;
  onBack?: () => void;
  onReply?: (msg: MessageDetail) => void;
  onReplyAll?: (msg: MessageDetail) => void;
  onForward?: (msg: MessageDetail) => void;
  onDelete?: (uid: number, folderEnc?: string) => void;
  onToggleStar?: (uid: number, folderEnc?: string) => void;
  onLoadRemoteImages?: (uid: number) => void;
  onAlwaysTrustSender?: (senderEmail: string) => void;
  /** Switch the expanded stack card (loads that UID's detail). */
  onExpandThreadUid?: (uid: number, folderEnc?: string) => void;
  onMarkThreadRead?: () => void;
  onArchiveThread?: () => void;
  onDeleteThread?: () => void;
  onOpenAISettings?: () => void;
}

const FRAME_MIN_HEIGHT = 420;
// Generous ceiling against runaway measurement on pathological bodies;
// taller mail simply scrolls the pane instead of being clipped.
const FRAME_MAX_HEIGHT = 10000;

/**
 * Height of the frame's srcdoc content, measured from the parent side.
 * Requires sandbox="allow-same-origin" (no allow-scripts — the CSP meta
 * injected by the sanitizer keeps the body inert, so script execution is
 * impossible even though the document is same-origin).
 */
function measureFrameHeight(frame: HTMLIFrameElement): number {
  try {
    const doc = frame.contentDocument;
    if (!doc || !doc.body) return 0;
    const body = doc.body;
    const docEl = doc.documentElement;
    return Math.max(
      body ? body.scrollHeight : 0,
      docEl ? docEl.scrollHeight : 0,
      body ? body.offsetHeight : 0,
    );
  } catch {
    // Cross-origin or not yet loaded — keep the current height.
    return 0;
  }
}

function SanitizedHtmlFrame({
  title,
  html,
}: {
  title: string;
  html: string | null;
}) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const [frameHeight, setFrameHeight] = useState(FRAME_MIN_HEIGHT);
  const resizeDebounce = useRef<number | null>(null);

  const remeasure = useCallback(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const height = measureFrameHeight(frame);
    if (height > 0) {
      setFrameHeight((prev) => {
        const next = Math.min(
          FRAME_MAX_HEIGHT,
          Math.max(FRAME_MIN_HEIGHT, Math.ceil(height)),
        );
        return Math.abs(prev - next) > 1 ? next : prev;
      });
    }
  }, []);

  // Re-measure once the srcdoc has parsed and again after its images
  // load — an <img> without intrinsic size collapses to ~0px, so the
  // first pass can undershoot.
  const handleLoad = useCallback(() => {
    remeasure();
    const doc = frameRef.current?.contentDocument;
    if (!doc) return;
    const pendingImages = Array.from(doc.images).filter((img) => !img.complete);
    for (const img of pendingImages) {
      img.addEventListener("load", remeasure, { once: true });
      img.addEventListener("error", remeasure, { once: true });
    }
  }, [remeasure]);

  // Body content that resizes without a load event (fonts, reflow after
  // webfont swap) — watch it from the parent while the document persists.
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    let observer: ResizeObserver | null = null;
    const attach = () => {
      const body = frame.contentDocument?.body;
      if (body && typeof ResizeObserver !== "undefined") {
        observer?.disconnect();
        observer = new ResizeObserver(() => {
          if (resizeDebounce.current != null) {
            window.clearTimeout(resizeDebounce.current);
          }
          resizeDebounce.current = window.setTimeout(() => {
            if (typeof window.requestAnimationFrame === "function") {
              window.requestAnimationFrame(remeasure);
            } else {
              remeasure();
            }
          }, 80);
        });
        observer.observe(body);
      }
    };
    if (frame.contentDocument?.body) {
      attach();
    } else {
      frame.addEventListener("load", attach, { once: true });
    }
    return () => {
      observer?.disconnect();
      if (resizeDebounce.current != null) {
        window.clearTimeout(resizeDebounce.current);
        resizeDebounce.current = null;
      }
    };
  }, [html, remeasure]);

  // A new message must not inherit the previous mail's height.
  useEffect(() => {
    setFrameHeight(FRAME_MIN_HEIGHT);
  }, [html]);

  // F4: the debounced height write can still land in the same frame that
  // the observer fired in, which raises Chrome's benign "ResizeObserver
  // loop" notification on every message open. Suppress exactly that
  // notification (both historical wordings) while the pane is mounted.
  useEffect(() => {
    if (typeof window === "undefined") return;
    const isLoopNotice = (message: string) =>
      message ===
        "ResizeObserver loop completed with undelivered notifications." ||
      message === "ResizeObserver loop limit exceeded";
    const handleError = (event: ErrorEvent) => {
      if (isLoopNotice(event.message)) event.preventDefault();
    };
    window.addEventListener("error", handleError);
    return () => window.removeEventListener("error", handleError);
  }, []);

  if (html == null) {
    return (
      <div
        data-testid="mail-html-skeleton"
        aria-busy="true"
        className="vellum-skeleton-fade space-y-3"
      >
        <SkeletonBlock className="h-3 w-2/3" />
        <SkeletonBlock className="h-3 w-1/2" />
        <SkeletonBlock className="h-24 w-full" />
      </div>
    );
  }
  return (
    /* §11-6 mail-body-bucket: every HTML body renders on a defined white
       surface so mail-native dark backgrounds stay inside the bucket and
       never bleed onto the card (vellum) — plain-text path has no bucket. */
    <div
      data-testid="mail-body-bucket"
      data-mail-body-bucket="true"
      className="rounded-lg bg-white p-3 sm:p-4 max-w-full"
    >
      <iframe
        ref={frameRef}
        data-testid="mail-html-frame"
        title={title}
        // allow-same-origin (alone, never with allow-scripts) so the parent
        // can measure the srcdoc body; the sanitizer-injected CSP meta keeps
        // the document inert, preserving the XSS defense line.
        sandbox="allow-same-origin"
        referrerPolicy="no-referrer"
        className="w-full rounded border-0 bg-transparent"
        style={{
          height: frameHeight,
          minHeight: FRAME_MIN_HEIGHT,
          transition: "height 160ms ease-out",
        }}
        onLoad={handleLoad}
        srcDoc={html}
      />
    </div>
  );
}

export function WebmailReadingPane({
  message,
  folderEnc,
  isLoading = false,
  isGlobalRemoteImagesAllowed = false,
  isManualRemoteImagesAllowed = false,
  threadMembers,
  threadTotal,
  threadUnreadCount,
  threadExpandedKey,
  threadFocusKey,
  threadSentFolderName,
  threadEarlierCount,
  threadNewerCount,
  threadSummaryState,
  onLoadThreadEarlier,
  onLoadThreadNewer,
  onRetryThreadSummary,
  onBack,
  onReply,
  onReplyAll,
  onForward,
  onDelete,
  onToggleStar,
  onLoadRemoteImages,
  onAlwaysTrustSender,
  onExpandThreadUid,
  onMarkThreadRead,
  onArchiveThread,
  onDeleteThread,
  onOpenAISettings,
}: WebmailReadingPaneProps) {
  const { t } = useTranslation();
  // B2-2 防闪：详情请求 in-flight >300ms 才渲染阅读区骨架。
  const showReadingSkeleton = useDelayedFlag(isLoading, 300);
  const [localManualGranted, setLocalManualGranted] = useState(false);
  const [isAddingContact, setIsAddingContact] = useState(false);
  const [contactAdded, setContactAdded] = useState(false);

  // AI Summary & Security State
  const [summaryResponse, setSummaryResponse] =
    useState<MailboxAIAssistResponse | null>(null);
  const [summaryOp, setSummaryOp] = useState<
    "standard" | "shorter" | "todos" | "translate"
  >("standard");
  const [isGeneratingSummary, setIsGeneratingSummary] = useState(false);
  const [summaryError, setSummaryError] = useState<string | null>(null);
  const [isSummaryDismissed, setIsSummaryDismissed] = useState(false);
  const [showUnconfiguredModal, setShowUnconfiguredModal] = useState(false);
  const summaryAbortControllerRef = useRef<AbortController | null>(null);

  // On-demand phishing/safety check (assist op=phishing, two-step contract).
  const [phishingCheck, setPhishingCheck] = useState<{
    score?: number;
    risk_level?: "low" | "medium" | "high" | "critical";
    reasons?: string[];
    indicators?: MailboxAIIndicator[];
  } | null>(null);
  const [isRunningSecurityCheck, setIsRunningSecurityCheck] = useState(false);
  const [securityCheckNote, setSecurityCheckNote] = useState<string | null>(
    null,
  );
  const securityCheckAbortControllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    summaryAbortControllerRef.current?.abort();
    securityCheckAbortControllerRef.current?.abort();
    setSummaryResponse(null);
    setSummaryOp("standard");
    setIsGeneratingSummary(false);
    setSummaryError(null);
    setIsSummaryDismissed(false);
    setShowUnconfiguredModal(false);
    // Un-analyzed mail stays completely silent: drop any on-demand result.
    setPhishingCheck(null);
    setSecurityCheckNote(null);
    setIsRunningSecurityCheck(false);
  }, [message?.uid]);

  const handleGenerateSummary = async (options?: {
    lengthLimit?: number;
    operation?: "summarize" | "todos" | "translate";
    targetLang?: string;
  }) => {
    if (!message || !message.uid) return;
    summaryAbortControllerRef.current?.abort();
    const controller = new AbortController();
    summaryAbortControllerRef.current = controller;

    setIsGeneratingSummary(true);
    setSummaryError(null);

    const op = options?.operation || "summarize";
    const currentSummaryOp =
      options?.operation === "todos"
        ? "todos"
        : options?.operation === "translate"
          ? "translate"
          : options?.lengthLimit
            ? "shorter"
            : "standard";
    setSummaryOp(currentSummaryOp);
    const plainText =
      message.text ||
      (message.html ? message.html.replace(/<[^>]+>/g, " ").trim() : "");

    try {
      const target = await fetchMailboxAITarget(controller.signal);
      if (!target.configuration_digest) {
        setShowUnconfiguredModal(true);
        return;
      }
      // selection means "the text the user actually selected": the server
      // requires it to be a literal substring of its own plain-text rendering
      // of the message, otherwise it rejects the call with AI_SOURCE_CHANGED.
      // Reading-pane operations act on the whole message, so selection stays
      // unset and the server derives the body from the live message itself.
      const input = {
        operation: op,
        configuration_digest: target.configuration_digest,
        confirmed: true,
        request_key: `summary-${crypto.randomUUID()}`,
        folder_enc: folderEnc,
        uid: message.uid,
        uid_validity: message.uid_validity,
        body: plainText,
        subject: message.subject || "",
        length_limit: options?.lengthLimit,
        target_language: options?.targetLang || "zh-CN",
      };

      const preview = await previewMailboxAI(
        "assist",
        input,
        controller.signal,
      );

      const resp = await runMailboxAI(
        { ...input, content_digest: preview.content_digest },
        controller.signal,
      );

      if (!controller.signal.aborted) {
        if (op === "translate" && summaryResponse) {
          setSummaryResponse({
            ...summaryResponse,
            result: {
              ...summaryResponse.result,
              translated_text:
                resp.result.translated_text ||
                resp.result.text ||
                resp.result.summary,
            },
          });
        } else {
          setSummaryResponse(resp);
        }
      }
    } catch (err: unknown) {
      if (controller.signal.aborted) return;
      const apiErr = err as { code?: string; message?: string };
      if (
        apiErr?.code === "AI_UNCONFIGURED" ||
        apiErr?.code === "AI_DISABLED"
      ) {
        setShowUnconfiguredModal(true);
      } else {
        setSummaryError(
          t(`error.${apiErr?.code}`, {
            defaultValue: t("page.webmail.ai_failed"),
          }),
        );
      }
    } finally {
      if (!controller.signal.aborted) {
        setIsGeneratingSummary(false);
      }
    }
  };

  const isImageAllowed = Boolean(
    isGlobalRemoteImagesAllowed ||
    isManualRemoteImagesAllowed ||
    localManualGranted,
  );
  const useWorker = sanitizeWorkerAvailable();
  const [workerHtml, setWorkerHtml] = useState<string | null>(null);

  useEffect(() => {
    setLocalManualGranted(false);
    setContactAdded(false);
  }, [message?.uid]);

  useEffect(() => {
    if (!useWorker || !message?.html) {
      setWorkerHtml(null);
      return;
    }
    let cancelled = false;
    setWorkerHtml(null);
    void sanitizeHtmlMailAsync(message.html, {
      allowRemoteImages: isImageAllowed,
    }).then((html) => {
      if (!cancelled) setWorkerHtml(html);
    });
    return () => {
      cancelled = true;
    };
  }, [useWorker, message?.html, message?.uid, isImageAllowed]);

  if (showReadingSkeleton) {
    return (
      <div
        data-testid="webmail-reading-pane"
        className="flex-1 h-full bg-[var(--vellum-ground)] p-6"
      >
        <ReadingSkeleton
          testId="reading-skeleton"
          label={t("webmail_next.loading_message")}
        />
      </div>
    );
  }

  if (!message) {
    return (
      <div
        data-testid="webmail-reading-pane"
        className="flex-1 h-full flex flex-col items-center justify-center bg-[var(--vellum-ground)] text-xs text-[var(--vellum-ink-secondary)]"
      >
        <div className="w-12 h-12 rounded-full bg-[var(--vellum-row-hover)] flex items-center justify-center mb-2">
          <Mail className="w-6 h-6 text-[var(--vellum-ink-secondary)]" />
        </div>
        <span className="text-sm font-semibold leading-[22px] text-[var(--vellum-ink)]">
          {t("webmail_next.empty_reading_title")}
        </span>
        <span className="mt-1 text-xs leading-4 text-[var(--vellum-ink-secondary)]">
          {t("webmail_next.empty_reading_body")}
        </span>
      </div>
    );
  }

  const sender = message.from?.[0];
  const senderEmail = sender?.email || "";
  const senderName = sender?.name || senderEmail;
  const isStarred = Boolean(
    Array.isArray(message.flags) && message.flags.includes("\\Flagged"),
  );

  // Server SanitizeHTML drops the <img> and emits note "remote_images_blocked".
  // It does not leave a remote src or inject data-blocked-src. HTML markers
  // remain only as a fallback if a body still contains them.
  const containsRemoteImages = Boolean(
    message.html &&
    (/<img\b[^>]*\bsrc\s*=\s*["']?https?:\/\//i.test(message.html) ||
      message.html.includes("data-blocked-src")),
  );
  const hasBlockedNote =
    Array.isArray(message.notes) &&
    message.notes.includes("remote_images_blocked");
  const hasBlockedImages =
    !isImageAllowed && (hasBlockedNote || containsRemoteImages);

  const handleShowImages = () => {
    setLocalManualGranted(true);
    if (message.uid) {
      onLoadRemoteImages?.(message.uid);
    }
  };

  const handleAlwaysTrust = () => {
    setLocalManualGranted(true);
    if (message.uid) {
      onLoadRemoteImages?.(message.uid);
    }
    onAlwaysTrustSender?.(senderEmail);
  };

  const handleAddContact = async () => {
    if (!senderEmail || contactAdded || isAddingContact) return;
    try {
      setIsAddingContact(true);
      await createContact(senderName, senderEmail);
      setContactAdded(true);
    } catch {
      // ignore
    } finally {
      setIsAddingContact(false);
    }
  };

  const isConsentStaleError = (err: unknown): boolean => {
    const apiErr = err as {
      status?: number;
      code?: string;
      error?: { code?: string };
    };
    const code = apiErr?.code || apiErr?.error?.code || "";
    return (
      apiErr?.status === 409 ||
      code === "AI_CONTENT_CONSENT_STALE" ||
      code === "AI_CLOUD_CONSENT_STALE"
    );
  };

  // On-demand phishing/safety check for mails the server has not analysed.
  // Same two-step contract as every other assist call: preview_only first to
  // obtain content_digest, then one confirmed run with the same request_key;
  // a 409 consent-stale silently re-runs the pair once before surfacing.
  const handleRunSecurityCheck = async () => {
    if (!message || !message.uid || isRunningSecurityCheck) return;
    securityCheckAbortControllerRef.current?.abort();
    const controller = new AbortController();
    securityCheckAbortControllerRef.current = controller;

    setIsRunningSecurityCheck(true);
    setSecurityCheckNote(null);

    const plainText =
      message.text ||
      (message.html ? message.html.replace(/<[^>]+>/g, " ").trim() : "");

    try {
      const target = await fetchMailboxAITarget(controller.signal);
      if (!target.configuration_digest) {
        setShowUnconfiguredModal(true);
        return;
      }

      const input = {
        operation: "phishing" as const,
        configuration_digest: target.configuration_digest,
        confirmed: true,
        request_key: `security-${crypto.randomUUID()}`,
        folder_enc: folderEnc,
        uid: message.uid,
        uid_validity: message.uid_validity,
        body: plainText,
        subject: message.subject || "",
      };

      const executeCheck = async (digest: string) => {
        const preview = await previewMailboxAI(
          "assist",
          { ...input, configuration_digest: digest },
          controller.signal,
        );
        return await runMailboxAI(
          {
            ...input,
            configuration_digest: digest,
            content_digest: preview.content_digest,
          },
          controller.signal,
        );
      };

      let resp: MailboxAIAssistResponse;
      try {
        resp = await executeCheck(target.configuration_digest);
      } catch (firstErr: unknown) {
        if (!isConsentStaleError(firstErr)) throw firstErr;
        const fresh = await fetchMailboxAITarget(controller.signal);
        if (!fresh.configuration_digest) {
          setShowUnconfiguredModal(true);
          return;
        }
        resp = await executeCheck(fresh.configuration_digest);
      }

      if (controller.signal.aborted) return;
      const checkScore = resp.result.score;
      const checkLevel = resp.result.risk_level;
      const checkHigh =
        checkScore !== undefined
          ? checkScore >= 0.6
          : checkLevel === "high" || checkLevel === "critical";
      setPhishingCheck({
        score: checkScore,
        risk_level: checkLevel,
        reasons: resp.result.reasons,
        indicators: resp.result.indicators,
      });
      // score < 0.6 stays silent (no red banner); leave a quiet one-line
      // acknowledgement next to the trigger so the click is not a no-op.
      // High-risk results speak through the red banner instead.
      setSecurityCheckNote(
        checkHigh ? null : t("webmail_next.security_check_clear"),
      );
    } catch (err: unknown) {
      if (controller.signal.aborted) return;
      const apiErr = err as {
        code?: string;
        error?: { code?: string };
      };
      const code = apiErr?.code || apiErr?.error?.code || "";
      if (code === "AI_UNCONFIGURED" || code === "AI_DISABLED") {
        setShowUnconfiguredModal(true);
      } else {
        setSecurityCheckNote(
          t(`error.${code}`, {
            defaultValue: t("page.webmail.ai_failed"),
          }),
        );
      }
    } finally {
      if (!controller.signal.aborted) {
        setIsRunningSecurityCheck(false);
      }
    }
  };

  // Red banner rule: score >= 0.6 renders, anything below stays silent.
  // On-demand check results take precedence over the persisted verdict in
  // message.phishing (Backend #87); when the score is absent the coarse
  // risk_level decides, and mail without a phishing key renders nothing.
  const activePhishing = phishingCheck ?? message.phishing;
  const activeRiskScore = activePhishing?.score;
  const activeRiskLevel = activePhishing?.risk_level ?? message.risk_level;
  const activeIndicators = activePhishing?.indicators ?? message.indicators;
  const activeReasons = activePhishing?.reasons;
  const isHighRisk =
    activeRiskScore !== undefined
      ? activeRiskScore >= 0.6
      : activeRiskLevel === "high" || activeRiskLevel === "critical";

  const messageTextLength =
    (message.text?.length ?? 0) ||
    (message.html ? message.html.replace(/<[^>]+>/g, " ").length : 0);
  const isLongMail = messageTextLength > 1500;
  const shouldShowCueStrip =
    !isSummaryDismissed &&
    !summaryResponse &&
    !isGeneratingSummary &&
    (isLongMail ||
      Boolean(message.security_notes?.includes("foreign_language")));

  const aiSlot = (
    <>
      {isHighRisk && (
        // Decision ①b: v1 is light-only, so the banner has no dark variants —
        // it must read var(--vellum-danger-fill)/var(--vellum-danger-border)/var(--vellum-danger-text) in every state.
        <div
          data-testid="phishing-warning-banner"
          className="mb-4 p-3 bg-[var(--vellum-danger-fill)] border border-[var(--vellum-danger-border)] rounded-lg text-xs text-[var(--vellum-danger-text)] flex items-start gap-2.5"
        >
          <ShieldAlert className="w-4 h-4 text-[var(--vellum-danger-strong)] shrink-0 mt-0.5" />
          <div className="space-y-1">
            <div className="font-semibold">
              {t("webmail_next.ai_phishing_high_risk")}
            </div>
            {activeRiskScore !== undefined && (
              <div className="opacity-80" data-testid="phishing-risk-score">
                {t("webmail_next.ai_phishing_score", {
                  score: activeRiskScore.toFixed(2),
                })}
              </div>
            )}
            {activeIndicators && activeIndicators.length > 0 && (
              <ul className="list-disc list-inside space-y-0.5 opacity-90">
                {activeIndicators.map((ind, i) => (
                  <li key={i}>
                    <span className="font-medium">{ind.type}</span>
                    {ind.evidence ? `: ${ind.evidence}` : ""}
                  </li>
                ))}
              </ul>
            )}
            {(!activeIndicators || activeIndicators.length === 0) &&
              activeReasons &&
              activeReasons.length > 0 && (
                <ul className="list-disc list-inside space-y-0.5 opacity-90">
                  {activeReasons.map((reason, i) => (
                    <li key={i}>{reason}</li>
                  ))}
                </ul>
              )}
          </div>
        </div>
      )}
      {shouldShowCueStrip && (
        <WebmailAICueStrip
          onTriggerSummary={() => handleGenerateSummary()}
          isLoading={isGeneratingSummary}
        />
      )}
      {(summaryResponse || isGeneratingSummary || summaryError) &&
        !isSummaryDismissed && (
          <WebmailStructuredSummary
            response={summaryResponse}
            isLoading={isGeneratingSummary}
            error={summaryError}
            messageUid={message.uid}
            activeOperation={summaryOp}
            onDismiss={() => {
              summaryAbortControllerRef.current?.abort();
              setIsSummaryDismissed(true);
            }}
            onRetry={() => handleGenerateSummary()}
            onCancel={() => {
              summaryAbortControllerRef.current?.abort();
              setIsGeneratingSummary(false);
            }}
            onRefineStandard={() => handleGenerateSummary()}
            onRefineShorter={() => handleGenerateSummary({ lengthLimit: 100 })}
            onRefineTodos={() => handleGenerateSummary({ operation: "todos" })}
            onRefineTranslate={() =>
              handleGenerateSummary({ operation: "translate" })
            }
          />
        )}
    </>
  );

  const bodySlot = (
    <>
      {aiSlot}
      {hasBlockedImages && (
        <div
          data-testid="remote-images-banner"
          className="mb-3 p-3 bg-[var(--vellum-warning-fill)] border border-[var(--vellum-warning-border)] rounded-lg flex flex-col sm:flex-row items-start sm:items-center justify-between gap-2.5 sm:gap-3 text-xs text-[var(--vellum-warning-text)]"
        >
          <div className="flex items-center gap-2 min-w-0">
            <ShieldAlert className="w-4 h-4 text-[var(--vellum-warning-strong)] shrink-0" />
            <span className="break-words leading-relaxed">
              {t("webmail_next.remote_images_blocked")}
            </span>
          </div>
          <div className="flex items-center gap-2 shrink-0 flex-wrap">
            <button
              data-testid="show-images-button"
              type="button"
              onClick={handleShowImages}
              className="px-2 py-1 bg-white border border-[var(--vellum-warning-strong)]/30 text-[var(--vellum-warning-text)] font-medium rounded hover:bg-[var(--vellum-warning-fill)] transition-colors"
            >
              {t("webmail_next.show_images")}
            </button>
            <button
              data-testid="always-trust-button"
              type="button"
              onClick={handleAlwaysTrust}
              className="px-2 py-1 text-[var(--vellum-warning-text)] hover:underline transition-colors"
            >
              {t("webmail_next.always_trust_images")}
            </button>
          </div>
        </div>
      )}
      <div
        data-testid="mail-body-container"
        className="select-text text-sm text-[var(--vellum-ink)] leading-relaxed min-h-[220px]"
      >
        {message.html ? (
          <SanitizedHtmlFrame
            title={message.subject || "Email Content"}
            html={
              useWorker
                ? workerHtml
                : sanitizeHtmlMail(message.html, {
                    allowRemoteImages: isImageAllowed,
                  })
            }
          />
        ) : (
          <pre className="whitespace-pre-wrap font-sans text-sm leading-[22px] text-[var(--vellum-ink)] max-w-[65ch]">
            {message.text || "(Empty message)"}
          </pre>
        )}
      </div>
      {message.attachments && message.attachments.length > 0 && (
        <div data-testid="attachments-container" className="pt-2">
          <div className="text-xs font-semibold text-[var(--vellum-ink-secondary)] mb-3 select-none">
            {t("webmail_next.attachments_count", {
              count: message.attachments.length,
            })}
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {message.attachments.map((att) => (
              <WebmailAttachmentCard
                key={att.part_id}
                attachment={att}
                folderEnc={folderEnc}
                uid={message.uid}
              />
            ))}
          </div>
        </div>
      )}
    </>
  );

  // Thread stack (VISUAL §3): when the loaded set has 2+ members of the
  // same thread, render the stacked cards instead of the single-message pane.
  if (threadMembers && threadMembers.length > 1 && message) {
    return (
      <div
        data-testid="webmail-reading-pane"
        className="flex-1 h-full flex flex-col bg-[var(--vellum-ground)] overflow-hidden"
      >
        {onBack && (
          <div className="md:hidden h-10 px-3 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center shrink-0">
            <button
              data-testid="reading-back-button"
              type="button"
              onClick={onBack}
              className="p-1.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] rounded"
              aria-label={t("webmail_next.back_to_list")}
            >
              <ArrowLeft className="w-4 h-4" />
            </button>
          </div>
        )}
        {threadSummaryState != null &&
          ((threadEarlierCount ?? 0) > 0 || threadSummaryState === "error") && (
            <div className="px-3 sm:px-6 pt-3 shrink-0">
              <WebmailThreadSummaryBar
                earlierCount={threadEarlierCount ?? 0}
                state={threadSummaryState}
                step={THREAD_LOAD_STEP}
                onLoadEarlier={() => onLoadThreadEarlier?.()}
                onRetry={() => onRetryThreadSummary?.()}
              />
            </div>
          )}
        <WebmailThreadStack
          members={threadMembers}
          totalOverride={threadTotal}
          unreadCount={threadUnreadCount}
          expandedKey={threadExpandedKey}
          focusKey={threadFocusKey}
          sentFolderName={threadSentFolderName}
          expandedUid={message.uid}
          expandedDetail={message}
          isDetailLoading={isLoading}
          bodySlot={bodySlot}
          onExpand={(uid, folderEnc) => onExpandThreadUid?.(uid, folderEnc)}
          onReply={(msg) => onReply?.(msg)}
          onReplyAll={(msg) => onReplyAll?.(msg)}
          onForward={(msg) => onForward?.(msg)}
          onToggleStar={(uid, folderEnc) => onToggleStar?.(uid, folderEnc)}
          onDelete={(uid, folderEnc) => onDelete?.(uid, folderEnc)}
          onMarkThreadRead={() => onMarkThreadRead?.()}
          onArchiveThread={() => onArchiveThread?.()}
          onDeleteThread={() => onDeleteThread?.()}
        />
        {(threadNewerCount ?? 0) > 0 && (
          <div className="px-3 sm:px-6 pb-3 shrink-0">
            <WebmailThreadNewerBar
              newerCount={threadNewerCount ?? 0}
              onLoadNewer={() => onLoadThreadNewer?.()}
            />
          </div>
        )}
      </div>
    );
  }

  return (
    <div
      data-testid="webmail-reading-pane"
      className="flex-1 h-full flex flex-col bg-[var(--vellum-ground)] overflow-y-auto"
    >
      {/* 1. Header Toolbar */}
      <div className="h-12 px-6 border-b border-[var(--vellum-border)] bg-[var(--vellum-surface)] flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2">
          {onBack && (
            <button
              data-testid="reading-back-button"
              type="button"
              onClick={onBack}
              className="md:hidden p-1.5 text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] rounded hover:bg-[var(--vellum-row-hover)]"
              aria-label={t("webmail_next.back_to_list")}
              title={t("webmail_next.back_to_list")}
            >
              <ArrowLeft className="w-4 h-4" />
            </button>
          )}
          <button
            data-testid="reading-reply-button"
            type="button"
            onClick={() => onReply?.(message)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] transition-colors"
          >
            <Reply className="w-4 h-4" />
            <span>{t("webmail_next.reply")}</span>
          </button>
          <button
            type="button"
            onClick={() => onReplyAll?.(message)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] transition-colors"
          >
            <ReplyAll className="w-4 h-4" />
            <span>{t("webmail_next.reply_all")}</span>
          </button>
          <button
            type="button"
            onClick={() => onForward?.(message)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium text-[var(--vellum-ink)] hover:bg-[var(--vellum-row-hover)] transition-colors"
          >
            <Forward className="w-4 h-4" />
            <span>{t("webmail_next.forward")}</span>
          </button>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => onToggleStar?.(message.uid)}
            className={`p-1.5 rounded hover:bg-[var(--vellum-row-hover)] transition-colors ${
              isStarred
                ? "text-[var(--vellum-star)]"
                : "text-[var(--vellum-ink-secondary)]"
            }`}
            aria-label={t("webmail_next.star")}
            title={t("webmail_next.star")}
          >
            <Star
              className={`w-4 h-4 ${isStarred ? "fill-[var(--vellum-star)]" : ""}`}
            />
          </button>
          <button
            type="button"
            onClick={() => onDelete?.(message.uid)}
            className="p-1.5 rounded text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-danger-strong)] hover:bg-[var(--vellum-row-hover)] transition-colors"
            aria-label={t("webmail_next.delete")}
            title={t("webmail_next.delete")}
          >
            <Trash2 className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* 2. Message Detail Content — full width like Gmail/Outlook; the
          readable-measure cap applies to plain-text paragraphs only, never
          to the body container itself. */}
      <div className="px-3 sm:px-6 py-3 sm:py-4 w-full space-y-4">
        {/* Subject & Security */}
        <div>
          <h1 className="text-[20px] leading-7 font-[650] text-[var(--vellum-ink)] tracking-tight">
            {message.subject || "(No subject)"}
          </h1>
          <div className="flex flex-wrap items-center justify-between gap-3 mt-3 pt-3 border-t border-[var(--vellum-border)]">
            <div className="flex items-center gap-3">
              <div className="w-9 h-9 rounded-full bg-[var(--brand-primary,var(--brand-primary))] text-white font-bold text-sm flex items-center justify-center shrink-0">
                {avatarInitial(senderName, senderEmail)}
              </div>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-semibold text-[var(--vellum-ink)]">
                    {senderName}
                  </span>
                  <span className="text-xs text-[var(--vellum-ink-secondary)]">
                    &lt;{senderEmail}&gt;
                  </span>
                  <WebmailSecurityBadge
                    authentication={message.authentication}
                    riskLevel={activeRiskLevel}
                    indicators={activeIndicators}
                  />
                  {/* On-demand phishing/safety check entry: the button is
                      always visible on every message (verified or already
                      analysed too) as a user-triggered action; only the
                      results (banner / notes) render after a check has run. */}
                  <button
                    data-testid="btn-security-check"
                    type="button"
                    onClick={() => void handleRunSecurityCheck()}
                    disabled={isRunningSecurityCheck}
                    className="flex items-center gap-1 px-1.5 py-0.5 rounded text-xs text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] hover:bg-[var(--vellum-border)] transition-colors disabled:opacity-50 select-none"
                    title={t("page.webmail.ai_quick_security")}
                    aria-label={t("page.webmail.ai_quick_security")}
                  >
                    {isRunningSecurityCheck ? (
                      <Loader2 className="w-3.5 h-3.5 animate-spin" />
                    ) : (
                      <ShieldCheck className="w-3.5 h-3.5" />
                    )}
                    <span className="hidden sm:inline">
                      {t("page.webmail.ai_quick_security")}
                    </span>
                  </button>
                  {securityCheckNote && (
                    <span
                      role="status"
                      data-testid="security-check-note"
                      className="text-xs text-[var(--vellum-ink-secondary)]"
                    >
                      {securityCheckNote}
                    </span>
                  )}
                  <button
                    data-testid="btn-add-contact"
                    type="button"
                    onClick={handleAddContact}
                    disabled={contactAdded || isAddingContact}
                    className={`ml-1 flex items-center gap-1 px-1.5 py-0.5 rounded text-xs transition-colors ${
                      contactAdded
                        ? "text-[var(--vellum-success-text)] bg-[var(--vellum-success-fill)]"
                        : "text-[var(--vellum-ink-secondary)] hover:text-[var(--vellum-ink)] hover:bg-[var(--vellum-border)]"
                    }`}
                    aria-label={
                      contactAdded
                        ? t("webmail_next.contact_added")
                        : t("webmail_next.add_contact")
                    }
                    title={
                      contactAdded
                        ? t("webmail_next.contact_added")
                        : t("webmail_next.add_contact")
                    }
                  >
                    {contactAdded ? (
                      <>
                        <Check className="w-3 h-3" />
                        <span>{t("webmail_next.contact_added", "Added")}</span>
                      </>
                    ) : (
                      <>
                        <UserPlus className="w-3 h-3" />
                        <span>
                          {t("webmail_next.add_contact", "+ Add Contact")}
                        </span>
                      </>
                    )}
                  </button>
                </div>
                <div className="text-xs text-[var(--vellum-ink-secondary)] mt-0.5">
                  {message.to && message.to.length > 0
                    ? t("webmail_next.to_recipients", {
                        recipients: message.to
                          .map((t) => t.name || t.email)
                          .join(", "),
                      })
                    : t("webmail_next.to_me")}
                </div>
              </div>
            </div>

            <div className="text-xs text-[var(--vellum-ink-secondary)]">
              {message.date ? new Date(message.date).toLocaleString() : ""}
            </div>
          </div>
        </div>

        {bodySlot}
      </div>

      {showUnconfiguredModal && (
        <div
          data-testid="ai-unconfigured-modal"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
        >
          <div className="bg-white rounded-xl shadow-xl max-w-sm w-full p-5 space-y-4 border border-[var(--vellum-border)]">
            <div className="text-sm font-semibold text-[var(--vellum-ink)]">
              {t("page.webmail.ai_settings_title")}
            </div>
            <p className="text-xs text-[var(--vellum-ink-secondary)] leading-relaxed">
              {t("webmail_next.ai_unconfigured_prompt")}
            </p>
            <div className="flex justify-end gap-2 pt-2">
              <button
                type="button"
                onClick={() => setShowUnconfiguredModal(false)}
                className="px-3 py-1.5 text-xs text-[var(--vellum-ink-secondary)] hover:bg-[var(--vellum-row-hover)] rounded-md font-medium"
              >
                {t("page.webmail.ai_cancel")}
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowUnconfiguredModal(false);
                  onOpenAISettings?.();
                }}
                className="px-3 py-1.5 text-xs bg-[var(--brand-primary)] hover:bg-[var(--vellum-brand-strong)] text-white rounded-md font-semibold"
              >
                {t("webmail_next.ai_go_to_settings")}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
