# V05：Roundcube1.7.5 metadata 隔离迁移器

本工具目前只支持**本地/隔离目标演练**，没有生产apply、清库、任意SQL或任意schema入口。成功报告固定`fixture_only=true`、`activation_authority=false`，不能拿它解除v80生产激活门禁。真实RC新DSN/插件运行、DES解密、NC/ZPush和跨应用最终备份另行验收。

## 输入与固定profile

`migrate_metadata.py`仅接受已核定Roundcube1.7.5、CardDAV5.1.4 vendor schema。第三方SQL/PHP不打包进源码，使用外部私有bundle中的固定SHA256白名单；不执行vendor PHP，也不重放已经完成的CardDAV PHP数据迁移。根据pinned PostgreSQL initial/current schema建目标，核对SQLite core版本和23条已完成CardDAV历史，不直接复制SQLite system版本。

私有bundle为0700目录、0600普通文件，包含一致SQLite backup API生成的standalone snapshot、vendor SQL/迁移代码及input-manifest。拒绝任何snapshot WAL/SHM/journal旁文件、路径逃逸、symlink、manifest/hash变化、未知表/列或未核定版本。源文件字节只读，复制到短时0600私密immutable副本后才查询，避免TOCTOU重读和SQLite WAL header对内存deserialize的限制；不修改原header或忽略未提交sidecar。

保留全部非auth业务资料，包括用户ID/关系、偏好、签名、联系人、CardDAV账户/地址簿/vCard及加密password字节。逐表、逐列规范化digest验证，文本不做Unicode规范化或PHP反序列化；跨backend时间类型按RC UTC语义统一，字节值按精确字节处理。不仅比较count。

旧session、persistent_login auth_tokens和session-bound uploads清空，报告分别记录源存在性和丢弃计数。只清过期core cache；CardDAV contacts不是可丢弃缓存。14个业务序列按固定vendor映射对账：取当前MAX、存在时源sqlite_sequence高水位、目标已分配/预留counter下限三者的最高值，绝不只用MAX回退已分配序列。源无AUTOINCREMENT历史时明确available=false/value=null，不猜删除历史。每条输出before/after、resync_performed及next_value_floor；没有真正调用setval不会声称已resync。vendor的独立nextval(text)没有OWNED BY，不能依赖pg_get_serial_sequence。

## 目标门禁

- DSN只从`URMAIL_ROUNDCUBE_MIGRATION_DSN`环境读取，禁止命令行密码；本机loopback、随机`urmail_fixture_<24hex>`数据库、固定`urmail_roundcube`。
- 必须真正以该LOGIN连接，session_user=current_user且role=none，无super/createdb/createrole/replication/bypassrls；root SET ROLE不能冒充runtime。
- 根fixture控制器在数据库COMMENT绑定随机nonce、DB名/OID、源snapshot SHA；受限角色不能自己创建该证明。固定私有roundcube schema须归该角色，public不可CREATE；任何表/函数/类型等依赖对象存在均拒绝，不提供清库或盲重放。
- 专用PG集群必须抑制详细行/语句ERROR日志，否则拒绝导入；客户端错误只返回固定code、SQLSTATE、已核定table和数字rowRef，不回传源内容、DSN或异常DETAIL。
- DDL、参数化value导入、FK/关系和全列digest验证在同一事务。数据库故障或同count内容篡改导致全量回滚。已提交非空目标不得重放；只有明确验证空回滚才可重试。

提交确认丢失返回`COMMIT_OUTCOME_UNKNOWN_NO_REPLAY`，不假称已安全回滚；已提交目标非空会拒绝再次导入。

报告路径在打开目标前独占预留，已存在路径直接拒绝，不覆盖旧PASS。若数据库已提交但报告持久写入失败，返回`COMMITTED_REPORT_WRITE_FAILED_NO_REPLAY`，须人工检查目标，不自动重做；工具不伪称数据库/文件跨介质原子提交。

## 本轮复现与范围

依赖记录在`metadata-requirements.txt`，本轮Python3.11、psycopg3.3.6。使用独立venv，不改系统Python。

- `inspect --private-bundle <私有输入目录> --report <全新报告路径>`只核对源schema/manifest。
- `apply-fixture`使用相同参数与环境中的受限LOGIN DSN，且必须通过上述owned fixture证明；没有生产模式。
- `tests/metadata_migration_test.py`使用环境`URMAIL_V05_PRIVATE_BUNDLE`和`URMAIL_V05_CLUSTER_ADMIN_DSN`启动可选真实演练。测试要求预先创建的**独立**集群`cluster_name=urmail-v05-private-rehearsal`，且不存在urmail_roundcube角色，拒绝修改共享集群既有角色。创建/清理随机owned数据库，root仅做fixture证明和权限拒绝测试，客户metadata实际导入使用受限LOGIN。

本轮真实输入36users/37identities/5contacts/42collected/35CardDAVaccounts/42books/72contacts/23migration全部对账；22表、134列digest通过，26session清空，sequence max+1通过。完整证据在`docs/reviews/evidence/m8-v80-compatibility/metadata/README.md`。

目标是新的独立PostgreSQL16 native arm64容器，仅127.0.0.1临时端口，未修改共享dev角色/密码；其public.mailboxes仅合成拒绝哨兵，不冒充整个平台schema的端到端恢复或真实Ubuntu22 amd64 PHP运行。真实源快照来自父SSH backup API，未跨所有应用停写，因此也不是最终切流量备份。


本轮13测试另模拟“数据库已真实commit但确认包丢失”：工具返回COMMIT_OUTCOME_UNKNOWN_NO_REPLAY，目标保持已提交且重放被非空门禁拒绝；不能把此类通信失败说成安全回滚。提交前SQL/digest故障才由事务回滚测试直接证明。


### 最终角色边界复审（本节覆盖前轮13测试状态）

最终16测试4.650s GREEN。runtime另要求NOINHERIT且没有任何以当前角色为member的pg_auth_members记录；operator反向取得RC角色可接受。故意授INHERIT、可SET ROLE的pg_write_all_data成员、直接公共表权限、非系统`pgevil_role`权限及公共SECURITYDEFINER旁路均拒绝。真实LOGIN对public/他角色哨兵SELECT/INSERT/UPDATE/CREATE逐项42501。

目标检查在catalog-only search_path执行，确认空schema后才启用roundcube路径。真实旧私有schema恶意root-owned current_database() SECURITYDEFINER函数不能抢先执行，public哨兵不变且非空被拒绝。防止空库门禁本身先调用不可信旧函数。最终隐私扫描与集群/volume/临时凭据清理已重做，tool-sha256绑定当前冻结源。旧13测试日志/manifest为历史，不作为当前完整角色约束的单独证据。


## 序列高水位修复后的当前验收

先前16测试及max+1文字仅代表迭代历史。当前18测试4.849s GREEN，保留全部真实LOGIN/权限/隐私/回滚门禁；新增删除最高SQLite AUTOINCREMENT ID后仍保留700高水位、目标已分配900和未调用预留1200不回退的真实PG证明。下一次nextval分别701、901、1200，按既有counter语义保留，未伪造source历史。plain INTEGER PRIMARY KEY无sqlite_sequence时明确无可用高水位。

本次真实源14条业务序列均无可用sqlite_sequence历史，8条在迁移器中实际进行了setval，其余初始空序列未虚报resync；逐条manifest包含源是否可用、当前MAX、目标before/after和next floor。客户数据逐列对账仍通过。此结论不能证明无法观测的旧删除ID历史，也不能替代最终停写备份。证据见metadata/sequence-review；新的独立PG容器、volume和凭据已清理，source原SHA未变。
