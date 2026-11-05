// web/src/features/webmail-next/hooks/use-range-selection.ts
import { useState, useCallback } from "react";

export function useRangeSelection<T extends { uid: number }>(items: T[]) {
  const [selectedUids, setSelectedUids] = useState<Set<number>>(new Set());
  const [lastSelectedUid, setLastSelectedUid] = useState<number | null>(null);

  const toggleSelect = useCallback(
    (uid: number, isShiftKey = false, isMetaOrCtrl = false) => {
      setSelectedUids((prev) => {
        const next = new Set(prev);

        if (isShiftKey && lastSelectedUid !== null) {
          const lastIdx = items.findIndex((item) => item.uid === lastSelectedUid);
          const currentIdx = items.findIndex((item) => item.uid === uid);

          if (lastIdx !== -1 && currentIdx !== -1) {
            const start = Math.min(lastIdx, currentIdx);
            const end = Math.max(lastIdx, currentIdx);
            for (let i = start; i <= end; i++) {
              next.add(items[i].uid);
            }
            return next;
          }
        }

        if (isMetaOrCtrl) {
          if (next.has(uid)) {
            next.delete(uid);
          } else {
            next.add(uid);
          }
        } else {
          // Normal click: if multiple are selected or this one isn't, select just this one
          if (next.has(uid) && next.size === 1) {
            next.clear();
          } else {
            next.clear();
            next.add(uid);
          }
        }

        setLastSelectedUid(uid);
        return next;
      });
    },
    [items, lastSelectedUid]
  );

  const selectAll = useCallback(() => {
    setSelectedUids(new Set(items.map((i) => i.uid)));
  }, [items]);

  const clearSelection = useCallback(() => {
    setSelectedUids(new Set());
    setLastSelectedUid(null);
  }, []);

  return {
    selectedUids,
    toggleSelect,
    selectAll,
    clearSelection,
    isSelected: (uid: number) => selectedUids.has(uid),
  };
}
