// web/src/features/webmail-next/lib/system-sender.ts

/**
 * Frontend mirror of internal/webmail/threading.go systemSenderMarkers
 * (THREAD-UX-REVIEW F1; Q1 decision, interview 2026-11-26): automated
 * system senders (bounce daemons, verifiers, auto-responders,
 * postmasters) KEEP their true-chain grouping but render with weakened
 * reply affordances. The check covers the email AND the display name —
 * garbled senders can survive as display-only addresses with an empty
 * email (QA F-2), and the marker then lives in the name — matching the
 * backend rule exactly. UI-only presentation flag: grouping itself stays
 * server-side and is never changed here.
 */
const SYSTEM_SENDER_MARKERS = [
  "mailer-daemon",
  "noreply",
  "no-reply",
  "no_reply",
  "donotreply",
  "do-not-reply",
  "do_not_reply",
  "auto-reply",
  "autoreply",
  "auto_reply",
  "autoresponder",
  "auto-responder",
  "postmaster",
] as const;

export function isSystemSender(email?: string, name?: string): boolean {
  const e = (email ?? "").toLowerCase().trim();
  const n = (name ?? "").toLowerCase().trim();
  if (!e && !n) return false;
  return SYSTEM_SENDER_MARKERS.some((m) => e.includes(m) || n.includes(m));
}
