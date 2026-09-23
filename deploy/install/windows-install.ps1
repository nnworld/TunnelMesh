[CmdletBinding()]
param(
  [ValidateSet('server','agent','client')][string]$Role = 'agent',
  [Parameter(Mandatory=$true)][string]$Binary,
  [Parameter(Mandatory=$true)][string]$Config,
  [Parameter(Mandatory=$true)][string]$WinSW,
  [string]$InstallDir = "$env:ProgramFiles\TunnelMesh"
)

$ErrorActionPreference = 'Stop'
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run PowerShell as Administrator.' }
if (-not (Test-Path -LiteralPath $Binary)) { throw "Binary not found: $Binary" }
if (-not (Test-Path -LiteralPath $WinSW)) { throw "WinSW not found: $WinSW" }

New-Item -ItemType Directory -Force -Path $InstallDir, "$InstallDir\logs" | Out-Null
$service = "tunnelmesh-$Role"
$wrapper = Join-Path $InstallDir "$service-service.exe"
$xml = Join-Path $InstallDir "$service-service.xml"
Copy-Item -Force $Binary (Join-Path $InstallDir "tunnelmesh-$Role.exe")
Copy-Item -Force $WinSW $wrapper
$binaryName = "tunnelmesh-$Role.exe"
$roleTitle = $Role.Substring(0, 1).ToUpperInvariant() + $Role.Substring(1)
# 三个角色共用 deploy\windows\tunnelmesh-service.xml。此前 client 走模板、server/agent
# 走内联 here-string，client 分支会原样拷贝模板并静默忽略 -Config。
# 用 String.Replace 做字面替换：-replace 是正则，Windows 路径里的反斜杠会被当转义吃掉。
$configXml = $Config.Replace('&', '&amp;').Replace('<', '&lt;').Replace('>', '&gt;')
# 逐行替换而不是链式调用，避免依赖跨行成员访问的解析行为。
$template = Get-Content -Raw -Path "$PSScriptRoot\..\windows\tunnelmesh-service.xml"
$rendered = $template.Replace('__ROLE_TITLE__', $roleTitle)
$rendered = $rendered.Replace('__ROLE__', $Role)
$rendered = $rendered.Replace('__BINARY__', $binaryName)
$rendered = $rendered.Replace('__CONFIG__', $configXml)
$rendered = $rendered.Replace('__INSTALL_DIR__', $InstallDir)
$rendered = $rendered.Replace('__ENV_BLOCK__', '')
Set-Content -Encoding UTF8 -Path $xml -Value $rendered
& $wrapper stop 2>$null; & $wrapper uninstall 2>$null
& $wrapper install
& $wrapper start
Write-Host "installed $service; logs are under $InstallDir\logs"
