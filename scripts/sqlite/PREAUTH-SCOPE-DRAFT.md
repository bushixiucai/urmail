# Scope 四轴建模草案：platform / system / pre-auth（S3 前确认）

背景（QA 预审 ④）：`method_scopes.json` 当前只有 tenant|platform 两轴，而 469 条
platform 语句中大量是 **pre-auth / 系统入口**（登录路径、入站 SMTP、证书/调度器等），
被 handlers/webmail 直接调用。若 decorator 只认「platform=管理员」，这些入口会
要么被错误放行成为新绕过面，要么被错误拦截导致登录/收信断链。

## 提议的四轴

| 轴 | 授予对象 | 典型方法 |
|---|---|---|
| tenant | 带租户 ctx 的租户请求 | 绝大多数业务查询 |
| platform | 平台管理员操作（需 admin 会话） | PlatformCreateManagedTenant、PlatformSetTenantOwner… |
| system | 后台 worker/定时器/独立进程（无用户会话，进程级授权） | PlatformClaim*、PlatformRecover*、PlatformApply* |
| pre-auth | 认证前入口（登录、入站 SMTP、回信/退订 token 校验）——**按方法白名单逐入口授予，禁止泛化** | PlatformCreateVerifiedMailboxSession、PlatformInboundAllowed … |

## 落入 method_scopes.json 的方式

```json
{"scopes": {"<Method>": "tenant|platform|system|pre-auth"},
 "preauth_allowlist": ["<Method>", ...],   // pre-auth 轴唯一来源，逐项代码评审
 "counts": {"tenant":221,"platform":?,"system":?,"pre-auth":?}}
```

## 分类步骤（S3 开工前完成）

1. 从当前 469 条 platform 语句出发，按调用方归并：仅被 `internal/agentd`、
   `internal/backupauto`、`cmd/urmaild` 等无会话进程调用的 → `system`；
2. 被 handlers/webmail 在**认证中间件之前**或认证路径上调用的 → 候选 `pre-auth`，
   逐条人工签核（当前实测 app 侧引用到 483 个唯一 `Platform*` 方法名，远大于
   QA 报的 186——口径差在统计范围，签核表以方法为单位逐条列）；其余管理员操作
   保持 `platform`（decorator 校验 admin 会话）；
3. scope_lint 对 pre-auth 轴增加门：任何方法进入 preauth_allowlist 必须同时
   出现在 `scripts/sqlite/preauth-allowlist.md` 签核表（人审痕迹）。
4. decorator：`system` 由进程级启动谓词授予（argv/环境信号），`pre-auth` 仅按
   白名单入口在会话建立前授予，二者都不进普通请求 ctx。

## 待办

- S3 开工前：完成三轴归并表 + preauth 签核表（逐条，不抽样）。
- 该草案随 S2 分支入仓，QA 确认后作为 S3 装饰器的输入契约。
