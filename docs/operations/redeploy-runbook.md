# 重部署 runbook：自检容器生命周期与基线核对

适用范围：Phase4 及以后的版本化重部署（`urmail-local:<sha>-ver` 洁净构建 →
隔离自检 → 单次切换），以及一切会在生产宿主上创建一次性/自检容器的操作。
三条硬性口径：**自检容器用后即清**、**无过滤基线核对**、**例程文本更新核验落盘**。

## 1. 自检容器用后即清

- 隔离自检容器在 run 收尾必须清除：`docker rm -f <自检容器>`。
- 收尾核对：无过滤 `docker ps -a` 全量列表中自检容器＝0；有残留即视为 run
  未收尾，不得宣告完成。
- 创建时登记：容器名、镜像、NetworkMode、创建时点写入 run 记录，收尾时对照。
- 同型案例（2026-11-27/28，归并留档）：
  - `sc0`：前期测试容器非预期清除。宿主无第三方清除器，destroy 均系自身
    rm，判安定。
  - `sd2ver`：`d2fbdcf4-ver` 隔离自检遗留（Created 2026-11-27T22:55:24Z，
    NetworkMode=none、零端口、RestartCount=0），23:45Z 巡检发现为非基线
    容器，经授权即时清除（2026-11-27T23:51Z）。教训：自检容器不清理会进
    入巡检视野并触发告警流程，即使它无网络面、零服务影响。

## 2. 基线核对：无过滤 ps -a ＋ 白名单逐 ID

- 必须用无过滤全量扫描：

  ```
  docker ps -a --format '{{.ID}}  {{.Names}}  {{.Image}}  {{.Status}}'
  ```

- 禁止单独依赖 `--filter name=urmail-`：它有盲区，不带 `urmail-` 前缀的
  容器（如自检容器 sd2ver）会被漏看（2026-11-27 实证）。
- 白名单逐 ID 核对零漂移：容器 ID、镜像、运行态与登记基线逐一比对；白名
  单外任何容器（含 exited）＝异常。
- 当前生产白名单（8 容器，2026-11-27T23:51Z 实测；栈演进后由 DevOps 更新
  并注明时点）：

  | 容器 | 镜像 |
  |---|---|
  | urmail-urmaild-1 | urmail-local:d2fbdcf4-ver |
  | urmail-urmail-signer-1 | urmail-local:d2fbdcf4-ver |
  | urmail-postfix-1 | urmail-local:3e9313e3-ver |
  | urmail-dovecot-1 | urmail-local:3e9313e3-ver |
  | urmail-caddy-1 | caddy:2 |
  | urmail-alertmanager-1 | prom/alertmanager:v0.27.0 |
  | urmail-prometheus-1 | prom/prometheus:v2.53.0 |
  | urmail-m8-cutover-pg | postgres:16-alpine |

  混合栈期 postfix/dovecot 维持 3e9313e3-ver 属预期；PG cutover 容器保持
  Up 禁动。

## 3. 例程文本更新必须核验落盘

- 更新巡检/自动化例程后，必须回读核验新文本已生效（重读列表看
  instruction 与 updatedAt），再宣告「已更新」；声明须附 updatedAt 证据。
- 先例：2026-11-27T22:57Z 重部署后，例程文本更新未实际落盘，旧版基线
  （旧 version＋旧锚点）一直运行到 23:52:39Z 才修正——若不及时发现，下一
  轮巡检会对新 version 误报异常。
- 防回归口径：巡检指令中的 version/锚点与生产实际不一致时，先核验例程文
  本是否落盘，再判断是否真异常。
