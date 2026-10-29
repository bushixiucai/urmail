import { useEffect, useRef, useState } from "react";
import { useEditor, EditorContent } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Link from "@tiptap/extension-link";
import Placeholder from "@tiptap/extension-placeholder";
import { useTranslation } from "react-i18next";
import {
  Bold,
  Italic,
  Strikethrough,
  List,
  ListOrdered,
  Quote,
  Link as LinkIcon,
  RemoveFormatting,
  Code,
  Undo2,
  Redo2,
} from "lucide-react";
import { Button } from "@/components/ui/button";

export interface MailboxEditorProps {
  initialContent: string;
  isHtml?: boolean;
  disabled?: boolean;
  placeholder?: string;
  onChange: (data: { html: string; text: string }) => void;
  isSourceMode?: boolean;
  onToggleSourceMode?: () => void;
}

export function MailboxEditor({
  initialContent,
  disabled = false,
  placeholder = "",
  onChange,
  isSourceMode = false,
  onToggleSourceMode,
}: MailboxEditorProps) {
  const { t } = useTranslation();
  const isUpdatingRef = useRef(false);
  const isMountedRef = useRef(false);
  const [sourceCode, setSourceCode] = useState(initialContent);

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: { levels: [1, 2, 3] },
        // Avoid duplicate link extension if StarterKit includes it
        link: false as const,
      }),
      Link.configure({
        openOnClick: false,
        HTMLAttributes: {
          class: "text-primary underline hover:opacity-80 transition-opacity",
          target: "_blank",
          rel: "noopener noreferrer",
        },
      }),
      Placeholder.configure({
        placeholder: placeholder || t("page.webmail.compose_body"),
      }),
    ],
    content: initialContent || "",
    editable: !disabled,
    onUpdate: ({ editor }) => {
      if (isUpdatingRef.current) return;
      if (!isMountedRef.current) return;
      const html = editor.getHTML();
      const text = editor.getText();
      const isEmpty = editor.isEmpty;
      setSourceCode(isEmpty ? "" : html);
      onChange({ html: isEmpty ? "" : html, text });
    },
    editorProps: {
      attributes: {
        class:
          "min-h-[220px] max-h-[500px] overflow-y-auto px-4 py-3 text-sm focus:outline-none prose prose-sm max-w-none dark:prose-invert font-sans leading-relaxed selection:bg-primary/20",
      },
    },
  });

  useEffect(() => {
    // Delay marking as mounted to prevent initial mount onUpdate from triggering
    const timer = setTimeout(() => {
      isMountedRef.current = true;
    }, 50);
    return () => clearTimeout(timer);
  }, []);

  // Sync external changes when initial content changes significantly
  useEffect(() => {
    if (!editor) return;
    const currentHTML = editor.getHTML();
    if (initialContent !== currentHTML && !editor.isFocused) {
      isUpdatingRef.current = true;
      editor.commands.setContent(initialContent || "");
      setSourceCode(initialContent);
      isUpdatingRef.current = false;
    }
  }, [initialContent, editor]);

  // Sync disabled state
  useEffect(() => {
    if (editor) {
      editor.setEditable(!disabled);
    }
  }, [disabled, editor]);

  const handleInsertLink = () => {
    if (!editor) return;
    const previousUrl = editor.getAttributes("link").href;
    const url = window.prompt(
      t("page.webmail.compose_link_prompt"),
      previousUrl || "https://",
    );
    if (url === null) return;
    if (url === "") {
      editor.chain().focus().extendMarkRange("link").unsetLink().run();
      return;
    }
    editor.chain().focus().extendMarkRange("link").setLink({ href: url }).run();
  };

  const handleSourceChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const next = e.target.value;
    setSourceCode(next);
    if (editor) {
      isUpdatingRef.current = true;
      editor.commands.setContent(next || "");
      onChange({ html: next, text: editor.getText() });
      isUpdatingRef.current = false;
    }
  };

  return (
    <div className="border border-border/70 rounded-lg overflow-hidden bg-background focus-within:ring-1 focus-within:ring-ring/40 transition-all shadow-xs">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-0.5 px-2 py-1.5 bg-muted/40 border-b border-border/60 text-muted-foreground select-none">
        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("bold") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_bold")}
          onClick={() => editor?.chain().focus().toggleBold().run()}
        >
          <Bold className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("italic") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_italic")}
          onClick={() => editor?.chain().focus().toggleItalic().run()}
        >
          <Italic className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("strike") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_strikethrough")}
          onClick={() => editor?.chain().focus().toggleStrike().run()}
        >
          <Strikethrough className="h-3.5 w-3.5" />
        </Button>

        <div className="h-3.5 w-px bg-border/70 mx-1" />

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("bulletList") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_bullet_list")}
          onClick={() => editor?.chain().focus().toggleBulletList().run()}
        >
          <List className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("orderedList") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_ordered_list")}
          onClick={() => editor?.chain().focus().toggleOrderedList().run()}
        >
          <ListOrdered className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("blockquote") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_quote")}
          onClick={() => editor?.chain().focus().toggleBlockquote().run()}
        >
          <Quote className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("link") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_link")}
          onClick={handleInsertLink}
        >
          <LinkIcon className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_clear_format")}
          onClick={() =>
            editor?.chain().focus().unsetAllMarks().clearNodes().run()
          }
        >
          <RemoveFormatting className="h-3.5 w-3.5" />
        </Button>

        <div className="h-3.5 w-px bg-border/70 mx-1" />

        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor?.can().undo()}
          title="撤销"
          onClick={() => editor?.chain().focus().undo().run()}
        >
          <Undo2 className="h-3.5 w-3.5" />
        </Button>

        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor?.can().redo()}
          title="重做"
          onClick={() => editor?.chain().focus().redo().run()}
        >
          <Redo2 className="h-3.5 w-3.5" />
        </Button>

        <div className="flex-1" />

        {onToggleSourceMode && (
          <Button
            type="button"
            size="sm"
            variant={isSourceMode ? "secondary" : "ghost"}
            className="h-7 px-2 text-xs gap-1 font-mono cursor-pointer"
            disabled={disabled}
            title={
              isSourceMode
                ? t("page.webmail.compose_rich_text")
                : t("page.webmail.compose_source_code")
            }
            onClick={onToggleSourceMode}
          >
            <Code className="h-3.5 w-3.5" />
            <span>
              {isSourceMode
                ? t("page.webmail.compose_rich_text")
                : t("page.webmail.compose_source_code")}
            </span>
          </Button>
        )}
      </div>

      {/* Editor Body */}
      {isSourceMode ? (
        <textarea
          rows={10}
          value={sourceCode}
          disabled={disabled}
          onChange={handleSourceChange}
          placeholder="<html><body>...</body></html>"
          className="w-full h-full min-h-[220px] p-3 font-mono text-xs bg-muted/10 outline-none resize-y"
        />
      ) : (
        <EditorContent editor={editor} />
      )}
    </div>
  );
}
