# 集群模式

集群节点共享 MySQL 管理数据。节点租约默认写入 MySQL，可通过配置切换 etcd；租约带 epoch，旧 epoch 的 Agent runtime stats 和 relay 操作必须被拒绝（fencing）。节点间 relay 使用 mTLS，入口连接仍由各节点的 HTTPS/WSS listener 接收。

MySQL 是管理事实来源，registry 只负责租约和发现，不替代资源、Token、路由或审计数据。生产部署应使用高可用 MySQL、连接池、读写超时和备份恢复演练。
