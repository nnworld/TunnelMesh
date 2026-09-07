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

集群模式必须使用 MySQL，并要求 MySQL TLS 和节点 ID：

```bash
export MYSQL_PASSWORD='change-me'
export MYSQL_ROOT_PASSWORD='change-root-me'
export TUNNELMESH_STORAGE_MYSQL_DSN='tunnelmesh:change-me@tcp(mysql:3306)/tunnelmesh?parseTime=true&tls=true'
docker compose -f docker-compose.cluster.yml up -d --build
```

在启动前准备 `deploy/certs/mysql-ca.pem`，并确认 DSN 的 TLS 配置与证书匹配。不要把密码、私钥或证书提交到仓库。

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
  -p 80:80 -p 443:443 \
  tunnelmesh:server run
```

配置优先级仍为：命令行参数 > 环境变量 > 配置文件 > 默认值。容器内推荐使用环境变量或挂载只读配置文件。

Server 的内置 HTTP runtime 负责提供管理 API、嵌入式 Web 和 Agent WebSocket，但当前监听器本身不终止 TLS。生产部署必须在反向代理或负载均衡器处终止 HTTPS/WSS，再将受保护的内部 HTTP 连接转发到 Server；同时为 Agent 注入有效的 API bearer token。

## 安全与升级

- 镜像以非 root 用户运行。
- 生产环境将 TLS 证书、MySQL DSN 和 Token 放在 Secret 管理系统中。
- 升级前备份 MySQL/SQLite，并先在一台节点灰度验证。
- schema 自动初始化关闭时，必须先执行受控的数据库迁移，再启动应用。
- 回滚使用上一版本镜像，并保留兼容的数据库 schema。
