# urMail 受管观测

默认不启用监控服务或外部接收器。应用通过专用只读 `URMAIL_METRICS_TOKEN` 为实际 loopback 客户端开放 `/internal/metrics`；公开 Nginx 对该路径返回 404。该 token 不具备管理会话、租户或邮箱访问能力。Prometheus 使用单独的 `urmail-monitor` 用户、受限 systemd unit 和仅该用户可读的 token 文件。不得使用平台管理员会话代替 scrape token。

deb 打包与配置分发机制：
- deb 包构建仅将可观测模板安装至 `/usr/share/urmail/observability/*.yml`，绝不直接将数据文件拷入 `/etc`。
- 首次安装时由 `postinst` 在目标配置从未存在时从 `/usr/share/urmail/observability/` 初始化 seed 至 `/etc/urmail/observability/`。
- deb 包升级时，`preinst` 在解包前检测现存的 `/etc/urmail/observability/` 配置与 enabled 标记，安全快照至 `/var/lib/urmail-config-migration/observability/`（严格权限 0700）；解包后由 `postinst` 优先还原快照中的现有配置，只有在目标配置从未存在时才从 `/usr/share` seed 模板，彻底杜绝 deb 包升级覆写外部 Alertmanager 接收器配置与凭据的问题。

启用前由运维完成：安装发行版的 Prometheus、promtool 和 Alertmanager，确认其绝对可执行路径；创建没有登录权限的 `urmail-monitor` 用户；确认 `/etc/urmail/observability` 配置就绪；私密设置应用 token 并写同值到 `metrics.token`（root:urmail-monitor 0640）；验证 `promtool check config`、`promtool check rules` 和 `amtool check-config`。含接收凭据的配置应设为 root:urmail-monitor 0640，禁止将密钥写入公开报告。配置审核通过后才创建 `monitor.enabled`。监控数据目录由 unit 的 StateDirectory 管理，服务仅监听 loopback。

外部接收器是单独门禁：替换默认本地接收器，配置获批 HTTPS webhook 或 SMTP 接收器及验证凭据；用无客户数据的故障演练确认 firing 与 resolved 均到达、重复告警按一小时间隔限流；保存接收凭据（不保存密钥）后才创建 `receiver.enabled`。本地 HTTP stand-in 或单元测试不能替代实际接收方验收。未配置接收方时，平台 `operational_alerts` 和站内通知仍持续保留状态。
`urmail-alertmanager.service` 包含 `ExecStartPre` 前置校验：若 `receiver.enabled` 存在但 `alertmanager.yml` 仍指向 `127.0.0.1` / `localhost` 占位接收器或默认未配置状态，服务启动将立刻拒绝并退出失败，绝不假报 Healthy 或 active。

默认阈值由平台 `observability_thresholds` 配置覆盖：`queue_count=1000`、`queue_age_seconds=3600`、`resource_used_fraction=0.9`、`certificate_days=7`、`collector_stale_seconds=120`。队列快照被截断必告警，展示的数量是下界。资源缺失、collector 断流、未知归属不会被计成健康或成功。

指标仅含固定的 measurement/kind/status 标签；禁止把邮箱、租户 ID、收件人、域名、Message-ID 放入指标标签。健康页的 SMTP/LMTP acceptance 仅表示下一跳接受，不代表进收件箱或已读。
