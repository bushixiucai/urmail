// 批次 2 B2-2（WEBMAIL-BATCH2-SPEC.md §三）：骨架屏。
// 8 行双条列表骨架 / 阅读区骨架；块底 --vellum-surface-muted、圆角 4px、
// 无描边、无 pulse 循环（PHASE3 规格明令无闪光），整块一次性 opacity 淡入。
// 宽度用确定性伪随机（index 驱动，±10%），避免每次渲染跳动。

export function SkeletonBlock({
  className = "",
  style,
}: {
  className?: string;
  style?: React.CSSProperties;
}) {
  return (
    <div
      aria-hidden="true"
      className={`vellum-skeleton-block ${className}`}
      style={style}
    />
  );
}

/** 单行双条骨架（分页加载等单行场景）。 */
export function SkeletonRow({ height }: { height: number }) {
  return (
    <div
      className="flex items-center gap-3 px-4 border-b border-[var(--vellum-border)]"
      style={{ height }}
    >
      <SkeletonBlock className="w-8 h-8 rounded-full shrink-0" />
      <div className="flex-1 space-y-1.5 min-w-0">
        <SkeletonBlock className="h-[13px]" style={{ width: "58%" }} />
        <SkeletonBlock className="h-3" style={{ width: "38%" }} />
      </div>
    </div>
  );
}

// 主条宽度伪随机序列（55–65%，行间 ±10%）与次条（35–45%）。
const MAIN_WIDTHS = [60, 55, 65, 58, 63, 56, 66, 61];
const SUB_WIDTHS = [40, 35, 44, 37, 42, 36, 45, 38];

export function MailListSkeleton({
  rows = 8,
  rowHeight,
  testId,
}: {
  rows?: number;
  rowHeight: number;
  testId?: string;
}) {
  return (
    <div data-testid={testId} aria-busy="true" className="vellum-skeleton-fade">
      {Array.from({ length: rows }, (_, index) => (
        <div
          key={index}
          className="vellum-skeleton-fade"
          style={{ animationDelay: `${index * 20}ms` }}
        >
          <div
            className="flex items-center gap-3 px-4 border-b border-[var(--vellum-border)]"
            style={{ height: rowHeight }}
          >
            <SkeletonBlock className="w-8 h-8 rounded-full shrink-0" />
            <div className="flex-1 space-y-1.5 min-w-0">
              <SkeletonBlock
                className="h-[13px]"
                style={{ width: `${MAIN_WIDTHS[index % MAIN_WIDTHS.length]}%` }}
              />
              <SkeletonBlock
                className="h-3"
                style={{ width: `${SUB_WIDTHS[index % SUB_WIDTHS.length]}%` }}
              />
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

/** 阅读区骨架：标题条 2/3（16px）+ 发件人行 1/3（12px）+ 正文两条（100%/90%，12px）+ 96px 块。 */
export function ReadingSkeleton({
  label,
  testId,
}: {
  label?: string;
  testId?: string;
}) {
  return (
    <div
      data-testid={testId}
      aria-busy="true"
      aria-label={label}
      className="vellum-skeleton-fade space-y-3"
    >
      <SkeletonBlock className="h-4 w-2/3" />
      <SkeletonBlock className="h-3 w-1/3" />
      <SkeletonBlock className="h-3 w-full" />
      <SkeletonBlock className="h-3 w-[90%]" />
      <SkeletonBlock className="h-24 w-full" />
    </div>
  );
}
