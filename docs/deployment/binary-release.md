# 跨平台可执行文件打包

TunnelMesh 的发行物包含三个可执行文件：`tunnelmesh-server`、`tunnelmesh-agent`、`tunnelmesh-client`。发行构建默认使用 `CGO_ENABLED=0`，不把配置、Token、密码、DSN、证书、私钥、数据库和日志放进归档。

## GitHub Release

正式发行包维护在 GitHub Release：

```text
https://github.com/nnworld/TunnelMesh/releases
```

Release 由以下两种方式触发：

- 推送完整 `vMAJOR.MINOR.PATCH` 标签，例如 `v1.2.3`。
- 管理员手动执行 `release` workflow，并输入完整版本号。

workflow 会先完成前端测试/构建、Go 单测、race 测试、vet 和 embed 一致性检查，再生成不可变 Release。不会创建或移动 `v1`、`v1.2` 这类可变标签。

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

归档命名固定为：

```text
tunnelmesh-vMAJOR.MINOR.PATCH-PLATFORM.ARCHIVE
```

例如 `tunnelmesh-v1.2.3-darwin-arm64.tar.gz` 和
`tunnelmesh-v1.2.3-windows-amd64.zip`。每个 Release 都包含：

- 六个平台归档；
- `SHA256SUMS`；
- `manifest.json`。

`manifest.json` 记录版本、主版本、Commit、UTC 构建时间、Schema 版本、三个二进制、六个平台和资产清单。当前 Schema 版本为 11。

在 Release 目录内校验：

```bash
cd dist/v0.1.0
sha256sum -c SHA256SUMS
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

Linux 归档包含 `deploy/systemd`，macOS 归档包含 `deploy/macos`，Windows 归档包含 `deploy/windows`。所有归档都包含三个二进制、安装脚本和文档。

## 回滚

1. 在后台“下载”页或 GitHub Release 页面选择上一个版本。
2. 下载并校验上一个版本的 `SHA256SUMS`。
3. 停止当前进程，替换二进制和 service 模板，但保留配置、数据和密钥。
4. 启动服务并检查健康状态、日志、客户端连接和核心链路。
5. 如涉及数据库迁移，按 `docs/operations/schema-upgrades.md` 的版本说明执行兼容回滚；不能自动猜测反向 DDL。

## 私有仓库权限

若仓库为私有，只有具备仓库下载权限的账号或 CI 凭据可以访问 GitHub Release。管理员后台只展示下载 URL，不代理或缓存 GitHub 凭据。当前配置只支持 `github.com` 上的 `owner/name` 仓库；如需企业内网镜像，应另行实现独立的下载分发服务，不能把凭据写入 TunnelMesh 配置。
