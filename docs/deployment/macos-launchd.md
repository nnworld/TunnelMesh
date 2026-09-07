# macOS launchd 安装

使用用户级 launchd，避免让服务进程直接以 root 运行：

```bash
mkdir -p "$HOME/.config/tunnelmesh"
# 先写入 agent.yaml/server.yaml，并执行对应的 check-config
./deploy/install/macos-install.sh agent ./tunnelmesh-agent "$HOME/.config/tunnelmesh/agent.yaml"
launchctl print "gui/$(id -u)/com.tunnelmesh.agent"
tail -f "$HOME/Library/Logs/tunnelmesh-agent.log"
```

Server 同样支持 `server` 参数。默认日志位置为 `~/Library/Logs/tunnelmesh-server.log` 和 `.err.log`。停止并移除：

```bash
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist"
rm -f "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist"
```

macOS 防火墙、网络扩展和系统代理可能影响 WSS；优先使用 `wss://`，并检查系统时间和证书链。
