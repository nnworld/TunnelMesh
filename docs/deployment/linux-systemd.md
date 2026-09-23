# Linux systemd 安装

> 推荐先用[一键安装脚本](oneclick-install.md)：一条命令完成下载校验、生成配置、注册单元、启动与自检，
> Linux 默认走**用户级** systemd unit（不需要 root），`--mode system` 才走下面的系统级流程。
> 本页描述的是手工安装与系统级单元的语义。

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

Agent token 同理：`agent.token` 在配置模型里是 `yaml:"-"`，**根本无法**写进 YAML，只能走环境变量。
`tunnelmesh-agent.service` 因此带一行可选的 `EnvironmentFile=-/etc/tunnelmesh/agent.env`
（前缀 `-` 表示文件缺失不阻塞启动），权限 `0640 root:tunnelmesh`，写入
`TUNNELMESH_AGENT_TOKEN="..."`。一键脚本在 system 模式下会自动生成它，在 user 模式下生成
`~/.config/tunnelmesh/agent.env`（`0600`）。

查看状态和日志：

```bash
systemctl status tunnelmesh-server.service
journalctl -u tunnelmesh-server.service -f
journalctl -u tunnelmesh-agent.service --since '15 min ago'
journalctl -u tunnelmesh-client.service -f
```

卸载时先 `systemctl disable --now`，再移除 unit 和二进制。脚本不会删除 `/etc/tunnelmesh` 配置、`/etc/tunnelmesh/client.env` 或 `/var/lib/tunnelmesh*` 数据。

## 用户级安装

没有 root、或希望服务跟随当前用户生命周期时，用 systemd **user** 单元。模板在
[`deploy/systemd-user/`](../../deploy/systemd-user/)，四个占位符 `__BINARY__`、`__CONFIG__`、
`__ENV_FILE__`、`__STATE_DIR__` 由一键脚本渲染；手工安装时按默认路径替换即可：

```bash
mkdir -p ~/.config/systemd/user ~/.local/share/tunnelmesh
sed -e "s|__BINARY__|$HOME/.local/bin/tunnelmesh-agent|" \
    -e "s|__CONFIG__|$HOME/.config/tunnelmesh/agent.yaml|" \
    -e "s|__ENV_FILE__|$HOME/.config/tunnelmesh/agent.env|" \
    -e "s|__STATE_DIR__|$HOME/.local/share/tunnelmesh|" \
    deploy/systemd-user/tunnelmesh-agent.service \
  > ~/.config/systemd/user/tunnelmesh-agent.service
systemctl --user daemon-reload
systemctl --user enable --now tunnelmesh-agent.service
# 退出登录后仍要保持运行：
loginctl enable-linger "$USER"
```

与系统级单元的三点差异：

- 日志用 `journalctl --user -u tunnelmesh-<role>`，不是 `journalctl -u ...`。
- user 单元以非 root 运行，且 `ReadWritePaths` 只放开状态目录：`init-node-id` 的默认路径
  `/var/lib/tunnelmesh/node-id` 不可写，因此 server 的 user 单元必须带
  `--node-id-path __STATE_DIR__/node-id`（模板已内置），否则会以 `permission denied` 失败。
- 没有 `ProtectHome=true`：user 单元本身就跑在 `$HOME` 下。

## drop-in 覆盖

system 模式下需要换服务运行账户或改路径时，一键脚本不改仓库模板，而是生成
`/etc/systemd/system/tunnelmesh-<role>.service.d/oneclick.conf`：先 `ExecStart=` / `ExecStartPre=`
清空再重设，并覆盖 `User=`、`Group=` 与 `WorkingDirectory=`。只有当用户确实选择了非默认值
（`--run-user`、`--run-group`、`--bin-dir`、`--config-dir`、`--state-dir`）时才生成。

```bash
sudo systemctl cat tunnelmesh-server.service          # 查看合并后的有效配置
sudo editor /etc/systemd/system/tunnelmesh-server.service.d/oneclick.conf
sudo systemctl daemon-reload && sudo systemctl restart tunnelmesh-server
```

卸载（`--uninstall`）会一并删除该 drop-in 目录。手工安装不涉及 drop-in：直接编辑
`/etc/tunnelmesh/*.yaml` 或用 `systemctl edit` 自建 override。
