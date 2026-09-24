# MySQL 5.6 本地复现 profile 实施计划

状态：已确认并完成实施（用户在 `mysql56` CI 门禁合并后回复「加上」），实现记录见
`docs/pull-requests/2026-09-24-mysql56-contract-ci-gate.md` 之后的
`docs/pull-requests/2026-09-24-mysql56-compose-profile.md`。

日期：2026-09-24

## 1. 目标

让贡献者能在本地一键起出与 CI `mysql56` 相同的 MySQL 下限环境并复跑同一批契约测试，同时把 CI
唯一没覆盖的那一半——生产 charset（`utf8mb4_general_ci`）下的 767-byte 索引前缀限制——放进同一条
路径。

## 2. 非目标

- 不改集群部署形态：`mysql`（8.4）服务、两个 Server、relay、证书流程一概不动。
- 不做 Schema、API、协议、生产代码变更。
- 不把 profile 变成默认启动的服务，也不给它加 `restart` 策略（一次性测试库应在重启后保持停止）。
- 不引入 etcd 或 8.4 contract 矩阵（仍属 deferred，见 `docs/operations/completeness-checklist.md`）。

## 3. 架构决策

- **P-D1 放在 `docker-compose.cluster.yml` 而不是新文件**：下限环境与被验证对象（集群模式的
  MySQL 管理数据）同源，新增一份 compose 文件只会多一个漂移点。代价是必须做到「默认不启动」，
  由 `profiles` 承担。
- **P-D2 profile 名与服务名同名 `mysql56`**：与 `docker-compose.local.yml` 的 `agent`/`client`
  profile 惯例一致，`--profile mysql56 up -d mysql56` 无需记忆额外名字。
- **P-D3 只绑 `127.0.0.1:3307`**：5.6 无 TLS，发布到 `0.0.0.0` 等于把可写的匿名库放到局域网上；
  3307 避开本机已有的 MySQL 8 与集群 `mysql` 服务。
- **P-D4 charset 用 `conf.d` 而不是 `initdb.d`**：`/docker-entrypoint-initdb.d/*.sql` 只在数据目录
  为空时执行，卷复用后会静默回到 latin1；`/etc/mysql/conf.d` 每次启动都生效，
  `MYSQL_DATABASE` 也按该 charset 创建，行为与生产一致且可复现。
- **P-D5 不放宽 `innodb_large_prefix`**：一旦设置，「所有索引键 ≤767 byte」这条被测命题就永远测不
  出来。测试断言直接把它钉死。
- **P-D6 版本单一来源靠测试守护**：CI job 与 profile 若各自写死版本，一次 `mysql:5.6 → 8.0` 的
  改动就会悄悄改变「已验证」的含义。`deploy/mysql56/mysql56_service_test.go` 比较两处 image 值。
- **P-D7 口令默认占位值可覆盖**：`${MYSQL56_TEST_PASSWORD:-tunnelmesh}`。集群 `mysql` 服务用
  `${MYSQL_PASSWORD:?}` 强制要求是对的（真实数据），但 `:?` 会在**任何**对该文件的插值上生效、
  与 profile 无关，会让既有集群三步引导流程直接启动失败——这是回归，必须避免。

## 4. 技术栈与规格引用

- Docker Compose v2（`--profile`、顶层 `volumes`、`healthcheck`）、镜像 `mysql:5.6`。
- `migrations/ddl.sql`（唯一权威全量 DDL）、`internal/storage.OpenMySQL`（默认 `auto-init=true`）。
- 既有门禁事实与命令：`docs/development/testing.md`、`.github/workflows/ci.yml` 的 `mysql56` job。
- InnoDB 767-byte 前缀限制依据：`docs/operations/schema-upgrades.md`、`docs/operations/connection-pool.md`。

## 5. 全局约束

- 不把口令、DSN 或任何生产凭据写入仓库；默认值是本地一次性测试库占位符，且在文档中显式标注不得用于真实数据。
- 交付产物与说明分离：`deploy/` 只放可直接使用的文件，步骤留在 `docs/`（`deploy/README.md` 的既有约定）。
- `deploy/*/..._test.go` 是仓库一致性检查，`scripts/build-release.sh` 按目录白名单打包，因此
  `deploy/mysql56/` 不会进入发布归档。

## 6. 文件清单

| 文件 | 变更 |
| --- | --- |
| `docker-compose.cluster.yml` | 新增 opt-in `mysql56` 服务（image、profiles、env、`127.0.0.1:3307` 端口、conf.d 挂载、healthcheck）与顶层 `tunnelmesh-mysql56-test-data` 卷 |
| `deploy/mysql56/utf8mb4.cnf` | 新增：`[mysqld]` `character-set-server=utf8mb4`、`collation-server=utf8mb4_general_ci`，并写明为什么不能设 `innodb_large_prefix` |
| `deploy/mysql56/mysql56_service_test.go` | 新增：三条一致性测试（版本单一来源、opt-in 且只绑回环、conf.d 内容/挂载与被测命题） |
| `docs/development/testing.md` | 本地复跑命令改为 profile 路径并补 `-p 1`/`multiStatements` 原因；新增 CI 与 profile 的分工（charset 边界由谁覆盖） |
| `docs/deployment/docker.md` | 集群模式新增「MySQL 5.6 契约环境（可选）」小节 |
| `deploy/README.md` | 目录表登记 `mysql56/utf8mb4.cnf` 与一致性测试 |
| `docs/operations/completeness-checklist.md` | charset 端到端项转为已完成，另立 8.4 矩阵为 deferred |
| 计划与本 PR 记录 + 四份生成索引 | `python3 scripts/gen_doc_index.py` |

## 7. 任务间接口

- 启动：`docker compose -f docker-compose.cluster.yml --profile mysql56 up -d mysql56`。
- 复跑：`TUNNELMESH_TEST_MYSQL_DSN='tunnelmesh:tunnelmesh@tcp(127.0.0.1:3307)/tunnelmesh_test?parseTime=true&tls=false&multiStatements=true' go test -p 1 ./internal/storage ./internal/registry -count=1 -run MySQL`。
- 销毁：`docker compose -f docker-compose.cluster.yml --profile mysql56 down -v mysql56`。
- 契约测试侧不变：`OpenMySQL` 仍需空库 + 建表权限；`MYSQL_DATABASE` 名字与 CI 保持一致（`tunnelmesh_test`）。

## 8. TDD 步骤（本节为补记）

实际执行顺序是先落 compose 服务与 `utf8mb4.cnf`，再写一致性测试，所以**不存在**「先看到红灯再实现」
的原始输出，本节只记录已验证事实，不虚构当时的红灯。等价证据是三条断言的**反向验证**：人为破坏被测
对象后必须失败，每条都实际执行并还原。

| 破坏 | 命令 | 实际失败输出 |
| --- | --- | --- |
| 移走 `utf8mb4.cnf` | `go test ./deploy/mysql56 -count=1 -run TestMySQL56ServerConfig` | `mysql56_service_test.go:131: open utf8mb4.cnf: no such file or directory` |
| 把 profile 镜像改成 `mysql:8.0`（制造与 CI 的版本漂移） | `go test ./deploy/mysql56 -count=1 -run TestMySQL56ProfileMatches` | `CI image "mysql:5.6" != ../../docker-compose.cluster.yml image "mysql:8.0": the MySQL floor version must have one source` |
| 删掉 `profiles: ["mysql56"]`（变成随集群栈启动） | `go test ./deploy/mysql56 -count=1 -run TestMySQL56ProfileIsOptIn` | `profiles = [], want [mysql56] so up for the cluster never starts it` |

1. 断言自身的缺陷也暴露过一次：最初写成整文件 `strings.Contains(config, "innodb_large_prefix")`，
   因 cnf 的**注释**里解释性地提到该词而失败，说明它检查的不是选项行；改为逐行扫描、跳过 `#` 注释。
2. 还原后：`go test ./deploy/mysql56 -count=1` → ok。
3. 回归：`go test ./... -count=1`、`go test -race ./... -count=1 -timeout 30m`、`go vet ./...`、
   `git diff --check`、`gofmt -l deploy/mysql56`（无输出）。

## 9. 验证命令与预期

```bash
go test ./deploy/... -count=1          # 三条 profile 一致性测试通过
go test ./... -count=1                 # 无回归：本次不改生产代码
go vet ./...                           # 通过
git diff --check                       # 无空白错误
ruby -ryaml -e 'YAML.load_file("docker-compose.cluster.yml")'   # YAML 可解析
```

未在本机执行、需在 CI 或有 Docker 的机器上补做（本机无容器运行时，`/usr/local/bin/docker` 是悬空符号链接）：

```bash
docker compose -f docker-compose.cluster.yml config                    # 默认不含 mysql56，且不需要 MYSQL56_* 变量
docker compose -f docker-compose.cluster.yml --profile mysql56 config  # 含 mysql56，healthcheck/挂载可渲染
docker compose -f docker-compose.cluster.yml --profile mysql56 up -d mysql56   # 健康检查转 healthy
```

## 10. 回滚注意事项

- 纯增量的开发/测试环境资产：`git revert` 合并提交即可，无 Schema、无生产代码路径、无配置分发。
- 若 `deploy/mysql56/utf8mb4.cnf` 挂载在新版 Docker 上报错，可临时删掉该 volumes 行退回 latin1；
  但这会让「生产 charset 下建表」重新变成未验证项，必须同步把
  `docs/operations/completeness-checklist.md` 的对应项改回未完成。
- 已存在的 `tunnelmesh-mysql56-test-data` 卷只含测试数据，`down -v` 或 `docker volume rm` 清理即可。
