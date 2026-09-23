# TunnelMesh Client 一键安装（Windows）。设计与参数见 docs/deployment/oneclick-install.md。
# 本文件刻意保持薄：下载、校验、交互、渲染、服务注册全部在共享模块里。
# Windows 服务是机器级的，必须以管理员身份运行 PowerShell；否则退出码 3。
<#
.SYNOPSIS
  TunnelMesh Client 一键安装（Windows 服务）。
.DESCRIPTION
  解析版本 → 下载并校验 Release 归档 → 安装 tunnelmesh-client.exe → 渲染 client.yaml 与服务 XML
  → check-config → 注册并启动 WinSW 服务 → 输出摘要。
  必须以管理员身份运行 PowerShell。
  退出码：0 成功、2 参数、3 preflight、4 下载、5 校验、6 配置校验、7 服务、8 卸载。
  client token 不接受命令行参数（会进 PowerShell 历史与进程列表），只走 -TokenFile、
  -SecretEnvFile 或 TUNNELMESH_CLIENT_TOKEN 环境变量。
.PARAMETER Version
  指定 vX.Y.Z；默认解析最新 Release。
.PARAMETER Yes
  非交互模式：所有提示取默认值；缺少必填项时以退出码 3 失败。
.PARAMETER ServerUrl
  Server 的 WebSocket 地址，必须以 /ws/client 结尾。
.PARAMETER Tunnel
  本地转发，可重复传入；spec = name:protocol:listen_host:listen_port:agent_id[:target_host:target_port]，
  protocol 取 tcp、udp、http、socks5。
.PARAMETER Uninstall
  卸载服务与二进制，保留 C:\ProgramData\TunnelMesh 下的配置与日志。
.EXAMPLE
  .\install-client.ps1
.EXAMPLE
  .\install-client.ps1 -Yes -ServerUrl wss://tunnel.example.com/ws/client -TokenFile C:\secure\client.token -Tunnel pg:tcp:127.0.0.1:15432:agent-01:10.0.0.5:5432
.LINK
  docs/deployment/oneclick-install.md
.LINK
  docs/deployment/windows-service.md
#>
[CmdletBinding()]
param(
  [string]$Version,
  [switch]$Yes,
  [switch]$Uninstall,
  [switch]$NoService,
  [switch]$NoStart,
  [switch]$NoEnable,
  [switch]$NoCache,
  [switch]$KeepConfig,
  [switch]$Reconfigure,
  [string]$BaseUrl,
  [string]$RawBaseUrl,
  [string]$Archive,
  [string]$TokenFile,
  [string]$SecretEnvFile,
  [string]$InstallDir,
  [string]$ConfigDir,
  [string]$StateDir,
  [string]$ServerUrl,
  [string]$AgentId,
  [string[]]$Tunnel,
  [string]$WinSW
)

# --- bootstrap-begin ---
# 定位共享模块：同目录 → raw 下载。
# `irm ... -OutFile install-<role>.ps1` 只落一个入口文件，同目录没有共享模块，必须回退下载。
# 三个入口的这段引导必须逐字节一致（TestPowerShellEntryBootstrapIsIdentical 守护）。
$tmRaw = if ($RawBaseUrl) { $RawBaseUrl } elseif ($env:TUNNELMESH_RAW_BASE_URL) { $env:TUNNELMESH_RAW_BASE_URL } elseif ($env:TM_RAW_BASE_URL) { $env:TM_RAW_BASE_URL } else { 'https://raw.githubusercontent.com/nnworld/TunnelMesh' }
$tmRef = if ($env:TM_ONECLICK_REF) { $env:TM_ONECLICK_REF } else { 'main' }
$tmLocal = if ($PSScriptRoot) { Join-Path $PSScriptRoot 'tunnelmesh-install-common.ps1' } else { '' }
if ($tmLocal -and (Test-Path -LiteralPath $tmLocal)) {
  . $PSScriptRoot\tunnelmesh-install-common.ps1
} else {
  $tmDir = Join-Path $env:TEMP ('tunnelmesh-oneclick-' + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Force -Path $tmDir | Out-Null
  foreach ($tmName in @('tunnelmesh-install-common.ps1', 'winsw-checksums.txt')) {
    try {
      Invoke-WebRequest -UseBasicParsing -ErrorAction Stop -Uri "$tmRaw/$tmRef/deploy/install/oneclick/$tmName" -OutFile (Join-Path $tmDir $tmName)
    } catch {
      Write-Error "ERROR: cannot download $tmName from $tmRaw/$tmRef"
      exit 4
    }
  }
  . (Join-Path $tmDir 'tunnelmesh-install-common.ps1')
}
# --- bootstrap-end ---

Invoke-TmMain -Role client -Bound $PSBoundParameters
