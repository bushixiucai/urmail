# S2 引擎切换分批计划（fix/phase1-s2-queries）

裁决（PM 2026-11-19）：方案 A——留在本分支分批完成 sqlc sqlite 引擎切换，每批独立
commit + `go build ./...` 绿；**半切换状态不合 main**。归档取证见
`/var/lib/openbot/OpenBot/Shared/design/S2-QUERY-REWRITE/`。

## 前置（B4，全批次的公共前置）

1. **sqlc.arg 命名参数统一**：sqlite 引擎会丢弃子查询内参数（探针实证：
   `IN (SELECT value FROM json_each(?1))` 未生成参数）。先用 sqlc v1.30.0 sqlite 引擎
   在临时 out 目录反复 generate，按「生成参数缺失」清单把相关占位符改为
   `sqlc.arg(<列名>)`（生成字段名不变，调用点无感），直到参数集合与 PG 版本逐方法一致。
2. **time.Time → int64(unix micro) 调用点改造**：schema 时间列为 INTEGER，生成类型
   int64；按分片把 `time.Time` 实参与扫描改 `timeutil.Micro()`/`FromMicro()`（S4 同型
   改造，先于引擎切换落地以保持构建可编译）。

## 批次

| 批次 | 内容 | 语句数（实测） | 验收 |
|---|---|---|---|
| B0（已提交） | 行锁语义登记 63 条 + 取证归档 + 本计划 | 0 | build 绿；登记可复核 |
| B1 | 数据修改 CTE（`WITH … AS (INSERT/UPDATE/DELETE)`）重写：拆为顺序语句/`INSERT…RETURNING` 链或合并为单条 DML | 28 | build 绿；sqlc sqlite 对 B1 文件 generate 无错 |
| B2 | `UPDATE 表 别名` 全量去别名（48 处） | 48 | 同上 |
| B3 | 时间/文本函数族：EXTRACT 18、make_interval 11、to_char 3、jsonb_agg 5、`~` 正则 2（正则改应用层断言或 LIKE 近似 + 登记） | 39 | 同上 |
| B4 | sqlc.arg 参数统一 + int64 调用点改造（按包分片） | 生成驱动 | 全仓 build 绿 |
| B5 | 引擎切换落地：sqlc.yaml engine=sqlite、生成物替换、全量调用点修复、handlers 分片测试 | 690 | `go build ./...` 绿 + handlers 套件绿 |

每批各自 commit；B1–B3 期间生成物保持现状（不重生成），build 恒绿。

## 语义损失复核（S3/S4）

`scripts/sqlite/lock-semantics.{json,md}`：63 条 FOR UPDATE/SHARE 删除逐条登记
原用途（队列抢占/租约互斥 44、读-改-写 8、一致性读 11）与替代（条件 UPDATE +
changes()==1、BEGIN IMMEDIATE 串行、WAL 快照读），S3 装饰器与 S4 分片复核时
逐条勾兑，不做静默删除。

## S4 拉前时的切片表注记

若后续把 S4 分片拉前：「引擎切换批次」（B1–B5）作为 **S4-0 前置**写入 v3.2 切片表
S4 行，顺序不变（B5 完成前任何分片的 sqlite 测试都不可达）。
