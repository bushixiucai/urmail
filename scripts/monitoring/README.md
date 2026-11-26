# Monitoring probes (GRAFANA-DESIGN §3 补位)

仓内只交付探针脚本与 provisioning 资产；systemd timer/cron 注册、compose 服务、
`.env` 变量与生产接线由 DevOps 按设计 §5 接线步骤执行。全部脚本只读或只推
pushgateway，不产生业务写入，不打印凭据。

| 脚本 | 指标 | 形态 | 建议节奏 |
|---|---|---|---|
| `postfix_queue_depth.sh` | `urmail_postfix_queue_depth` | node-exporter textfile | 1/min（仪表盘 4）——须在能看到 postfix spool 的位置运行（宿主或共享 spool 卷的容器；QA PR150 P2-2） |
| `backup_chain_metrics.sh` | `urmail_backup_last_id` / `urmail_backup_id_gap` / `urmail_backup_*_last_success_timestamp_seconds` | textfile（只读 SQLite） | 每次备份后 + 5/min（仪表盘 7） |
| `dual_write_probe.sh` | `urmail_dual_write_probe_rc` / `_healthy` | pushgateway（期望恒 75） | 12s（仪表盘 6，E8 停写判据） |
| `smtp_imaps_probe.sh` | `urmail_probe_smtp_*` / `urmail_probe_imaps_*` | pushgateway | 5/min（仪表盘 2/3） |
| `mailbox_bytes.sh` | `urmail_mailbox_bytes{domain}` | textfile（仅域级聚合） | 15/min（仪表盘 2/5 关联行） |

拓扑注记（QA PR150 P1-3.3）：node-exporter 以 compose 服务名被 Prometheus
抓取（bridge 内 `node-exporter:9100`），不再走宿主回环；compose 服务定义由
DevOps 按此对齐。双写探针的锁文件只由在跑的 urmaild writer 创建与独占持有；
探针绝不创建它，缺文件按不健康上报（rc=1）。

环境变量（进 `.env`/Vault，不入参数与日志）：`URMAIL_TEXTFILE_DIR`、
`URMAIL_SQLITE_PATH`、`URMAIL_WRITER_LEASE`、`URMAIL_PUSHGATEWAY`、
`URMAIL_PROBE_SMTP_HOST/PORT/USER/PASSWORD`、`URMAIL_PROBE_IMAPS_HOST/PORT`、
`URMAIL_PROBE_TLS_VERIFY`（1=校验生产证书；缺省为了首装自签证书友好而关闭，
生产签准后应置 1）、`URMAIL_STORAGE_ROOT`。

systemd timer 模板（DevOps 落地时按上表节奏实例化）：

```ini
# /etc/systemd/system/urmail-monitor@.service（ExecStart 传脚本名）
[Unit]
Description=urMail monitoring probe (%i)
[Service]
Type=oneshot
EnvironmentFile=/etc/urmail/monitor.env
ExecStart=/usr/local/lib/urmail/monitoring/%i
```
