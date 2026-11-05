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
  uid?: number;
  uid_validity?: number;
}

const STORAGE_KEY = "urmail.webmail.active_draft";

export function useDebouncedDraft(initialDraft?: Partial<DraftPayload>) {
  const [draft, setDraftState] = useState<DraftPayload>(() => {
    if (typeof window !== "undefined") {
      try {
        const stored = sessionStorage.getItem(STORAGE_KEY);
        if (stored) {
          const parsed = JSON.parse(stored);
          return {
            to: [],
            cc: [],
            bcc: [],
            subject: "",
            text: "",
            html: "",
            attachments: [],
            ...initialDraft,
            ...parsed,
          };
        }
      } catch {
        // ignore
      }
    }
    return {
      to: initialDraft?.to || [],
      cc: initialDraft?.cc || [],
      bcc: initialDraft?.bcc || [],
      subject: initialDraft?.subject || "",
      text: initialDraft?.text || "",
      html: initialDraft?.html || "",
      attachments: initialDraft?.attachments || [],
      uid: initialDraft?.uid,
      uid_validity: initialDraft?.uid_validity,
    };
  });

  const [isSaving, setIsSaving] = useState(false);
  const [lastSavedAt, setLastSavedAt] = useState<string | null>(null);
  const debounceTimerRef = useRef<NodeJS.Timeout | null>(null);

  const saveToServer = useCallback(async (currentDraft: DraftPayload) => {
    // Only save if there is content to save
    if (
      !currentDraft.subject.trim() &&
      !currentDraft.text.trim() &&
      (!currentDraft.to || currentDraft.to.length === 0)
    ) {
      return;
    }

    setIsSaving(true);
    try {
      // 1. Local session storage backup
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(currentDraft));

      // 2. Server IMAP APPEND
      const res = await saveDraft({
        to: currentDraft.to,
        cc: currentDraft.cc,
        bcc: currentDraft.bcc,
        subject: currentDraft.subject,
        text: currentDraft.text,
        html: currentDraft.html,
        attachments: currentDraft.attachments,
        uid: currentDraft.uid,
        uid_validity: currentDraft.uid_validity,
      });

      if (res.saved) {
        setDraftState((prev) => ({
          ...prev,
          uid: res.uid,
          uid_validity: res.uid_validity,
        }));
        setLastSavedAt(new Date().toLocaleTimeString());
      }
    } catch {
      // Silent fail on network error, local sessionStorage remains preserved
    } finally {
      setIsSaving(false);
    }
  }, []);

  const updateDraft = useCallback(
    (updates: Partial<DraftPayload>) => {
      setDraftState((prev) => {
        const next = { ...prev, ...updates };

        // Save immediately to sessionStorage to survive page refresh
        try {
          sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next));
        } catch {
          // ignore
        }

        // Debounce server call by 2 seconds
        if (debounceTimerRef.current) {
          clearTimeout(debounceTimerRef.current);
        }
        debounceTimerRef.current = setTimeout(() => {
          saveToServer(next);
        }, 2000);

        return next;
      });
    },
    [saveToServer],
  );

  const clearDraft = useCallback(() => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
    }
    try {
      sessionStorage.removeItem(STORAGE_KEY);
    } catch {
      // ignore
    }
    setDraftState({
      to: [],
      cc: [],
      bcc: [],
      subject: "",
      text: "",
      html: "",
      attachments: [],
    });
    setLastSavedAt(null);
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
  };
}
