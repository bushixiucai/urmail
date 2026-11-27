// web/src/features/webmail-next/hooks/use-debounced-draft.ts
import { useState, useEffect, useRef, useCallback } from "react";
import {
  saveDraft,
  type OutgoingAttachment,
} from "@/features/webmail/mailbox-api";

interface DraftPayload {
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  text: string;
  html?: string;
  attachments?: OutgoingAttachment[];
  /**
   * Original Message-ID when this draft continues a reply/reply-all. Kept in
   * the persisted payload so resuming the draft still threads the message;
   * fresh and forward drafts never set it (v0.6.22 semantics).
   */
  in_reply_to?: string;
  uid?: number;
  uid_validity?: number;
}

/**
 * Local draft stash durability (REVIEW-UX-002): `localStorage` so the only
 * copy of an unsent draft survives both page refreshes and full browser
 * restarts — the server copy is not guaranteed while the drafts endpoint is
 * gated. The legacy sessionStorage key is migrated on first read.
 */
const STORAGE_KEY = "urmail.webmail.active_draft";
const LEGACY_STORAGE_KEY = STORAGE_KEY; // pre-durability key lived in sessionStorage

function hasDraftContent(draft: DraftPayload | Partial<DraftPayload>): boolean {
  return Boolean(
    (draft.subject ?? "").trim() ||
    (draft.text ?? "").trim() ||
    (draft.html ?? "").trim() ||
    (draft.to ?? []).length > 0 ||
    (draft.cc ?? []).length > 0 ||
    (draft.bcc ?? []).length > 0 ||
    (draft.attachments ?? []).length > 0,
  );
}

function readStoredDraft(): DraftPayload | null {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) return JSON.parse(stored) as DraftPayload;
  } catch {
    // ignore malformed or unavailable storage
  }
  try {
    const legacy = sessionStorage.getItem(LEGACY_STORAGE_KEY);
    if (legacy) return JSON.parse(legacy) as DraftPayload;
  } catch {
    // ignore
  }
  return null;
}

function writeStoredDraft(draft: DraftPayload): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(draft));
  } catch {
    // ignore
  }
}

const EMPTY_DRAFT: DraftPayload = {
  to: [],
  cc: [],
  bcc: [],
  subject: "",
  text: "",
  html: "",
  attachments: [],
};

export function useDebouncedDraft(initialDraft?: Partial<DraftPayload>) {
  const restoredFromStashRef = useRef(false);
  const [draft, setDraftState] = useState<DraftPayload>(() => {
    if (initialDraft) {
      return {
        to: initialDraft.to || [],
        cc: initialDraft.cc || [],
        bcc: initialDraft.bcc || [],
        subject: initialDraft.subject || "",
        text: initialDraft.text || "",
        html: initialDraft.html || "",
        attachments: initialDraft.attachments || [],
        in_reply_to: initialDraft.in_reply_to,
        uid: initialDraft.uid,
        uid_validity: initialDraft.uid_validity,
      };
    }
    if (typeof window !== "undefined") {
      const stored = readStoredDraft();
      if (stored && hasDraftContent(stored)) {
        restoredFromStashRef.current = true;
        return {
          ...EMPTY_DRAFT,
          ...stored,
        };
      }
    }
    return { ...EMPTY_DRAFT };
  });
  /** True when this composer mounted from the local stash, not fresh input. */
  const restoredFromStash = restoredFromStashRef.current;

  const draftRef = useRef(draft);
  draftRef.current = draft;

  const [isSaving, setIsSaving] = useState(false);
  const [lastSavedAt, setLastSavedAt] = useState<string | null>(null);
  const [lastSaveFailed, setLastSaveFailed] = useState(false);
  const debounceTimerRef = useRef<NodeJS.Timeout | null>(null);

  const saveToServer = useCallback(
    async (currentDraft: DraftPayload): Promise<boolean> => {
      // Only save if there is content to save
      if (!hasDraftContent(currentDraft)) return true;

      setIsSaving(true);
      // A new attempt supersedes the previous failure indication; there is
      // deliberately no silent automatic retry loop — explicit retry and the
      // post-restore attempt are the recovery paths.
      setLastSaveFailed(false);
      try {
        // 1. Local durable backup (localStorage, survives refresh+restart)
        writeStoredDraft(currentDraft);

        // 2. Server IMAP APPEND
        const res = await saveDraft({
          to: currentDraft.to,
          cc: currentDraft.cc,
          bcc: currentDraft.bcc,
          subject: currentDraft.subject,
          text: currentDraft.text,
          html: currentDraft.html,
          attachments: currentDraft.attachments,
          in_reply_to: currentDraft.in_reply_to,
          uid: currentDraft.uid,
          uid_validity: currentDraft.uid_validity,
        });

        if (res.saved) {
          setDraftState((prev) => ({
            ...prev,
            uid: res.uid,
            uid_validity: res.uid_validity,
          }));
          // REVIEW-UIUX-014 (QA pr164 single point): same-source h23 clock
          // as the list/reading panes — never the locale-default 12h form.
          setLastSavedAt(
            new Intl.DateTimeFormat(undefined, {
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
              hour12: false,
              hourCycle: "h23",
            }).format(new Date()),
          );
          return true;
        }
        // The server answered without confirming the save: the draft exists
        // locally only, and the UI must not present it as saved.
        setLastSaveFailed(true);
        return false;
      } catch {
        // Surface the failure instead of failing silently; the local
        // durable backup keeps the current input and attachments intact.
        setLastSaveFailed(true);
        return false;
      } finally {
        setIsSaving(false);
      }
    },
    [],
  );

  const updateDraft = useCallback(
    (updates: Partial<DraftPayload>) => {
      setDraftState((prev) => {
        const next = { ...prev, ...updates };

        // Save immediately to durable local storage to survive refreshes
        writeStoredDraft(next);

        // Debounce server call by 2 seconds
        if (debounceTimerRef.current) {
          clearTimeout(debounceTimerRef.current);
        }
        debounceTimerRef.current = setTimeout(() => {
          void saveToServer(next);
        }, 2000);

        return next;
      });
    },
    [saveToServer],
  );

  /** Manual retry entry: drop any pending debounce and save what is on screen. */
  const retrySave = useCallback((): Promise<boolean> => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    return saveToServer(draftRef.current);
  }, [saveToServer]);

  const clearDraft = useCallback(() => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
    }
    try {
      localStorage.removeItem(STORAGE_KEY);
    } catch {
      // ignore
    }
    try {
      sessionStorage.removeItem(LEGACY_STORAGE_KEY);
    } catch {
      // ignore
    }
    setDraftState({ ...EMPTY_DRAFT });
    setLastSavedAt(null);
    setLastSaveFailed(false);
  }, []);

  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
      }
    };
  }, []);

  return {
    draft,
    updateDraft,
    clearDraft,
    isSaving,
    lastSavedAt,
    lastSaveFailed,
    retrySave,
    restoredFromStash,
  };
}
