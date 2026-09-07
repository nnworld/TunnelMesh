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

## 日志安全

日志中不得出现密码、Bearer Token、私钥、完整 DSN、目标响应体或完整硬件指纹。排障时可以使用 trace ID、Agent ID、路由 ID、错误分类、HTTP 状态和时间范围进行关联。日志级别和 JSON 文件输出尚未提供配置项，若需要集中采集，请在进程管理器或容器层完成。
