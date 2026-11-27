import {
  render,
  screen,
  fireEvent,
  cleanup,
  act,
  waitFor,
} from "@testing-library/react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { WebmailComposeDock } from "./webmail-compose-dock";
import { sendMessage, saveDraft } from "@/features/webmail/mailbox-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("@/features/webmail/mailbox-api", async () => {
  const actual = await vi.importActual<
    typeof import("@/features/webmail/mailbox-api")
  >("@/features/webmail/mailbox-api");
  return {
    ...actual,
    saveDraft: vi
      .fn()
      .mockResolvedValue({ saved: true, uid: 999, uid_validity: 1 }),
    sendMessage: vi.fn().mockResolvedValue({ sent: true }),
  };
});

afterEach(() => {
  cleanup();
  window.sessionStorage.clear();
  window.localStorage.clear();
  vi.mocked(saveDraft).mockClear();
  vi.mocked(sendMessage).mockClear();
  vi.mocked(saveDraft).mockResolvedValue({
    saved: true,
    uid: 999,
    uid_validity: 1,
    folder: "Drafts",
    name_enc: "b64.RHJhZnRz",
  });
  vi.mocked(sendMessage).mockResolvedValue({ sent: true } as never);
});

describe("WebmailComposeDock", () => {
  it("converts entered email into recipient chip on Enter", () => {
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);

    const toInput = screen.getByTestId("compose-to-input");
    fireEvent.change(toInput, { target: { value: "test@example.com" } });
    fireEvent.keyDown(toInput, { key: "Enter", code: "Enter" });

    expect(screen.getByTestId("recipient-pill-test@example.com")).toBeTruthy();
  });

  it("blocks attachments exceeding 5MB single file limit", () => {
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);

    const fileInput = screen.getByTestId("compose-file-input");
    const oversizedFile = new File(["dummy".repeat(1024 * 1024)], "huge.pdf", {
      type: "application/pdf",
    });
    Object.defineProperty(oversizedFile, "size", { value: 6 * 1024 * 1024 });

    fireEvent.change(fileInput, { target: { files: [oversizedFile] } });

    expect(screen.getByTestId("attachment-error-notice")).toBeTruthy();
    const sendBtn = screen.getByTestId("compose-send-button");
    expect(sendBtn.hasAttribute("disabled")).toBe(true);
  });

  it("shows undo toast after sending", () => {
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["client@example.com"]}
        initialSubject="Contract Attached"
      />,
    );

    const sendBtn = screen.getByTestId("compose-send-button");
    fireEvent.click(sendBtn);

    expect(screen.getByTestId("undo-send-toast")).toBeTruthy();
    expect(screen.getByTestId("undo-send-button")).toBeTruthy();
  });

  it("sends in_reply_to when the dock is opened for a reply/forward", async () => {
    vi.useFakeTimers();
    try {
      render(
        <WebmailComposeDock
          isOpen={true}
          onClose={vi.fn()}
          initialTo={["alice@example.com"]}
          initialSubject="Re: Hello"
          inReplyTo="<original@example.test>"
        />,
      );

      fireEvent.change(screen.getByTestId("compose-to-input"), {
        target: { value: "alice@example.com" },
      });
      fireEvent.click(screen.getByTestId("compose-send-button"));

      // The 10-second undo buffer gates the real send call.
      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBe("<original@example.test>");
    } finally {
      vi.useRealTimers();
    }
  });

  it("omits in_reply_to when no prop is passed (fresh compose; forward also opens without it)", async () => {
    vi.useFakeTimers();
    try {
      render(
        <WebmailComposeDock
          isOpen={true}
          onClose={vi.fn()}
          initialTo={["bob@example.com"]}
        />,
      );

      fireEvent.click(screen.getByTestId("compose-send-button"));

      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  // Draft resume: the compose dock mirrors the active draft (content only
  // until v0.6.22) into sessionStorage, so closing the dock or reloading the
  // page and reopening compose restores it without any initial* props. The
  // original Message-ID must survive that round-trip for reply drafts.
  const persistDraftAndReopen = (props: {
    initialTo: string[];
    initialSubject: string;
    initialHtml?: string;
    initialText?: string;
    inReplyTo?: string;
  }) => {
    const first = render(
      <WebmailComposeDock isOpen={true} onClose={vi.fn()} {...props} />,
    );
    // Any edit writes the draft payload that a reload would keep. The value
    // must differ from the initial one, or React skips the change event.
    fireEvent.change(screen.getByTestId("compose-subject-input"), {
      target: { value: `${props.initialSubject} ` },
    });
    first.unmount();
    // Reopen compose the way the workbench does after a reload: no props, so
    // useDebouncedDraft restores the stored draft.
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);
  };

  it("keeps in_reply_to when a saved reply draft is resumed and sent", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(sendMessage).mockClear();
      persistDraftAndReopen({
        initialTo: ["alice@example.com"],
        initialSubject: "Re: Hello",
        initialHtml: "<p>thank you</p>",
        inReplyTo: "<original@example.test>",
      });

      fireEvent.click(screen.getByTestId("compose-send-button"));
      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBe("<original@example.test>");
    } finally {
      vi.useRealTimers();
    }
  });

  it("omits in_reply_to when a saved forward draft is resumed and sent", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(sendMessage).mockClear();
      persistDraftAndReopen({
        initialTo: ["carol@example.com"],
        initialSubject: "Fwd: Hello",
        initialHtml: "<p>forwarded body</p>",
      });

      fireEvent.click(screen.getByTestId("compose-send-button"));
      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("omits in_reply_to when a fresh draft is resumed and sent", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(sendMessage).mockClear();
      persistDraftAndReopen({
        initialTo: ["bob@example.com"],
        initialSubject: "Status update",
        initialText: "plain body",
      });

      fireEvent.click(screen.getByTestId("compose-send-button"));
      await vi.advanceTimersByTimeAsync(10_000);

      expect(sendMessage).toHaveBeenCalledTimes(1);
      const body = vi.mocked(sendMessage).mock.calls[0][0];
      expect(body.in_reply_to).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("renders quoted content with blockquote when initialHtml is provided (DEF-5)", () => {
    const quotedHtml = `<p><br></p><div class="urmail-quoted-message"><p>On Nov 8, 2026, Alice wrote:</p><blockquote style="border-left: 2px solid #ccc; padding-left: 8px; margin-left: 8px;"><p>Hello original email</p></blockquote></div>`;
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["alice@example.com"]}
        initialSubject="Re: Hello"
        initialHtml={quotedHtml}
      />,
    );

    const blockquotes = document.querySelectorAll("blockquote");
    expect(blockquotes.length).toBeGreaterThan(0);
    expect(document.body.textContent).toContain("Hello original email");
  });

  it("toggles AI companion panel and expands dock width from 580px to 880px", () => {
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["partner@example.com"]}
        initialSubject="Partnership Agreement"
      />,
    );

    const dock = screen.getByTestId("webmail-compose-dock");
    expect(dock.className).toContain("w-[580px]");

    const aiBtn = screen.getByTestId("compose-ai-button");
    fireEvent.click(aiBtn);

    expect(dock.className).toContain("w-[880px]");
    expect(screen.getByTestId("compose-ai-companion")).toBeTruthy();

    // Toggle off
    fireEvent.click(aiBtn);
    expect(dock.className).toContain("w-[580px]");
    expect(screen.queryByTestId("compose-ai-companion")).toBeNull();
  });

  it("applies draft from companion and updates subject input and draft", () => {
    render(
      <WebmailComposeDock
        isOpen={true}
        onClose={vi.fn()}
        initialTo={["partner@example.com"]}
        initialSubject="Original Subject"
        initialText="Original Text"
      />,
    );

    // Open companion
    fireEvent.click(screen.getByTestId("compose-ai-button"));
    expect(screen.getByTestId("compose-ai-companion")).toBeTruthy();

    // Apply draft from companion
    const applyBtn = screen.getByTestId("btn-apply-draft");
    fireEvent.click(applyBtn);

    const subjectInput = screen.getByTestId(
      "compose-subject-input",
    ) as HTMLInputElement;
    expect(subjectInput.value).toBe("Original Subject");
  });
});

describe("draft autosave failure visibility", () => {
  it("shows a visible failure notice and never the saved label when autosave fails", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(saveDraft).mockClear();
      vi.mocked(saveDraft).mockRejectedValueOnce(new Error("503"));
      render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);

      fireEvent.change(screen.getByTestId("compose-subject-input"), {
        target: { value: "Visibility check" },
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2100);
      });

      expect(screen.getByTestId("draft-save-error-notice")).toBeTruthy();
      expect(
        screen.getByTestId("draft-save-error-notice").textContent,
      ).toContain("webmail_next.draft_save_failed");
      // The retry entry rides on the failure notice (REVIEW-UX-002).
      expect(screen.getByTestId("draft-save-retry")).toBeTruthy();
      // The 503-class failure must not be presented as a server save.
      expect(screen.queryByTestId("draft-status-indicator")).toBeNull();
      // No retry storm: exactly one autosave attempt for one debounced edit.
      expect(saveDraft).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("clears the failure notice and shows the saved label after a later successful save", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(saveDraft).mockClear();
      vi.mocked(saveDraft)
        .mockRejectedValueOnce(new Error("503"))
        .mockResolvedValueOnce({
          saved: true,
          uid: 7,
          uid_validity: 1,
          folder: "Drafts",
          name_enc: "s5a-visibility",
        });
      render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);

      fireEvent.change(screen.getByTestId("compose-subject-input"), {
        target: { value: "First attempt" },
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2100);
      });
      expect(screen.getByTestId("draft-save-error-notice")).toBeTruthy();

      fireEvent.change(screen.getByTestId("compose-subject-input"), {
        target: { value: "First attempt, edited" },
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2100);
      });

      expect(screen.queryByTestId("draft-save-error-notice")).toBeNull();
      expect(
        screen.getByTestId("draft-status-indicator").textContent,
      ).toContain("webmail_next.draft_saved");
    } finally {
      vi.useRealTimers();
    }
  });

  it("marks a restored draft without a server uid as local only, then auto-saves it once", async () => {
    localStorage.setItem(
      "urmail.webmail.active_draft",
      JSON.stringify({
        to: ["partner@example.test"],
        subject: "Restored draft",
        text: "body text",
      }),
    );
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);
    // The restore flush pushes the stashed draft to the server immediately
    // (REVIEW-UX-002), so the local-only state resolves to saved.
    await waitFor(() =>
      expect(
        screen.getByTestId("draft-status-indicator").textContent,
      ).toContain("webmail_next.draft_saved"),
    );
    expect(saveDraft).toHaveBeenCalledTimes(1);
    const body = vi.mocked(saveDraft).mock.calls[0][0];
    expect(body.subject).toBe("Restored draft");
    expect(body.to).toEqual(["partner@example.test"]);
  });

  it("shows a retry entry when the draft save fails and re-saves on click", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(saveDraft).mockRejectedValueOnce(
        Object.assign(new Error("503"), { status: 503 }),
      );
      render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);
      fireEvent.change(screen.getByTestId("compose-subject-input"), {
        target: { value: "Persist me" },
      });
      // The 2s debounce gates the server save.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_100);
      });
      expect(saveDraft).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(screen.getByTestId("draft-save-error-notice")).toBeTruthy();
      const retry = screen.getByTestId("draft-save-retry");
      fireEvent.click(retry);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(saveDraft).toHaveBeenCalledTimes(2);
      expect(screen.queryByTestId("draft-save-error-notice")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows failure instead of success when send rejects (503), keeps the draft and retries", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(sendMessage).mockRejectedValueOnce(
        Object.assign(new Error("503"), { status: 503 }),
      );
      const onClose = vi.fn();
      render(
        <WebmailComposeDock
          isOpen={true}
          onClose={onClose}
          initialTo={["a@b.test"]}
          initialSubject="Hello"
        />,
      );
      fireEvent.click(screen.getByTestId("compose-send-button"));
      // Undo window must NOT claim success before the API resolves.
      expect(screen.queryByTestId("send-sent-toast")).toBeNull();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(sendMessage).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // Failure: no sent toast, dock stays open, explicit retry entry.
      expect(screen.queryByTestId("send-sent-toast")).toBeNull();
      expect(screen.getByTestId("send-failed-toast")).toBeTruthy();
      expect(screen.getByTestId("send-retry-button")).toBeTruthy();
      expect(onClose).not.toHaveBeenCalled();
      expect(screen.getByTestId("compose-send-button")).toBeTruthy();
      // Retry send succeeds → success toast + close.
      fireEvent.click(screen.getByTestId("send-retry-button"));
      await vi.advanceTimersByTimeAsync(10_000);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(sendMessage).toHaveBeenCalledTimes(2);
      expect(screen.getByTestId("send-sent-toast")).toBeTruthy();
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("compose focus management (REVIEW-UIUX-016)", () => {
  afterEach(() => {
    window.sessionStorage.clear();
    window.localStorage.clear();
  });

  it("moves focus into the To field when the dock opens", async () => {
    const { waitFor } = await import("@testing-library/react");
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);
    await waitFor(() => {
      expect(document.activeElement?.getAttribute("data-testid")).toBe(
        "compose-to-input",
      );
    });
  });

  it("keeps an accessible name on the recipient input after a chip exists", () => {
    render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);
    const toInput = screen.getByTestId("compose-to-input");
    fireEvent.change(toInput, { target: { value: "a@b.test" } });
    fireEvent.keyDown(toInput, { key: "Enter", code: "Enter" });
    expect(screen.getByTestId("recipient-pill-a@b.test")).toBeTruthy();
    const input = screen.getByTestId("compose-to-input") as HTMLInputElement;
    expect(input.getAttribute("aria-label")).toBe("To");
  });

  it("returns focus to the opener when the dock closes after send", async () => {
    vi.useFakeTimers();
    try {
      const container = render(
        <>
          <button type="button" data-testid="opener">
            Compose
          </button>
          <WebmailComposeDock isOpen={false} onClose={() => {}} />
        </>,
      );
      // The opener holds focus before the user opens the dock (production:
      // the Compose button). Opening records it for the close-time restore.
      const opener = screen.getByTestId("opener");
      opener.focus();
      expect(document.activeElement).toBe(opener);
      const openTree = (
        <>
          <button type="button" data-testid="opener">
            Compose
          </button>
          <WebmailComposeDock
            isOpen={true}
            onClose={() => {}}
            initialTo={["c@d.test"]}
          />
        </>
      );
      const closedTree = (
        <>
          <button type="button" data-testid="opener">
            Compose
          </button>
          <WebmailComposeDock isOpen={false} onClose={() => {}} />
        </>
      );
      container.rerender(openTree);
      // Dock opening moves focus into the form (REVIEW-UIUX-016 ①).
      await vi.advanceTimersByTimeAsync(50);
      expect(document.activeElement?.getAttribute("data-testid")).toBe(
        "compose-to-input",
      );
      // Closing (e.g. after send) returns focus to the opener anchor
      // (REVIEW-UIUX-016 ③) instead of stranding it on a hidden button.
      container.rerender(closedTree);
      await vi.advanceTimersByTimeAsync(0);
      expect(document.activeElement?.getAttribute("data-testid")).toBe(
        "opener",
      );
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("draft saved-at clock shape (REVIEW-UIUX-014 QA pr164)", () => {
  afterEach(() => {
    window.sessionStorage.clear();
    window.localStorage.clear();
  });

  it("renders the saved-at hint in h23 shape, never locale-default 12h", async () => {
    vi.useFakeTimers();
    try {
      // Fix the fake system clock so the expected h23 string is exact.
      vi.setSystemTime(new Date(2026, 10, 27, 15, 4, 5));
      render(<WebmailComposeDock isOpen={true} onClose={vi.fn()} />);
      fireEvent.change(screen.getByTestId("compose-subject-input"), {
        target: { value: "Clock shape" },
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_100);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      const indicator = screen.getByTestId("draft-status-indicator")
        .textContent as string;
      // h23 shape "HH:MM:SS" — the saved-at hint must not fall back to the
      // locale default (en-US would render "3:04:05 PM" here).
      expect(indicator).toMatch(
        /^webmail_next\.draft_saved \d{2}:\d{2}:\d{2}$/,
      );
      expect(indicator).not.toMatch(/AM|PM/);
      // Hour 15 pins h23 (the 12h locale default would show 3 with PM).
      expect(indicator).toMatch(/15:04:\d{2}/);
    } finally {
      vi.useRealTimers();
    }
  });
});
