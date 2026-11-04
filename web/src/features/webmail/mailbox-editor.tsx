import { useEffect, useRef, useState } from "react";
import { useEditor, EditorContent, type Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Link from "@tiptap/extension-link";
import Placeholder from "@tiptap/extension-placeholder";
import { Node, Extension, Mark } from "@tiptap/core";
import * as pmTables from "@tiptap/pm/tables";
import type { Schema, Node as ProseMirrorNode } from "@tiptap/pm/model";
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
  Table as TableIcon,
  Plus,
  Trash2,
  Rows,
  Columns,
  Palette,
  Image as ImageIcon,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import type { OutgoingAttachment } from "./mailbox-api";

export function ensureTableTypes(schema: Schema) {
  if (schema.nodes.table) schema.nodes.table.spec.tableRole = "table";
  if (schema.nodes.table_row) schema.nodes.table_row.spec.tableRole = "row";
  if (schema.nodes.table_cell) schema.nodes.table_cell.spec.tableRole = "cell";
  if (schema.nodes.table_header)
    schema.nodes.table_header.spec.tableRole = "header_cell";
  schema.cached.tableNodeTypes = {
    table: schema.nodes.table,
    row: schema.nodes.table_row,
    cell: schema.nodes.table_cell,
    header_cell: schema.nodes.table_header,
  };
}

export const CustomTable = Node.create({
  name: "table",
  group: "block",
  content: "table_row+",
  isolating: true,
  parseHTML() {
    return [{ tag: "table" }];
  },
  renderHTML({ HTMLAttributes }) {
    return [
      "table",
      {
        ...HTMLAttributes,
        class: "border-collapse border border-border/80 my-3 w-full text-sm",
      },
      ["tbody", 0],
    ];
  },
});

export const CustomTableRow = Node.create({
  name: "table_row",
  content: "(table_cell | table_header)*",
  parseHTML() {
    return [{ tag: "tr" }];
  },
  renderHTML({ HTMLAttributes }) {
    return ["tr", HTMLAttributes, 0];
  },
});

export const CustomTableCell = Node.create({
  name: "table_cell",
  content: "block+",
  isolating: true,
  addAttributes() {
    return {
      colspan: { default: 1 },
      rowspan: { default: 1 },
      colwidth: { default: null },
    };
  },
  parseHTML() {
    return [{ tag: "td" }];
  },
  renderHTML({ HTMLAttributes }) {
    return [
      "td",
      {
        ...HTMLAttributes,
        class: "border border-border/60 p-2 min-w-[60px] align-top text-left",
      },
      0,
    ];
  },
});

export const CustomTableHeader = Node.create({
  name: "table_header",
  content: "block+",
  isolating: true,
  addAttributes() {
    return {
      colspan: { default: 1 },
      rowspan: { default: 1 },
      colwidth: { default: null },
    };
  },
  parseHTML() {
    return [{ tag: "th" }];
  },
  renderHTML({ HTMLAttributes }) {
    return [
      "th",
      {
        ...HTMLAttributes,
        class:
          "border border-border/60 p-2 bg-muted/60 font-semibold text-left min-w-[60px] align-top",
      },
      0,
    ];
  },
});

export const TableSetup = Extension.create({
  name: "tableSetup",
  onCreate() {
    ensureTableTypes(this.editor.schema);
  },
  addProseMirrorPlugins() {
    return [pmTables.tableEditing()];
  },
});

export const CustomImage = Node.create({
  name: "image",
  group: "inline",
  inline: true,
  draggable: true,
  selectable: true,
  atom: true,
  addAttributes() {
    return {
      src: { default: null },
      alt: { default: null },
      title: { default: null },
      "data-cid": { default: null },
    };
  },
  parseHTML() {
    return [{ tag: "img[src]" }];
  },
  renderHTML({ HTMLAttributes }) {
    return [
      "img",
      {
        ...HTMLAttributes,
        class:
          "max-w-full h-auto rounded my-1.5 inline-block align-middle shadow-xs border border-border/40",
      },
    ];
  },
});

export const FontColorMark = Mark.create({
  name: "textColor",
  addAttributes() {
    return {
      color: {
        default: null,
        parseHTML: (el) => el.style.color || el.getAttribute("color") || null,
        renderHTML: (attrs) => {
          if (!attrs.color) return {};
          return { style: `color: ${attrs.color}` };
        },
      },
    };
  },
  parseHTML() {
    return [{ tag: "span[style*=color]" }, { tag: "font[color]" }];
  },
  renderHTML({ HTMLAttributes }) {
    return ["span", HTMLAttributes, 0];
  },
});

export const FontSizeMark = Mark.create({
  name: "fontSize",
  addAttributes() {
    return {
      size: {
        default: null,
        parseHTML: (el) => el.style.fontSize || null,
        renderHTML: (attrs) => {
          if (!attrs.size) return {};
          return { style: `font-size: ${attrs.size}` };
        },
      },
    };
  },
  parseHTML() {
    return [{ tag: "span[style*=font-size]" }];
  },
  renderHTML({ HTMLAttributes }) {
    return ["span", HTMLAttributes, 0];
  },
});

export interface MailboxEditorProps {
  initialContent: string;
  isHtml?: boolean;
  disabled?: boolean;
  placeholder?: string;
  onChange: (data: { html: string; text: string }) => void;
  isSourceMode?: boolean;
  onToggleSourceMode?: () => void;
  onCompositionChange?: (composing: boolean) => void;
  onPasteAttachment?: (attachment: OutgoingAttachment) => void;
  onEditorReady?: (editor: Editor) => void;
}

const PRESET_COLORS = [
  { label: "Default", value: "" },
  { label: "Black", value: "#000000" },
  { label: "Dark Gray", value: "#4b5563" },
  { label: "Red", value: "#dc2626" },
  { label: "Blue", value: "#2563eb" },
  { label: "Green", value: "#16a34a" },
  { label: "Amber", value: "#d97706" },
  { label: "Purple", value: "#9333ea" },
];

const PRESET_SIZES = [
  { label: "Normal", value: "" },
  { label: "Small", value: "12px" },
  { label: "Medium", value: "16px" },
  { label: "Large", value: "18px" },
  { label: "Huge", value: "24px" },
];

export function MailboxEditor({
  initialContent,
  isHtml = true,
  onCompositionChange,
  disabled = false,
  placeholder = "",
  onChange,
  isSourceMode = false,
  onToggleSourceMode,
  onPasteAttachment,
  onEditorReady,
}: MailboxEditorProps) {
  const { t } = useTranslation();
  const isUpdatingRef = useRef(false);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const onPasteAttachmentRef = useRef(onPasteAttachment);
  onPasteAttachmentRef.current = onPasteAttachment;
  const imageInputRef = useRef<HTMLInputElement>(null);

  const [showColorPicker, setShowColorPicker] = useState(false);
  const [showTableMenu, setShowTableMenu] = useState(false);
  const [fontSize, setFontSize] = useState("");

  const normalize = (value: string) =>
    isHtml
      ? value
      : value
          .replace(/&/g, "&amp;")
          .replace(/</g, "&lt;")
          .replace(/>/g, "&gt;")
          .replace(/\n/g, "<br>");
  const [sourceCode, setSourceCode] = useState(initialContent);

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: { levels: [1, 2, 3] },
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
      CustomTable,
      CustomTableRow,
      CustomTableCell,
      CustomTableHeader,
      TableSetup,
      CustomImage,
      FontColorMark,
      FontSizeMark,
    ],
    content: normalize(initialContent) || "",
    editable: !disabled,
    onUpdate: ({ editor }) => {
      if (isUpdatingRef.current) return;
      const html = editor.getHTML();
      const text = editor.getText();
      const isEmpty = editor.isEmpty;
      setSourceCode(isEmpty ? "" : html);
      onChangeRef.current({ html: isEmpty ? "" : html, text });
    },
    editorProps: {
      attributes: {
        role: "textbox",
        "aria-label": t("page.webmail.compose_rich_text"),
        "aria-multiline": "true",
        class:
          "min-h-[220px] max-h-[500px] overflow-y-auto px-4 py-3 text-sm focus:outline-none prose prose-sm max-w-none dark:prose-invert font-sans leading-relaxed selection:bg-primary/20",
      },
      handlePaste(view, event) {
        const items = event.clipboardData?.items;
        if (!items) return false;
        for (let i = 0; i < items.length; i++) {
          const item = items[i];
          if (item.type.startsWith("image/")) {
            const file = item.getAsFile();
            if (!file) continue;
            const reader = new FileReader();
            reader.onload = () => {
              const dataUrl = String(reader.result || "");
              const b64 = dataUrl.includes(",")
                ? dataUrl.split(",")[1]
                : dataUrl;
              const cid = `inline-${Date.now()}-${Math.random()
                .toString(36)
                .slice(2, 8)}@urmail`;
              const outgoing: OutgoingAttachment = {
                filename: file.name || `screenshot-${Date.now()}.png`,
                content_type: file.type || "image/png",
                content_base64: b64,
                inline: true,
                content_id: cid,
              };
              onPasteAttachmentRef.current?.(outgoing);
              const node = view.state.schema.nodes.image.create({
                src: dataUrl,
                alt: file.name || "clipboard-image",
                "data-cid": cid,
              });
              view.dispatch(view.state.tr.replaceSelectionWith(node));
            };
            reader.readAsDataURL(file);
            return true;
          }
        }
        return false;
      },
      transformPastedHTML(html) {
        if (!html) return html;
        return html
          .replace(/<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/gi, "")
          .replace(/<iframe\b[^<]*(?:(?!<\/iframe>)<[^<]*)*<\/iframe>/gi, "")
          .replace(/\bon\w+\s*=\s*(["'][^"']*["']|[^\s>]+)/gi, "")
          .replace(/href\s*=\s*["']\s*javascript:[^"']*["']/gi, 'href="#"');
      },
    },
  });

  useEffect(() => {
    if (editor && onEditorReady) {
      onEditorReady(editor);
    }
  }, [editor, onEditorReady]);

  // External adoption, signature changes and quote changes also update a focused editor.
  useEffect(() => {
    if (!editor || isUpdatingRef.current) return;
    const next = isHtml
      ? initialContent
      : initialContent
          .replace(/&/g, "&amp;")
          .replace(/</g, "&lt;")
          .replace(/>/g, "&gt;")
          .replace(/\n/g, "<br>");
    if (
      next === editor.getHTML() ||
      (!isHtml && initialContent === editor.getText())
    )
      return;
    isUpdatingRef.current = true;
    editor.commands.setContent(next || "", { emitUpdate: false });
    setSourceCode(isSourceMode ? initialContent : editor.getHTML());
    isUpdatingRef.current = false;
  }, [initialContent, isHtml, editor, isSourceMode]);

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

  const handleInsertTable = (rows = 3, cols = 3) => {
    if (!editor) return;
    ensureTableTypes(editor.schema);
    const schema = editor.schema;
    const rowNodes: ProseMirrorNode[] = [];
    for (let r = 0; r < rows; r++) {
      const cells: ProseMirrorNode[] = [];
      const isHeader = r === 0;
      const type = isHeader
        ? schema.nodes.table_header
        : schema.nodes.table_cell;
      for (let c = 0; c < cols; c++) {
        const cell = type.createAndFill();
        if (cell) cells.push(cell);
      }
      const row = schema.nodes.table_row.create(null, cells);
      if (row) rowNodes.push(row);
    }
    const table = schema.nodes.table.create(null, rowNodes);
    editor.view.dispatch(
      editor.view.state.tr.replaceSelectionWith(table).scrollIntoView(),
    );
    editor.view.focus();
    setShowTableMenu(false);
  };

  const handleAddRow = () => {
    if (!editor) return;
    ensureTableTypes(editor.schema);
    pmTables.addRowAfter(editor.view.state, editor.view.dispatch);
    editor.view.focus();
  };

  const handleAddCol = () => {
    if (!editor) return;
    ensureTableTypes(editor.schema);
    pmTables.addColumnAfter(editor.view.state, editor.view.dispatch);
    editor.view.focus();
  };

  const handleDeleteRow = () => {
    if (!editor) return;
    ensureTableTypes(editor.schema);
    pmTables.deleteRow(editor.view.state, editor.view.dispatch);
    editor.view.focus();
  };

  const handleDeleteCol = () => {
    if (!editor) return;
    ensureTableTypes(editor.schema);
    pmTables.deleteColumn(editor.view.state, editor.view.dispatch);
    editor.view.focus();
  };

  const handleDeleteTable = () => {
    if (!editor) return;
    ensureTableTypes(editor.schema);
    pmTables.deleteTable(editor.view.state, editor.view.dispatch);
    editor.view.focus();
  };

  const handleSetColor = (color: string) => {
    if (!editor) return;
    if (!color) {
      editor.chain().focus().unsetMark("textColor").run();
    } else {
      editor.chain().focus().setMark("textColor", { color }).run();
    }
    setShowColorPicker(false);
  };

  const handleSetFontSize = (size: string) => {
    setFontSize(size);
    if (!editor) return;
    if (!size) {
      editor.chain().focus().unsetMark("fontSize").run();
    } else {
      editor.chain().focus().setMark("fontSize", { size }).run();
    }
  };

  const handleImageFileSelect = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file || !editor) return;
    const reader = new FileReader();
    reader.onload = () => {
      const dataUrl = String(reader.result || "");
      const b64 = dataUrl.includes(",") ? dataUrl.split(",")[1] : dataUrl;
      const cid = `inline-${Date.now()}-${Math.random()
        .toString(36)
        .slice(2, 8)}@urmail`;
      const outgoing: OutgoingAttachment = {
        filename: file.name || `image-${Date.now()}.png`,
        content_type: file.type || "image/png",
        content_base64: b64,
        inline: true,
        content_id: cid,
      };
      onPasteAttachmentRef.current?.(outgoing);
      const node = editor.schema.nodes.image.create({
        src: dataUrl,
        alt: file.name || "uploaded image",
        "data-cid": cid,
      });
      editor.view.dispatch(
        editor.view.state.tr.replaceSelectionWith(node).scrollIntoView(),
      );
    };
    reader.readAsDataURL(file);
    e.target.value = "";
  };

  const handleSourceChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const next = e.target.value;
    setSourceCode(next);
    if (editor) {
      isUpdatingRef.current = true;
      editor.commands.setContent(next || "");
      onChangeRef.current({ html: next, text: editor.getText() });
      isUpdatingRef.current = false;
    }
  };

  const isTableActive =
    editor?.isActive("table") ||
    editor?.isActive("table_row") ||
    editor?.isActive("table_cell") ||
    editor?.isActive("table_header");

  return (
    <div className="border border-border/70 rounded-lg overflow-hidden bg-background focus-within:ring-1 focus-within:ring-ring/40 transition-all shadow-xs relative">
      <input
        ref={imageInputRef}
        type="file"
        accept="image/*"
        className="hidden"
        onChange={handleImageFileSelect}
      />

      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-0.5 px-2 py-1.5 bg-muted/40 border-b border-border/60 text-muted-foreground select-none">
        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("bold") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_bold")}
          aria-label={t("page.webmail.compose_bold")}
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
          aria-label={t("page.webmail.compose_italic")}
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
          aria-label={t("page.webmail.compose_strikethrough")}
          onClick={() => editor?.chain().focus().toggleStrike().run()}
        >
          <Strikethrough className="h-3.5 w-3.5" />
        </Button>

        {/* Font size selector */}
        <select
          title={t("page.webmail.compose_font_size", {
            defaultValue: "Font size",
          })}
          aria-label={t("page.webmail.compose_font_size", {
            defaultValue: "Font size",
          })}
          disabled={disabled || isSourceMode || !editor}
          className="h-7 px-1.5 text-xs bg-transparent border border-border/50 rounded hover:bg-muted/50 cursor-pointer focus:outline-none"
          value={fontSize || editor?.getAttributes("fontSize")?.size || ""}
          onChange={(e) => handleSetFontSize(e.target.value)}
        >
          {PRESET_SIZES.map((s) => (
            <option key={s.label} value={s.value}>
              {s.label}
            </option>
          ))}
        </select>

        {/* Font color picker button */}
        <div className="relative">
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-7 w-7 p-0 cursor-pointer"
            disabled={disabled || isSourceMode || !editor}
            title={t("page.webmail.compose_font_color", {
              defaultValue: "Text color",
            })}
            aria-label={t("page.webmail.compose_font_color", {
              defaultValue: "Text color",
            })}
            onClick={() => setShowColorPicker(!showColorPicker)}
          >
            <Palette className="h-3.5 w-3.5" />
          </Button>

          {showColorPicker && (
            <div className="absolute top-8 left-0 z-50 p-2 bg-popover border border-border rounded-md shadow-md flex flex-wrap gap-1 w-36">
              {PRESET_COLORS.map((c) => (
                <button
                  key={c.label}
                  type="button"
                  title={c.label}
                  className="h-6 w-6 rounded-full border border-border cursor-pointer flex items-center justify-center transition-transform hover:scale-110"
                  style={{
                    backgroundColor: c.value || "var(--foreground)",
                  }}
                  onClick={() => handleSetColor(c.value)}
                />
              ))}
            </div>
          )}
        </div>

        <div className="h-3.5 w-px bg-border/70 mx-1" />

        <Button
          type="button"
          size="sm"
          variant={editor?.isActive("bulletList") ? "secondary" : "ghost"}
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_bullet_list")}
          aria-label={t("page.webmail.compose_bullet_list")}
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
          aria-label={t("page.webmail.compose_ordered_list")}
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
          aria-label={t("page.webmail.compose_quote")}
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
          aria-label={t("page.webmail.compose_link")}
          onClick={handleInsertLink}
        >
          <LinkIcon className="h-3.5 w-3.5" />
        </Button>

        {/* Image insertion */}
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_insert_image", {
            defaultValue: "Insert image",
          })}
          aria-label={t("page.webmail.compose_insert_image", {
            defaultValue: "Insert image",
          })}
          onClick={() => imageInputRef.current?.click()}
        >
          <ImageIcon className="h-3.5 w-3.5" />
        </Button>

        {/* Table insertion and table commands */}
        <div className="relative">
          <Button
            type="button"
            size="sm"
            variant={isTableActive ? "secondary" : "ghost"}
            className="h-7 w-7 p-0 cursor-pointer"
            disabled={disabled || isSourceMode || !editor}
            title={t("page.webmail.compose_insert_table", {
              defaultValue: "Table",
            })}
            aria-label={t("page.webmail.compose_insert_table", {
              defaultValue: "Table",
            })}
            onClick={() => {
              if (isTableActive) {
                setShowTableMenu(!showTableMenu);
              } else {
                handleInsertTable(3, 3);
              }
            }}
          >
            <TableIcon className="h-3.5 w-3.5" />
          </Button>

          {showTableMenu && isTableActive && (
            <div className="absolute top-8 left-0 z-50 p-1.5 bg-popover border border-border rounded-md shadow-md flex flex-col gap-1 min-w-32 text-xs">
              <button
                type="button"
                className="px-2 py-1 text-left rounded hover:bg-muted cursor-pointer flex items-center gap-1.5"
                onClick={handleAddRow}
              >
                <Rows className="h-3 w-3" />
                {t("page.webmail.compose_add_row", { defaultValue: "Add row" })}
              </button>
              <button
                type="button"
                className="px-2 py-1 text-left rounded hover:bg-muted cursor-pointer flex items-center gap-1.5"
                onClick={handleAddCol}
              >
                <Columns className="h-3 w-3" />
                {t("page.webmail.compose_add_col", {
                  defaultValue: "Add column",
                })}
              </button>
              <div className="h-px bg-border/60 my-0.5" />
              <button
                type="button"
                className="px-2 py-1 text-left rounded hover:bg-muted cursor-pointer text-destructive flex items-center gap-1.5"
                onClick={handleDeleteRow}
              >
                <Trash2 className="h-3 w-3" />
                {t("page.webmail.compose_delete_row", {
                  defaultValue: "Delete row",
                })}
              </button>
              <button
                type="button"
                className="px-2 py-1 text-left rounded hover:bg-muted cursor-pointer text-destructive flex items-center gap-1.5"
                onClick={handleDeleteCol}
              >
                <Trash2 className="h-3 w-3" />
                {t("page.webmail.compose_delete_col", {
                  defaultValue: "Delete col",
                })}
              </button>
              <button
                type="button"
                className="px-2 py-1 text-left rounded hover:bg-muted cursor-pointer text-destructive flex items-center gap-1.5"
                onClick={handleDeleteTable}
              >
                <Trash2 className="h-3 w-3" />
                {t("page.webmail.compose_delete_table", {
                  defaultValue: "Delete table",
                })}
              </button>
            </div>
          )}
        </div>

        {/* Quick table controls when table is active */}
        {isTableActive && !isSourceMode && (
          <div className="flex items-center gap-0.5 ml-1 pl-1 border-l border-border/70">
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 px-1 text-xs gap-0.5 cursor-pointer"
              title={t("page.webmail.compose_add_row", {
                defaultValue: "Add row",
              })}
              aria-label={t("page.webmail.compose_add_row", {
                defaultValue: "Add row",
              })}
              onClick={handleAddRow}
            >
              <Plus className="h-3 w-3" />
              <span className="text-[10px]">Row</span>
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 px-1 text-xs gap-0.5 cursor-pointer"
              title={t("page.webmail.compose_add_col", {
                defaultValue: "Add col",
              })}
              aria-label={t("page.webmail.compose_add_col", {
                defaultValue: "Add col",
              })}
              onClick={handleAddCol}
            >
              <Plus className="h-3 w-3" />
              <span className="text-[10px]">Col</span>
            </Button>
          </div>
        )}

        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0 cursor-pointer"
          disabled={disabled || isSourceMode || !editor}
          title={t("page.webmail.compose_clear_format")}
          aria-label={t("page.webmail.compose_clear_format")}
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
          title={t("page.webmail.compose_undo")}
          aria-label={t("page.webmail.compose_undo")}
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
          title={t("page.webmail.compose_redo")}
          aria-label={t("page.webmail.compose_redo")}
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
          aria-label={t("page.webmail.compose_source_code")}
          onCompositionStart={() => onCompositionChange?.(true)}
          onCompositionEnd={() => onCompositionChange?.(false)}
          rows={10}
          value={sourceCode}
          disabled={disabled}
          onChange={handleSourceChange}
          placeholder="<html><body>...</body></html>"
          className="w-full h-full min-h-[220px] p-3 font-mono text-xs bg-muted/10 outline-none resize-y"
        />
      ) : (
        <EditorContent
          editor={editor}
          onCompositionStart={() => onCompositionChange?.(true)}
          onCompositionEnd={() => onCompositionChange?.(false)}
        />
      )}
    </div>
  );
}
