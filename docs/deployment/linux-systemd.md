# Linux systemd 安装

以 root 执行：

```bash
sudo ./deploy/install/linux-install.sh --role server --binary-dir ./dist/v0.1.0
sudo install -o tunnelmesh -g tunnelmesh -m 0600 server.yaml /etc/tunnelmesh/server.yaml
sudo tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id
sudo tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
sudo systemctl enable --now tunnelmesh-server.service
```

`init-node-id` 会在 YAML 缺少 `node.id` 时生成稳定 ID 并回写文件。打包的 `tunnelmesh-server.service` 已把它作为第一个 `ExecStartPre`，前缀 `+` 表示以 root 执行；随后的 `check-config` 和 `run` 仍使用 `tunnelmesh` 用户。因此 `/etc/tunnelmesh` 可保持只读，`/var/lib/tunnelmesh` 保持可写。初始化完成后再签发 relay 证书，证书 SAN 必须与最终 `node.id` 精确一致。

安装 Agent：

```bash
sudo ./deploy/install/linux-install.sh --role agent --binary-dir ./dist/v0.1.0
sudo install -o tunnelmesh -g tunnelmesh -m 0600 agent.yaml /etc/tunnelmesh/agent.yaml
sudo systemctl enable --now tunnelmesh-agent.service
```

安装 Client：

```bash
sudo ./deploy/install/linux-install.sh --role client --binary-dir ./dist/v0.1.0
sudo install -o root -g tunnelmesh -m 0640 client.yaml /etc/tunnelmesh/client.yaml
sudo install -o root -g tunnelmesh -m 0640 client.env /etc/tunnelmesh/client.env
sudo systemctl enable --now tunnelmesh-client.service
```

Client token、本地代理密码等敏感值应写入 `/etc/tunnelmesh/client.env`，由 systemd 的 `EnvironmentFile` 注入；不要写入 YAML 或 unit。Client 本地入口默认只监听 loopback，非 loopback 必须显式 `allow_remote: true` 并启用认证。

查看状态和日志：

```bash
systemctl status tunnelmesh-server.service
journalctl -u tunnelmesh-server.service -f
journalctl -u tunnelmesh-agent.service --since '15 min ago'
journalctl -u tunnelmesh-client.service -f
```

卸载时先 `systemctl disable --now`，再移除 unit 和二进制。脚本不会删除 `/etc/tunnelmesh` 配置、`/etc/tunnelmesh/client.env` 或 `/var/lib/tunnelmesh*` 数据。
