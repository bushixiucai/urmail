import { useState, useRef, useEffect } from "react";
import { useEditor, EditorContent } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Link from "@tiptap/extension-link";
import {
  Bold,
  Italic,
  Strikethrough,
  List,
  ListOrdered,
  Quote,
  Code,
  FileCode,
} from "lucide-react";
import { WebmailSelectionPolish } from "./ai/webmail-selection-polish";

interface WebmailEditorTiptapProps {
  content: string;
  isPlainText: boolean;
  onChange: (html: string, text: string) => void;
  onTogglePlainText: () => void;
  onOpenAISettings?: () => void;
}

export function WebmailEditorTiptap({
  content,
  isPlainText,
  onChange,
  onTogglePlainText,
  onOpenAISettings,
}: WebmailEditorTiptapProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const [polishSelection, setPolishSelection] = useState<{
    text: string;
    position: { top: number; left: number };
  } | null>(null);

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: { levels: [1, 2, 3] },
        link: false,
      }),
      Link.configure({
        openOnClick: false,
      }),
    ],
    content,
    onUpdate: ({ editor }) => {
      onChange(editor.getHTML(), editor.getText());
    },
    editorProps: {
      attributes: {
        class:
          "prose prose-sm max-w-none focus:outline-none min-h-[220px] p-3 text-sm text-[var(--vellum-ink)]",
      },
    },
  });

  // Keep editor content in sync when updated from parent (rollback or AI draft)
  useEffect(() => {
    if (editor && !editor.isDestroyed) {
      const currentHtml = editor.getHTML();
      const currentText = editor.getText();
      if (content !== currentHtml && content !== currentText) {
        editor.commands.setContent(content);
      }
    }
  }, [content, editor]);

  // Track text selection for in-situ polish micro-capsule (selection >= 5 characters)
  useEffect(() => {
    const handleSelectionChange = () => {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed) {
        setPolishSelection(null);
        return;
      }
      const text = sel.toString().trim();
      if (text.length < 5) {
        setPolishSelection(null);
        return;
      }
      const container = containerRef.current;
      if (
        !container ||
        !sel.anchorNode ||
        !container.contains(sel.anchorNode)
      ) {
        setPolishSelection(null);
        return;
      }

      try {
        const range = sel.getRangeAt(0);
        const rect = range.getBoundingClientRect();
        if (rect.width === 0 && rect.height === 0) return;
        const top = Math.max(10, rect.top - 36);
        const left = rect.left + rect.width / 2;
        setPolishSelection({ text, position: { top, left } });
      } catch {
        // Selection range out of scope
      }
    };

    document.addEventListener("selectionchange", handleSelectionChange);
    return () => {
      document.removeEventListener("selectionchange", handleSelectionChange);
    };
  }, []);

  const handleReplaceSelection = (revisedText: string) => {
    if (!isPlainText && editor && !editor.isDestroyed) {
      editor.chain().focus().insertContent(revisedText).run();
      setPolishSelection(null);
      return;
    }

    const textarea = textareaRef.current;
    if (textarea) {
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const updated =
        content.slice(0, start) + revisedText + content.slice(end);
      onChange(updated, updated);
      setPolishSelection(null);
    }
  };

  // Plain-text snapshot of the draft: the polish request needs the whole draft
  // body as context, and the assist endpoint rejects an empty body.
  const draftPlainText = isPlainText ? content : editor?.getText() || content;

  if (isPlainText) {
    return (
      <div
        ref={containerRef}
        className="flex-1 flex flex-col min-h-[260px] bg-white relative"
      >
        <textarea
          ref={textareaRef}
          data-testid="plaintext-editor"
          value={content}
          onChange={(e) => onChange(e.target.value, e.target.value)}
          placeholder="Compose email in plain text..."
          className="flex-1 w-full p-3 text-sm text-[var(--vellum-ink)] outline-none resize-none font-mono"
        />
        <div className="h-9 px-3 border-t border-[var(--vellum-border)] bg-[var(--vellum-ground)] flex items-center justify-between text-xs text-[var(--vellum-ink-secondary)]">
          <span className="text-xs">Plain Text Mode</span>
          <button
            type="button"
            onClick={onTogglePlainText}
            className="text-xs text-[var(--brand-primary,var(--brand-primary))] hover:underline"
          >
            Switch to Rich Text
          </button>
        </div>

        {polishSelection && (
          <WebmailSelectionPolish
            selectedText={polishSelection.text}
            draftBody={draftPlainText}
            position={polishSelection.position}
            onReplace={handleReplaceSelection}
            onClose={() => setPolishSelection(null)}
            onOpenAISettings={onOpenAISettings}
          />
        )}
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      className="flex-1 flex flex-col min-h-[260px] bg-white overflow-hidden relative"
    >
      <div className="flex-1 overflow-y-auto">
        <EditorContent editor={editor} data-testid="tiptap-editor" />
      </div>

      {/* Modern Bottom Toolbar */}
      <div className="h-9 px-2 border-t border-[var(--vellum-border)] bg-[var(--vellum-ground)] flex items-center justify-between text-[var(--vellum-ink-secondary)] text-xs shrink-0 select-none">
        <div className="flex items-center gap-0.5">
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleBold().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("bold")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Bold (Ctrl+B)"
          >
            <Bold className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleItalic().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("italic")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Italic (Ctrl+I)"
          >
            <Italic className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleStrike().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("strike")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Strikethrough"
          >
            <Strikethrough className="w-3.5 h-3.5" />
          </button>

          <div className="w-[1px] h-4 bg-[var(--vellum-border)] mx-1" />

          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleBulletList().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("bulletList")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Bullet List"
          >
            <List className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleOrderedList().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("orderedList")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Numbered List"
          >
            <ListOrdered className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleBlockquote().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("blockquote")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Quote"
          >
            <Quote className="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            onClick={() => editor?.chain().focus().toggleCodeBlock().run()}
            className={`p-1 rounded hover:bg-[var(--vellum-border)] hover:text-[var(--vellum-ink)] ${
              editor?.isActive("codeBlock")
                ? "bg-[var(--vellum-border)] text-[var(--vellum-ink)]"
                : ""
            }`}
            title="Code Block"
          >
            <Code className="w-3.5 h-3.5" />
          </button>
        </div>

        <div>
          <button
            type="button"
            onClick={onTogglePlainText}
            className="flex items-center gap-1 text-xs hover:text-[var(--vellum-ink)] px-1.5 py-0.5 rounded hover:bg-[var(--vellum-border)]"
            title="Switch to Plain Text Mode"
          >
            <FileCode className="w-3.5 h-3.5" />
            <span>Plain Text</span>
          </button>
        </div>
      </div>

      {polishSelection && (
        <WebmailSelectionPolish
          selectedText={polishSelection.text}
          draftBody={draftPlainText}
          position={polishSelection.position}
          onReplace={handleReplaceSelection}
          onClose={() => setPolishSelection(null)}
          onOpenAISettings={onOpenAISettings}
        />
      )}
    </div>
  );
}
