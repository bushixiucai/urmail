# M8 V06：Nextcloud 34.0.5 PostgreSQL 隔离转换准备

早期 `nextcloud-prepare.py` 仍只作 **PREPARED_NOT_EXECUTABLE / NOT_RUN** 本地准备。新增可执行的 `nextcloud-rehearse.py` 仅在严格隔离容器中提供inspect/preflight/convert三个阶段；实际运行结果必须绑定其独立证据，不能由本地测试替代。真实副本由父代理私有保存并执行，我方不访问SSH。跨应用快照不一致时只可演练，不能切换生产。

## 目标与权限

同一 urMail PostgreSQL database 内使用 `nextcloud` schema，不建立第二个数据库。`urmail_nextcloud` 只拥有该 schema，NOINHERIT、无membership、无超级/建库/建角色/复制/BYPASSRLS权限，默认 `search_path=nextcloud`。不授平台/归档/支付或Roundcube表与sequence权限，不可执行privileged SECURITY DEFINER函数。schema USAGE本身不等于数据访问权，不为此全局撤销现有public USAGE。

本目录Go测试使用 `testdb.New` 自建一次性库与schema0068，验证角色flags、schema默认、public/Roundcube读写拒绝、privileged函数拒绝、私有schema可DDL，及受控UID→principal→calendar和fileid/storage关联。**SET ROLE只证明权限；不能证明真实LOGIN无法RESET ROLE升级。** 后续真实运行须专用隔离PG cluster中随机LOGIN口令，不在共享devcluster全局改固定角色LOGIN状态。

## 私有准备

`python3 scripts/m8/nextcloud-prepare.py --code <private-code-copy> --data <private-owncloud-copy> --output <new-private-bundle> --image nextcloud@sha256:<approved-digest> --database <same-owned-platform-db> --socket-directory <private-local-PG-socket-dir>`

这条命令只执行本仓库Python，不执行复制的PHP/occ或Docker。输入根目录必须当前操作者拥有且0700，输出必须为独立的新目录；已有目录绝不删除。源码精确34.0.5，SQLite必须为一致备份且没有WAL/journal。拒绝特殊文件、可被他人写的文件、硬链接与越界相对symlink。

合法绝对symlink只接受精确映射：代码 `config/config.php` → `/home/user-data/owncloud/config.php`、代码 `data` → `/home/user-data/owncloud`。**只检查link文本，绝不在宿主读取其绝对目标**；未来容器里该绝对路径由私有mirror挂载。同目录其它绝对link默认拒绝，真实清单若有额外合法link须先核定精确映射，不能任意follow到宿主。

输出0700 bundle、0600 plan.json与配置overlay。公开stdout仅状态与plan摘要hash，不打印config、凭据、UID或表内容。计划内的固定Docker argv仅供审核，本脚本无执行分支。命令规定无网络、只读容器根、drop全部capabilities/no-new-privileges、非root UID、临时目录及资源上限，只挂私有code/data与目标PG Unix socket，不能挂docker.sock、真实根目录或生产目录。

隔离overlay把trusted host/CLI URL改localhost，mail指向本地不可用端口，关闭外部redis/replica/sharding/objectstore/backend入口、appstore/updatechecker与互联网声明。它不重写源的instanceid/passwordsalt/secret，也不改UID。应用数据库中残留的外部connector仍必须受无网络命名空间阻断；不能把配置overlay单独当作无出口证明。

## 官方命令与不可省略的检查

未来经过门禁后，固定命令为：

```
php occ db:convert-type --no-interaction --all-apps --port=<local-port> pgsql urmail_nextcloud /run/urmail-pg <same-platform-db>
```

密码经stdin传入，绝不使用`--password`或环境/argv。34.0.5读取stdin后保留原样，因此口令文件不得带CR/LF/NUL，不能用会追加换行的echo。converter会保存新DB密码到私有复制config，报告和公开日志不能读出它。

**禁止 `--clear-schema`。** schema执行前必须真正为空，current_database/current_user/current_schema必须匹配；role flags/memberships/权限必须再次核对。不得由平台超级凭据代替NC运行身份。命令`--all-apps`仅包括仍存在于apps目录的应用，删除应用的残留表仍可能被遗漏；`-n`会默认继续，绝不是允许丢表的授权。必须绑定真实code+SQLite摘要核对source全部表与迁移schema覆盖，任何缺表阻断。对已复核的残留表（obsolete core `file_metadata`、已删除 groupfolders、过时 app_api provider 表），harness 在官方 createSchema 之后按 SQLite 列定义创建空 PG 表，再让官方 copyTable 路径复制，禁止 clear-schema 或静默丢行。未复核缺表仍阻断。隔离 overlay 必须覆盖 `config_is_read_only=>false`，否则 saveDBInfo 无法写入私有副本的 db* 键。官方 `PgSqlTools::resynchronizeDatabaseSequences` 在私有 `nextcloud` schema 下会生成非法 `MAX() FROM` SQL，演练 convert **跳过**该步骤，改由 harness 对 owned sequence 做 setval/nextval/真实 INSERT+rollback 证明。createSchema 可能向空 PG 的 `oc_appconfig` 注入源中不存在的键（已复核：`core/default_encryption_module`、`user_ldap/configuration_prefixes`）；copy 后必须删除这些注入再做逐表摘要比对，未知多余键仍阻断。准备器即使收到声明覆盖证明也只标记“待runtime核验”，永远不会自动执行。

34.0.5 Doctrine将current_schema的table名去前缀，私有schema的表过滤原则上可行；但sequence仅对public去schema前缀，Nextcloud序列过滤`^oc_`可能跳过`nextcloud.oc_*`。真实vendor副本必须再次核定，并对所有owned sequence与列关联进行校验：最大已有ID后的第一次真实INSERT成功、不复用旧ID，nextval大于已有最大值。若需受控resync，只能用NC普通role对本schema的绑定sequence处理，不修改生产vendor或清public表。

## 数据连续性验收不能只数表

- NC UID原值、user_external映射、principals/users关系、组成员与share接收方不重建。
- addressbooks/cards、calendars/calendarobjects、DAV URI/etag/sync token与对象正文逐行摘要一致，抽样实际CardDAV/CalDAV读取及新增/修改后sequence正确。
- storages.numeric_id、filecache.fileid/storage/path、shares/fileid、versions/trash元数据、物理文件相对路径/大小/摘要对应；加密场景保留密钥与instance secret并实际读取。
- 所有source表有明确去向；只能对事先核定的会话/临时锁等表作有理由的例外，不能静默漏掉removed-app表。
- 复制的session/auth token及remembered客户端授权需明确撤销，保留账号/业务数据。用户可能须重登；不伪称未知第三方token已删除。精确撤销方法待真实安装app/schema核定，不在宿主执行PHP解析config。
- 转换阶段无网络/PGsocket；后续独立消费者验收环境仅可访问其本地同PG邮件userdb/IMAP，不接生产IMAP。准备overlay禁用user_backend不代表IMAP认证兼容已验收。

## 回滚与切换门禁

converter按chunk提交，失败后目标可能半成品，复制config也可能已切DB；不能直接重跑或清空共享schema。须保留不可变源备份、目标schema/平台/Roundcube同一恢复点、私有配置与凭据、文件数据快照、源代码和应用清单。失败时隔离目标、恢复本次专用目标与私有config后重新演练；不可破坏别的schema。任何NC schema已含对象的迁移down明确拒绝，不能以down删除用户数据。

只有逐对象/文件/身份/DAV/真实LOGIN与IMAP消费者验收通过，且三应用停写后跨应用一致最终备份通过，才可形成另行审核的cutover步骤。当前演练副本跨应用不一致，始终BLOCKED于实际切换。

参考：[官方34.0.5 ConvertType](https://raw.githubusercontent.com/nextcloud/server/v34.0.5/core/Command/Db/ConvertType.php)、[ConnectionFactory](https://raw.githubusercontent.com/nextcloud/server/v34.0.5/lib/private/DB/ConnectionFactory.php)、[PgSqlTools](https://raw.githubusercontent.com/nextcloud/server/v34.0.5/lib/private/DB/PgSqlTools.php)、[固定DBAL SchemaManager](https://raw.githubusercontent.com/nextcloud/3rdparty/v34.0.5/doctrine/dbal/src/Schema/PostgreSQLSchemaManager.php)。没有复制上游实现。


## 可执行服务器演练入口

父代理负责上传本仓库的四个固定文件（不执行宿主PHP）：`container_entry.py`、`schema_preflight.php`、`config_identity.php` 与 `scripts/m8/nextcloud-rehearse.py`，保持仓库相对布局。运行镜像来源必须显式绑定：最初原生镜像由original-v80 PHP8.4.27 build checkpoint源码编译；若因共享扩展ABI改用已校验的v80 release DebCLI/common/modules及同版源码PDOpgsql，则必须声明 `verified-v80-release-plus-same-source-pdo-pgsql`，不能沿用checkpoint编译的叙述。PHP入口固定 `/opt/urmail-rehearsal/php/bin/php`，允许镜像内指向精确8.4.27 DebCLI的symlink。结果绑定image ID、binary SHA及来源。必需镜像包为PG14、python3-psycopg2、util-linux/runuser与NC所需PHP扩展。postgres/www-data UID/GID在镜像内动态发现。

```
python3 <uploaded-root>/scripts/m8/nextcloud-rehearse.py \
 --image sha256:<immutable-runtime-image-id> \
 --php-provenance verified-v80-release-plus-same-source-pdo-pgsql \
 --mirror /root/urmail-m8-staging/20261024-rehearsal-01/mirror \
 --manifest /root/urmail-m8-staging/20261024-rehearsal-01/manifest.private.json \
 --manifest-sha256 9c98b38e77b94c1965834bfdbc3672f1d0931a9dfe1cb50d1f8e85b585b85a0b \
 --work /root/urmail-m8-staging/v06-inspect-01 --mode inspect
```

每次必须全新work。先inspect（copy/版本/扩展/160表/配置identity摘要），再新work preflight（独立PG +真实NC LOGIN+官方createSchema全表覆盖），最后新work convert（原生官方converter+连续性与序列/认证状态验证）。**禁止复用半成品目标，禁止clear-schema。**

Launcher只创建自己的nonce-label容器，并在start前复核Docker实际NetworkMode=none、readonly root、无ports/privileged/host PID/IPC、CPU1/3GiB/pids256、实际bind清单。cap-drop ALL后仅允许CHOWN/SETUID/SETGID初始化，绝不加SYS_ADMIN/DAC_OVERRIDE。容器内校验宿主与容器mnt/net/pid namespace不同、仅lo、ro source/harness/manifest/marker、无docker.sock；再把NCcode/data复制进独立可写bind而非运行源目录。宿主/source原路径不可变，唯一绝对symlink精确映射到容器私有owncloud clone。

私有PG cluster仅Unixsocket（listen_addresses为空）：postgres管理身份peer认证，NC身份scram密码真实LOGIN，拒其它身份；错误密码必须失败，RESET ROLE仍NC，SET ROLE postgres必须拒绝。`urmail`同库有平台/Roundcube受保护sentinel与nextcloud私有schema，不能用NC专属第二数据库回避隔离。本演练的其它schema为明确夹具，不冒充完整平台恢复测试。

预检调用固定34.0.5官方converter的schema阶段，在写用户数据前确认全部160张源表都有迁移schema；正式convert仍运行原生occ、stdin密码，不改vendor。全表逐列/逐行typed摘要比较，migrations表按官方重建核源历史包含关系；所有owned序列由普通NC角色resync并核nextval>已有MAX，另对oc_mimetypes作真实INSERT/ROLLBACK及再次全表比较。原instanceid/passwordsalt/secret保持，原物理文件除config/SQLite元数据内容全部校验。完整数据比较通过后，才在私有目标显式清空复制的authtoken与存在的login_flow_v2，记录理由/数量，第三轮比较确认其它表不变；真实浏览器授权与DAV/IMAP消费者仍单独NOT_RUN。

stdout只输出脱敏摘要/固定错误code；privatework保留0600 phase、转换日志、覆盖/连续性/序列/认证撤销证据与PG数据。失败停止并删除本次专属容器，保留本次work，不改源、不重跑或清空目标。后续消费者验收与真正跨应用一致最终备份仍是切换门禁。
