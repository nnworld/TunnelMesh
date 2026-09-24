# 项目完整性清单

- [x] Go Server、Agent、Client 三个二进制
- [x] SQLite 本地模式、MySQL 集群管理数据、MySQL/etcd registry 选项
- [x] 唯一权威 DDL：`migrations/ddl.sql`
- [x] Dockerfile、Compose、本地和 WSS/Nginx 部署说明
- [x] Linux systemd、macOS launchd、Windows WinSW 安装脚本和文档
- [x] Linux/Windows/macOS amd64/arm64 发行归档和 SHA256 清单脚本
- [x] Web 管理后台、Token/RBAC、Agent metadata 和运行统计
- [x] TCP、UDP、HTTP、WebSocket/SSH over WebSocket 用户文档
- [x] `/health/live`、`/health/ready`、`/metrics`
- [x] Prometheus rules 与单一 Grafana Dashboard
- [x] 运行统计、探针摘要、epoch fencing 和 cursor 分页
- [x] tp-* 托管 HTTP 代理入口（OpenResty 搬运层 + Server 策略内核 + 管理后台）
- [ ] 完整 capability-gated protocol v2、flow control、UDP association 和正式 relay protobuf
- [ ] 配置 etcd 的真实集成测试（当前仅保留实现与文档接口）
- [x] 真实 MySQL contract 进入 CI：`mysql56` job 起 `mysql:5.6` 服务容器，执行与 SQLite 同一套
      repository/registry 契约并作为 main 的必需检查
- [ ] MySQL charset 端到端验证（CI 为服务端默认 latin1，生产为 `utf8mb4_general_ci`）与
      `mysql:8.4` 开发环境的版本矩阵
- [ ] 生产环境容量压测、故障注入和跨平台发布产物

未完成项必须保持显式 deferred，不得在发布说明中宣称已支持。

tp-* 代理入口已支持的是 HTTP/HTTPS 正向代理；**SOCKS5 托管入口与单路由多出口池化仍属 deferred**，
不得写成已支持。
