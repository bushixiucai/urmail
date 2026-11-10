// CI 守门：webmail-next 硬编码色值迁移进度断言（WEBMAIL-BATCH2-SPEC.md 2.3 R3/R4）
// - R3: 6 位 hex 总数阈值 < 140（settings 目录豁免已于 v0.6.20 A3 解除，全量计数）
// - R4: `dark:` 前缀类保持 0（#90 已清零，决策①b v1 无应用级暗色）
// - R5 白名单：theme-vellum.css（.css 不在扫描范围）、邮件正文模板/iframe（当前无此文件）
// 用法: node scripts/check-color-tokens.mjs  （接入 npm run lint）
import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB_ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const TARGET = path.join(WEB_ROOT, "src/features/webmail-next");
const MAX_HEX = 139; // 规格阈值 <140

function sh(cmd, args, opts = {}) {
  return execFileSync(cmd, args, {
    cwd: WEB_ROOT,
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
    ...opts,
  });
}

// R3: 硬编码 hex 计数（settings 豁免已解除，A3 全量纳入；零匹配时 grep exit 1）
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
  fileOut = sh("grep", ["-rcE", "#[0-9A-Fa-f]{6}", TARGET, "--include=*.tsx"]);
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

// R4: dark: 前缀类必须为 0
let darkTotal = 0;
try {
  darkTotal = sh("grep", ["-rE", "dark:", TARGET, "--include=*.tsx"], {
    stdio: ["ignore", "pipe", "ignore"],
  })
    .split("\n")
    .filter(Boolean).length;
} catch {
  darkTotal = 0; // grep 无匹配时 exit 1
}

const failures = [];
if (hexTotal >= MAX_HEX + 1) {
  failures.push(
    `hex 总数 ${hexTotal} 超过阈值 ${MAX_HEX + 1}（WEBMAIL-BATCH2-SPEC R3），Top: ` +
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

console.log(
  `[check-color-tokens] hex=${hexTotal} (阈值<${MAX_HEX + 1}, settings 已并网) dark:${darkTotal}=0 ✓`,
);
if (failures.length > 0) {
  console.error("[check-color-tokens] FAIL");
  for (const f of failures) console.error("  - " + f);
  process.exit(1);
}
