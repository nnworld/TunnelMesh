# SSH over WebSocket

TunnelMesh 支持通过 Server 的 WebSocket TCP bridge 传输 SSH 原始字节。SSH 仍由目标主机的 `sshd` 负责用户认证、授权和退出码；TunnelMesh 不增加第二套 SSH 或 command-exec 协议。

## 前置条件

1. 在目标主机的 `~/.ssh/authorized_keys` 安装公钥，或让本地 `ssh-agent` 持有对应私钥。
2. Server policy 允许目标 Agent、目标地址和 TCP/22。
3. Agent 能从内网连接目标主机的 22 端口；Server 只需暴露 80/443。

例如：

```bash
ssh-copy-id -i ~/.ssh/id_ed25519.pub devuser@target-host
ssh-add ~/.ssh/id_ed25519
```

## tunnelmesh-client ProxyCommand

本地 client 直接复用 TCP proxy，适用于显式 Agent/目标地址：

```sshconfig
Host tunnelmesh-agent
    HostName ignored-by-proxy
    User devuser
    ProxyCommand tunnelmesh-client --config tunnelmesh.yaml proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22
```

临时执行：

```bash
ssh -o 'ProxyCommand=tunnelmesh-client --config tunnelmesh.yaml proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22' \
  devuser@ignored-by-proxy
```

## websocat 命令

```bash
websocat --binary -B 65536 - \
  'wss://agent-devbox-10-0-0-8-22.apps.example.com'
```

`--binary` 保证 WebSocket 使用 binary message，`-B 65536` 控制缓冲区大小。动态 wildcard 的格式是 `<agent-id>-<ip-octet>-<ip-octet>-<ip-octet>-<ip-octet>-<port>.apps.example.com`，因此上例会访问 Agent `agent-devbox` 的 `10.0.0.8:22`。如果使用显式托管路由，请把 URL 替换为该路由配置的域名和路径（例如由 Server 管理 API 创建的 `ssh.example.com` route）。

## SSH ProxyCommand

```sshconfig
Host tunnelmesh-agent
    HostName ignored-by-proxy
    User nami
    ProxyCommand websocat --binary -B 65536 - "wss://agent-devbox-10-0-0-8-22.apps.example.com"
```

也可以临时执行：

```bash
ssh -o 'ProxyCommand=websocat --binary -B 65536 - wss://agent-devbox-10-0-0-8-22.apps.example.com' \
  nami@ignored-by-proxy
```

通过 SSH 执行远程命令时，退出码由 SSH 原样返回：

```bash
ssh -o 'ProxyCommand=tunnelmesh-client --config tunnelmesh.yaml proxy tcp --agent agent-devbox --target-host 127.0.0.1 --target-port 22' \
  devuser@ignored-by-proxy 'uname -a && systemctl is-active sshd'
echo "ssh exit=$?"
```

## 安全边界

- TunnelMesh 只传输 SSH 的 TCP 字节，不解析用户名、私钥或命令内容。
- 不要把私钥、密码或完整 SSH 配置放入 Agent metadata；敏感 metadata 会被遮罩。
- `proxy tcp` 不是 shell 通道，不能用来绕过 `sshd`。脚本执行是另一条独立能力（Agent 侧，管理员发起，见 [ADR 0005](../architecture/adr/0005-agent-script-execution.md)），与 SSH 代理链路无关。

## 约束

- WebSocket endpoint 必须指向 Server 的 TCP bridge 路径或已配置的 TCP route。
- Server 只负责转发字节，不解析 SSH 协议。
- 生产环境使用 `wss://` 和有效证书。
- 遇到 404 时检查域名、路径和 wildcard DNS；遇到 502/504 时检查 Agent 在线状态和目标端口。
- 遇到 binary message 错误时确认 `websocat` 使用了 `--binary`。
