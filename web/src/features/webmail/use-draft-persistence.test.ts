import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useDraftPersistence } from "./use-draft-persistence";

afterEach(cleanup);
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("serialized draft persistence", () => {
  it("serializes edits during an in-flight save and replaces the returned UID", async () => {
    const first = deferred<{ uid: number }>();
    const save = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue({ uid: 12 });
    const { result, rerender } = renderHook(
      ({ text }) =>
        useDraftPersistence({
          enabled: true,
          value: { text },
          save,
          remove: vi.fn(),
          uncertain: () => false,
          blocked: false,
          onError: vi.fn(),
        }),
      { initialProps: { text: "first" } },
    );
    let flushing!: Promise<boolean>;
    act(() => {
      flushing = result.current.flush();
    });
    rerender({ text: "latest" });
    await act(async () => {
      first.resolve({ uid: 11 });
      expect(await flushing).toBe(true);
    });
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1][0]).toEqual({ text: "latest" });
    expect(save.mock.calls[1][1]).toEqual({ uid: 11 });
    expect(result.current.persistedValue()).toEqual({ text: "latest" });
  });
  it("retains an uncertain attempt key and reconciles the frozen snapshot", async () => {
    const save = vi
      .fn()
      .mockRejectedValueOnce(new Error("lost reply"))
      .mockResolvedValue({ uid: 8 });
    const { result } = renderHook(() =>
      useDraftPersistence({
        enabled: true,
        value: { text: "mail" },
        save,
        remove: vi.fn(),
        uncertain: () => true,
        blocked: false,
        onError: vi.fn(),
      }),
    );
    await act(async () => {
      expect(await result.current.flush()).toBe(false);
    });
    await act(async () => {
      expect(await result.current.flush()).toBe(false);
    });
    expect(save).toHaveBeenCalledTimes(1);
    await act(async () => {
      expect(await result.current.flush(true)).toBe(true);
    });
    expect(save.mock.calls[1][2]).toBe(save.mock.calls[0][2]);
    expect(save.mock.calls[1][0]).toEqual(save.mock.calls[0][0]);
  });
  it("waits for an in-flight write before discarding its exact returned identity", async () => {
    const first = deferred<{ uid: number }>();
    const save = vi.fn().mockReturnValue(first.promise);
    const remove = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useDraftPersistence({
        enabled: true,
        value: { text: "mail" },
        save,
        remove,
        uncertain: () => true,
        blocked: false,
        onError: vi.fn(),
      }),
    );
    let flushing!: Promise<boolean>;
    let discarding!: Promise<boolean>;
    act(() => {
      flushing = result.current.flush();
      discarding = result.current.discard();
    });
    expect(remove).not.toHaveBeenCalled();
    await act(async () => {
      first.resolve({ uid: 19 });
      expect(await flushing).toBe(false);
      expect(await discarding).toBe(true);
    });
    expect(remove).toHaveBeenCalledExactlyOnceWith({ uid: 19 });
    await act(async () => {
      expect(await result.current.flush()).toBe(false);
    });
    expect(save).toHaveBeenCalledTimes(1);
  });
  it("blocks persistence while composition or attachment decoding is unfinished", async () => {
    const save = vi.fn().mockResolvedValue({ uid: 1 });
    const { result, rerender } = renderHook(
      ({ blocked }) =>
        useDraftPersistence({
          enabled: true,
          value: { text: "composition" },
          save,
          remove: vi.fn(),
          uncertain: () => false,
          blocked,
          onError: vi.fn(),
        }),
      { initialProps: { blocked: true } },
    );
    await act(async () => {
      expect(await result.current.flush()).toBe(false);
    });
    expect(save).not.toHaveBeenCalled();
    rerender({ blocked: false });
    await act(async () => {
      expect(await result.current.flush()).toBe(true);
    });
    expect(save).toHaveBeenCalledTimes(1);
  });
  it("exposes a definitive conflict without discarding current text", async () => {
    const onError = vi.fn();
    const save = vi.fn().mockRejectedValue(new Error("draft changed"));
    const { result } = renderHook(() =>
      useDraftPersistence({
        enabled: true,
        value: { text: "unsaved latest" },
        save,
        remove: vi.fn(),
        uncertain: () => false,
        blocked: false,
        onError,
      }),
    );
    await act(async () => {
      expect(await result.current.flush()).toBe(false);
    });
    expect(result.current.status).toBe("save_failed");
    expect(onError).toHaveBeenCalledTimes(1);
    expect(save.mock.calls[0][0]).toEqual({ text: "unsaved latest" });
  });
});

it("does not rewrite an unchanged persisted draft on open", async () => {
  vi.useFakeTimers();
  try {
    const save = vi.fn().mockResolvedValue({ uid: 8 });
    const initialValue = { text: "Persisted revision" };
    const { result } = renderHook(() =>
      useDraftPersistence({
        enabled: true,
        value: initialValue,
        initialValue,
        initialResult: { uid: 7 },
        save,
        remove: vi.fn(),
        uncertain: () => false,
        blocked: false,
        onError: vi.fn(),
      }),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1200);
      expect(await result.current.flush()).toBe(true);
    });
    expect(save).not.toHaveBeenCalled();
    expect(result.current.currentResult()).toEqual({ uid: 7 });
  } finally {
    vi.useRealTimers();
  }
});

it("compares edits made before identity loading with the original saved baseline", async () => {
  const save = vi.fn().mockResolvedValue({ uid: 8 });
  const initialValue = { text: "Original" };
  const { result, rerender } = renderHook(
    ({ enabled, text }) =>
      useDraftPersistence({
        enabled,
        value: { text },
        initialValue,
        initialResult: { uid: 7 },
        save,
        remove: vi.fn(),
        uncertain: () => false,
        blocked: false,
        onError: vi.fn(),
      }),
    { initialProps: { enabled: false, text: "Original" } },
  );
  rerender({ enabled: false, text: "Typed while loading" });
  rerender({ enabled: true, text: "Typed while loading" });
  await act(async () => {
    expect(await result.current.flush()).toBe(true);
  });
  expect(save.mock.calls[0][0]).toEqual({ text: "Typed while loading" });
  expect(save.mock.calls[0][1]).toEqual({ uid: 7 });
});

it("protects edits entered while loading without warning for a pristine empty editor", () => {
  const initialValue = { text: "" };
  const { rerender } = renderHook(
    ({ text }) =>
      useDraftPersistence({
        enabled: false,
        value: { text },
        initialValue,
        save: vi.fn(),
        remove: vi.fn(),
        uncertain: () => false,
        blocked: false,
        onError: vi.fn(),
      }),
    { initialProps: { text: "" } },
  );
  const pristine = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(pristine);
  expect(pristine.defaultPrevented).toBe(false);
  rerender({ text: "Typed before identity request finished" });
  const edited = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(edited);
  expect(edited.defaultPrevented).toBe(true);
});
