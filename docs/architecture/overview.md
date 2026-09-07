# TunnelMesh 架构概览

Server 负责控制面、管理 API、Web 管理后台、HTTP/WSS 公网入口和 relay；Agent 通过 TLS WebSocket 主动连接 Server 并访问其宿主机/内网服务；Client 通过 TLS WebSocket 发起用户侧 forward、publish 和 SSH/stdin 代理。

请求链路遵循 Handler → Service → Repository。管理数据以 SQLite 或 MySQL 为权威来源，租约/注册发现默认使用 MySQL，可切换 etcd。运行时连接和流状态不写入 Prometheus 高基数标签。

公网模式只需要 HTTP/HTTPS/WSS 入口；公网 UDP 不作为 Server 监听能力。Nginx 配置见 [Nginx 推荐配置](../deployment/nginx.md)。
