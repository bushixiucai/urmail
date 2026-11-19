# Upgrade fence：新鲜备份 pre-flight runbook

自 v0.6.25 起，`urmailctl upgrade fence` 在停写（stop writers）之前先做
fresh-backup pre-flight：最新一条 `backup_runs` 记录必须是 **complete**
（`local-mail-and-metadata`）且 **success**，并且备份代际时间在新鲜窗口内
（默认 24h，`--fresh-backup-window DURATION` 可调）。代际时间取 manifest 名称
中的 stamp（如 `urmail-20261119T120000Z.manifest.json`），因此重传旧 manifest
不会算作新鲜备份。

## 为什么

fence 会连备份 worker 一起停掉；而 release 侧的严格门禁
（`VerifyReleaseRuntime`）同样要求最新 run 是 complete success。若升级前恰好
有一次异地失败或只有 metadata-only 备份，出炉将只能走 break-glass。pre-flight
把这一检查前移到还来得及补救的时刻。

## 拒绝时怎么办

拒绝信息会给出原因，按序处理：

1. `no offsite backup run recorded yet` / `newest backup run ... is "failed"` ——
   先完成一次完整备份：等定时 worker，或手动执行（见下），再重试 fence。
2. `... is a "metadata-only" backup` —— `urmailctl backup` 默认
   metadata-only；完整备份需要 `--maintenance`（含邮件归档），确认后重跑
   offsite 上传。
3. `... is Nh old (window ...)` —— 最新代际已过窗；先跑一次新的完整备份。

## 手动触发一次会被记录的完整备份

```
urmailctl backup --maintenance --out-dir /var/backups/urmail   # 生成本地代际
URMAIL_BACKUP_S3_* 与 --recipient 按环境准备后：
urmailctl backup upload --manifest /var/backups/urmail/urmail-<stamp>.manifest.json \
  --uri s3://<bucket>/<prefix> --recipient <age公钥>
```

手动上传同样落 `backup_runs`，且只有远端复检通过才标 success——preflight 与
release 门禁看到与定时 worker 同一真相。

## 显式跳过（留审计）

- 交互 CLI：`urmailctl upgrade fence --skip-preflight`
- 受管安装（install.sh）：`URMAIL_SKIP_FRESH_BACKUP_PREFLIGHT=1 ./install.sh ...`

两种方式都会把跳过记录写进 fence reason（`[fresh-backup pre-flight skipped
via --skip-preflight]`），供事后审计。

## 平台库不可达时

config 加载失败或平台库连不上时，preflight **不会**拒绝 fence：此时降级为警
告并把 `[preflight degraded: platform DB unreachable: <原因>]` 写入 fence
reason 留痕（修库往往正是升级的目的）。降级不代表备份新鲜，release 阶段的
严格门禁仍然生效。
