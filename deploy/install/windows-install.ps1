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
if ($Role -eq 'client') {
  Get-Content -Raw -Path "$PSScriptRoot\..\windows\tunnelmesh-client-service.xml" | Set-Content -Encoding UTF8 -Path $xml
} else {
  @"
<service>
  <id>$service</id>
  <name>TunnelMesh $Role</name>
  <description>TunnelMesh $Role service</description>
  <executable>$binaryName</executable>
  <arguments>--config &quot;$Config&quot; run</arguments>
  <workingdirectory>$InstallDir</workingdirectory>
  <logpath>$InstallDir\logs</logpath>
  <log mode="roll-by-size" />
  <onfailure action="restart" delay="5 sec" />
</service>
"@ | Set-Content -Encoding UTF8 -Path $xml
}
& $wrapper stop 2>$null; & $wrapper uninstall 2>$null
& $wrapper install
& $wrapper start
Write-Host "installed $service; logs are under $InstallDir\logs"
