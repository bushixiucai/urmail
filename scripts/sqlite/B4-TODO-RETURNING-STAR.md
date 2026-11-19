# B4 待办：RETURNING 别名星号展开（10 处）

QA B0.6 第 4 条：n.*/j.*/e.*/p.*/t.*/s.* 形态的 RETURNING 依赖别名，B4 一并展开为显式列清单。
来源文件与方法（sqlite 引擎切后生成物需列清单以保 json 标签稳定）：

- platform_collaboration.sql PlatformUpdateContact（已展开，参照样例）
- 其余 9 处在 B4 批次内以 grep "RETURNING \w+\.\*" 逐条枚举执行。

执行口径：RETURNING <别名>.* → RETURNING <表>.<列1>, <表>.<列2>, ...（列序与 S1 models 一致），
或直接 RETURNING <列清单>（无别名）。sqlc sqlite 对 `表.*` 支持未证实——保守展开。
