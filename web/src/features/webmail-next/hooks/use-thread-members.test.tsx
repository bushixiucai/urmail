import { describe, expect, it, vi, beforeEach } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useThreadMembers } from "./use-thread-members";
import type { ThreadMembersResponse } from "../lib/thread-members-api";

vi.mock("../lib/thread-members-api", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../lib/thread-members-api")>();
  return {
    ...actual,
    fetchThreadMembers: vi.fn(),
  };
});

const { fetchThreadMembers } = await import("../lib/thread-members-api");
const mockFetch = vi.mocked(fetchThreadMembers);

function member(uid: number, date: string) {
  return {
    uid,
    subject: `m${uid}`,
    from: [],
    date,
    flags: [],
    size: 1,
    folder: "INBOX",
    folder_enc: "SUJPQ1g=",
    uid_validity: 1,
  };
}

function page(
  uids: [number, string][],
  extra: Partial<ThreadMembersResponse> = {},
): ThreadMembersResponse {
  return {
    thread_id: 1,
    total: 30,
    unread_count: 2,
    scope: "server",
    messages: uids.map(([uid, date]) => member(uid, date)),
    has_more_older: true,
    oldest: `INBOX:${uids[0][0]}`,
    ...extra,
  };
}

function renderThreadMembers(args: {
  folder: string | null;
  uid: number | null;
  anchorUid?: number | null;
}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return renderHook(
    () =>
      useThreadMembers({
        originFolderEnc: args.folder,
        originUid: args.uid,
        anchorUid: args.anchorUid ?? null,
      }),
    { wrapper },
  );
}

beforeEach(() => {
  mockFetch.mockReset();
  cleanup();
});

describe("useThreadMembers (C1 data layer)", () => {
  it("loads the anchored window and back-fills older pages in order", async () => {
    mockFetch.mockImplementation(
      (_folder: string, _uid: number, opts: { before?: string } = {}) => {
        if (!opts.before) {
          return Promise.resolve(
            page([
              [20, "2026-01-20T00:00:00Z"],
              [22, "2026-01-22T00:00:00Z"],
            ]),
          );
        }
        return Promise.resolve(
          page(
            [
              [10, "2026-01-10T00:00:00Z"],
              [12, "2026-01-12T00:00:00Z"],
            ],
            {
              has_more_older: false,
              oldest: "INBOX:10",
            },
          ),
        );
      },
    );

    const { result } = renderThreadMembers({
      folder: "SUJPQ1g=",
      uid: 1,
      anchorUid: 20,
    });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.members.map((m) => m.uid)).toEqual([20, 22]);
    expect(result.current.total).toBe(30);
    expect(result.current.earlierCount).toBe(28);
    expect(mockFetch).toHaveBeenNthCalledWith(
      1,
      "SUJPQ1g=",
      1,
      expect.objectContaining({ limit: 20, anchorUid: 20 }),
    );

    result.current.loadEarlier();
    await waitFor(() =>
      expect(result.current.members.map((m) => m.uid)).toEqual([
        10, 12, 20, 22,
      ]),
    );
    expect(mockFetch).toHaveBeenNthCalledWith(
      2,
      "SUJPQ1g=",
      1,
      expect.objectContaining({ before: "INBOX:20" }),
    );
    // Thread start reached: earlier count reflects total - loaded, done.
    expect(result.current.hasMoreOlder).toBe(false);
    expect(result.current.earlierCount).toBe(26);
    result.current.loadEarlier();
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("stays disabled without an origin member", () => {
    const { result } = renderThreadMembers({ folder: null, uid: null });
    expect(result.current.isLoading).toBe(false);
    expect(result.current.members).toEqual([]);
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("surfaces errors for the §8 fallback path", async () => {
    mockFetch.mockRejectedValue(new Error("boom"));
    const { result } = renderThreadMembers({ folder: "SUJPQ1g=", uid: 1 });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.members).toEqual([]);
  });
});
