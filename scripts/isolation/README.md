# Phase 1 隔离工具链（S0 落仓件）

本目录是 Phase 1（PG → SQLite）分类与移植面的**单一事实源**。全部数字钉死在
`scripts/classify-tables.py` 的 PINNED（QA 2026-11-19 复审实测口径）：
102 业务表 / 85 直接含 tenant_id / 101 RLS / 43 FORCE / 4 表 10 条 RESTRICTIVE
策略 / shared 16 / 60 触发器 / 119 业务函数条目（distinct 118，
`urmail_record_audit` 双签名；pg_trgm 等 31 个扩展函数不计）。

## 文件

- `tables.golden.json`：分类 golden（102 表逐张类别/标签/urmail_app 授权）。
- `appendix.md`：同上的人类可读版。
- `port-inventory.json` / `port-inventory.md`：119 函数条目 + 60 触发器的
  三分类底表（sqlite-trigger 30 / app-assume 73 / deprecated 15），逐项带
  调用证据与替代物；deprecated 名单含宿主死表证据。
- `shared16-signoff.md`：shared 16 张逐张复核签字。

## 运行（干净检出）

```bash
# 1) 生成 catalog dump（需 URMAIL_TEST_DATABASE_URL 指向迁移到 HEAD 的 PG16）
CATALOG_DUMP_PATH=/tmp/catalog.tsv \
URMAIL_TEST_DATABASE_URL=postgres://... \
  go test ./internal/testdb/ -run TestDumpCatalog -count=1

# 2) 分类 + 钉子门 + golden diff（--check 只读比较，不覆写）
python3 scripts/classify-tables.py \
  --catalog /tmp/catalog.tsv --migrations migrations \
  --out-dir scripts/isolation --check

# 3) 移植面三分类 + 设计门（119 条目全覆盖、60 触发器全映射）
python3 scripts/classify-port-surface.py \
  --catalog /tmp/catalog.tsv --repo . --out-dir scripts/isolation

# 4) deprecated 函数代码零命中门（15 名，git 语义）
python3 scripts/classify-port-surface.py \
  --catalog /tmp/catalog.tsv --repo . --check-deprecated
```

无 `CATALOG_DUMP_PATH` 时 `TestDumpCatalog` 自动 skip，不影响常规测试。

## CI 接线说明（S7 落地）

CI 接线随 S7 的 Integration/隔离快集改造一并落地（v3 设计 §6）；S0 不改
`.github/workflows/ci.yaml`，原因：发布门禁 `scripts/verify-release-ci.py`
对 ci.yaml 的 REQUIRED_JOBS 做精确匹配（urmail-release-gate-ci-checks 技能
约束），S0 单独加 job 会制造门禁漂移。

## 更新纪律

- golden 漂移（新表/新函数/新策略）不是「跑一遍覆写」：先人工核对
  schema 变更是否合理，更新 PINNED 与本目录 golden 需同 PR 且带 QA 复核。
- `--check` 只读比较；播种（无 `--check` 生成）只允许发生在人工 review 流程。
