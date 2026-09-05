# TunnelMesh 平台架构设计

## 目标与边界

TunnelMesh 使用 Go 提供 Server、Agent Client 和 tunnelmesh-client。Agent 通过 TLS WebSocket 主动连接 Server；Client 支持本地 TCP、UDP、HTTP forward；Server 通过 80/443 提供 HTTP/HTTPS/WebSocket 泛域名托管和 TCP-over-WebSocket Bridge；公网托管不开放额外端口，不支持公网 UDP。

本地模式使用 SQLite，集群模式使用 MySQL 管理数据；集群注册中心默认 MySQL 节点租约，可配置切换到 etcd。Web 使用 Vue 3、TypeScript、Vite、Pinia、Vue Router、Element Plus。

## 总体架构

Agent 通过 WS 连接某个 Server，User Client 或公网请求可到任意 Server，节点之间用 mTLS gRPC/HTTP2 relay 转发实时流。MySQL 保存管理数据，MySQL Lease 或 etcd 保存运行时节点和 Agent 归属。跨模块只依赖接口，遵守 Handler -> Service -> DAO。

## 连接与协议

WebSocket 端点为 /ws/agent/v1、/ws/client/v1、/ws/tcp，默认均在 443。Agent/User WS 使用版本化二进制帧，TCP 为有序字节流，UDP 为带边界数据报；每流具备超时、最大帧、缓冲区和背压限制。TCP-over-WebSocket Bridge 为一条 WS 对应一条 TCP，双向透传 binary message，不做 Base64，支持 ssh + websocat。

## 路由

显式路由字段为 domain、path_prefix、agent_id、target_host、target_port。动态泛域名格式为 <agent-id>-<ip-text>-<port>.apps.example.com，例如 a7k3m9q2-192-168-1-20-3000.apps.example.com。Agent ID 不含横杠；右侧解析端口；中间横杠还原为 IPv4 点号；首期动态路由限定 IPv4，IPv6 先用后台显式路由。

路由优先级为显式 route_id > 精确域名加路径 > 精确域名 > 显式泛域名 > 动态泛域名 > 404/403。动态路由必须匹配 Agent CIDR/端口白名单，默认拒绝回环、链路本地和云元数据地址。

## 数据、注册中心与管理员恢复

唯一 DDL 文件为 migrations/ddl.sql，兼容 SQLite/MySQL；核心表包括 users、api_tokens、agents、agent_policies、tunnel_groups、tunnels、server_nodes、agent_runtime_leases、audit_logs、schema_meta。storage.auto_init=true 时通过 embed 执行，false 时缺表启动失败。

MySQL 注册模式使用事务条件更新、TTL 和 epoch/fencing；etcd 模式使用 Lease、CAS、Watch。MySQL 是管理数据权威来源，租约表或 etcd 只保存临时运行状态。

首次启动且没有管理员时生成短随机用户名和高熵密码，仅保存密码哈希，明文只输出一次。控制台输出丢失时运行 tunnelmesh-server admin regenerate-credentials --config <path> --confirm；命令获取恢复锁、撤销旧会话、生成新凭据、写审计并打印，不提供匿名 HTTP 恢复接口。

## 配置、后台与部署

配置优先级为命令行参数 > 环境变量 > 配置文件 > 默认值。后台使用 Vue 3 + Element Plus，API 使用 /api/v1、统一响应、cursor 分页和幂等键，角色为 admin/user。

必须交付 Dockerfile、.dockerignore、docker-compose.local.yml、docker-compose.cluster.yml 和 deploy/docker/README.md。Dockerfile 使用 web-build、go-build、runtime 多阶段构建，通过 ARG APP=server|agent|client 生成镜像，runtime 使用非 root、固定基础镜像、CA 和健康检查。公网默认只开放 80/443。

## TDD、可观测性与文档

测试覆盖协议、Repository、NodeRegistry、relay、动态域名、策略校验、TCP Bridge、SSH 字节完整性、Agent 重连、Web 和 E2E。CI 执行 go test ./...、go test -race ./...、go vet ./...、lint、fuzz、前端构建和 Docker 构建。

维护 README、架构、协议、API、部署、用户指南、运维、安全、测试和 ADR。AGENTS.md 是唯一项目规范源；CLAUDE.md 只指向 AGENTS.md；禁止未经明确指令自动 commit/push/merge。

## 验收与风险

验收覆盖无配置本地启动、SQLite 自动建表开关、MySQL 默认集群、etcd 切换、80/443 公网入口、HTTP 泛域名、ssh + websocat、Docker Compose 和管理员凭据恢复。主要风险通过 CIDR/端口白名单、epoch/fencing、背压、限流、readiness、版本协商和审计控制。
