# macOS launchd 安装

> 推荐先用[一键安装脚本](oneclick-install.md)：`install-<role>.sh` 会自动渲染 plist、注入敏感值、
> 执行 `check-config` 并 `launchctl bootstrap`。本页描述手工安装的语义。

使用用户级 launchd，避免让服务进程直接以 root 运行：

```bash
mkdir -p "$HOME/.config/tunnelmesh"
# 先写入 agent.yaml/server.yaml，并执行对应的 check-config
./deploy/install/macos-install.sh agent ./tunnelmesh-agent "$HOME/.config/tunnelmesh/agent.yaml"
launchctl print "gui/$(id -u)/com.tunnelmesh.agent"
tail -f "$HOME/Library/Logs/tunnelmesh-agent.log"
```

Client 安装：

```bash
./tunnelmesh-client --config "$HOME/.config/tunnelmesh/client.yaml" check-config
./deploy/install/macos-install.sh client ./tunnelmesh-client "$HOME/.config/tunnelmesh/client.yaml"
launchctl print "gui/$(id -u)/com.tunnelmesh.client"
tail -f "$HOME/Library/Logs/tunnelmesh-client.log"
```

Server 同样支持 `server` 参数。默认日志位置为 `~/Library/Logs/tunnelmesh-server.log`、`tunnelmesh-agent.log` 和 `tunnelmesh-client.log`，错误日志为同名 `.err.log`。停止并移除：

```bash
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist"
rm -f "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist"
```

macOS 防火墙、网络扩展和系统代理可能影响 WSS；优先使用 `wss://`，并检查系统时间和证书链。

## 敏感值注入：`__ENVIRONMENT__` 占位符

[`deploy/macos/tunnelmesh.plist`](../../deploy/macos/tunnelmesh.plist) 是三角色共用的模板，
占位符为 `__ROLE__`、`__HOME__`、`__BINARY__`、`__CONFIG__`、`__ENVIRONMENT__`。

`__ENVIRONMENT__` 渲染为 launchd 的 `EnvironmentVariables` dict：

```xml
  <key>EnvironmentVariables</key>
  <dict>
    <key>TUNNELMESH_AGENT_TOKEN</key>
    <string>...</string>
  </dict>
```

之所以走 plist 而不是 YAML：`agent.token` 与 `client.token` 在配置模型里是 `yaml:"-"`，
**根本无法**写进 YAML；launchd 又没有 systemd 的 `EnvironmentFile` 机制。因此渲染后的 plist
本身就成了敏感值载体，必须按 `0600` 落盘（一键脚本与 `macos-install.sh` 都这么做），
并且**仓库里的模板副本永远不含真实值**——`deploy/install/macos-install.sh` 把
`__ENVIRONMENT__` 渲染为空串，只有携带敏感值的一键安装路径才会填内容。

校验渲染结果仍是合法 plist：

```bash
plutil -lint "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist"
```

## 卸载

一键安装的卸载（保留配置、密钥与日志，并逐条打印保留路径）：

```bash
/bin/bash /tmp/tunnelmesh-install-agent.sh --uninstall
```

手工安装的卸载见上面的 `launchctl bootout` + 删除 plist。两种方式都不删除
`~/.config/tunnelmesh/` 与 `~/.local/share/tunnelmesh/`。
