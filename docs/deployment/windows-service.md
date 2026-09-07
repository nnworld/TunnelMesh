# Windows Service 安装

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

WinSW 会把 stdout/stderr 滚动写入 `C:\Program Files\TunnelMesh\logs`，并在进程失败后重启。卸载：

```powershell
.\deploy\install\windows-uninstall.ps1 -Role agent
```

配置文件 ACL 只授予 TunnelMesh 服务账户和管理员；不要把 Bearer Token 放进 PowerShell 历史或服务命令行。
