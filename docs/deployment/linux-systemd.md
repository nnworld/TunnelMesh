# Linux systemd 安装

以 root 执行：

```bash
sudo ./deploy/install/linux-install.sh --role server --binary-dir ./dist/v0.1.0
sudo install -o tunnelmesh -g tunnelmesh -m 0600 server.yaml /etc/tunnelmesh/server.yaml
sudo tunnelmesh-server --config /etc/tunnelmesh/server.yaml check-config
sudo systemctl enable --now tunnelmesh-server.service
```

安装 Agent：

```bash
sudo ./deploy/install/linux-install.sh --role agent --binary-dir ./dist/v0.1.0
sudo install -o tunnelmesh -g tunnelmesh -m 0600 agent.yaml /etc/tunnelmesh/agent.yaml
sudo systemctl enable --now tunnelmesh-agent.service
```

查看状态和日志：

```bash
systemctl status tunnelmesh-server.service
journalctl -u tunnelmesh-server.service -f
journalctl -u tunnelmesh-agent.service --since '15 min ago'
```

卸载时先 `systemctl disable --now`，再移除 unit 和二进制。脚本不会删除 `/etc/tunnelmesh` 配置或 `/var/lib/tunnelmesh*` 数据。
