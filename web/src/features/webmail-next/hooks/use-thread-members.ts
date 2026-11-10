// web/src/features/webmail-next/hooks/use-thread-members.ts
import { useInfiniteQuery, type InfiniteData } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
  fetchThreadMembers,
  THREAD_LOAD_STEP,
  type ThreadMemberSummary,
  type ThreadMembersResponse,
} from "../lib/thread-members-api";

export interface ThreadMembersState {
  /** All loaded members, oldest→newest (server order preserved). */
  members: ThreadMemberSummary[];
  /** Full member count (incl. Sent, incl. unloaded pages). */
  total: number;
  /** Unread count, origin-folder members only. */
  unreadCount: number;
  /** False when the thread start has been reached (summary bar done). */
  hasMoreOlder: boolean;
  /** Unloaded newer members exist (deep-anchored window). */
  hasMoreNewer: boolean;
  /** Unloaded newer member count (latest window reports 0). */
  newerCount: number;
  /** members.length on error-free loads; 0 while loading. */
  loadedCount: number;
  /** total - loaded - newer (≥0); top summary bar count. */
  earlierCount: number;
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  isLoadingEarlier: boolean;
  loadEarlier: () => void;
  /** Jump to the latest window (the "更新的 N 封" entry). */
  loadNewer: () => void;
  refetch: () => void;
}

interface UseThreadMembersArgs {
  originFolderEnc: string | null;
  /** Any known member uid of the thread (list row root uid works). */
  originUid: number | null;
  /** Selected member to keep inside the initial window. */
  anchorUid?: number | null;
  enabled?: boolean;
}

function flattenPages(data: InfiniteData<ThreadMembersResponse> | undefined): {
  members: ThreadMemberSummary[];
  initial: ThreadMembersResponse | undefined;
  oldestPage: ThreadMembersResponse | undefined;
} {
  if (!data || data.pages.length === 0) {
    return { members: [], initial: undefined, oldestPage: undefined };
  }
  // pages[0] is the initial (latest-window) page; fetchPreviousPage
  // prepends strictly-older pages, so concatenation stays date-ascending.
  const members = data.pages.flatMap((page) => page.messages);
  return {
    members,
    initial: data.pages[data.pages.length - 1],
    oldestPage: data.pages[0],
  };
}

/**
 * Data layer for the reading-pane thread stack (C1 skeleton). Loads the
 * anchored window from the cross-folder members endpoint and progressively
 * back-fills older pages via the `oldest` keyset cursor.
 */
export function useThreadMembers({
  originFolderEnc,
  originUid,
  anchorUid,
  enabled = true,
}: UseThreadMembersArgs): ThreadMembersState {
  const canLoad = enabled && originFolderEnc != null && originUid != null;

  // "loadNewer" re-fetches the default (latest) window; the anchor resets
  // whenever a different thread is opened.
  const [anchorMode, setAnchorMode] = useState<"args" | "latest">("args");
  useEffect(() => {
    setAnchorMode("args");
  }, [originFolderEnc, originUid]);
  const effectiveAnchor = anchorMode === "latest" ? undefined : anchorUid;

  const query = useInfiniteQuery({
    queryKey: [
      "thread-members",
      originFolderEnc,
      originUid,
      effectiveAnchor ?? null,
    ] as (string | number | null)[],
    enabled: canLoad,
    staleTime: 10_000,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }): Promise<ThreadMembersResponse> => {
      if (pageParam === undefined) {
        return fetchThreadMembers(originFolderEnc!, originUid!, {
          limit: THREAD_LOAD_STEP,
          anchorUid: effectiveAnchor ?? undefined,
          signal,
        });
      }
      return fetchThreadMembers(originFolderEnc!, originUid!, {
        limit: THREAD_LOAD_STEP,
        before: pageParam,
        signal,
      });
    },
    // Forward pagination is unused — threads only grow older-side pages.
    getNextPageParam: () => undefined,
    getPreviousPageParam: (lastPage) =>
      lastPage?.has_more_older && lastPage.oldest ? lastPage.oldest : undefined,
  });

  const { data, isLoading, isError, error, fetchPreviousPage, refetch } = query;
  // With the gate closed (anchor gone / no verified thread_id) the cached
  // pages must NOT keep feeding the UI: stale members would render a dead
  // stack and enable actions against an unverified context (Backend/QA).
  const { members, initial, oldestPage } = canLoad
    ? flattenPages(data)
    : { members: [], initial: undefined, oldestPage: undefined };

  const hasMoreOlder = oldestPage?.has_more_older ?? true;
  const total = initial?.total ?? members.length;
  const unreadCount = initial?.unread_count ?? 0;
  const loadedCount = members.length;
  const newerCount = initial?.newer_count ?? 0;
  const hasMoreNewer = initial?.has_more_newer ?? false;
  const earlierCount = Math.max(total - loadedCount - newerCount, 0);

  const loadEarlier = () => {
    if (!canLoad || !hasMoreOlder || query.isFetchingPreviousPage) return;
    void fetchPreviousPage();
  };

  return {
    members,
    total,
    unreadCount,
    hasMoreOlder,
    hasMoreNewer,
    newerCount,
    loadedCount,
    earlierCount,
    isLoading: canLoad ? isLoading : false,
    isError: canLoad ? isError : false,
    error,
    isLoadingEarlier: query.isFetchingPreviousPage,
    loadEarlier,
    loadNewer: () => {
      if (!canLoad) return;
      setAnchorMode("latest");
    },
    refetch: () => {
      void refetch();
    },
  };
}
