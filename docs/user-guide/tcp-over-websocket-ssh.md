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
  'wss://8081-agent-id.apps.example.com'
```

`--binary` 保证 WebSocket 使用 binary message，`-B 65536` 控制缓冲区大小。实际 URL 需替换为你的显式路由或动态 wildcard 路由。

## SSH ProxyCommand

```sshconfig
Host tunnelmesh-agent
    HostName ignored-by-proxy
    User nami
    ProxyCommand websocat --binary -B 65536 - "wss://8081-agent-id.apps.example.com"
```

也可以临时执行：

```bash
ssh -o 'ProxyCommand=websocat --binary -B 65536 - wss://8081-agent-id.apps.example.com' \
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
- 任意命令执行不是本期能力；不要把 `proxy tcp` 当作绕过 `sshd` 的 shell 通道。

## 约束

- WebSocket endpoint 必须指向 Server 的 TCP bridge 路径或已配置的 TCP route。
- Server 只负责转发字节，不解析 SSH 协议。
- 生产环境使用 `wss://` 和有效证书。
- 遇到 404 时检查域名、路径和 wildcard DNS；遇到 502/504 时检查 Agent 在线状态和目标端口。
- 遇到 binary message 错误时确认 `websocat` 使用了 `--binary`。
