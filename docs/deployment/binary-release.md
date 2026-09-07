# 跨平台可执行文件打包

TunnelMesh 的发行物包含三个可执行文件：`tunnelmesh-server`、`tunnelmesh-agent`、`tunnelmesh-client`。发行构建默认使用 `CGO_ENABLED=0`，不把配置、Token、密码、DSN、证书、私钥、数据库和日志放进归档。

## 构建矩阵

| 平台 | 架构 | 归档 |
| --- | --- | --- |
| Linux | amd64、arm64 | `.tar.gz` |
| macOS | amd64、arm64 | `.tar.gz` |
| Windows | amd64、arm64 | `.zip` |

在仓库根目录执行：

```bash
VERSION=v0.1.0 ./scripts/build-release.sh
sha256sum -c dist/v0.1.0/SHA256SUMS
```

等价的 Make 入口：

```bash
make release VERSION=v0.1.0
```

macOS 没有 `sha256sum` 时脚本会自动使用 `shasum -a 256`。单个平台也可以直接交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o tunnelmesh-agent_linux_arm64 ./cmd/tunnelmesh-agent
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o tunnelmesh-client_darwin_arm64 ./cmd/tunnelmesh-client
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o tunnelmesh-server_windows_amd64.exe ./cmd/tunnelmesh-server
```

## 安装方式

- Linux：使用 [linux-install.sh](../../deploy/install/linux-install.sh)，由 systemd 管理 Server/Agent。
- macOS：使用 [macos-install.sh](../../deploy/install/macos-install.sh)，由 launchd 管理用户级服务；Server 建议监听 8080 并由反向代理接管 80/443。
- Windows：使用 [windows-install.ps1](../../deploy/install/windows-install.ps1)，通过 WinSW 注册 Windows Service；不要把 Token 写入脚本参数，放在受 ACL 保护的配置文件或环境变量中。

安装脚本只安装二进制和进程管理配置，不自动生成业务配置，也不会覆盖已有配置和数据。
