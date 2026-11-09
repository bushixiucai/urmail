import { useEffect, useState } from "react";

/**
 * B2-2 防闪（WEBMAIL-BATCH2-SPEC.md §3.1）：请求 in-flight 超过 delayMs
 * 才让骨架可见；提前完成则全程不显示骨架，直接空→内容。
 */
export function useDelayedFlag(active: boolean, delayMs = 300): boolean {
  const [persisted, setPersisted] = useState(false);
  useEffect(() => {
    if (!active) {
      setPersisted(false);
      return;
    }
    const timer = window.setTimeout(() => setPersisted(true), delayMs);
    return () => window.clearTimeout(timer);
  }, [active, delayMs]);
  return active && persisted;
}
