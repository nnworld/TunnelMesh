# Windows Service 安装

## 一键安装（推荐）

三角色各一个入口脚本：`install-server.ps1`、`install-agent.ps1`、`install-client.ps1`。
以**管理员身份**打开 PowerShell，先下载再审阅、然后执行：

```powershell
Set-ExecutionPolicy -Scope Process Bypass
irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1 -OutFile install-agent.ps1
notepad .\install-agent.ps1
.\install-agent.ps1
```

等价的一行式（参数直接跟在 scriptblock 后面，不需要占位名）：

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-agent.ps1))) -Yes -ServerUrl wss://tunnel.example.com/ws/agent -TokenFile C:\secure\agent.token
```

查参数：`Get-Help .\install-agent.ps1 -Detailed` 或 `.\install-agent.ps1 -?`。

脚本会：校验并下载 Release 归档 → 安装 `tunnelmesh-<role>.exe` 到 `C:\Program Files\TunnelMesh`
→ 渲染 `C:\ProgramData\TunnelMesh\<role>.yaml` → 把 token 等敏感值渲染进 WinSW 服务 XML 的
`<env>` 块并收紧 ACL → `check-config` → 注册并启动服务 → 打印日志路径与卸载方式。
重复运行即升级：保留现有配置，旧二进制备份为 `<exe>.bak-<UTC时间戳>`（只保留最近 1 份）。

敏感值不接受命令行参数，只走 `-TokenFile`、`-SecretEnvFile` 或 `TUNNELMESH_*` 环境变量；
非交互安装加 `-Yes`，缺必填项时以退出码 3 失败而不是静默用默认值。

WinSW 由脚本自动获取，但**只下载 [winsw-checksums.txt](../../deploy/install/oneclick/winsw-checksums.txt)
中已登记校验和的版本**；没有登记条目时脚本中止并提示用 `-WinSW <本地路径>`。补录条目（需在可访问
github.com Release 的机器上执行）：

```powershell
Invoke-WebRequest -OutFile $env:TEMP\WinSW-x64.exe `
  https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe
(Get-FileHash $env:TEMP\WinSW-x64.exe -Algorithm SHA256).Hash.ToLowerInvariant()
# 把结果按「<version> <arch> <sha256> <asset-name>」追加到
# deploy/install/oneclick/winsw-checksums.txt 并提交
```

卸载（保留 `C:\ProgramData\TunnelMesh` 下的配置与 `logs\`）：

```powershell
.\install-agent.ps1 -Uninstall -Yes
```

交互项、非交互（CI）示例、镜像源与离线安装、退出码排障表见
[一键安装脚本](oneclick-install.md)。

## 手工安装

Windows 使用 [WinSW](https://github.com/winsw/winsw) 作为服务包装器。下载与版本校验应由发布流程完成，并选择与 Windows 架构匹配的 WinSW（amd64/arm64），再以管理员 PowerShell 执行：

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\tunnelmesh-agent.exe --config C:\ProgramData\TunnelMesh\agent.yaml check-config
.\deploy\install\windows-install.ps1 `
  -Role agent `
  -Binary .\tunnelmesh-agent.exe `
  -Config C:\ProgramData\TunnelMesh\agent.yaml `
  -WinSW C:\Tools\WinSW-x64.exe
Get-Service tunnelmesh-agent
```

安装 Client：

```powershell
.\tunnelmesh-client.exe --config C:\ProgramData\TunnelMesh\client.yaml check-config
.\deploy\install\windows-install.ps1 `
  -Role client `
  -Binary .\tunnelmesh-client.exe `
  -Config C:\ProgramData\TunnelMesh\client.yaml `
  -WinSW C:\Tools\WinSW-x64.exe
Get-Service tunnelmesh-client
```

WinSW 会把 stdout/stderr 滚动写入 `C:\Program Files\TunnelMesh\logs`，并在进程失败后重启。卸载：

```powershell
.\deploy\install\windows-uninstall.ps1 -Role agent
```

配置文件 ACL 只授予 TunnelMesh 服务账户和管理员；不要把 Bearer Token 或本地代理密码放进 PowerShell 历史或服务命令行。Windows 环境变量可通过系统级环境变量或 Secret Manager 注入。
