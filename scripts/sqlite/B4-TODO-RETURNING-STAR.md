# B4 待办：RETURNING 别名星号展开

QA 预审第 4 条：n.*/j.*/e.*/p.*/t.*/s.* 形态 RETURNING 依赖 UPDATE 别名；B2 已去除别名，
B4 按 sqlc sqlite 生成物核列清单展开。执行口径：RETURNING <别名>.* → RETURNING 显式列清单
（列序与 S1 models 一致），或无别名 RETURNING *（sqlc sqlite 支持通配 RETURNING，但为保
json 标签稳定优先显式列）。

## B4-1 探针三结论（json_each 参数化，可复跑）

探针目录 /tmp/p1facts/sqlc-probe（query3.sql ProbeJsonEachNamed，sqlc v1.30.0）：
- `json_each(?N)`：sqlc 不生成参数（首轮探针已证）；
- `json_each(sqlc.arg(x))`：sqlc 将 sqlc.arg 视为**列引用**，方法零参数（本轮探针证）；
- `json_each(CAST(?N AS TEXT))`：同样不生成参数（本轮探针证）。

**结论**：json_each 是表值函数，sqlc sqlite 引擎对其内部参数一律无法生成——非语句写法问题。
**B5 修法（定型）**：这两个位点（platform_alias_senders / platform_legacy_import）迁入
internal/store/sqlite_methods.go 手写层（参数化 Exec/Query，运行时完全正确），
SQL 文件保留原语句供文档；不阻塞合 main（手写方法在 B5 前落地即可）。
