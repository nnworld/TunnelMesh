# 日志位置与查看方式

当前程序没有内置文件日志和日志轮转配置。普通启动信息由 Cobra 命令写入 stdout，致命错误由 Go `log.Fatal` 写入 stderr，部分安全审计使用 Go `slog` 写入 stderr。不会自动写入项目目录或数据库。

## 不同部署方式

| 部署方式 | 默认位置 | 查看命令 |
| --- | --- | --- |
| 前台运行 | 当前终端 stdout/stderr | `tunnelmesh-server ... run` |
| systemd | journald | `journalctl -u tunnelmesh-server.service -f` |
| Docker/Compose | 容器 stdout/stderr | `docker logs -f tunnelmesh-server` |
| macOS launchd | `~/Library/Logs/tunnelmesh-*.log` | `tail -f ~/Library/Logs/tunnelmesh-server.log` |
| Windows + WinSW | `C:\Program Files\TunnelMesh\logs` | PowerShell `Get-Content ... -Wait` |

systemd unit 已显式设置 `StandardOutput=journal` 和 `StandardError=journal`；Docker 不做文件挂载时由 Docker logging driver 管理；Windows 脚本由 WinSW 负责滚动日志。

## 保存到文件

临时前台运行可以重定向：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml run >>/var/log/tunnelmesh/server.log 2>&1
```

生产环境优先使用 journald、Docker logging driver 或集中式日志系统，不建议多个进程直接共享一个文件。管理员首次启动凭据会输出到控制台，必须通过安全终端读取，禁止把这段输出发送到共享日志、工单或聊天系统。

## 数据面事件

传输被截断或提前丢弃时，三端都会输出 `WARN` 结构化事件，字段只包含协议、状态码、声明长度、实际字节数与错误分类，不含地址、内容或凭据：

| 事件 | 位置 | 含义 |
| --- | --- | --- |
| `client_response_truncated` | Client | 托管 HTTP 转发写回本地监听时，实际字节少于 `Content-Length` |
| `proxy_response_truncated` | Server | 托管 HTTP 路由回写响应时被截断 |
| `client_stream_queue_full_reset` | Server | Client 发送超过自己通告 credit 的数据，仅该流被 `RESET` |
| `agent_stream_send_reset` | Agent | 目标写失败或等待 credit 失败，仅该流被 `RESET` |

浏览器侧的 `ERR_CONTENT_LENGTH_MISMATCH` 现在都能在对应进程日志中找到同一条记录；`docs/operations/troubleshooting.md` 的截断排障路径以这些事件名为准。

## 日志安全

日志中不得出现密码、Bearer Token、私钥、完整 DSN、目标响应体或完整硬件指纹。排障时可以使用 trace ID、Agent ID、路由 ID、错误分类、HTTP 状态和时间范围进行关联。日志级别和 JSON 文件输出尚未提供配置项，若需要集中采集，请在进程管理器或容器层完成。
