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
- [ ] 完整 capability-gated protocol v2、flow control、UDP association 和正式 relay protobuf
- [ ] 配置 etcd 的真实集成测试（当前仅保留实现与文档接口）
- [ ] 配置 `TUNNELMESH_TEST_MYSQL_DSN` 后执行真实 MySQL contract
- [ ] 生产环境容量压测、故障注入和跨平台发布产物

未完成项必须保持显式 deferred，不得在发布说明中宣称已支持。
