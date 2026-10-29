import { useState, useEffect, useCallback, useRef } from "react";

export type LayoutMode = "vertical" | "horizontal";

interface UseResizablePanesProps {
  initialFolderWidth?: number;
  initialListWidth?: number;
  initialListHeight?: number;
  minFolderWidth?: number;
  maxFolderWidth?: number;
  minListWidth?: number;
  maxListWidth?: number;
  minListHeight?: number;
  maxListHeight?: number;
}

export function useResizablePanes({
  initialFolderWidth = 220,
  initialListWidth = 380,
  initialListHeight = 350,
  minFolderWidth = 160,
  maxFolderWidth = 360,
  minListWidth = 280,
  maxListWidth = 650,
  minListHeight = 180,
  maxListHeight = 600,
}: UseResizablePanesProps = {}) {
  const [folderWidth, setFolderWidth] = useState(() => {
    const saved = localStorage.getItem("urmail.webmail.folderWidth");
    return saved
      ? Math.max(minFolderWidth, Math.min(maxFolderWidth, parseInt(saved, 10)))
      : initialFolderWidth;
  });

  const [listWidth, setListWidth] = useState(() => {
    const saved = localStorage.getItem("urmail.webmail.listWidth");
    return saved
      ? Math.max(minListWidth, Math.min(maxListWidth, parseInt(saved, 10)))
      : initialListWidth;
  });

  const [listHeight, setListHeight] = useState(() => {
    const saved = localStorage.getItem("urmail.webmail.listHeight");
    return saved
      ? Math.max(minListHeight, Math.min(maxListHeight, parseInt(saved, 10)))
      : initialListHeight;
  });

  const [layoutMode, setLayoutMode] = useState<LayoutMode>(() => {
    const saved = localStorage.getItem("urmail.webmail.layoutMode");
    return saved === "horizontal" ? "horizontal" : "vertical";
  });

  const [isDragging, setIsDragging] = useState(false);
  const dragTargetRef = useRef<"folder" | "list-width" | "list-height" | null>(
    null,
  );
  const startPosRef = useRef({ x: 0, y: 0 });
  const startDimRef = useRef(0);

  const startDragging = useCallback(
    (
      target: "folder" | "list-width" | "list-height",
      clientX: number,
      clientY: number,
    ) => {
      dragTargetRef.current = target;
      startPosRef.current = { x: clientX, y: clientY };
      if (target === "folder") startDimRef.current = folderWidth;
      else if (target === "list-width") startDimRef.current = listWidth;
      else if (target === "list-height") startDimRef.current = listHeight;
      setIsDragging(true);
      document.body.style.userSelect = "none";
      document.body.style.cursor =
        target === "list-height" ? "row-resize" : "col-resize";
    },
    [folderWidth, listWidth, listHeight],
  );

  useEffect(() => {
    if (!isDragging) return;

    const handleMouseMove = (e: MouseEvent) => {
      const target = dragTargetRef.current;
      if (!target) return;

      if (target === "folder") {
        const deltaX = e.clientX - startPosRef.current.x;
        const newWidth = Math.max(
          minFolderWidth,
          Math.min(maxFolderWidth, startDimRef.current + deltaX),
        );
        setFolderWidth(newWidth);
      } else if (target === "list-width") {
        const deltaX = e.clientX - startPosRef.current.x;
        const newWidth = Math.max(
          minListWidth,
          Math.min(maxListWidth, startDimRef.current + deltaX),
        );
        setListWidth(newWidth);
      } else if (target === "list-height") {
        const deltaY = e.clientY - startPosRef.current.y;
        const newHeight = Math.max(
          minListHeight,
          Math.min(maxListHeight, startDimRef.current + deltaY),
        );
        setListHeight(newHeight);
      }
    };

    const handleMouseUp = () => {
      setIsDragging(false);
      const target = dragTargetRef.current;
      if (target === "folder") {
        localStorage.setItem("urmail.webmail.folderWidth", String(folderWidth));
      } else if (target === "list-width") {
        localStorage.setItem("urmail.webmail.listWidth", String(listWidth));
      } else if (target === "list-height") {
        localStorage.setItem("urmail.webmail.listHeight", String(listHeight));
      }
      dragTargetRef.current = null;
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
    };

    window.addEventListener("mousemove", handleMouseMove);
    window.addEventListener("mouseup", handleMouseUp);

    return () => {
      window.removeEventListener("mousemove", handleMouseMove);
      window.removeEventListener("mouseup", handleMouseUp);
    };
  }, [
    isDragging,
    folderWidth,
    listWidth,
    listHeight,
    maxFolderWidth,
    maxListHeight,
    maxListWidth,
    minFolderWidth,
    minListHeight,
    minListWidth,
  ]);

  const toggleLayoutMode = useCallback(() => {
    setLayoutMode((prev) => {
      const next = prev === "vertical" ? "horizontal" : "vertical";
      localStorage.setItem("urmail.webmail.layoutMode", next);
      return next;
    });
  }, []);

  return {
    folderWidth,
    listWidth,
    listHeight,
    layoutMode,
    isDragging,
    startDragging,
    toggleLayoutMode,
    setLayoutMode,
  };
}

export function SplitterHandle({
  orientation = "vertical",
  onMouseDown,
}: {
  orientation?: "vertical" | "horizontal";
  onMouseDown: (e: React.MouseEvent) => void;
}) {
  return (
    <div
      role="separator"
      tabIndex={0}
      aria-orientation={orientation}
      className={
        orientation === "vertical"
          ? "w-1 hover:w-1.5 -mx-0.5 z-10 cursor-col-resize hover:bg-primary/40 active:bg-primary transition-all shrink-0 select-none group flex items-center justify-center"
          : "h-1 hover:h-1.5 -my-0.5 z-10 cursor-row-resize hover:bg-primary/40 active:bg-primary transition-all shrink-0 select-none group flex items-center justify-center"
      }
      onMouseDown={onMouseDown}
    >
      <div
        className={
          orientation === "vertical"
            ? "h-8 w-0.5 rounded-full bg-border group-hover:bg-primary/80 transition-colors"
            : "w-8 h-0.5 rounded-full bg-border group-hover:bg-primary/80 transition-colors"
        }
      />
    </div>
  );
}
