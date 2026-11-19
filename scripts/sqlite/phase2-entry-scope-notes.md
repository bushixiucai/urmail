# 豁免清单非 Platform* 方法归类标注（QA B0.6 复核要求）

platform-methods.txt 中 10 个非 Platform* 前缀的方法，逐条归类（四轴：tenant/platform/system/pre-auth）：

| 方法 | 轴 | 理由 |
|---|---|---|
| GetUserByEmail | pre-auth | 登录路径按 email 查用户（无会话） |
| SaveDNSCredential | system | DNS onboarding worker 后台写（进程级授权） |
| RevokeDNSCredential | system | 同上 |
| CreateDNSPlan | system | DNS 计划创建（worker 事务内） |
| ConfirmDNSPlan | system | DNS 计划确认（worker 事务内） |
| CancelDNSPlan | system | DNS 计划取消 |
| RequestOnboardingRetry | system | onboarding 重试（worker） |
| RequestDNSRollback | system | DNS 回滚（worker） |
| ReviewDNSPlan | system | DNS 计划评审（worker） |
| AbandonDNSPlan | system | DNS 计划废弃（worker） |

另有 CheckPlatformAdmin 等：platform 轴（admin 会话校验）。
sign-off：Backend 起草，QA+PM 复核后写入 method_scopes preauth/system allowlist（S3 消费）。
