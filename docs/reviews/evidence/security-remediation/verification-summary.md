# 安全修复验证索引

本目录绑定 2026-10-26 当前分支未提交源码。基线 HEAD 为 `3322bbf26805dcb17f7289e602bebe4d0771b204`；实际交付身份以 `source-manifest.json` 的逐文件摘要为准。历史日志与后续补强回归分层记录，不把不同构建混为同一二进制。

## 1. 构建、静态与应用回归

| 检查 | 实际结果 | 证据 |
|---|---|---|
| `make build` | 前端及四个 Go 二进制成功；业务冻结后再执行 `make build-go` | `checks/build-complete.log`、`checks/build-go-frozen.log`、`checks/build-go-delivery.log` |
| `make lint` | Go 0 issues；ESLint/Prettier通过 | `checks/lint-frozen.log`、`checks/lint-delivery.log` |
| `make i18n-check` | 2 locales、1398 keys一致 | `checks/i18n-frozen.log` |
| `sqlc generate`前后逐文件比较 | 75个store Go文件无漂移 | `checks/sqlc-determinism.json` |
| `URMAIL_TEST_DATABASE_URL=<独占本地测试库> go test -race -p 1 ./...` | 全仓库 exit 0；真实PG集成启用，串行包避免cluster role夹具冲突 | `checks/go-race-frozen.log`、`checks/go-race-delivery.log` |
| 最后新增的PG补强回归 | CLI坏种子/审计失败整批回滚、真实行锁迟到登录拒绝/rehash不覆盖换密、16/64/128MiB真实HTTP流式导出，全部通过 | `checks/supplemental-pg.log` |
| `npm test -- --run` | 33个文件、127项通过 | `checks/frontend-frozen.log` |
| 前端TypeScript | 通过；构建同时执行类型检查 | `checks/typecheck.log`、构建日志 |
| Playwright原生Webmail隐私fixture | Chromium 1项通过 | `checks/browser.log` |
| Postman本地OpenAPI语法检查 | violations为空；未发布云端 | `checks/openapi-frozen.json` |

补强PG命令筛选的具体测试：

```text
TestLegacyTOTPMigrationRollsBackBadSeedAndAuditFailure
TestPanelLateLoginWaitsForPasswordRotationAndRejectsOldProof
TestPanelRehashWaitsForPasswordRotationWithoutOverwritingNewPassword
TestArchiveExportLayeredSizesOverRealHTTP
```

本地 PG URL 仅指向既有测试夹具创建的可丢弃测试库；没有连接或迁移生产库。全仓库测试中显式依赖真实邮件环境的测试按其设计跳过，其替代证据在第3节单独列明，不能把Go命令的成功当作所有环境测试均运行。

## 2. 依赖与发布

- govulncheck **v1.9.0 / Go1.27.3**，Darwin arm64、Linux arm64和Linux amd64三种构建目标均正常完成：可达符号0、导入包0。module-only保留 `GO-2026-5932`；其OpenPGP包没有被本项目导入。原始JSON和汇总在 `checks/govulncheck-{frozen,linux-arm64,linux-amd64}.json`、`checks/go-vulnerability-summary.json`。这是扫描时点的结果。
- `npm audit --registry=https://registry.npmjs.org` 对完整锁文件：总计425依赖，prod81/dev345（npm分类含交叉统计），漏洞总计0，见 `checks/npm-audit-frozen.json`。不能由此推断所有系统包均无漏洞。
- Argon2id本机有界基准3次约237ms/op、33.56MB/op，见 `checks/password-cost.log`；不是生产容量测试。
- `python3 scripts/test-verify-release-ci.py` 验证精确SHA、main/push和全部必要job决策，交付日志另存。容量指标补强后的最终Go构建、lint、vet、依赖扫描和发布决策测试再次通过，见delivery日志和`checks/release-gate.log`。GitHub远端workflow未执行，本地成功不替代远端发布门禁。

## 3. 真实隔离邮件与HTTP

`mail-runtime/` 保存Ubuntu22 arm64镜像的真实Postfix/Dovecot/PG/HTTP证据。命令：

```text
python3 scripts/native-webmail-protocol-acceptance.py --image urmail-native-webmail-ci:22.04 --evidence tmp/security-remediation/runtime
```

旧冻结源码与容量指标补强后的最终源码，协议、API、归档、milter阶段均exit0；最终runtime结果归档在`mail-runtime-delivery/`，receipt SHA256为`e69b81a9cb09b8f2fb3fd4c9455650487426ee1edfaf5d5e561c0a481d21b5ed`；Docker network=none、无宿主端口、临时容器清理。6项From授权、近128MiB预算BCC完整原文hash、超限SIZE正文前拒绝、定时任务终态不重放等已执行。组件版本、测试二进制及257项源码摘要见receipt。详见 `mail-runtime/RUNTIME_VERIFICATION.md`。

128MiB指Postfix queue/SIZE预算；本次成功样本的逻辑正文133806373字节、wire133940582字节、durable raw133806756字节。不能将其写为任意行布局的128MiB逻辑正文保证。

真实From身份用例运行在587 fixture；465/25及全部profile DKIM链未做完整实际运行矩阵，以静态/golden和部署门禁区分。另有1MiB独占tmpfs真实ENOSPC测试（非skip），见`enospc-delivery/`：生产Spool拒绝虚假持久确认。

## 4. 支付契约模拟

Postman1.75.0本地simulation，只绑定loopback，调用真实Stripe HTTP退款客户端；成功200、JSON503、HTML503三种路径通过。失败测试要求实际错误含`stripe refund: status 503`，连接失败不会被当作业务拒绝通过。配置、事件及客户端日志在 `payment-simulation/`；没有真实商户请求或实付款。注入错误的member可能不产生request事件，不能仅凭事件缺失推断是否调用。

## 5. 安装包和恢复

`package-validation/README.md` 是完整索引，receipt分别描述Ubuntu22.04/PG14和Ubuntu24.04/PG16 ARM64。该包早于最后容量计数补强及部分测试；快照差异清单保留，不将它称作最终发布包。包SHA：`4172c4020e85f5ba402447ff0e1eafcf9d40365924115f31cadd5785d331f656`。解包、权限、签名下载9路径、schema83新装及83→79→83、非root实际HTTP登录、typed agent及UID拒绝均通过。

四类真实enc:v1密文完成私有pg_dump/pg_restore：原key4/4成功、错误key4/4拒绝，实际支付registry及恢复任务hold通过。密钥、dump和测试数据库随独占容器销毁，无副本进入证据目录。

**完整systemd安装BLOCKED**：容器PID1不是可工作的systemd，正常dpkg configure失败。没有伪造systemctl、忽略依赖或放宽namespace。该环境缺口未关闭；部分安装层通过不能称作完整新机部署通过。原生amd64运行、生产、ACME、实付款和全机恢复仍是独立门禁。

## 6. 证据纪律与复核

原始中间失败保留在本地`tmp/security-remediation/`，最终成功日志按独立文件归档。旧扫描器解析失败、并行生成中途编译失败、测试fixture大小/pipe错误及证据helper扩展名导致的lint失败均未伪装为成功。最终只引用相应重跑结果。

没有提交、推送、生产操作或公开安全issue。原有`.impeccable/`及`scripts/w13/__pycache__/`保持原样。审查报告、实施计划、结果和运维手册位于仓库忽略的docs目录，是本地交付文件；源码清单包括未跟踪的新修复文件。


## 7. 最后交付确认

容量指标补强后，`go test -race -p 1 ./...`（真实PG）exit0，最终构建/lint/vet/SCA均exit0。最后追加到既有测试的断流用例在全量之后单独使用真实PG、`-race -count=1 -v`通过，见`checks/archive-interruption-delivery.log`：输出4096字节后EPIPE，成功READ已提交、独立stream_failed/502、failed counter/warning及传输许可释放均有断言。

最终真实邮件运行receipt和补充源码摘要见`mail-runtime-delivery/`；旧`mail-runtime/`作为前轮证据保留。真实ENOSPC在`enospc-delivery/`，非skip。包快照与最终源码的8项修改及1个新增分层测试文件已记录：两个业务文件仅增加容量计数，其余为测试；不将旧包标为最终发布包。

二次审查按认证/密钥、支付退款、邮件与特权边界分工完成，随后由主代理逐项核对计划、实际源码和证据。九项源码根因均关闭；完整systemd/amd64与生产环境门禁保持未通过，最终就绪结论为CONDITIONAL。没有未执行的本地必需回归被计为成功。
