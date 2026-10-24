# urMail 受管观测

默认不启用监控服务或外部接收器。应用通过专用只读 `URMAIL_METRICS_TOKEN` 为实际 loopback 客户端开放 `/internal/metrics`；公开 Nginx 对该路径返回 404。该 token 不具备管理会话、租户或邮箱访问能力。Prometheus 使用单独的 `urmail-monitor` 用户、受限 systemd unit 和仅该用户可读的 token 文件。不得使用平台管理员会话代替 scrape token。

启用前由运维完成：安装发行版的 Prometheus、promtool 和 Alertmanager，确认其绝对可执行路径；创建没有登录权限的 `urmail-monitor` 用户；把本目录配置安装至 `/etc/urmail/observability`；私密设置应用 token 并写同值到 `metrics.token`（root:urmail-monitor 0640）；验证 `promtool check config`、`promtool check rules` 和 `amtool check-config`。含接收凭据的配置应设为 root:urmail-monitor 0640，禁止将密钥写入公开报告。配置审核通过后才创建 `monitor.enabled`。监控数据目录由 unit 的 StateDirectory 管理，服务仅监听 loopback。

外部接收器是单独门禁：替换默认本地接收器，配置获批 HTTPS webhook 或 SMTP 接收器及验证凭据；用无客户数据的故障演练确认 firing 与 resolved 均到达、重复告警按一小时间隔限流；保存接收凭据（不保存密钥）后才创建 `receiver.enabled`。本地 HTTP stand-in 或单元测试不能替代实际接收方验收。未配置接收方时，平台 `operational_alerts` 和站内通知仍持续保留状态。

默认阈值由平台 `observability_thresholds` 配置覆盖：`queue_count=1000`、`queue_age_seconds=3600`、`resource_used_fraction=0.9`、`certificate_days=7`、`collector_stale_seconds=120`。队列快照被截断必告警，展示的数量是下界。资源缺失、collector 断流、未知归属不会被计成健康或成功。

指标仅含固定的 measurement/kind/status 标签；禁止把邮箱、租户 ID、收件人、域名、Message-ID 放入指标标签。健康页的 SMTP/LMTP acceptance 仅表示下一跳接受，不代表进收件箱或已读。
