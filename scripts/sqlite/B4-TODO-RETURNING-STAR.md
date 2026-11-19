# B4 待办：RETURNING 别名星号展开

QA 预审第 4 条：n.*/j.*/e.*/p.*/t.*/s.* 形态 RETURNING 依赖 UPDATE 别名；B2 已去除别名，
B4 按 sqlc sqlite 生成物核列清单展开。执行口径：RETURNING <别名>.* → RETURNING 显式列清单
（列序与 S1 models 一致），或无别名 RETURNING *（sqlc sqlite 支持通配 RETURNING，但为保
json 标签稳定优先显式列）。
