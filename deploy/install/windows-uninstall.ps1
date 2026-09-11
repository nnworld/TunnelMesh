[CmdletBinding()]
param(
  [ValidateSet('server','agent','client')][string]$Role = 'agent',
  [string]$InstallDir = "$env:ProgramFiles\TunnelMesh"
)
$ErrorActionPreference = 'Stop'
$wrapper = Join-Path $InstallDir "tunnelmesh-$Role-service.exe"
if (Test-Path -LiteralPath $wrapper) { & $wrapper stop 2>$null; & $wrapper uninstall }
Write-Host "uninstalled tunnelmesh-$Role; configuration and logs were retained"
