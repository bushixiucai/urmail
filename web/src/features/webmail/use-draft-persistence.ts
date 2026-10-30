import { useEffect, useRef, useState } from "react";

export type DraftPersistenceStatus =
  | "clean"
  | "dirty"
  | "saving"
  | "saved"
  | "save_failed"
  | "save_uncertain"
  | "discarding";

/** Only identity and revisions cross tabs; message contents never enter storage. */
export function useDraftPersistence<T, R>({
  enabled,
  value,
  initialResult,
  initialValue,
  save,
  remove,
  uncertain,
  blocked,
  onError,
}: {
  enabled: boolean;
  value: T;
  initialResult?: R;
  initialValue?: T;
  save: (value: T, previous: R | undefined, requestKey: string) => Promise<R>;
  remove: (previous: R) => Promise<unknown>;
  uncertain: (error: unknown) => boolean;
  blocked: boolean;
  onError: (error: unknown) => void;
}) {
  const [status, setStatus] = useState<DraftPersistenceStatus>("clean");
  const state = useRef({
    result: initialResult,
    initial: JSON.stringify(initialValue ?? value),
    savedValue: initialValue ?? value,
    saved: initialResult && initialValue ? JSON.stringify(initialValue) : "",
    generation: 0,
    discarded: false,
    inFlight: null as Promise<boolean> | null,
    attempt: null as { value: T; key: string; serialized: string } | null,
    uncertain: false,
  });
  const latest = useRef({
    enabled,
    value,
    save,
    remove,
    uncertain,
    blocked,
    onError,
  });
  latest.current = {
    enabled,
    value,
    save,
    remove,
    uncertain,
    blocked,
    onError,
  };
  const serialized = JSON.stringify(value);

  const flush = async (reconcile = false): Promise<boolean> => {
    const s = state.current;
    if (!latest.current.enabled || latest.current.blocked || s.discarded)
      return false;
    if (s.inFlight) {
      if (!(await s.inFlight)) return false;
      return flush(reconcile);
    }
    if (s.uncertain && !reconcile) return false;
    const target = JSON.stringify(latest.current.value);
    if (!s.attempt && target === s.saved) return true;
    const generation = s.generation;
    const attempt = s.attempt ?? {
      value: structuredClone(latest.current.value),
      key: crypto.randomUUID(),
      serialized: target,
    };
    s.attempt = attempt;
    setStatus("saving");
    const operation = (async () => {
      try {
        const result = await latest.current.save(
          attempt.value,
          s.result,
          attempt.key,
        );
        // A discard waits for the write before deleting its returned identity.
        s.result = result;
        s.saved = attempt.serialized;
        s.savedValue = attempt.value;
        s.attempt = null;
        s.uncertain = false;
        if (generation === s.generation && !s.discarded) setStatus("saved");
        return true;
      } catch (error) {
        s.uncertain = latest.current.uncertain(error);
        if (!s.uncertain) s.attempt = null;
        setStatus(s.uncertain ? "save_uncertain" : "save_failed");
        latest.current.onError(error);
        return false;
      } finally {
        s.inFlight = null;
      }
    })();
    s.inFlight = operation;
    const ok = await operation;
    if (!ok || s.discarded || generation !== s.generation) return false;
    if (JSON.stringify(latest.current.value) !== s.saved) return flush();
    return true;
  };

  const discard = async (): Promise<boolean> => {
    const s = state.current;
    if (latest.current.blocked || s.uncertain) return false;
    s.discarded = true;
    s.generation++;
    setStatus("discarding");
    if (s.inFlight && !(await s.inFlight)) {
      s.discarded = false;
      return false;
    }
    try {
      if (s.result) await latest.current.remove(s.result);
      return true;
    } catch (error) {
      s.discarded = false;
      setStatus("save_failed");
      latest.current.onError(error);
      return false;
    }
  };

  const flushRef = useRef(flush);
  flushRef.current = flush;
  useEffect(() => {
    const s = state.current;
    if (
      !enabled ||
      blocked ||
      s.discarded ||
      s.uncertain ||
      serialized === s.saved
    )
      return;
    setStatus((current) => (current === "saving" ? current : "dirty"));
    const timer = setTimeout(() => void flushRef.current(), 800);
    return () => clearTimeout(timer);
  }, [enabled, blocked, serialized]);

  useEffect(() => {
    const protect = (event: BeforeUnloadEvent) => {
      const s = state.current;
      const current = JSON.stringify(latest.current.value);
      if (
        s.discarded ||
        (!s.inFlight &&
          !s.uncertain &&
          (current === s.saved ||
            (!latest.current.enabled && !s.result && current === s.initial)))
      )
        return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", protect);
    return () => window.removeEventListener("beforeunload", protect);
  }, []);

  return {
    status,
    flush,
    discard,
    currentResult: () => state.current.result,
    flushValue: (next: T) => {
      latest.current.value = next;
      return flush();
    },
    persistedValue: () => state.current.savedValue,
    result: state.current.result,
  };
}
