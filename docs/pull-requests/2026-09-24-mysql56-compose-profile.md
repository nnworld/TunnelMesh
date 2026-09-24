# MySQL 5.6 本地复现 profile

## Title

`chore(deploy): add an opt-in MySQL 5.6 contract profile to the cluster compose`

## Target branch

`main`

## Summary

给 `docker-compose.cluster.yml` 加一份 **opt-in** 的 `mysql56` 服务，让贡献者能在本地起出与 CI
`mysql56` 检查相同的项目下限版本并复跑同一批契约测试；顺带补上 CI 唯一证明不了的那一半——生产
charset（`utf8mb4_general_ci`）下 `migrations/ddl.sql` 是否满足 MySQL 5.6 InnoDB 的 767-byte 索引
前缀限制。是 #27（MySQL 5.6 契约门禁）的后续。

实施计划：`docs/superpowers/plans/2026-09-24-mysql56-compose-profile.md`

## User impact

- 只影响贡献者/运维的本地复现路径：多一条 `docker compose ... --profile mysql56 up -d mysql56` +
  一条带 `TUNNELMESH_TEST_MYSQL_DSN` 的 `go test`。
- 集群部署不变：不带 `--profile mysql56` 时该服务不出现，`docs/deployment/docker.md` 的三步引导
  不需要设置任何 `MYSQL56_*` 变量（这一点由 CI 的 `config` 校验钉住）。
- 无 API、CLI、协议行为变化。

## API、Schema 与配置影响

- 无 Schema 变更，`SchemaVersion` 仍为 15，因此 `docs/operations/schema-upgrades.md` 无新章节。
- 配置：`docker-compose.cluster.yml` 新增 `mysql56` 服务与顶层卷 `tunnelmesh-mysql56-test-data`；
  新增环境变量 `MYSQL56_TEST_PASSWORD`、`MYSQL56_ROOT_PASSWORD`（默认占位值，只服务本地测试库）。
- CI：`packaging` job 增加集群文件的 `docker compose config` 校验（默认渲染 + `--profile mysql56`
  渲染，并断言默认结果里没有 `mysql56`）。必需检查名不变（`build-test`、`packaging`、`mysql56`）。
- 新增仓库文件 `deploy/mysql56/utf8mb4.cnf`、`deploy/mysql56/mysql56_service_test.go`；
  `scripts/build-release.sh` 按目录白名单打包，二者不进入发布归档。

## 安全与授权影响

- 5.6 无 TLS，因此端口只绑 `127.0.0.1:3307`；不发布到 `0.0.0.0`，与集群 `mysql` 服务和本机
  MySQL 8 都不冲突。
- 默认口令是仓库内可见的占位值，文档明确「不得用于任何真实数据」；未新增任何真实 DSN、密钥或
  Token。`deploy/certs/` 之类的 gitignore 机密不涉及。
- 独立卷 + 显式 `down -v` 清理，测试数据不会与集群数据集互相污染。

## 测试证据

一致性测试是在 compose 服务与 cnf 落地之后才写的，因此**没有**「先失败」的原始红灯可引用（补记计划
见计划第 8 节）。替代证据是三条断言的反向验证——人为破坏被测对象后必须失败，每条都执行并还原：

| 破坏 | 实际失败输出 |
| --- | --- |
| 移走 `deploy/mysql56/utf8mb4.cnf` | `mysql56_service_test.go:131: open utf8mb4.cnf: no such file or directory` |
| profile 镜像改成 `mysql:8.0` | `CI image "mysql:5.6" != ../../docker-compose.cluster.yml image "mysql:8.0": the MySQL floor version must have one source` |
| 删除 `profiles: ["mysql56"]` | `profiles = [], want [mysql56] so up for the cluster never starts it` |

断言本身也修正过一次真实缺陷：最初写成整文件 `strings.Contains(config, "innodb_large_prefix")`，
因 cnf 的**注释**里解释性提到该词而失败，说明它检查的不是选项行；改为逐行扫描、跳过 `#` 注释。

- 绿灯：`go test ./deploy/... -count=1` → `deploy/grafana`、`deploy/install`、
  `deploy/install/oneclick`、`deploy/mysql56`、`deploy/openresty` 全部 ok。
- `ruby -ryaml` 解析 `docker-compose.cluster.yml` 与 `.github/workflows/ci.yml`：结构符合预期
  （`image: mysql:5.6`、`profiles: ["mysql56"]`、`ports: ["127.0.0.1:3307:3306"]`、conf.d 只读挂载、
  healthcheck 存在、顶层卷已声明）。
- **未在本机执行**（本机无容器运行时：`/usr/local/bin/docker` 是悬空符号链接，`Docker.app` 不存在），
  由本次新增的 `packaging` job 步骤作为替代证据：`docker compose -f docker-compose.cluster.yml
  config`（默认不含 `mysql56`）与 `... --profile mysql56 config`（含 `mysql56`）。真正的
  `up -d mysql56` + 契约复跑需在装有 Docker 的机器上按 `docs/development/testing.md` 的命令执行。
- 其余门禁：`go vet ./...`、`go test ./... -count=1`、`go test -race ./... -count=1 -timeout 30m`、
  `git diff --check`。前端未变更。

## 发布步骤

1. 合并后确认 `packaging`、`mysql56`、`build-test` 在 main 上为绿。
2. 无需重启、无需迁移；贡献者按需使用 profile。

## 回滚步骤

- `git revert` 本 PR 合并提交：无生产代码路径、无 Schema，回滚不影响任何已部署实例。
- 若某些 Docker 版本对单文件 bind mount 报错，可先删该 `volumes` 行退回 latin1，但必须同时把
  `docs/operations/completeness-checklist.md` 的 charset 项改回未完成——否则会把未验证项写成已验证。
- 残留卷清理：`docker compose -f docker-compose.cluster.yml --profile mysql56 down -v mysql56`。

## Reviewer 关注点

- `P-D7`：为什么 `mysql56` 用 `${VAR:-默认}` 而集群 `mysql` 保持 `${VAR:?}`。`:?` 的插值与 profile
  无关，任何一次 `docker compose -f docker-compose.cluster.yml up` 都会被它拦住，会直接破坏既有的三步
  集群引导流程。
- 为什么 profile 名与服务名同名、为什么不加 `restart`：一次性测试库应在宿主机重启后保持停止。
- 为什么不设 `innodb_large_prefix`：那条限制正是被测命题，放宽它等于永久测不出「键过宽」。
- CI 的 `mysql56` job 与 compose profile 的镜像版本由
  `deploy/mysql56/mysql56_service_test.go` 强制一致：版本各写一处会让「已验证的下限版本」悄悄漂移。

## 集成状态

- 分支：`codex/mysql56-compose-profile`，基线 `main`（含 #27）。
- 必需检查：`build-test`、`packaging`、`mysql56`。
