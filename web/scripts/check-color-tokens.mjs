// CI 守门：硬编码色与品牌蓝预算断言
// - WEBMAIL-BATCH2-SPEC.md 2.3 R3/R4：hex ratchet、`dark:` 前缀类为 0
// - BRAND-REGRESS-REVIEW-20261128 BR-013：扫描面扩至 web/src（排除 test 与
//   theme-vellum.css），阈值按扩面后基线冻结（ratchet，只许下降）
// - BRAND-REGRESS-REVIEW-20261128 BR-014：--brand-primary / --vellum-brand-*
//   消费白名单＝预算三类（未读点 / 3px 阅读指示条 / 焦点环）＋ 搜索 chips
//   选中 outline（BR-011 判定预算内）；越界即红，新消费须走「单点注册 +
//   白名单扩充」流程
// 用法: node scripts/check-color-tokens.mjs  （接入 npm run lint）
import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB_ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const TARGET = path.join(WEB_ROOT, "src");
const MAX_HEX = 12; // BR-013 扩面基线冻结 @pm-brand-batch1-1（bdbe9225+e016b9d7 实测）
const MAX_BLUE_CLASSES = 17; // BR-013 蓝系 Tailwind 类基线冻结（旧 webmail/platform 待重写线/批次 2 吸收）

function sh(cmd, args, opts = {}) {
  return execFileSync(cmd, args, {
    cwd: WEB_ROOT,
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
    ...opts,
  });
}

function grepCount(args) {
  try {
    return sh("grep", args, { stdio: ["ignore", "pipe", "ignore"] })
      .split("\n")
      .filter(Boolean).length;
  } catch {
    return 0; // grep 无匹配时 exit 1
  }
}

// R3: 硬编码 hex 计数（全 src tsx，排除测试；theme-vellum.css 为令牌注册单点，豁免）
let hexOut = "";
try {
  hexOut = sh("grep", [
    "-rhoE",
    "#[0-9A-Fa-f]{6}",
    TARGET,
    "--include=*.tsx",
    "--exclude=*.test.*",
  ]);
} catch {
  hexOut = "";
}
const hexTotal = hexOut.split("\n").filter(Boolean).length;

// 逐文件计数，便于超限时定位（零匹配 → 空表）
let fileOut = "";
try {
  fileOut = sh("grep", [
    "-rcE",
    "#[0-9A-Fa-f]{6}",
    TARGET,
    "--include=*.tsx",
    "--exclude=*.test.*",
  ]);
} catch {
  fileOut = "";
}
const perFile = fileOut
  .split("\n")
  .filter((l) => !l.endsWith(":0"))
  .map((l) => {
    const idx = l.lastIndexOf(":");
    return {
      file: path.relative(WEB_ROOT, l.slice(0, idx)),
      count: Number(l.slice(idx + 1)),
    };
  })
  .sort((a, b) => b.count - a.count);

// R4: dark: 前缀类必须为 0（v1 light-only，决策①b；辖域＝webmail-next，
// 与原 R4 一致——legacy/platform 面 dark: 不在决策①b 范围，随重写线吸收）
const darkTotal = grepCount([
  "-rE",
  "dark:",
  path.join(TARGET, "features/webmail-next"),
  "--include=*.tsx",
  "--exclude=*.test.*",
]);

// R6 (BR-013): 蓝系 Tailwind 类（blue/indigo/sky）ratchet，只许下降
const blueTotal = grepCount([
  "-rEho",
  "(bg|text|border|ring|from|to|via|fill|stroke|outline|decoration)-(blue|indigo|sky)-[0-9]+",
  TARGET,
  "--include=*.tsx",
  "--exclude=*.test.*",
]);

// R7 (BR-014): brand-blue 消费白名单
//   允许形态 1：focus 族类内消费（焦点环预算：focus:ring / focus:border /
//     focus-visible:outline 等前缀 token）
//   允许形态 2：下列精确 token（未读点 / 3px 指示条 / chips 选中 outline）
const FOCUS_TOKEN =
  /[a-zA-Z0-9_-]*focus[a-zA-Z0-9_-]*:[a-zA-Z0-9_\-\[]*var\(--(brand-primary|vellum-brand[a-z-]*)/g;
const ALLOWED_TOKENS = new Set([
  "w-[3px] bg-[var(--brand-primary)]", // 正在读 3px 指示条（message-row）
  "w-1.5 h-1.5 rounded-full bg-[var(--brand-primary)]", // 未读点（message-row）
  "outline-[var(--brand-primary)]", // 搜索 chips 选中 outline（BR-011 预算内）
]);
let brandLines = "";
try {
  brandLines = sh(
    "grep",
    [
      "-rnE",
      "var\(--brand-primary|var\(--vellum-brand",
      TARGET,
      "--include=*.tsx",
      "--exclude=*.test.*",
    ],
    { stdio: ["ignore", "pipe", "ignore"] },
  );
} catch {
  brandLines = "";
}
const brandViolations = [];
for (const line of brandLines.split("\n").filter(Boolean)) {
  const idx = line.indexOf(":");
  const file = path.relative(WEB_ROOT, line.slice(0, idx));
  const stripped = line.replace(FOCUS_TOKEN, "/*focus*/");
  const badTokens = stripped
    .split(/[\s"'`]/)
    .filter(
      (tok) =>
        tok.includes("--brand-primary") || tok.includes("--vellum-brand"),
    )
    .filter((tok) => !ALLOWED_TOKENS.has(tok));
  if (badTokens.length > 0) {
    brandViolations.push(`${file} → ${badTokens.join(" , ")}`);
  }
}

const failures = [];
if (hexTotal > MAX_HEX) {
  failures.push(
    `hex 总数 ${hexTotal} 超过冻结阈值 ${MAX_HEX}（BR-013 扩面 ratchet，只许下降），Top: ` +
      perFile
        .slice(0, 8)
        .map((f) => `${f.file}=${f.count}`)
        .join(", "),
  );
}
if (darkTotal !== 0) {
  failures.push(
    `dark: 前缀类出现 ${darkTotal} 处（WEBMAIL-BATCH2-SPEC R4，应为 0）`,
  );
}
if (blueTotal > MAX_BLUE_CLASSES) {
  failures.push(
    `蓝系 Tailwind 类出现 ${blueTotal} 处，超过冻结阈值 ${MAX_BLUE_CLASSES}（BR-013 ratchet，只许下降；旧页面蓝类随重写线/批次 2 吸收）`,
  );
}
if (brandViolations.length > 0) {
  failures.push(
    `brand 蓝预算外消费 ${brandViolations.length} 处（BR-014 白名单：未读点/3px 指示条/焦点环/chips 选中 outline）:`,
  );
  for (const v of brandViolations.slice(0, 12)) failures.push(`  - ${v}`);
}

console.log(
  `[check-color-tokens] hex=${hexTotal}/${MAX_HEX} dark:${darkTotal}=0 blue-classes=${blueTotal}/${MAX_BLUE_CLASSES} brand-budget=${brandViolations.length} ✓`,
);
if (failures.length > 0) {
  console.error("[check-color-tokens] FAIL");
  for (const f of failures) console.error("  - " + f);
  process.exit(1);
}
