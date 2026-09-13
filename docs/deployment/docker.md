# Docker 部署

## 构建镜像

Dockerfile 是多阶段构建：先编译 Vue 管理后台，再编译 Go 二进制，最后使用非 root distroless 运行时镜像。

```bash
docker build --build-arg APP=server -t tunnelmesh:server .
docker build --build-arg APP=agent  -t tunnelmesh:agent .
docker build --build-arg APP=client -t tunnelmesh:client .
```

`APP` 只允许 `server`、`agent`、`client`。镜像默认工作目录为 `/var/lib/tunnelmesh`，本地 SQLite 数据库默认写入该目录。

## 本地模式

```bash
mkdir -p data/server
docker compose -f docker-compose.local.yml up -d --build server agent
docker compose -f docker-compose.local.yml --profile client run --rm client login
```

Server 数据保存在 Compose volume `tunnelmesh-server-data`。需要备份时先停止 Server，再复制 volume 或绑定目录中的 SQLite 文件。

## 集群模式

集群模式必须使用 MySQL，并要求 MySQL TLS 和节点 ID。`docker-compose.cluster.yml` 默认启用两个
Server 之间的 relay（`TUNNELMESH_SERVER_RELAY_ENABLED=true`）；不开启 relay 时两个节点各自独立，
Client 连到没有目标 Agent 路由的节点就无法访问另一节点上的 Agent。

### 1. 准备证书

MySQL CA 放在 `deploy/certs/mysql-ca.pem`，并确认 DSN 的 TLS 配置与证书匹配。relay mTLS 材料由
脚本生成，节点 ID 必须与 Compose 里的 `TUNNELMESH_NODE_ID` 完全一致，因为它会写入证书 SAN：

```bash
./scripts/gen-relay-certs.sh server-1 server-2
```

生成结果位于 `deploy/certs/`（已 gitignore）：

```text
deploy/certs/relay/relay-ca.pem              CA 证书，每个节点都挂载
deploy/certs/relay/<node-id>/relay.pem       节点证书
deploy/certs/relay/<node-id>/relay-key.pem   节点私钥，0600
deploy/certs/private/relay-ca-key.pem        CA 私钥，0600，只在宿主机
```

Compose 只挂载 CA 证书和该节点自己的目录，因此任一容器都读不到 CA 私钥或其它节点的私钥。
新增节点时重跑脚本并追加节点 ID 即可，已有 CA 会被复用；`--force` 只重签命令行上列出的节点证书，
不会替换 CA。不要把密码、私钥或证书提交到仓库。

**容器必须能读到挂载进去的私钥。** 运行时镜像是 distroless，以 `USER nonroot:nonroot`
（uid/gid 65532）运行；bind mount 保留宿主机属主和权限，而脚本生成的节点私钥是 `0600` 且属于
执行脚本的用户，容器内会因权限不足无法加载，relay 在启动时报证书读取失败。为 Compose 准备材料时
把属主交给容器用户：

```bash
sudo chown -R 65532:65532 deploy/certs/relay
```

这只影响 `relay/` 下会被挂载的 CA 证书与节点材料，`private/` 里的 CA 私钥不挂载，保持
`0700`/`0600` 不变。用 systemd 直接部署时不要 chown 给 65532，而是按
[Relay mTLS 证书生成与配置](../operations/relay-mtls.md) 第 6 节安装成 `root:tunnelmesh` 且
`0640`。

### 2. 引导 relay token

relay 需要 `server_node` 类型的 service token，而 token 只能由已运行的 Server 签发，因此首次
启动先关闭 relay：

```bash
export MYSQL_PASSWORD='change-me'
export MYSQL_ROOT_PASSWORD='change-root-me'
export TUNNELMESH_STORAGE_MYSQL_DSN='tunnelmesh:change-me@tcp(mysql:3306)/tunnelmesh?parseTime=true&tls=true'
# 必须 export 而不是只加在 up 前面：compose 对 exec 同样会重新插值 service 的
# environment 块，否则 exec 里 relay 仍为 enabled 且 token 为空，bootstrap 会先失败。
export TUNNELMESH_SERVER_RELAY_ENABLED=false

docker compose -f docker-compose.cluster.yml up -d --build

# 容器内三个二进制统一叫 /usr/local/bin/tunnelmesh（由 APP build arg 决定）。
docker compose -f docker-compose.cluster.yml exec server-1 \
  /usr/local/bin/tunnelmesh admin bootstrap
```

在管理后台创建一个 `server_node` service token：`scope.serverNodeIds` 留空表示 fleet token，
允许所有启用且未逻辑删除的节点；填写时只允许列出的节点。明文只返回一次。

### 3. 启用 relay

```bash
export TUNNELMESH_SERVER_RELAY_NODE_TOKEN='replace-with-server-node-service-token'
# 覆盖上一步的 export，或直接在当前 shell 取消它。
unset TUNNELMESH_SERVER_RELAY_ENABLED
docker compose -f docker-compose.cluster.yml up -d
docker compose -f docker-compose.cluster.yml logs -f server-1 server-2
```

token 为空且 relay 开启时 `check-config` 会以 `server relay node token is required` 直接失败，
不会静默降级为单节点。`TUNNELMESH_SERVER_RELAY_ENDPOINT` 已由 Compose 按节点设为
`server-1:9443` 与 `server-2:9443`；relay 端口只在 Compose 网络内可达，不发布到宿主机。

受控内网可以改用明文 relay：把 `TUNNELMESH_SERVER_RELAY_CA`、`_CERT`、`_KEY`、`_SERVER_NAME`
四个变量导出为空字符串。明文模式仍校验 token、节点状态和 epoch，但不提供传输加密，也无法用
证书证明节点身份。四个字段必须同时为空或同时非空，只填一部分会被拒绝。

证书字段含义、SAN 要求和轮换步骤见 [Relay mTLS 证书生成与配置](../operations/relay-mtls.md)。

### 注册发现

注册发现默认使用数据库 lease；切换 etcd：

```bash
export TUNNELMESH_REGISTRY_TYPE=etcd
export TUNNELMESH_REGISTRY_ENDPOINTS='https://etcd-1:2379,https://etcd-2:2379'
```

## 运行时配置

```bash
docker run --rm \
  -v "$PWD/data/server:/var/lib/tunnelmesh" \
  -e TUNNELMESH_MODE=local \
  -e TUNNELMESH_STORAGE_AUTO_INIT=true \
  -p 80:80 \
  tunnelmesh:server run
```

Server 只有一个监听器，容器内默认 `:80`。映射 443 前必须先启用原生 TLS，否则该端口没有进程监听：

```bash
docker run --rm \
  -v "$PWD/data/server:/var/lib/tunnelmesh" \
  -v "$PWD/certs:/certs:ro" \
  -e TUNNELMESH_TLS_ENABLED=true \
  -e TUNNELMESH_TLS_CERT_FILE=/certs/server.pem \
  -e TUNNELMESH_TLS_KEY_FILE=/certs/server-key.pem \
  -e TUNNELMESH_TLS_MIN_VERSION=1.2 \
  -e TUNNELMESH_SERVER_HTTP_ADDR=:443 \
  -p 443:443 \
  tunnelmesh:server run
```

配置优先级仍为：命令行参数 > 环境变量 > 配置文件 > 默认值。容器内推荐使用环境变量或挂载只读配置文件。

Server 的内置 HTTP runtime 在同一个监听器上提供管理 API、嵌入式 Web、Agent/Client WebSocket、WebSSH 和 TCP bridge。设置 `tls.enabled: true`（容器内 `TUNNELMESH_TLS_ENABLED=true`）时该监听器会自行终止 TLS，缺少 `cert_file`/`key_file` 或 `min_version` 不是 `1.2`/`1.3` 会在启动时直接失败而不是降级为明文。生产部署仍推荐在反向代理或负载均衡器处终止 HTTPS/WSS，再将受保护的内部 HTTP 连接转发到 Server；同时为 Agent 注入绑定该 Agent 的 `agent` service token。监听地址与 TLS 的完整说明见[配置说明](../operations/configuration.md#监听地址与-tls)。

Nginx 的完整 HTTPS/WSS、Authorization 透传、Upgrade、超时、限流和 `/metrics` 保护示例见 [Nginx 推荐配置](nginx.md)。

容器日志默认输出到 stdout/stderr，由 Docker logging driver 管理；查看方式和其它平台日志位置见 [日志位置与查看方式](../operations/logging.md)。

## 安全与升级

- 镜像以非 root 用户运行。
- 生产环境将 TLS 证书、MySQL DSN 和 Token 放在 Secret 管理系统中。
- 升级前备份 MySQL/SQLite，并先在一台节点灰度验证。
- schema 自动初始化关闭时，必须先执行受控的数据库迁移，再启动应用。
- 回滚使用上一版本镜像，并保留兼容的数据库 schema。

服务凭据建议使用 Compose/Kubernetes Secret 注入，而不是写入镜像或 YAML：

```bash
docker run --rm --env-file ./secrets/tunnelmesh-client.env tunnelmesh:client run
```

首次创建 Agent/Client token 后，把 secret 写入权限为 `0600` 的 Secret 文件；轮换时先下发新 secret、验证新连接，再撤销旧 secret。集群 relay 还需要独立的 `server_node` token、mTLS 证书和显式 `server.relay.listen`。
