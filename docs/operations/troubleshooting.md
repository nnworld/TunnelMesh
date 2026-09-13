# 故障排查

## `admin account does not exist`

该错误表示当前配置连接的数据库中没有 `role=admin` 的用户。新库首次初始化应执行：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml admin bootstrap
```

管理员已存在但凭据遗失时才使用 `admin regenerate-credentials --confirm`。如果预期管理员已经存在，请先用 `print-config` 核对当前命令与 systemd 服务是否使用同一配置文件和数据库；不要反复执行 bootstrap 掩盖连错数据库的问题。

## 启动失败

先执行：

```bash
tunnelmesh-server --config tunnelmesh.yaml check-config
tunnelmesh-server --config tunnelmesh.yaml print-config
```

`print-config` 输出的是脱敏后的有效配置。重点检查 mode、storage.driver、MySQL DSN、TLS、node.id 和 registry.type。

服务日志位置和 systemd/Docker/macOS/Windows 查看命令见 [日志位置与查看方式](logging.md)。

## schema 错误

- 自动初始化关闭时，确认数据库已经执行 `migrations/ddl.sql`。
- 检查 `schema_meta` 版本是否与当前程序一致。
- SQLite 检查挂载目录是否可写；容器中通常是 `/var/lib/tunnelmesh`。
- MySQL 检查账号是否有建表、索引和事务权限。

### MySQL 5.6：`Error 1071 Specified key was too long`

MySQL 5.6 在默认 InnoDB 配置下通常只有 767 字节的索引前缀上限。旧版
DDL 中的 `utf8mb4 VARCHAR(255)` 主键或联合索引可能超过该上限；如果不能
修改 MySQL 服务参数（例如 `innodb_large_prefix`），请使用包含字节长度受控
键列的新版 `tunnelmesh-server`，其 DDL 不依赖 `innodb_large_prefix`。

首次初始化失败后，先停止服务并确认该数据库没有需要保留的数据：

```bash
systemctl stop tunnelmesh-server
mysql -h 10.228.128.81 -P 4963 -u '<user>' -p tunnelmesh \\
  -e "SHOW TABLES;"
```

如果只是失败初始化留下的空库，备份后清理该 TunnelMesh 数据库（或创建一个
新的空数据库）再启动服务，让 `storage.auto_init: true` 重新执行新版 DDL。
已有业务数据时不要直接 `DROP DATABASE`；先做完整备份，并安排停机窗口执行
经过验证的表结构迁移。`CREATE TABLE IF NOT EXISTS` 不会自动把已有旧列改成
新的键类型。

不要在命令行、日志或工单中粘贴真实密码；此前已经暴露过的数据库凭据应立即
轮换，并通过 systemd EnvironmentFile 或 Secret Manager 注入 DSN。

## Agent 不在线

1. 确认 Agent 能访问 Server 的 `wss://` 地址。
2. 确认证书链、SNI 和系统时间正确。
3. 检查 Agent ID 是否稳定且没有重复注册。
4. 检查 Server 节点租约是否过期，以及集群节点时间是否同步。
5. 确认目标服务从 Agent 所在网络可达，而不是只在 Server 主机可达。

连接池部署中，Agent 详情应同时展示 instance 和 connection。一个 instance 离线不代表逻辑 Agent 离线；只要还有健康连接，流量会选择剩余连接。若全部连接为 0，检查 Agent token、`instance_id` 文件权限和 `server_url`，并观察 `tunnelmesh_agent_connection_errors_total`。

全链路探针可通过 `POST /api/v1/agents/{agentId}/diagnose` 发起 TCP/HTTP/UDP 诊断，并通过 `GET /api/v1/agents/{agentId}/probes` 查看受限结果摘要；目标响应体不会被保存或返回。详见 [全链路网络探针](network-probes.md)。

## 集群连接查询或关闭失败

- 看不到远端连接：确认所有 Server 都已升级到支持连接租约的版本，`node.id` 唯一且稳定，`server.relay.enabled=true`，集群时间同步。
- 关闭返回 `503`：owner Server 的 relay 不可达。检查连接列表中的 Server 地址是否可从当前节点路由、mTLS 证书 SAN 是否精确匹配、CA 是否一致、server-node token 是否有效、节点 epoch 是否一致。数据库租约会保留，不要手工删除。
- 关闭返回 `409`：传入的 `connectionEpoch` 已过期，说明该连接 ID 被替换。刷新 `GET /api/v1/agents/{agentId}/connections`，使用新的 epoch 重试。
- 关闭成功后连接又出现：这是预期行为。关闭只断开一条物理 WebSocket，不会禁用 Agent 或阻止其按连接池策略重连；需要长期停止时禁用 Agent 或撤销 token。
- 查询返回 `503`：检查数据库连接注册表可用性和管理 API 日志；不要用本地 metadata 连接数推断集群状态。

## HTTP 路由失败

- 404：检查域名、pathPrefix、wildcard DNS 和 API 路由是否匹配。
- 401/403：检查 Token、角色和资源 owner。
- 409：域名和路径已存在，重用原配置或删除旧路由。
- 502/504：检查 Agent session、目标地址、目标端口和 policy。

## SSH / websocat 失败

- 必须使用 `websocat --binary`。
- 检查 ProxyCommand URL 是否使用 `wss://` 和正确的域名/端口编码。
- 看到 WebSocket 文本帧错误时，检查客户端是否误用了 text 模式。
- 使用 `ssh -vvv` 和 `websocat -v` 获取握手与关闭原因。

## Service token 失败

- 401：检查 token 是否为空、类型是否正确、是否已过期或已撤销；`agent` 连接 `/ws/agent`，`client` 连接 `/ws/client`。
- 403：检查 token owner、Agent 绑定、scope 与 Agent Policy 的交集。
- 409：轮换/撤销并发冲突时，复用同一个 `Idempotency-Key` 重试；不要期待再次返回 secret。
- Server-node relay 失败：检查 `server.relay.node_token` 是否有效、节点是否在 `scope.serverNodeIds` 允许列表内、节点是否被禁用或逻辑删除、epoch 是否一致，以及 `server.relay.listen` 是否已监听。mTLS 模式还需检查证书 SAN 是否精确匹配、共同 `server_name` 是否存在、CA 是否正确；明文模式需确认网络确实处于受控内网。

明文 secret 只在创建/轮换响应出现一次。不要从日志、审计记录或数据库恢复 token；遗失时直接轮换并撤销旧 token。

## Agent Policy 逻辑删除

- 删除后默认列表不显示：这是预期行为。管理员在 Agent 详情页切换到“已删除”或“全部”筛选查看记录。
- 删除后新流被拒绝：`deleted_at` 非空的策略不参与授权；已有连接的关闭策略仍由协议和会话生命周期决定。
- 修改已删除策略返回 409：必须先恢复，再修改，避免误以为修改了一条仍在生效的策略。
- 恢复后仍被拒绝：刷新列表确认 `deletedAt` 为空，再检查 Token scope、Agent owner 和目标 CIDR/端口。
- 升级到 Schema v10 前确认版本为 9；MySQL `ALTER TABLE` 失败后版本不会提前推进，确认具体错误后可重试。
- 回滚到 v9 前必须恢复需要继续生效的策略；旧版本会把带 `deleted_at` 的记录当作有效策略。

## 数据安全

排障日志中可以保留 trace ID、Agent ID、路由 ID 和错误码，但不能输出密码、Bearer Token、私钥或完整 DSN。

## 子账号与 Schema v6

- `username_invalid`：用户名需为 3–64 个 ASCII 字符，只允许字母、数字、`.`、`_`、`-`。
- `account_deleted`：已删除账号只能在管理员页面的“已删除”筛选中恢复；恢复不会改变原用户名或关联资源。
- `admin_account_protected`：管理员账号不能通过子账号接口禁用、重置、删除或恢复。
- 升级前确认 `schema_meta.version=5`、增量链完整且数据库账号可执行 `ALTER TABLE`/`CREATE INDEX`。脚本失败时不会推进版本；MySQL DDL 可能隐式提交，按日志修复前置状态后可安全重试。
- v6 的 `users.deleted_at` 是可空列，应用回滚时保留该列；不要直接删除列或复用已删除用户名。

## 子账号与 Schema v7 连接池

- 升级前确认 `schema_meta.version=6`、v6→v7 增量脚本存在且数据库账号可执行 `CREATE TABLE`/`CREATE INDEX`。
- v7 新增 `agent_connection_leases` 与 `agent_instance_metadata`；旧 `agent_runtime_leases`、`agent_runtime_metadata` 保留用于回滚，不要手工删除。
- 迁移失败时先查看具体 SQL 错误和已创建对象；MySQL DDL 可能已隐式提交，修复后可重试，版本号不会在失败前推进。
- 回滚应用可保留 v7 新表；如需精确回滚业务数据，使用升级前备份恢复。
- 连接池扩容发布顺序、指标和止血步骤见[逻辑 Agent 连接池运维指南](connection-pool.md)。

## Server 节点与 Schema v8

- Server 未出现在 `/servers` 页面：确认已完成 `node.id` 初始化并成功启动；自注册失败会在 journal 中输出 `register server node` 相关错误。
- 节点显示 offline：检查 Server 进程、数据库连通性、集群时间同步，以及 `last_seen_at/expires_at` 是否持续更新。
- relay 认证拒绝：同时检查共享 token、允许列表、节点启用状态、逻辑删除状态和 epoch；mTLS 模式还需检查 SAN。任意一项不满足都会失败。
- 升级前确认 `schema_meta.version=7`、v7→v8 增量脚本存在且数据库账号可执行 `ALTER TABLE`/`UPDATE`。
- v8 只为 `server_nodes` 增加 `name`、`enabled`、`deleted_at` 并回填名称；失败时版本不会提前推进，MySQL DDL 可能已隐式提交，确认已完成的列后可重试。
- 回滚应用时保留 v8 新增列；如需精确恢复 Schema，使用升级前备份，不要手工删除列或修改版本号。

## 授权缓存与 Schema v9

- `/ready` 中 `authorization_cache` 为 unhealthy：检查 SQLite 文件或 MySQL 可用性、网络超时和数据库账号 `SELECT authorization_revision` 权限。组件错误只提示修订号源不可用，不会暴露 DSN 或完整数据库错误。
- Token 已撤销但个别节点仍放行新流：确认所有节点使用 Schema v9 或更高版本；检查 `server.authorization_cache.revision_poll_interval` 是否被调大；观察 Prometheus 的 `tunnelmesh_authorization_revision_poll_total`。本节点创建/轮换/撤销 token 会立即失效本地缓存，其它节点依赖轮询。
- 轮询失败超过 `max_stale_on_poll_error` 后，Server 会 fail-closed，不再使用正向缓存；负向缓存仍受 `negative_ttl` 限制。若需要立即排障，可临时设置 `server.authorization_cache.enabled=false` 并滚动重启。
- 升级前确认 `schema_meta.version=8`、v8→v9 增量脚本存在，数据库账号可创建表并更新 `schema_meta`。
- v9 的 `authorization_revision` 是授权缓存的一致性依据。回滚应用可保留该表；不要手工删除表、修改 revision 或回退版本号。需要精确恢复时使用升级前备份。

## WebSSH 大文件传输中断

- 症状：终端里执行 `sz <几 MB 文件>` 传到一半就断，页面显示“远端会话已结束 / 远端退出码：0”，浏览器没有下载到文件；或 `rz` 选完文件后远端报 `removed.` 且终端出现乱码。
- 根因是链路上某一跳没有把背压传回发送方，快的一端打爆了慢的一端的缓冲区，随后中继把该 stream `RESET`，SSH 通道随之 EOF。退出码 0 是 SSH 通道被本地关闭的假象，不是远端真的退出。
- 判别方法：
  - 终端上方出现红色告警 `SSH 通道异常中断（本地 WebSocket 传输失败）` → 浏览器侧 WebSocket 失败（网络切换、反代超时、发送缓冲耗尽）。
  - Server 日志出现 `relay: backpressure` 或 `protocol: window exhausted` → 中继接收/发送窗口被超额，通常是 Server 与 Agent 版本不一致（一端不懂窗口协商）。
  - 页面显示真实的 `远端退出码：N`（N≠0）→ 远端程序自己失败，例如磁盘满、`sz` 参数错误。
- 处理：
  1. 确认 Server 与 Agent 是同一版本；流控窗口是内置协议约定，混版本必然失败。
  2. 经 Nginx 反代时确认 `/ws/webssh/` 关闭了 `proxy_buffering` 且 `proxy_read_timeout` 足够长，见 [Nginx/WSS 推荐配置](../deployment/nginx.md)。
  3. 终端乱码是 pty 回显了传输中的协议字节，`reset` 或 `stty sane` 即可恢复，不代表会话损坏。
  4. 超大文件优先用 SFTP 页面传输：ZMODEM 接收方向会在浏览器内存中缓冲整个文件后才触发下载。
- 流控默认额度与三层背压说明见[大文件传输与通道流控](../user-guide/server-admin.md#大文件传输与通道流控)，均为内置行为，没有可调参数。
- 复现与回归验证：`node test/e2e/webssh/run.mjs`（4 MiB 下载 + 1.5 MiB 上传，逐字节校验），详见 [WebSSH 浏览器端到端测试](../../test/e2e/webssh/README.md)。

## WebSFTP 上传失败

- 症状：SFTP 页面上传文件后弹出红色 `文件传输失败`，或上传中途提示 `SFTP 通道已关闭`；目标主机上文件已创建但被截断，落盘大小常见停在 768 KiB 或 2 MiB 附近。
- 两种提示对应两类原因，排查时先看提示文案：
  - `文件传输失败`：浏览器侧 `libssh2_sftp_write` 只消费了部分字节。SSH 会话是非阻塞的，短写返回的是“已写入字节数”而不是 `EAGAIN`，旧实现把它当致命错误直接失败。现已按 `pointer + written` 循环补写剩余字节，连续 5 秒零进度才判定停滞并抛错。
  - `SFTP 通道已关闭`：Server 侧中继 stream 被判死。入站 WINDOW_UPDATE 除了给发送窗口加额度，还会镜像进容量 16 的 control 队列供 `ReadControl` 使用，而 WebSSH/SFTP 桥接只调用 `Read`、从不消费 control 队列；上传累计超过 16 次窗口更新即溢出并以 `protocol: flow-control window exhausted` 失败整条流。镜像现已改为尽力而为（队列满则丢弃，权威额度保存在发送窗口里），回归测试为 `TestAgentRelayStreamSurvivesUndrainedWindowUpdates`。
- 判别方法：
  - 截断点固定且与窗口额度相关（实测复现时消耗约 16 × 128 KiB ≈ 2 MiB 额度后 stream 被杀，sshd 落盘滞后，磁盘上只剩 768 KiB）→ 中继 stream 死亡，确认 Server 与 Agent 都已包含该修复。
  - 失败大小随机，且浏览器控制台或页面出现 `SFTP write stalled after N of M bytes` → 通道真的卡住（网络中断、Agent 离线、Nginx 超时），按上一节的链路排查。
  - 只有上传失败、下载正常 → 属于本节问题；下载方向的额度走 `WriteControl`，不经过 control 镜像队列，因此不受影响。
  - 报错是 SFTP 状态码（permission denied、no space left）→ 目标目录权限或磁盘问题，与通道无关。
- 处理：
  1. Server、Agent、Web 产物必须同版本发布。中继修复只在 Server 侧，只刷新前端 bundle 无效；只升级 Server 也不会修复浏览器的短写截断。
  2. 大文件优先用 SFTP 而不是终端 `rz`：SFTP 分块写入不会在浏览器内存里缓冲整个文件。
  3. 经 Nginx 反代时同样需要 `/ws/webssh/` 关闭 `proxy_buffering`，见 [Nginx/WSS 推荐配置](../deployment/nginx.md)。
- 复现与回归验证：`node test/e2e/webssh/run.mjs` 中的 `SFTP upload writes the file byte-for-byte` 检查（默认 3 MiB，可用 `TM_E2E_SFTP_UPLOAD_BYTES` 调整，逐字节 sha256 校验），详见 [WebSSH 浏览器端到端测试](../../test/e2e/webssh/README.md)。

## ZMODEM 在真实网络上报 ZRPOS / OO 错误

- 症状（任一）：
  - `rz` 上传时弹 `ZMODEM 传输失败：Unhandled header: ZRPOS`，随后 `Peer aborted session`，终端出现大段乱码（内容正是正在上传的文件）。
  - `sz` 下载完成后敲回车弹 `ZMODEM 传输失败：PROTOCOL: Only thing after ZFIN should be “OO” (79,79), not: 27,93,…`，终端停在空行，看似卡死。
  - 传输面板的进度长时间停在某个百分比且无报错（例如 291 KiB / 17.2 MiB）：浏览器回送的 ZACK 被插队写破坏后，lrzsz 会暂停发送等待合法 ZACK，直到自身超时。与上面两种报错同根因。
  - `sz` 卡住几十秒后整页变成“SSH 通道已关闭”空态：卡住是上述暂停，而旧前端把出站写失败（5 秒零进度停滞保护）连带关闭了整条通道；新版本只中止传输并保留终端。
  - 本机或同机房测试一切正常，只在真实网络、经 Nginx WSS 反代的链路上出现。
- 根因是浏览器侧同一条 SSH 通道上存在多个并发出站写入（ZMODEM 的同步回调是 fire-and-forget）。通道窗口紧张时 libssh2 只消费部分字节，部分写的续写被另一个写者插队，对端收到坏帧：`rz` CRC 失败回 `ZRPOS`，`sz` 收不到合法收尾直接退出、不发 `OO`。错误里的 `27,93` 是 ESC ]，即 shell 的 OSC 标题序列——提示符跑到了本该是 `OO` 的位置。
- 进度事件洪泛是卡住的诱因之一：progress 原先每个 ZDATA 子包触发一次（17 MiB 文件约上万个 Vue 更新与进度条重绘），主线程被占用时 ZACK 写出被推迟，`sz` 随即暂停。现在按 64 KiB 字节步进或 500 ms 安静间隔上报，首个子包无条件上报。
- “通道被关闭”是次生缺陷：`toPeer` 的写失败 catch 曾无条件关闭通道。现在传输仍活跃时只本地中止传输（`abortLocal`，不向已坏的管道发 ZABORT）并弹 `ZMODEM 传输失败：…`；通道是否真的死亡由读循环裁决（EOF 会给出正常结束态），写路径不再越权关通道。
- 终端停空行是次生缺陷：协议抛错时解析器缓冲区里的提示符字节曾随会话一起被丢弃。现在这些字节会回放给终端，报错横幅出现的同时提示符立即恢复。
- 处理：
  1. 升级 Web 前端 bundle 与嵌入它的 Server 二进制到包含单写者闸门的版本；Server/Agent 无需改配置。
  2. 进度卡死请先确认线上 bundle 是否已包含单写者闸门（该修复只在前端）：旧 bundle 上卡进度、`ZRPOS`、`OO` 报错是同一缺陷的三种表现。
  3. 终端乱码是 lrzsz 中止后对未消费在途字节的 pty 回显，属 lrzsz 固有行为；传输不再异常中止后自然消失，已出现的乱码用 `reset` 或 `stty sane` 恢复。
  4. 升级后仍出现 `ZRPOS` 或进度卡死，说明链路存在真实丢包或中间层缓冲，按 [Nginx/WSS 推荐配置](../deployment/nginx.md) 检查 `/ws/webssh/` 的 `proxy_buffering` 与超时设置，并保留 Server 日志与浏览器控制台输出用于定位。
  5. 遇到“卡住后通道关闭”请先升级前端：新版本下该序列只中止传输、保留终端，`sz` 自身超时退出后提示符会回来；若通道确实已死，读循环会随后给出正常结束态而不是静默悬挂。
- 回归验证：交错不变式由 `never interleaves concurrent shell writes when libssh2 consumes partially` 与 `never interleaves an SFTP write with a concurrent SFTP request` 两个单元测试固化。该缺陷在 localhost E2E 中不可复现（通道窗口从不紧张），**不要用 E2E 绿作为已修复的依据**。

## ZMODEM 传输结束后终端不回显、同一条错误反复弹出

- 症状（成组出现）：
  - `sz` 传输结束后终端不再回显，敲回车没有任何输出，看似卡死；页面顶部堆叠多条**完全相同**的 `ZMODEM 传输失败：PROTOCOL: Only thing after ZFIN should be “OO” (79,79), not: …`，敲一次回车多一条。
  - 文件其实已经下载完成（浏览器下载目录里字节完整），但页面报了失败。
  - 大文件（几十 MiB 以上）`sz` 在传输中途被本地中止，弹出 `ZMODEM 传输失败：SSH shell write stalled after N of M bytes`，进度面板消失，终端仍在但传输没了。
- 与上一节的区别：上一节是并发写插队造成对端收到坏帧（`ZRPOS`、乱码、对端中止），本节是**解析器状态没有随会话一起释放**与**停滞期限对大文件过严**，两者可以叠加出现。判别要点是错误文案：`Only thing after ZFIN` 反复出现且终端完全静默 → 本节；`ZRPOS`/`Peer aborted session` 且终端有大段乱码 → 上一节。
- 根因一（横幅堆叠 + 终端静默）：vendored `zmodem.js` 的 `Sentry.consume()` 只要内部 `_zsession` 非空就把所有输入投喂给它，而 `_zsession` 只由会话自身的 `session_end` 事件清空。`_consume_first()` 在 `_got_ZFIN` 后收到非 `OO` 字节时是直接 `throw`，`session_end` 永不触发，库也没有公开的复位入口。于是会话虽然在桥接里被置空，Sentry 仍持有那个死会话：之后每个字节都被再次投喂、再次抛出，既产生新横幅又永远到不了终端。
- 根因二（把已完成的传输报成失败）：ZMODEM 的 `ZFIN` 只在 `ZEOF` 之后交换、`OO` 只在 `ZFIN` 之后打印。能触发这个 throw 说明文件字节早已全部 spool 完、`completed` 已上报、浏览器已存盘，缺的只是对端的两个字节收尾。它属收尾噪声，不是传输失败。
- 根因三（大文件中途被中止）：大文件传输期间浏览器回送的 ZACK 会在出站方向堆积数兆字节，lrzsz 在等待重传确认时暂停读取，通道窗口收紧后协议写出现零进度；旧的 5 秒停滞保护把这个合法暂停判成死链路并抛错，`toPeer` 的处置策略随即本地中止传输。
- 现在的行为：
  - 协议抛错后桥接**重建 Sentry 实例**（`abort()`、`abortLocal()` 同样重建），终端立即恢复普通回显，同一条错误不再重复上报。
  - `receive` 会话在 `_got_ZFIN` 之后抛错时按已完成处理：直接发 `ended` 收起面板、把解析器缓冲区里的提示符回放给终端，并保留吃掉晚到 `OO` 的逻辑，不弹任何错误。传输中途抛错仍然照常报 `error`。
  - ZMODEM 协议写不再继承 5 秒停滞期限（`write(data, { stallLimitMs: 0 })`），按键写的默认期限不变。同时 `close()` 不再排在写闸门之后，否则一个无期限的停滞写者会让 `closeTerminal()` 永久悬挂并泄漏 libssh2 句柄。
- 处理：
  1. 这三项修复全部只在前端 bundle：必须 `npm run build` + `rsync` 到 `internal/server/web_dist/` 后重建 Server 二进制。只替换二进制不重建嵌入产物等于没有发布。
  2. 若仍看到 `SSH shell write stalled after …`，先确认线上 bundle 是否包含停滞豁免（在产物中检索 `stallLimitMs`）；旧 bundle 上“大文件中途被中止”是必然结果。
  3. 豁免只覆盖零进度等待，不覆盖真实错误：隧道断开时 libssh2 返回负值错误码，仍会抛错并按“传输活跃则中止传输、保留终端”的既有策略处置，通道是否真的死亡由读循环裁决。
  4. 传输结束后若终端仍有残留乱码，是 lrzsz 对未消费在途字节的 pty 回显，`reset` 或 `stty sane` 恢复。
- 回归验证：`reports a finished receive as ended when the peer skips the OO trailer`、`returns to plain terminal passthrough after the parser session dies`、`parses shell output normally after abortLocal`、`waits for window credit without a deadline when the stall limit is disabled`、`still reports a stalled shell write once the default deadline passes` 五个单元测试固化；E2E 的 `transfer panel closes and the prompt returns` 覆盖“传输完成 → 面板收起 → 提示符回归且控制台洁净”。

## sz 下载中偶现“SSH 通道已关闭”空态

- 症状：大文件 `sz` 下载过程中或刚结束时，页面突然变成“已断开 / SSH 通道已关闭”空态；没有红色传输告警，也没有“远端会话已结束 / 远端退出码”提示；浏览器下载目录里的文件可能已经完整。
- 判别：该空态的渲染条件是本地 `closeTerminal()` 且读循环从未报告远端退出。传输期间按键被丢弃、协议写失败只中止传输不关通道，因此空态只可能来自浏览器侧把“拥塞”误判成“死亡”：
  - 传输尾部出站窗口拥塞，传输结束瞬间的按键写零进度达到旧判定窗口（5 秒）即抛错并关闭会话；
  - 或传输已结束、但一条仍在等窗口额度的协议写随后拒绝，旧逻辑在“传输非活跃”分支直接关闭会话。
  若页面带红色 `SSH 通道异常中断（本地 WebSocket 传输失败）` 告警或“远端退出码”，则不是本节问题，按 [WebSSH 大文件传输中断](#webssh-大文件传输中断) 排查。
- 根因：浏览器侧“通道已死”的判定窗口曾是 5 秒零进度（shell 写与 SFTP 写共用），而大文件传输的尾部拥塞 routinely 超过这个量级；同时第十五轮让协议写无期限等待窗口额度，传输结束后的迟到拒绝也会走到关会话分支。Server 侧唯一的时间型判定 `server.webssh.idle_timeout` 默认 5 分钟，不是本问题来源。
- 现在的行为：
  - “通道已死”的判定窗口提高到 30 秒零进度（shell 写与 SFTP 写共用）。真正死掉的隧道通常由 WebSocket 关闭或读循环 EOF 更早给出结束态，30 秒只是兜底上限。
  - 传输结束后的迟到协议写拒绝不再关闭会话；通道存活性归读循环与按键路径裁决。
- 处理：
  1. 该修复只在前端 bundle：`npm run build` + `rsync` 到 `internal/server/web_dist/` 后重建 Server 二进制；只换二进制不重建嵌入产物等于没有发布。
  2. Server 侧无需改配置；若曾为排查问题把 `server.webssh.idle_timeout` 调小，请恢复默认 5m 或保证不小于传输最久暂停时间。
  3. 经 Nginx 反代时仍按 [Nginx/WSS 推荐配置](../deployment/nginx.md) 关闭 `/ws/webssh/` 的 `proxy_buffering` 并放宽读写超时，避免中间层先于 30 秒窗口断开连接。
  4. 行为变化知会：死隧道的写路径兜底 surfaced 从 5 秒变为 30 秒；期间终端可能短暂无回显，属预期。
- 回归验证：`only reports a stalled shell write after thirty seconds of zero progress`、`fails an SFTP write only after thirty seconds of zero progress` 两个单元测试（20 秒不报、35/36 秒报），以及源码断言 `fails the transfer instead of the channel when a protocol write fails`（`toPeer` 片段不得包含 `closeTerminal()`）。

## 管理后台打开是 Nginx / OpenResty 欢迎页

- 症状：访问管理后台域名返回 “Welcome to OpenResty!”（或 Nginx 默认页），但 `/api/v1/…` 接口和 WebSocket 可能依旧正常；刷新前端路由（如 `/remote-servers`）同样是欢迎页。
- 根因：server 块里缺少兜底 `location / { proxy_pass …; }`。SPA history fallback 由 Server 内嵌静态服务完成，前提是请求先到达 Server；没有兜底反代时，`/` 与所有前端路由命中 Nginx 默认 root（OpenResty 常为 `/usr/local/openresty/nginx/html`）下的 `index.html`，即欢迎页。该块常被误认为“前端路由配置”而在精简配置时删掉。
- 判别：
  - `curl -sI https://<管理后台域名>/` 返回 200 且正文是欢迎页，而 `curl -sI https://<管理后台域名>/api/v1/health` 或登录接口正常 → 兜底反代缺失。
  - 检查 nginx 配置：server 块只有 `/api/`、`/ws/*`、`/health/*` 等 location，没有 `location /`。
  - 若 `/` 返回 404 而非欢迎页，则不是本节问题，多为 Server 未启动或 upstream 不可达。
- 处理：
  1. 加回兜底反代，见 [Nginx 推荐配置](../deployment/nginx.md) 最后一段：`location / { proxy_pass http://tunnelmesh_server; proxy_http_version 1.1; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto https; }`。**不需要** `try_files` 或 rewrite。
  2. `nginx -t` 通过后 reload；确认 `/`、任意前端路由深链、`/api/` 与 `/ws/webssh/` 四类路径都正常。
  3. 该 location 是最短前缀，不会吞掉 `/api/` 与 `/ws/*`；若曾为“修欢迎页”加过 `try_files $uri $uri/ /index.html;` 指向本地目录，请一并删除，避免静态托管与反代并存。
- 回归：配置类问题，无代码回归测试；[Nginx 推荐配置](../deployment/nginx.md) 的推荐配置注释与关键约束已写明“兜底反代不能删”，本节作为识别与恢复手册。
