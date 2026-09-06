# 配置说明

## 配置来源

支持 YAML、JSON、TOML 配置文件，也可以完全不使用配置文件：

```bash
tunnelmesh-server --mode local --storage.driver sqlite --storage.auto_init=true check-config
```

优先级从高到低：

1. 命令行参数
2. 环境变量（前缀 `TUNNELMESH_`，点号和连字符转换为下划线）
3. 配置文件
4. 内置默认值

## 本地模式示例

```yaml
mode: local
storage:
  driver: sqlite
  auto_init: true
  sqlite:
    path: /var/lib/tunnelmesh/tunnelmesh.db
registry:
  type: database
server:
  http_addr: :80
  https_addr: :443
  tcp_bridge:
    enabled: true
    path: /ws/tcp
```

```bash
tunnelmesh-server --config tunnelmesh.local.yaml check-config
tunnelmesh-server --config tunnelmesh.local.yaml run
```

## 集群模式示例

```yaml
mode: cluster
storage:
  driver: mysql
  auto_init: true
  mysql:
    dsn: tunnelmesh:${MYSQL_PASSWORD}@tcp(mysql:3306)/tunnelmesh?parseTime=true&tls=true
    tls: true
    ca: /run/secrets/mysql-ca.pem
registry:
  type: database
node:
  id: server-1
```

切换 etcd：

```yaml
registry:
  type: etcd
  endpoints:
    - https://etcd-1:2379
    - https://etcd-2:2379
```

## 管理员凭据

首次初始化时由 Server 通过控制台输出管理员用户名和随机密码。若凭据丢失，在同一数据库配置下执行：

```bash
tunnelmesh-server --config tunnelmesh.yaml admin regenerate-credentials --confirm
```

旧 Token 会被撤销。不要把命令输出写入共享日志或工单系统。
